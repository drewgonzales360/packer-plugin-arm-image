//go:build !linux

package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	packer_common_common "github.com/hashicorp/packer-plugin-sdk/common"
	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"
	"github.com/hashicorp/packer-plugin-sdk/template/interpolate"
)

// stepSetupPodman starts a privileged Podman container and redirects all
// subsequent command-wrapper invocations through it. This enables Linux-only
// operations (losetup, mount, chroot, etc.) to run on macOS.
//
// Uses the Podman Go bindings for container lifecycle management.
// The command wrapper still produces `podman exec` CLI strings because the
// Packer SDK evaluates them via /bin/sh.
//
// Cleanup stops and removes the container. Because Packer cleans up steps in
// reverse order, the container stays alive while later steps clean up their
// mounts and loop devices.
type stepSetupPodman struct {
	podman *PodmanEnvironment
}

func (s *stepSetupPodman) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	config := state.Get("config").(*Config)
	ui := state.Get("ui").(packer.Ui)
	imagefile := state.Get("imagefile").(string)

	image := config.PodmanImage
	if image == "" {
		image = defaultPodmanImage
	}

	// Build volume mounts: we need the image directory (for losetup) and the
	// OS temp directory (for Packer SDK temp files used during provisioning).
	imageDir, err := filepath.Abs(filepath.Dir(imagefile))
	if err != nil {
		ui.Error(fmt.Sprintf("Failed to resolve image directory: %s", err))
		state.Put("error", err)
		return multistep.ActionHalt
	}

	tmpDir := os.TempDir()

	seen := map[string]bool{imageDir: true}
	volumes := []string{imageDir + ":" + imageDir}

	// Mount temp directories so Packer SDK file uploads work inside the container.
	for _, d := range []string{"/tmp", tmpDir} {
		abs, err := filepath.Abs(d)
		if err != nil {
			ui.Error(fmt.Sprintf("Warning: failed to resolve absolute path for %s: %s", d, err))
			continue
		}
		if abs != "" && !seen[abs] && !strings.HasPrefix(abs, imageDir) {
			volumes = append(volumes, abs+":"+abs)
			seen[abs] = true
		}
	}

	ui.Say(fmt.Sprintf("Starting Podman build environment (%s)...", image))

	s.podman = NewPodmanEnvironment(image, volumes)
	if err := s.podman.Start(ctx); err != nil {
		ui.Error(fmt.Sprintf("Failed to start Podman: %s", err))
		state.Put("error", err)
		return multistep.ActionHalt
	}

	ui.Say(fmt.Sprintf("Podman container started: %s", s.podman.ContainerID[:12]))

	// Detach any stale loop devices from previous failed runs, then ensure
	// enough loop device nodes exist for our build.
	if out, err := s.podman.ExecShell("losetup -D 2>/dev/null || true"); err != nil {
		ui.Say(fmt.Sprintf("Warning: losetup -D failed (may be harmless): %s %s", err, string(out)))
	}
	if out, err := s.podman.ExecShell("for i in $(seq 0 7); do [ -e /dev/loop$i ] || mknod /dev/loop$i b 7 $i; done"); err != nil {
		ui.Error(fmt.Sprintf("Failed to create loop device nodes: %s %s", err, string(out)))
		state.Put("error", err)
		return multistep.ActionHalt
	}

	// Install required Linux packages inside the container.
	ui.Say("Installing required packages in container...")
	packages := []string{"util-linux", "e2fsprogs", "psmisc", "kpartx"}
	if !config.ImageArch.IsNative() || config.QemuRequired {
		packages = append(packages, "qemu-user-static")
	}
	if err := s.podman.InstallPackages(packages); err != nil {
		ui.Error(fmt.Sprintf("Failed to install packages: %s", err))
		s.podman.Stop()
		state.Put("error", err)
		return multistep.ActionHalt
	}

	// Store in state so other steps can detect Podman mode.
	state.Put("podman_env", s.podman)

	// Replace the command wrapper so all subsequent run() calls and Packer SDK
	// steps (StepMountExtra, StepChrootProvision, etc.) route through Podman.
	// This must use CLI because the Packer SDK evaluates the wrapper via /bin/sh.
	originalWrapper := config.CommandWrapper
	podmanWrappedCommand := func(command string) (string, error) {
		// Apply the user's original command wrapper first, if any.
		if originalWrapper != "" && originalWrapper != "{{.Command}}" {
			config.ctx.Data = &wrappedCommandTemplate{Command: command}
			wrapped, err := interpolate.Render(originalWrapper, &config.ctx)
			if err != nil {
				return "", err
			}
			command = wrapped
		}
		return s.podman.WrapCommand(command), nil
	}
	state.Put("wrappedCommand", packer_common_common.CommandWrapper(podmanWrappedCommand))

	return multistep.ActionContinue
}

func (s *stepSetupPodman) Cleanup(state multistep.StateBag) {
	if s.podman != nil {
		ui := state.Get("ui").(packer.Ui)
		ui.Say("Stopping Podman build environment...")
		if err := s.podman.Stop(); err != nil {
			ui.Error(fmt.Sprintf("Warning: failed to stop Podman container: %s", err))
		}
	}
}
