//go:build unix

package commands

import "syscall"

const noFollow = syscall.O_NOFOLLOW
