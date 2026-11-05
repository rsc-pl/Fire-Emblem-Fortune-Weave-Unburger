//go:build linux

package main

import (
	"syscall"
	"unsafe"
)

type winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

// isTTY reports whether fd refers to a terminal that can report a window size.
func isTTY(fd uintptr) bool {
	var ws winsize
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)), 0, 0, 0)
	return errno == 0
}
