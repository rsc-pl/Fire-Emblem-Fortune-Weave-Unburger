//go:build !linux

package main

func isTTY(fd uintptr) bool { return false }
