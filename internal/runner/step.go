package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/bezmoradi/proctor/protocol"
	"github.com/bezmoradi/proctor/protocol/schema"
	"proctor/internal/expr"
	"proctor/internal/model"
)

const (
	modeEventually   = "eventually"
	modeConsistently = "consistently"
	modeNever        = "never"
)

const SamplingNotice = "passed on samples: a change between samples, or after the window, can't be seen (TIME-05)"

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

func (t timing) settled(pass bool) bool {
	switch t.mode {
	case modeEventually, modeNever:
		return pass
	case modeConsistently:
		return !pass
	}
	return true
}

func (t timing) lastDue() int {
	if t.mode == "" {
		return 0
	}
	k := int(t.window / t.every)
	if t.window%t.every != 0 {
		k++
	}
	return k
}

func (t timing) due(k int) time.Duration {
	if d := time.Duration(k) * t.every; d < t.window {
		return d
	}
	return t.window
}

type sampleResult struct {
	handle  string
	fact    any
	ev      protocol.Evidence
	asserts []Assertion
	pass    bool
	vacuous bool
	leaks   []leak
}

type leak struct {
	path, what string
}

const leaksShown = 20

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
	r.scrubHeaders(args)
	r.keepWith(&sr, args)
	files, err := r.files(op, st.With)
	if err != nil {
		return errorf(CauseInvalidRequest, "%v", err)
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
	callTimeout, err := r.callTimeoutOf(st)
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
	n, k, skipped, lastDue := 0, 0, 0, t.lastDue()
	for {
		s, perr := r.sample(ctx, h, op, args, files, cred, handle, checks, callTimeout)
		if s.handle != "" {
			if kerr := hs.keep(st, h, op, s.handle); kerr != nil && perr == nil {
				perr = kerr
			}
		}
		n++
		sr.DurationMS = millis(time.Since(start))
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
		if len(s.leaks) > 0 {
			break
		}
		if t.settled(s.pass) || (s.vacuous && t.mode == modeNever) || k >= lastDue {
			break
		}
		next, elapsed := k+1, time.Since(start)
		for next < lastDue && t.due(next+1) <= elapsed {
			next++
		}
		skipped += next - k - 1
		sr.SamplesSkipped = skipped
		k = next
		timer := time.NewTimer(max(t.due(k)-time.Since(start), 0))
		select {
		case <-ctx.Done():
			timer.Stop()
			sr.DurationMS = millis(time.Since(start))
			sr.Result, sr.Reason = Error, ReasonAborted
			sr.Message = fmt.Sprintf("the run was interrupted after %d samples", n)
			return sr
		case <-timer.C:
		}
	}
	if len(last.leaks) > 0 {
		r.keepFact(&sr, last.fact, op.Redact, t)
		return leakFailure(sr, last.leaks, n, t)
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	vacuity := ""
	if t.mode == modeNever && last.vacuous {
		vacuity = vacuityOf(checks, last, op.Name)
	}
	r.keepFact(&sr, last.fact, op.Redact, t)
	sr.Result = Pass
	if t.mode == modeNever {
		for i := range last.asserts {
			a := &last.asserts[i]
			switch {
			case a.vacuous:
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
		switch {
		case last.vacuous:
			sr.Result = Fail
			sr.Message = fmt.Sprintf("at sample %d a claim can't be judged, because %s, so never can't tell whether the expectations held", n, vacuity)
		case last.pass:
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
		got, ok := path.Select(last.fact)
		kept, isCut := truncatedText(check{kind: "equal", target: path.String(), path: path}, last.fact, got)
		switch {
		case !ok:
			a.Result, a.Message = Fail, "nothing to save: not present in the fact"
			sr.Result = Fail
		case isCut:
			a.Present, a.CutAt = true, kept
			a.Result, a.Message = Fail, fmt.Sprintf("can't save %s: it was cut at %d bytes (body_truncated: true), so the saved value wouldn't be the whole text", path.String(), kept)
			sr.Result = Fail
		default:
			a.Present = true
			r.scrubRedacted(path.String(), got, op.Redact)
			vars[name] = got
			sr.Saved = append(sr.Saved, name)
		}
		sr.Assertions = append(sr.Assertions, a)
	}
	if sr.Result != Pass {
		sr.Cause = CauseUnexpectedResponse
	} else if t.mode == modeConsistently || t.mode == modeNever {
		sr.Notice = SamplingNotice
	}
	return sr
}

func leakFailure(sr StepResult, leaks []leak, n int, t timing) StepResult {
	first := leaks[0]
	sr.Result, sr.Reason = Fail, ReasonSecretInTelemetry
	sr.Message = fmt.Sprintf("%s is in the telemetry at %s (shown as %s): whoever can read the telemetry can read it", first.what, first.path, redacted)
	if t.mode != "" {
		sr.Message += fmt.Sprintf("; the step failed at sample %d without sampling again, since a value already sent can't be taken back", n)
	}
	if len(leaks) > 1 {
		sr.Message += fmt.Sprintf("; %d places hold a known value, listed below", len(leaks))
	}
	for _, l := range leaks {
		sr.Assertions = append(sr.Assertions, Assertion{Kind: "secret_scan", Target: l.path, Actual: redacted, Present: true, Result: Fail,
			Message: l.what + " is in this text"})
	}
	return sr
}

func (r *Runner) scanFact(paths []string, fact any) []leak {
	var out []leak
	var walk func(at string, v any)
	hit := func(at, text string) {
		if what, found := r.scrub.leaked(text); found && len(out) < leaksShown {
			out = append(out, leak{path: at, what: what})
		}
	}
	walk = func(at string, v any) {
		switch x := v.(type) {
		case string:
			hit(at, x)
		case json.Number:
			hit(at, x.String())
		case map[string]any:
			for _, k := range sortedKeys(x) {
				child := expr.Child(at, k)
				hit(child, k)
				walk(child, x[k])
			}
		case []any:
			for i, e := range x {
				walk(fmt.Sprintf("%s[%d]", at, i), e)
			}
		}
	}
	for _, p := range paths {
		parsed, err := expr.ParsePath(p)
		if err != nil {
			continue
		}
		if v, ok := parsed.Select(fact); ok {
			walk(parsed.String(), v)
		}
	}
	return out
}

func vacuityOf(checks []check, s sampleResult, op string) string {
	var why []string
	add := func(w string) {
		for _, seen := range why {
			if seen == w {
				return
			}
		}
		why = append(why, w)
	}
	for i, a := range s.asserts {
		if !a.vacuous || i >= len(checks) {
			continue
		}
		c := checks[i]
		got, _ := c.path.Select(s.fact)
		if text, bad := invalidBody(c, s.fact); bad {
			add(fmt.Sprintf("the response body isn't valid JSON (%s: %s)", invalidMarkName, text))
		} else if _, cut := truncatedText(c, s.fact, got); cut {
			add(fmt.Sprintf("the text of %s was cut (%s: true)", c.target, cutMarkName))
		} else if list, _, over := overflowed(c, s.fact, op); over {
			unit := "records"
			if isStream(s.fact) {
				unit = "frames"
			}
			add(fmt.Sprintf("the driver left %s out of %s (%s: true)", unit, list, overflowMarkName))
		} else if got, isList := got.([]any); c.kind == "excludes" && isList && len(got) == 0 {
			add(fmt.Sprintf("%s is an empty list, so excludes on it can't be judged", c.target))
		} else {
			add(fmt.Sprintf("a key of the claim on %s is in no element (EXE-11)", c.target))
		}
	}
	return strings.Join(why, " and ")
}

func (r *Runner) record(sr *StepResult, ev protocol.Evidence) {
	sr.Action.Summary = ev.Summary
	if ev.RequestExcerpt == "" && ev.ResponseExcerpt == "" && ev.DurationMS == 0 {
		sr.Evidence = nil
		return
	}
	sr.Evidence = &Evidence{RequestExcerpt: r.redactExcerpt(ev.RequestExcerpt), ResponseExcerpt: r.redactExcerpt(ev.ResponseExcerpt), DriverDurationMS: ev.DurationMS}
}

func (r *Runner) callTimeoutOf(st model.Step) (time.Duration, error) {
	if st.Timeout == "" {
		return r.opt.CallTimeout, nil
	}
	d, err := time.ParseDuration(st.Timeout)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("timeout %q is not a positive duration", st.Timeout)
	}
	limit, err := r.cfg.TimeLimits.MaxCallLimit()
	if err != nil {
		return 0, err
	}
	if d > limit {
		return 0, fmt.Errorf("timeout %s is longer than time_limits.max_call (%s)", d, limit)
	}
	return d, nil
}

func (r *Runner) files(op protocol.Operation, with any) (map[string]protocol.File, error) {
	files, probs := r.sess.readFiles(op, with)
	if len(probs) > 0 {
		return nil, fmt.Errorf("%s: %s", probs[0].Path, probs[0].Message)
	}
	for _, at := range sortedKeys(files) {
		f := files[at]
		want, ok := r.checked[f.Path]
		switch {
		case !ok:
			return nil, fmt.Errorf("%s: %s wasn't read when the specifications were validated", "with"+strings.TrimPrefix(at, "$"), f.Path)
		case want != f.SHA256:
			return nil, fmt.Errorf("%s: %s changed after the specifications were validated (sha256 %s, now %s); nothing was sent", "with"+strings.TrimPrefix(at, "$"), f.Path, want, f.SHA256)
		}
	}
	return files, nil
}

func (r *Runner) sample(ctx context.Context, h *host, op protocol.Operation, args any, files map[string]protocol.File, cred *protocol.Credential, handle string, checks []check, callTimeout time.Duration) (sampleResult, *protocol.Error) {
	cctx, cancel := context.WithTimeout(ctx, callTimeout+callMargin)
	defer cancel()
	var res protocol.CallResult
	p := protocol.CallParams{Op: op.Name, Args: args, Credential: cred, Handle: handle, DeadlineMS: callTimeout.Milliseconds(), Files: files}
	err := h.client.Call(cctx, protocol.MethodCall, p, &res)
	if err != nil {
		if brokenClient(err) {
			h.dead = true
		}
		return sampleResult{}, asProtocolError(err)
	}
	if op.FactSchema != nil {
		if msgs := schema.Check(op.FactSchema, res.Fact); len(msgs) > 0 {
			if len(msgs) > 5 {
				msgs = append(msgs[:5], fmt.Sprintf("and %d more", len(msgs)-5))
			}
			return sampleResult{handle: res.Handle}, protocol.Errorf(protocol.CauseDriverFailed, "the fact doesn't match the fact_schema of %s: %s", op.Name, strings.Join(msgs, "; "))
		}
	}
	if op.Accumulates {
		r.scrubRedacted("$", res.Fact, op.Redact)
	}
	r.scrubHeaders(res.Fact)
	s := sampleResult{handle: res.Handle, fact: res.Fact, ev: res.Evidence, pass: true, leaks: r.scanFact(op.Scan, res.Fact)}
	for _, c := range checks {
		a := r.judge(c, res.Fact, op.Redact, op.Name)
		if a.Result != Pass {
			s.pass = false
		}
		if a.vacuous {
			s.vacuous = true
		}
		s.asserts = append(s.asserts, a)
	}
	return s, nil
}

func millis(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
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
			ne, err := escapedValue(expr.Child(path, k), e, vars, escape)
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
