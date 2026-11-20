//go:build !unix

package mutate

import (
	"context"
	"errors"
	"os"
	"time"
)

func Supported() error {
	return errors.New("proctor mutate needs a Unix system, to stop a script's whole process group at its timeout")
}

func runScript(ctx context.Context, path, workDir string, args, env []string, timeout time.Duration, out *os.File) Script {
	return Script{Path: path, ExitCode: -1, Error: Supported().Error()}
}
