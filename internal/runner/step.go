package runner

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"proctor/internal/expr"
	"proctor/internal/model"
	"proctor/protocol"
	"proctor/protocol/schema"
)

const (
	modeEventually   = "eventually"
	modeConsistently = "consistently"
	modeNever        = "never"
)

type timing struct {
	mode   string
	window time.Duration
	every  time.Duration
}

func timingOf(st model.Step) (timing, error) {
	var t timing
	n := 0
	parse := func(mode, field, window, every string) error {
		n++
		t.mode = mode
		d, err := time.ParseDuration(window)
		if err != nil || d <= 0 {
			return fmt.Errorf("%s.%s %q is not a positive duration", mode, field, window)
		}
		t.window = d
		t.every = defaultEvery
		if every != "" {
			e, err := time.ParseDuration(every)
			if err != nil || e <= 0 {
				return fmt.Errorf("%s.every %q is not a positive duration", mode, every)
			}
			t.every = e
		}
		return nil
	}
	var err error
	if st.Eventually != nil {
		err = errors.Join(err, parse(modeEventually, "within", st.Eventually.Within, st.Eventually.Every))
	}
	if st.Consistently != nil {
		err = errors.Join(err, parse(modeConsistently, "for", st.Consistently.For, st.Consistently.Every))
	}
	if st.Never != nil {
		err = errors.Join(err, parse(modeNever, "for", st.Never.For, st.Never.Every))
	}
	if n > 1 {
		return t, errors.New("a step can have only one of eventually, consistently and never")
	}
	return t, err
}

func (t timing) done(pass bool, elapsed time.Duration) bool {
	switch t.mode {
	case modeEventually, modeNever:
		return pass || elapsed >= t.window
	case modeConsistently:
		return !pass || elapsed >= t.window
	}
	return true
}

type sampleResult struct {
	handle  string
	fact    any
	ev      protocol.Evidence
	asserts []Assertion
	pass    bool
}

func (r *Runner) step(ctx context.Context, st model.Step, sr StepResult, vars expr.Vars, hs *handleSet) StepResult {
	errorf := func(cause, format string, args ...any) StepResult {
		sr.Result, sr.Cause = Error, cause
		sr.Message = fmt.Sprintf(format, args...)
		return sr
	}
	name, opName, _ := model.SplitDo(st.Do)
	sr.Driver = name
	sr.Action = &Action{Driver: name, Op: opName, Identity: st.Identity, Variant: st.Variant, As: st.As, On: st.On}
	h := r.sess.hosts[name]
	if h == nil {
		return errorf(CauseInvalidRequest, "driver %q isn't declared in proctor.yaml", name)
	}
	sr.Origin = h.origin
	op, ok := h.ops[opName]
	if !ok {
		return errorf(CauseInvalidRequest, "driver %s has no operation %q", name, opName)
	}
	handle := ""
	if st.On != "" {
		ref, msg := hs.use(st.On, h)
		if ref == nil {
			return errorf(CauseInvalidRequest, "%s", msg)
		}
		handle = ref.id
	}
	if st.As != "" {
		hs.missing[st.As] = st.ID
	}
	args, err := argsFor(st.With, vars, op.Escape)
	if err != nil {
		return errorf(CauseInvalidRequest, "with: %v", err)
	}
	var cred *protocol.Credential
	if st.Identity != "" && op.Credential != protocol.CredentialNone {
		if c, ok := r.creds[credKey(st.Identity, st.Variant)]; ok {
			cred = &c
		} else if st.Variant != "" || r.cfg.Identities[st.Identity].Type != "none" {
			return errorf(CauseInvalidRequest, "identity %q has no loaded credential", st.Identity)
		}
	}
	checks, err := r.prepare(st.Expect, vars)
	if err != nil {
		return errorf(CauseInvalidRequest, "%v", err)
	}
	t, err := timingOf(st)
	if err != nil {
		return errorf(CauseInvalidRequest, "%v", err)
	}
	if t.mode == modeNever {
		for _, c := range checks {
			if c.perr != nil {
				return errorf(CauseInvalidRequest, "%s %s: %v", c.kind, c.target, c.perr)
			}
		}
	}
	start := time.Now()
	var last sampleResult
	n := 0
	for {
		s, perr, d := r.sample(ctx, h, op, args, cred, handle, checks)
		if s.handle != "" {
			if kerr := hs.keep(st, h, op, s.handle); kerr != nil && perr == nil {
				perr = kerr
			}
		}
		n++
		sr.DurationMS += float64(d.Microseconds()) / 1000
		if t.mode != "" {
			sr.Samples = n
		}
		if perr != nil {
			sr.Result, sr.Cause, sr.Message = Error, perr.Data.Cause, perr.Message
			if n > 1 {
				sr.Message = fmt.Sprintf("sample %d: %s", n, perr.Message)
			}
			if errors.Is(ctx.Err(), context.Canceled) {
				sr.Reason = ReasonAborted
			}
			return sr
		}
		last = s
		r.record(&sr, s.ev)
		elapsed := time.Since(start)
		if t.done(s.pass, elapsed) {
			break
		}
		wait := t.every
		if rem := t.window - elapsed; rem < wait {
			wait = rem
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			sr.Result, sr.Reason = Error, ReasonAborted
			sr.Message = fmt.Sprintf("the run was interrupted after %d samples", n)
			return sr
		case <-timer.C:
		}
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	sr.Result = Pass
	if t.mode == modeNever {
		for i := range last.asserts {
			a := &last.asserts[i]
			switch {
			case last.pass:
				a.Result = Fail
				a.Message = "held, but never requires that the expectations don't all hold"
			case a.Result != Pass:
				a.Result = Pass
				a.Message = "did not hold, as never requires: " + a.Message
			default:
				a.Message = "held, but not together with every other expectation"
			}
		}
		if last.pass {
			sr.Result = Fail
			sr.Message = fmt.Sprintf("the expectations all held at sample %d, after %s; never requires that they don't for %s", n, elapsed, t.window)
		}
	} else if !last.pass {
		sr.Result = Fail
		switch t.mode {
		case modeEventually:
			sr.Message = fmt.Sprintf("the expectations didn't all hold within %s (%d samples); the last sample is shown", t.window, n)
		case modeConsistently:
			sr.Message = fmt.Sprintf("sample %d, after %s of the %s window, didn't meet the expectations", n, elapsed, t.window)
		}
	}
	sr.Assertions = append(sr.Assertions, last.asserts...)
	if sr.Result != Pass {
		sr.Cause = CauseUnexpectedResponse
		return sr
	}
	for _, name := range sortedKeys(st.Save) {
		path, err := expr.ParsePath(st.Save[name])
		if err != nil {
			return errorf(CauseInvalidRequest, "%v", err)
		}
		a := Assertion{Kind: "save", Target: name + " = " + path.String(), Result: Pass}
		if got, ok := path.Select(last.fact); !ok {
			a.Result, a.Message = Fail, "nothing to save: not present in the fact"
			sr.Result = Fail
		} else {
			a.Present = true
			r.scrubRedacted(path.String(), got, op.Redact)
			vars[name] = got
			sr.Saved = append(sr.Saved, name)
		}
		sr.Assertions = append(sr.Assertions, a)
	}
	if sr.Result != Pass {
		sr.Cause = CauseUnexpectedResponse
	}
	return sr
}

func (r *Runner) record(sr *StepResult, ev protocol.Evidence) {
	sr.Action.Summary = ev.Summary
	if ev.RequestExcerpt == "" && ev.ResponseExcerpt == "" && ev.DurationMS == 0 {
		sr.Evidence = nil
		return
	}
	sr.Evidence = &Evidence{RequestExcerpt: ev.RequestExcerpt, ResponseExcerpt: ev.ResponseExcerpt, DriverDurationMS: ev.DurationMS}
}

func (r *Runner) sample(ctx context.Context, h *host, op protocol.Operation, args any, cred *protocol.Credential, handle string, checks []check) (sampleResult, *protocol.Error, time.Duration) {
	cctx, cancel := context.WithTimeout(ctx, r.opt.CallTimeout+callMargin)
	defer cancel()
	var res protocol.CallResult
	p := protocol.CallParams{Op: op.Name, Args: args, Credential: cred, Handle: handle, DeadlineMS: r.opt.CallTimeout.Milliseconds()}
	t0 := time.Now()
	err := h.client.Call(cctx, protocol.MethodCall, p, &res)
	d := time.Since(t0)
	if err != nil {
		if brokenClient(err) {
			h.dead = true
		}
		return sampleResult{}, asProtocolError(err), d
	}
	if op.FactSchema != nil {
		if msgs := schema.Check(op.FactSchema, res.Fact); len(msgs) > 0 {
			if len(msgs) > 5 {
				msgs = append(msgs[:5], fmt.Sprintf("and %d more", len(msgs)-5))
			}
			return sampleResult{handle: res.Handle}, protocol.Errorf(protocol.CauseDriverFailed, "the fact doesn't match the fact_schema of %s: %s", op.Name, strings.Join(msgs, "; ")), d
		}
	}
	s := sampleResult{handle: res.Handle, fact: res.Fact, ev: res.Evidence, pass: true}
	for _, c := range checks {
		a := r.judge(c, res.Fact, op.Redact)
		if a.Result != Pass {
			s.pass = false
		}
		s.asserts = append(s.asserts, a)
	}
	return s, nil, d
}

func argsFor(with any, vars expr.Vars, escape map[string]string) (any, error) {
	if len(escape) == 0 {
		return vars.Value(with)
	}
	return escapedValue("$", with, vars, escape)
}

func escapedValue(path string, t any, vars expr.Vars, escape map[string]string) (any, error) {
	switch x := t.(type) {
	case string:
		if escape[path] == protocol.EscapePathSegment {
			return vars.Text(x, url.PathEscape)
		}
		return vars.Value(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			nk, err := vars.Text(k, nil)
			if err != nil {
				return nil, err
			}
			ne, err := escapedValue(path+"."+k, e, vars, escape)
			if err != nil {
				return nil, err
			}
			out[nk] = ne
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			ne, err := escapedValue(fmt.Sprintf("%s[%d]", path, i), e, vars, escape)
			if err != nil {
				return nil, err
			}
			out[i] = ne
		}
		return out, nil
	}
	return t, nil
}
