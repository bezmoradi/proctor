package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/proctor/protocol"
)

const (
	defaultDeadline = 10 * time.Second
	excerptBytes    = 1 << 10
)

type callArgs struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Stdin   *string  `json:"stdin"`
}

func (d *driver) Call(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	if p.Op != opRun {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "unknown operation %q; the cli driver has only %s", p.Op, opRun)
	}
	if d.repo == nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the cli driver has no connection")
	}
	if p.Credential != nil || p.Handle != "" {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "%s takes no credential and no handle", p.Op)
	}
	var a callArgs
	if err := protocol.Decode(p.Args, &a); err != nil {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "%s: with: %v", p.Op, err)
	}
	c := d.commands[a.Command]
	switch {
	case c == nil:
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "command %q isn't declared in the cli driver's policy (%s)", a.Command, argsRule)
	case len(a.Args) > c.ExtraArgs:
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "command %s takes at most %d arguments from a step, not %d (%s)", c.Name, c.ExtraArgs, len(a.Args), argsRule)
	case a.Stdin != nil && len(*a.Stdin) > stdinLimit:
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "stdin holds %d bytes; at most %d are allowed (%s)", len(*a.Stdin), stdinLimit, argsRule)
	}
	for _, s := range a.Args {
		if strings.ContainsRune(s, 0) {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "an argument holds a NUL character (%s)", argsRule)
		}
	}
	prog, err := d.repo.resolve(c)
	if err != nil {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "command %s: %v", c.Name, err)
	}
	dir, err := d.repo.workDir(c.Dir)
	if err != nil {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "command %s: dir: %v", c.Name, err)
	}
	argv := append(append([]string{c.Program}, c.Args...), a.Args...)
	if c.Kind == protocol.KindObserve {
		if err := d.repo.checkArgFiles(argv[1:], c.Dir); err != nil {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "command %s: %v", c.Name, err)
		}
	}
	limit := defaultDeadline
	if dl, ok := ctx.Deadline(); ok {
		limit = time.Until(dl)
	}
	if c.limit > 0 && c.limit < limit {
		limit = c.limit
	}
	stdin := ""
	if a.Stdin != nil {
		stdin = *a.Stdin
	}
	out, err := execute(ctx, run{path: prog, argv: argv, dir: dir, env: d.environ(c), stdin: stdin, limit: limit})
	if err != nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "command %s: cannot start %s: %v", c.Name, c.Program, err)
	}
	fact := out.fact(c.Name)
	return protocol.CallResult{Fact: fact, Evidence: protocol.Evidence{
		Summary:         summary(c, out),
		RequestExcerpt:  requestExcerpt(argv, dir, a.Stdin),
		ResponseExcerpt: responseExcerpt(out),
		DurationMS:      out.duration.Milliseconds(),
	}}, nil
}

func (d *driver) environ(c *command) []string {
	var env []string
	for _, k := range passedEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for _, k := range c.Env {
		env = append(env, k+"="+d.env[k])
	}
	return env
}

func (o outcome) fact(name string) map[string]any {
	stdout, stdoutCut := keep(o.stdout, o.stdoutTotal)
	stderr, stderrCut := keep(o.stderr, o.stderrTotal)
	fact := map[string]any{
		"command":          name,
		"exit_code":        o.exitCode,
		"duration_ms":      o.duration.Milliseconds(),
		"timed_out":        o.timedOut,
		"stdout_text":      stdout,
		"stdout_truncated": stdoutCut,
		"stderr_text":      stderr,
		"stderr_truncated": stderrCut,
		"overflow":         stdoutCut || stderrCut,
	}
	var why []string
	if stdoutCut {
		why = append(why, fmt.Sprintf("stdout wrote %d bytes, past the %d kept", o.stdoutTotal, streamLimit))
	}
	if stderrCut {
		why = append(why, fmt.Sprintf("stderr wrote %d bytes, past the %d kept", o.stderrTotal, streamLimit))
	}
	if len(why) > 0 {
		fact["overflow_reason"] = strings.Join(why, "; ")
	}
	if !stdoutCut {
		if v, ok := oneJSON(o.stdout); ok {
			fact["stdout_json"] = v
		}
	}
	return fact
}

func keep(b []byte, total int64) (string, bool) {
	cut := total > int64(len(b))
	if cut {
		for i := 0; i < utf8.UTFMax-1 && len(b) > 0; i++ {
			if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size != 1 {
				break
			}
			b = b[:len(b)-1]
		}
	}
	return string(b), cut
}

func oneJSON(b []byte) (any, bool) {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return v, true
}

func summary(c *command, o outcome) string {
	s := fmt.Sprintf("%s command %s exited %d in %d ms", c.Kind, c.Name, o.exitCode, o.duration.Milliseconds())
	if o.timedOut {
		s = fmt.Sprintf("%s command %s was killed with its process group at its deadline, after %d ms", c.Kind, c.Name, o.duration.Milliseconds())
	}
	if o.stdoutTotal > streamLimit || o.stderrTotal > streamLimit {
		s += "; output past 1 MiB was cut"
	}
	return s
}

func requestExcerpt(argv []string, dir string, stdin *string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = strconv.Quote(a)
	}
	s := fmt.Sprintf("exec [%s] in %s", strings.Join(quoted, " "), dir)
	if stdin != nil {
		s += fmt.Sprintf(" with %d bytes on stdin", len(*stdin))
	}
	return s
}

func responseExcerpt(o outcome) string {
	head := func(b []byte) string {
		if len(b) > excerptBytes {
			b = b[:excerptBytes]
		}
		return strings.ToValidUTF8(string(b), "�")
	}
	return fmt.Sprintf("stdout %s\nstderr %s", head(o.stdout), head(o.stderr))
}
