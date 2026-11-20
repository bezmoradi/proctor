//go:build !unix

package commands

import "io/fs"

func ownedByMe(fs.FileInfo) bool { return true }
