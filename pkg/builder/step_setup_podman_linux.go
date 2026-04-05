//go:build linux

package builder

import (
	"context"

	"github.com/hashicorp/packer-plugin-sdk/multistep"
)

// stepSetupPodman is a stub on Linux where Podman is not needed.
type stepSetupPodman struct{}

func (s *stepSetupPodman) Run(_ context.Context, _ multistep.StateBag) multistep.StepAction {
	return multistep.ActionContinue
}

func (s *stepSetupPodman) Cleanup(_ multistep.StateBag) {}
