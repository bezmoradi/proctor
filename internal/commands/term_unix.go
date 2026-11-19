//go:build darwin || freebsd || netbsd || openbsd || dragonfly || linux

package commands

import (
	"os"
	"syscall"
	"unsafe"
)

func isTerminal(f *os.File) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(termRequest), uintptr(unsafe.Pointer(&t)))
	return errno == 0
}
