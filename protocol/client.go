package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"sync"
	"time"
)

const (
	StderrLimit = 1 << 20
	closeGrace  = 2 * time.Second
)

var (
	ErrTimeout      = errors.New("the driver did not answer before the deadline")
	ErrDriverFailed = errors.New("the driver failed")
)

type Client struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	lines    chan []byte
	readErr  error
	stop     chan struct{}
	stopOnce sync.Once
	exited   chan struct{}
	waitErr  error
	stderr   *tail
	nextID   int
	broken   *Error
	closed   bool
}

func Start(ctx context.Context, path string, args []string, env []string) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command(path, args...)
	cmd.Env = append([]string{}, env...)
	cmd.WaitDelay = closeGrace
	ownGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c := &Client{
		cmd:    cmd,
		stdin:  stdin,
		lines:  make(chan []byte),
		stop:   make(chan struct{}),
		exited: make(chan struct{}),
		stderr: &tail{limit: StderrLimit},
	}
	cmd.Stderr = c.stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		c.read(stdout)
	}()
	go func() {
		<-readDone
		c.waitErr = cmd.Wait()
		close(c.exited)
	}()
	return c, nil
}

func (c *Client) read(stdout io.Reader) {
	defer close(c.lines)
	br := newReader(stdout)
	for {
		line, err := readLine(br)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.readErr = err
			}
			return
		}
		select {
		case c.lines <- line:
		case <-c.stop:
			return
		}
	}
}

func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.call(ctx, method, params, result)
}

func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	if c.broken != nil {
		return c.broken
	}
	if params == nil {
		params = Empty{}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return &Error{Code: CodeInvalidParams, Message: fmt.Sprintf("cannot encode %s params: %v", method, err), Data: ErrorData{Cause: CauseInvalidRequest}}
	}
	c.nextID++
	id := c.nextID
	b, err := json.Marshal(Request{JSONRPC: JSONRPC, ID: id, Method: method, Params: raw})
	if err != nil {
		return &Error{Code: CodeInvalidParams, Message: fmt.Sprintf("cannot encode %s: %v", method, err), Data: ErrorData{Cause: CauseInvalidRequest}}
	}
	if len(b) > MaxMessage {
		return &Error{Code: CodeInvalidRequest, Message: fmt.Sprintf("the %s request is larger than 16 MiB", method), Data: ErrorData{Cause: CauseInvalidRequest}}
	}
	b = append(b, '\n')
	written := make(chan error, 1)
	go func() {
		_, err := c.stdin.Write(b)
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			return c.fail("cannot send %s to the driver: %v%s", method, err, c.exitDetail())
		}
	case <-ctx.Done():
		return c.expire(ctx, method)
	}
	select {
	case line, ok := <-c.lines:
		if !ok {
			if errors.Is(c.readErr, errTooLarge) {
				return c.fail("the driver's answer to %s is larger than 16 MiB", method)
			}
			return c.fail("the driver stopped before answering %s%s", method, c.exitDetail())
		}
		return c.answer(line, id, method, result)
	case <-ctx.Done():
		return c.expire(ctx, method)
	}
}

func (c *Client) answer(line []byte, id int, method string, result any) error {
	env, err := envelope(line, "jsonrpc", "id", "result", "error")
	if err != nil {
		return c.fail("unreadable answer to %s: %v", method, err)
	}
	got, err := messageID(env)
	if err != nil {
		return c.fail("unreadable answer to %s: %v", method, err)
	}
	if got != id {
		return c.fail("the answer to %s has id %d, expected %d", method, got, id)
	}
	hasResult, hasError := present(env["result"]), present(env["error"])
	if hasResult == hasError {
		return c.fail("the answer to %s must have exactly one of result and error", method)
	}
	if hasError {
		var e Error
		if err := decodeStrict(env["error"], &e); err != nil {
			return c.fail("unreadable error in the answer to %s: %v", method, err)
		}
		if !slices.Contains(Causes, e.Data.Cause) {
			e.Message = fmt.Sprintf("the driver reported unknown cause %q: %s", e.Data.Cause, e.Message)
			e.Data.Cause = CauseDriverFailed
		}
		return &e
	}
	if result == nil {
		return nil
	}
	if err := decodeStrict(env["result"], result); err != nil {
		return &Error{Code: CodeOperationFailed, Message: fmt.Sprintf("unreadable result of %s: %v", method, err), Data: ErrorData{Cause: CauseDriverFailed}, local: ErrDriverFailed}
	}
	return nil
}

func (c *Client) expire(ctx context.Context, method string) error {
	c.kill()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		c.broken = &Error{Code: CodeOperationFailed, Message: fmt.Sprintf("the driver was stopped after it didn't answer %s before the deadline", method), Data: ErrorData{Cause: CauseDriverFailed}, local: ErrDriverFailed}
		return &Error{Code: CodeOperationFailed, Message: fmt.Sprintf("the driver didn't answer %s before the deadline and was stopped", method), Data: ErrorData{Cause: CauseTimeout}, local: ErrTimeout}
	}
	c.broken = &Error{Code: CodeOperationFailed, Message: fmt.Sprintf("the driver was stopped because %s was cancelled", method), Data: ErrorData{Cause: CauseDriverFailed}, local: errors.Join(ErrDriverFailed, ctx.Err())}
	return c.broken
}

func (c *Client) fail(format string, args ...any) error {
	c.kill()
	c.broken = &Error{Code: CodeOperationFailed, Message: fmt.Sprintf(format, args...), Data: ErrorData{Cause: CauseDriverFailed}, local: ErrDriverFailed}
	return c.broken
}

func (c *Client) kill() {
	c.stopOnce.Do(func() { close(c.stop) })
	_ = c.stdin.Close()
	killGroup(c.cmd)
}

func (c *Client) exitDetail() string {
	select {
	case <-c.exited:
		if c.waitErr != nil {
			return " (" + c.waitErr.Error() + ")"
		}
		return " (it exited)"
	case <-time.After(200 * time.Millisecond):
		return ""
	}
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	var err error
	if c.broken == nil {
		ctx, cancel := context.WithTimeout(context.Background(), closeGrace)
		err = c.call(ctx, MethodShutdown, Empty{}, nil)
		cancel()
		_ = c.stdin.Close()
		select {
		case <-c.exited:
			if err == nil && c.waitErr != nil {
				err = fmt.Errorf("the driver exited after shutdown with %v", c.waitErr)
			}
		case <-time.After(closeGrace):
			if err == nil {
				err = errors.New("the driver didn't exit after shutdown and was killed")
			}
		}
	}
	c.kill()
	select {
	case <-c.exited:
	case <-time.After(closeGrace):
	}
	if c.broken == nil {
		c.broken = &Error{Code: CodeOperationFailed, Message: "the driver was closed", Data: ErrorData{Cause: CauseDriverFailed}, local: ErrDriverFailed}
	}
	return err
}

func (c *Client) Stderr() []byte {
	return c.stderr.bytes()
}

func (c *Client) StderrDropped() int64 {
	return c.stderr.droppedBytes()
}

type tail struct {
	mu      sync.Mutex
	buf     []byte
	limit   int
	dropped int64
	skip    bool
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if t.skip {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			t.dropped += int64(len(p))
			return n, nil
		}
		t.dropped += int64(i + 1)
		p = p[i+1:]
		t.skip = false
	}
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.limit; over > 0 {
		i := bytes.IndexByte(t.buf[over:], '\n')
		if i < 0 {
			t.dropped += int64(len(t.buf))
			t.buf = nil
			t.skip = true
			return n, nil
		}
		cut := over + i + 1
		t.dropped += int64(cut)
		t.buf = append([]byte(nil), t.buf[cut:]...)
	}
	return n, nil
}

func (t *tail) bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf...)
}

func (t *tail) droppedBytes() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dropped
}
