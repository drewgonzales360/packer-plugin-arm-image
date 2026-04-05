//go:build linux

package builder

import "context"

// PodmanEnvironment is a stub on Linux where Podman is not needed.
// All Linux-specific operations (losetup, mount, chroot) run natively.
type PodmanEnvironment struct {
	ContainerID string
	Image       string
}

func NeedsPodman() bool { return false }

func getPodmanEnv(_ interface{ GetOk(string) (interface{}, bool) }) *PodmanEnvironment {
	return nil
}

func NewPodmanEnvironment(_ string, _ []string) *PodmanEnvironment { return nil }

func (p *PodmanEnvironment) Start(_ context.Context) error      { return nil }
func (p *PodmanEnvironment) Exec(_ ...string) ([]byte, error)   { return nil, nil }
func (p *PodmanEnvironment) ExecShell(_ string) ([]byte, error) { return nil, nil }
func (p *PodmanEnvironment) InstallPackages(_ []string) error    { return nil }
func (p *PodmanEnvironment) Stop() error                         { return nil }
func (p *PodmanEnvironment) WrapCommand(command string) string   { return command }

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }
