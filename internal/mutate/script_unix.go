//go:build unix

package mutate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const killGrace = 5 * time.Second

func Supported() error { return nil }

func runScript(ctx context.Context, path, workDir string, args, env []string, timeout time.Duration, out *os.File) Script {
	began := time.Now()
	cmd := exec.Command(path, args...)
	cmd.Dir, cmd.Env = workDir, env
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	s := Script{Path: path}
	if err := cmd.Start(); err != nil {
		s.ExitCode, s.Error = -1, err.Error()
		s.DurationMS = time.Since(began).Milliseconds()
		return s
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var err error
	select {
	case err = <-done:
	case <-timer.C:
		s.TimedOut = true
		err = killGroup(cmd.Process.Pid, done)
		s.Error = "timed out after " + timeout.String() + "; its process group was killed"
	case <-ctx.Done():
		s.Stopped = true
		err = killGroup(cmd.Process.Pid, done)
		s.Error = "interrupted; its process group was killed"
	}
	s.DurationMS = time.Since(began).Milliseconds()
	var exit *exec.ExitError
	switch {
	case err == nil:
		s.ExitCode = 0
	case errors.As(err, &exit):
		s.ExitCode = exit.ExitCode()
	default:
		s.ExitCode = -1
	}
	if s.Error == "" && err != nil {
		s.Error = err.Error()
	}
	return s
}

func killGroup(pid int, done chan error) error {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case err := <-done:
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		return err
	case <-time.After(killGrace):
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	return <-done
}
