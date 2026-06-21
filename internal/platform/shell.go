package platform

import "runtime"

// ShellCommand returns the platform shell invocation for command.
func ShellCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C", command}
	}

	return "/bin/sh", []string{"-c", command}
}
