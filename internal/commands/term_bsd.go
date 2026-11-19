//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package commands

import "syscall"

const termRequest = syscall.TIOCGETA
