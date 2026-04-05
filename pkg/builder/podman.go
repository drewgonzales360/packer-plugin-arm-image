//go:build !linux

package builder

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"runtime"
	"strings"

	"github.com/containers/podman/v5/pkg/api/handlers"
	"github.com/containers/podman/v5/pkg/bindings"
	"github.com/containers/podman/v5/pkg/bindings/containers"
	"github.com/containers/podman/v5/pkg/bindings/images"
	"github.com/containers/podman/v5/pkg/machine/env"
	"github.com/containers/podman/v5/pkg/machine/provider"
	"github.com/containers/podman/v5/pkg/machine/vmconfigs"
	"github.com/containers/podman/v5/pkg/specgen"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

const defaultPodmanImage = "ubuntu:26.04"

// PodmanEnvironment manages a privileged Podman container used as the build
// environment on non-Linux hosts (e.g. macOS). All Linux-specific operations
// (losetup, mount, chroot, etc.) run inside this container.
//
// Uses the Podman Go bindings (not CLI) for container lifecycle management.
// The command wrapper still produces `podman exec` CLI strings because the
// Packer SDK evaluates them via /bin/sh.
type PodmanEnvironment struct {
	conn        context.Context
	ContainerID string
	Image       string
	volumes     []string
}

// NewPodmanEnvironment creates a new PodmanEnvironment. If image is empty,
// defaults to ubuntu:26.04.
func NewPodmanEnvironment(image string, volumes []string) *PodmanEnvironment {
	if image == "" {
		image = defaultPodmanImage
	}
	return &PodmanEnvironment{
		Image:   image,
		volumes: volumes,
	}
}

// connect establishes a rootful connection to the Podman machine.
// We must use the root socket (/run/podman/podman.sock) because the build
// requires privileged operations (losetup, mount, chroot) that fail on the
// rootless socket even with --privileged containers.
func (p *PodmanEnvironment) connect(ctx context.Context) error {
	prov, err := provider.Get()
	if err != nil {
		return fmt.Errorf("failed to get podman provider: %w", err)
	}

	dirs, err := env.GetMachineDirs(prov.VMType())
	if err != nil {
		return fmt.Errorf("failed to get machine dirs: %w", err)
	}

	mc, err := vmconfigs.LoadMachineByName("podman-machine-default", dirs)
	if err != nil {
		return fmt.Errorf("failed to load podman machine (is it created? run: podman machine init --rootful): %w", err)
	}

	// Build the rootful SSH URI: ssh://root@127.0.0.1:<port>/run/podman/podman.sock
	rootURI := fmt.Sprintf("ssh://root@127.0.0.1:%d/run/podman/podman.sock", mc.SSH.Port)

	p.conn, err = bindings.NewConnectionWithIdentity(ctx, rootURI, mc.SSH.IdentityPath, true)
	if err != nil {
		return fmt.Errorf("failed to connect to podman root socket (is the machine running and rootful? run: podman machine start && podman machine set --rootful): %w", err)
	}

	return nil
}

// Start connects to Podman, pulls the image if needed, creates a privileged
// container with the configured volume mounts, and starts it.
func (p *PodmanEnvironment) Start(ctx context.Context) error {
	if err := p.connect(ctx); err != nil {
		return err
	}

	// Pull image if not already present.
	if _, err := images.Pull(p.conn, p.Image, nil); err != nil {
		return fmt.Errorf("failed to pull image %s: %w", p.Image, err)
	}

	s := specgen.NewSpecGenerator(p.Image, false)
	s.Name = fmt.Sprintf("packer-arm-image-builder-%d", rand.Uint32())
	privileged := true
	s.Privileged = &privileged
	s.Command = []string{"sleep", "infinity"}

	// Add volume mounts so the image file and temp dirs are shared.
	for _, v := range p.volumes {
		parts := strings.SplitN(v, ":", 2)
		if len(parts) == 2 {
			s.Mounts = append(s.Mounts, specs.Mount{
				Destination: parts[1],
				Type:        "bind",
				Source:      parts[0],
				Options:     []string{"rw"},
			})
		}
	}

	createResponse, err := containers.CreateWithSpec(p.conn, s, nil)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}
	p.ContainerID = createResponse.ID

	if err := containers.Start(p.conn, p.ContainerID, nil); err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	return nil
}

// Exec runs a command inside the container and returns the combined
// stdout+stderr output. Returns an error if the command fails.
func (p *PodmanEnvironment) Exec(args ...string) ([]byte, error) {
	execConfig := &handlers.ExecCreateConfig{}
	execConfig.Cmd = args
	execConfig.AttachStdout = true
	execConfig.AttachStderr = true

	sessionID, err := containers.ExecCreate(p.conn, p.ContainerID, execConfig)
	if err != nil {
		return nil, fmt.Errorf("exec create failed: %w", err)
	}

	var buf bytes.Buffer
	w := nopWriteCloser{&buf}
	opts := new(containers.ExecStartAndAttachOptions).
		WithOutputStream(w).
		WithAttachOutput(true).
		WithErrorStream(w).
		WithAttachError(true)

	if err := containers.ExecStartAndAttach(p.conn, sessionID, opts); err != nil {
		return buf.Bytes(), fmt.Errorf("exec failed: %w\n%s", err, buf.String())
	}

	// Check exit code
	inspect, err := containers.ExecInspect(p.conn, sessionID, nil)
	if err != nil {
		log.Printf("[WARN] ExecInspect failed, cannot verify exit code: %v", err)
		return buf.Bytes(), fmt.Errorf("exec inspect failed (command may have failed): %w", err)
	}
	if inspect.ExitCode != 0 {
		return buf.Bytes(), fmt.Errorf("command exited with code %d: %s", inspect.ExitCode, buf.String())
	}

	return buf.Bytes(), nil
}

// ExecShell runs a shell command inside the container.
func (p *PodmanEnvironment) ExecShell(command string) ([]byte, error) {
	return p.Exec("sh", "-c", command)
}

// InstallPackages installs the given apt packages inside the container,
// skipping if the key tools are already present.
func (p *PodmanEnvironment) InstallPackages(packages []string) error {
	// Check if all required tools already exist.
	allPresent := true
	for _, pkg := range packages {
		// Check for key binaries: package name often matches the binary,
		// but some don't. Check the most important ones explicitly.
		bins := map[string]string{
			"util-linux": "losetup",
			"e2fsprogs":  "resize2fs",
			"kpartx":     "kpartx",
			"psmisc":     "fuser",
		}
		bin, ok := bins[pkg]
		if !ok {
			bin = pkg
		}
		if _, err := p.Exec("which", bin); err != nil {
			allPresent = false
			break
		}
	}
	if allPresent {
		return nil
	}

	cmds := []string{
		"timeout 300 apt-get update -qq",
		fmt.Sprintf("timeout 600 env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq %s", strings.Join(packages, " ")),
	}
	for _, cmd := range cmds {
		if out, err := p.ExecShell(cmd); err != nil {
			return fmt.Errorf("failed to install packages: %w\n%s", err, string(out))
		}
	}
	return nil
}

// Stop stops and removes the container.
func (p *PodmanEnvironment) Stop() error {
	if p.ContainerID == "" || p.conn == nil {
		return nil
	}
	if err := containers.Stop(p.conn, p.ContainerID, nil); err != nil {
		log.Printf("[WARN] failed to stop container %s: %v", p.ContainerID[:12], err)
	}
	_, err := containers.Remove(p.conn, p.ContainerID, new(containers.RemoveOptions).WithForce(true))
	if err != nil {
		return fmt.Errorf("failed to remove container: %w", err)
	}
	p.ContainerID = ""
	return nil
}

// WrapCommand returns a shell command string that executes the given command
// inside the Podman container. This is needed for the Packer SDK's command
// wrapper which evaluates commands via /bin/sh.
func (p *PodmanEnvironment) WrapCommand(command string) string {
	escaped := strings.ReplaceAll(command, "'", "'\\''")
	return fmt.Sprintf("podman exec %s /bin/sh -c '%s'", p.ContainerID, escaped)
}

// NeedsPodman returns true if the current platform requires a container
// environment for the build (i.e. not native Linux).
func NeedsPodman() bool {
	return runtime.GOOS != "linux"
}

// getPodmanEnv retrieves the PodmanEnvironment from state, or nil if not set.
func getPodmanEnv(state interface {
	GetOk(string) (interface{}, bool)
}) *PodmanEnvironment {
	if d, ok := state.GetOk("podman_env"); ok {
		return d.(*PodmanEnvironment)
	}
	return nil
}

// nopWriteCloser wraps an io.Writer with a no-op Close method.
type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }
