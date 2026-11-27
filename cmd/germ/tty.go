package main

import (
	"errors"

	"golang.org/x/sys/unix"
)

func isTTY(fd int) bool {
	_, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	return err == nil
}

// rawMode gives stdin to the program key by key: no echo, line editing,
// signals (Ctrl+C is a key) or flow control (Ctrl+S is too), and Enter
// reads as \r. restore puts the terminal back.
func rawMode() (restore func(), err error) {
	old, err := unix.IoctlGetTermios(0, unix.TCGETS)
	if err != nil {
		return nil, errors.New("needs a terminal")
	}
	raw := *old
	raw.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG | unix.IEXTEN
	raw.Iflag &^= unix.IXON | unix.ICRNL
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(0, unix.TCSETS, &raw); err != nil {
		return nil, err
	}
	return func() { unix.IoctlSetTermios(0, unix.TCSETS, old) }, nil
}
