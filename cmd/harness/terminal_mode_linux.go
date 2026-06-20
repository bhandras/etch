//go:build linux

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// enableRawTerminal switches file into minimal raw input mode.
func enableRawTerminal(file *os.File) (syscall.Termios, error) {
	current, err := terminalState(file)
	if err != nil {
		return syscall.Termios{}, err
	}
	next := rawTerminalState(current)

	return current, setTerminalState(file, next)
}

// restoreTerminal restores file to a previously captured terminal mode.
func restoreTerminal(file *os.File, state syscall.Termios) error {
	return setTerminalState(file, state)
}

// terminalState reads the terminal mode through the Linux termios ioctl.
func terminalState(file *os.File) (syscall.Termios, error) {
	var state syscall.Termios
	// #nosec G103 -- TCGETS requires passing a pointer to a local termios
	// struct so the kernel can write terminal mode bits.
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL, file.Fd(), uintptr(syscall.TCGETS),
		uintptr(
			unsafe.Pointer(&state),
		),
	)
	if errno != 0 {
		return syscall.Termios{}, errno
	}

	return state, nil
}

// setTerminalState writes the terminal mode through the Linux termios ioctl.
func setTerminalState(file *os.File, state syscall.Termios) error {
	// #nosec G103 -- TCSETS requires passing a pointer to a local termios
	// struct so the kernel can read terminal mode bits.
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL, file.Fd(), uintptr(syscall.TCSETS),
		uintptr(
			unsafe.Pointer(&state),
		),
	)
	if errno != 0 {
		return errno
	}

	return nil
}

// rawTerminalState returns a mode suitable for byte-at-a-time prompt input.
func rawTerminalState(state syscall.Termios) syscall.Termios {
	state.Lflag &^= syscall.ECHO | syscall.ICANON
	state.Cc[syscall.VMIN] = 1
	state.Cc[syscall.VTIME] = 0

	return state
}
