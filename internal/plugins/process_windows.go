//go:build windows

package plugins

import "os/exec"

// preparePluginCommand leaves Windows plugin process setup at the os/exec
// default until the harness needs Windows process-tree termination semantics.
func preparePluginCommand(cmd *exec.Cmd) {}

// terminatePluginProcess has no portable graceful process-tree primitive on
// Windows yet, so Close waits once more before escalating to Kill.
func terminatePluginProcess(cmd *exec.Cmd) (bool, error) {
	return false, nil
}

// killPluginProcess terminates the direct plugin shell process on Windows.
func killPluginProcess(cmd *exec.Cmd) (bool, error) {
	if cmd == nil || cmd.Process == nil {
		return false, nil
	}
	if err := cmd.Process.Kill(); err != nil {
		return false, err
	}

	return true, nil
}
