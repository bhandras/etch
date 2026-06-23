//go:build !windows

package plugins

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// preparePluginCommand configures cmd so Close can terminate the whole shell
// process group rather than only the immediate shell child.
func preparePluginCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminatePluginProcess asks the plugin process group rooted at cmd to exit
// cooperatively before Close escalates to a kill.
func terminatePluginProcess(cmd *exec.Cmd) (bool, error) {
	if cmd == nil || cmd.Process == nil {
		return false, nil
	}

	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, syscall.SIGTERM); err == nil {
		return true, nil
	} else if !errorsIsProcessDone(err) {
		if signalErr := cmd.Process.Signal(
			syscall.SIGTERM,
		); signalErr == nil {
			return true, nil
		} else if !errorsIsProcessDone(signalErr) {
			return false, signalErr
		}
	}

	return false, nil
}

// killPluginProcess forcibly terminates the plugin process group rooted at cmd.
// The direct process is killed as a fallback when process-group signaling
// fails.
func killPluginProcess(cmd *exec.Cmd) (bool, error) {
	if cmd == nil || cmd.Process == nil {
		return false, nil
	}

	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, syscall.SIGKILL); err == nil {
		return true, nil
	} else if !errorsIsProcessDone(err) {
		if killErr := cmd.Process.Kill(); killErr == nil {
			return true, nil
		} else if !errorsIsProcessDone(killErr) {
			return false, killErr
		}
	}

	return false, nil
}

// errorsIsProcessDone reports whether err means the process already exited.
func errorsIsProcessDone(err error) bool {
	return errors.Is(err, syscall.ESRCH) ||
		errors.Is(err, os.ErrProcessDone)
}
