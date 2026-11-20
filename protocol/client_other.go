//go:build !unix

package protocol

import "os/exec"

func ownGroup(_ *exec.Cmd) {}

func killGroup(cmd *exec.Cmd) {
	_ = cmd.Process.Kill()
}
