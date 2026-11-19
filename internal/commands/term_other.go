//go:build !(darwin || freebsd || netbsd || openbsd || dragonfly || linux)

package commands

import "os"

func isTerminal(*os.File) bool { return false }
