//go:build linux

package commands

import "syscall"

const termRequest = syscall.TCGETS
