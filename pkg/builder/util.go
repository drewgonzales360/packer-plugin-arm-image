package builder

import (
	"bytes"
	"context"
	"fmt"

	packer_common_common "github.com/hashicorp/packer-plugin-sdk/common"
	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"
)

// runCleanup is like run but does not set errors in state. Use during
// Cleanup methods where failures should be logged but not abort the build.
func runCleanup(_ context.Context, state multistep.StateBag, cmds string) error {
	wrappedCommand := state.Get("wrappedCommand").(packer_common_common.CommandWrapper)
	ui := state.Get("ui").(packer.Ui)

	shellcmd, err := wrappedCommand(cmds)
	if err != nil {
		ui.Error(fmt.Sprintf("Error creating command '%s': %s", cmds, err))
		return err
	}

	stderr := new(bytes.Buffer)
	cmd := packer_common_common.ShellCommand(shellcmd)
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		ui.Error(fmt.Sprintf("Error executing command '%s': %s\nStderr: %s", cmds, err, stderr.String()))
		return err
	}
	return nil
}

func run(_ context.Context, state multistep.StateBag, cmds string) error {
	wrappedCommand := state.Get("wrappedCommand").(packer_common_common.CommandWrapper)
	ui := state.Get("ui").(packer.Ui)

	shellcmd, err := wrappedCommand(cmds)
	if err != nil {
		err := fmt.Errorf("Error creating command '%s': %s", cmds, err)
		state.Put("error", err)
		ui.Error(err.Error())
		return err
	}

	stderr := new(bytes.Buffer)

	cmd := packer_common_common.ShellCommand(shellcmd)
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		err := fmt.Errorf(
			"Error executing command '%s': %s\nStderr: %s", cmds, err, stderr.String())
		state.Put("error", err)
		ui.Error(err.Error())
		return err
	}
	return nil
}
