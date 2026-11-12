package tui

import (
	"errors"
	"fmt"
	"io"
	"syscall"

	"golang.org/x/term"
)

// keepTerminalMode runs a program that vpn hands the terminal to, then puts
// the terminal's mode back as it was, however the program ended. A program
// killed at a password prompt leaves echo off, and Bubble Tea keeps the mode
// it finds afterwards for every later program and for the shell after vpn
// quits. Input that is not a terminal has no mode to keep.
func keepTerminalMode(in io.Reader, run func() error) (err error) {
	file, ok := in.(interface{ Fd() uintptr })
	if !ok {
		return run()
	}
	fd := int(file.Fd())
	state, err := term.GetState(fd)
	if errors.Is(err, syscall.ENOTTY) {
		return run()
	}
	if err != nil {
		return fmt.Errorf("save terminal mode: %w", err)
	}
	defer func() {
		if restoreErr := term.Restore(fd, state); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore terminal mode: %w", restoreErr))
		}
	}()
	return run()
}
