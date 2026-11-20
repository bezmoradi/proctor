package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"time"
)

const drainGrace = 200 * time.Millisecond

type run struct {
	path  string
	argv  []string
	dir   string
	env   []string
	stdin string
	limit time.Duration
}

type outcome struct {
	exitCode    int
	duration    time.Duration
	timedOut    bool
	stdout      []byte
	stderr      []byte
	stdoutTotal int64
	stderrTotal int64
}

type capture struct {
	buf   []byte
	total int64
	done  chan struct{}
}

func newCapture() *capture {
	return &capture{done: make(chan struct{})}
}

func (c *capture) drain(r io.Reader) {
	defer close(c.done)
	b := make([]byte, 32<<10)
	for {
		n, err := r.Read(b)
		if n > 0 {
			c.total += int64(n)
			if room := streamLimit - len(c.buf); room > 0 {
				c.buf = append(c.buf, b[:min(n, room)]...)
			}
		}
		if err != nil {
			return
		}
	}
}

func execute(ctx context.Context, r run) (outcome, error) {
	var opened []*os.File
	closeAll := func() {
		for _, f := range opened {
			_ = f.Close()
		}
	}
	pipe := func() (*os.File, *os.File, error) {
		rd, wr, err := os.Pipe()
		if err == nil {
			opened = append(opened, rd, wr)
		}
		return rd, wr, err
	}
	outR, outW, err := pipe()
	if err != nil {
		return outcome{}, err
	}
	errR, errW, err := pipe()
	if err != nil {
		closeAll()
		return outcome{}, err
	}
	inR, inW, err := pipe()
	if err != nil {
		closeAll()
		return outcome{}, err
	}
	cmd := &exec.Cmd{Path: r.path, Args: r.argv, Dir: r.dir, Env: r.env, Stdin: inR, Stdout: outW, Stderr: errW}
	ownGroup(cmd)
	start := time.Now()
	if err := cmd.Start(); err != nil {
		closeAll()
		return outcome{}, err
	}
	_ = outW.Close()
	_ = errW.Close()
	_ = inR.Close()
	go func() {
		_, _ = io.WriteString(inW, r.stdin)
		_ = inW.Close()
	}()
	stdout, stderr := newCapture(), newCapture()
	go stdout.drain(outR)
	go stderr.drain(errR)
	waited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waited)
	}()
	timer := time.NewTimer(max(r.limit, 0))
	defer timer.Stop()
	timedOut := false
	select {
	case <-waited:
	case <-timer.C:
		timedOut = true
	case <-ctx.Done():
		timedOut = true
	}
	killGroup(cmd)
	<-waited
	duration := time.Since(start)
	grace := time.NewTimer(drainGrace)
	defer grace.Stop()
	for _, c := range []*capture{stdout, stderr} {
		select {
		case <-c.done:
		case <-grace.C:
			_ = outR.Close()
			_ = errR.Close()
			<-c.done
		}
	}
	_ = inW.Close()
	_ = outR.Close()
	_ = errR.Close()
	<-stdout.done
	<-stderr.done
	return outcome{
		exitCode:    cmd.ProcessState.ExitCode(),
		duration:    duration,
		timedOut:    timedOut,
		stdout:      stdout.buf,
		stderr:      stderr.buf,
		stdoutTotal: stdout.total,
		stderrTotal: stderr.total,
	}, nil
}
