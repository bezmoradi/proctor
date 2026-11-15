package runner

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"proctor/internal/expr"
	"proctor/internal/model"
	"proctor/protocol"
	"proctor/protocol/schema"
)

//go:embed official.sum
var officialSum string

var (
	officialName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	sumHex       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	refPattern   = regexp.MustCompile(`\$\{([^}]*)\}`)
)

type host struct {
	name     string
	official string
	path     string
	sha      string
	origin   string
	client   *protocol.Client
	hello    protocol.HelloResult
	ops      map[string]protocol.Operation
	proofs   []Proof
	stderr   string
	dropped  int64
	closed   bool
	dead     bool
}

type session struct {
	opt   Options
	hosts map[string]*host
	scrub *scrubber
}

func pins() (map[string]string, error) {
	out := map[string]string{}
	for i, line := range strings.Split(officialSum, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || !sumHex.MatchString(f[0]) || !strings.HasPrefix(f[1], "proctor-driver-") {
			return nil, fmt.Errorf("the built-in official.sum line %d isn't \"<sha256>  proctor-driver-<name>\" (INT-17)", i+1)
		}
		if _, dup := out[f[1]]; dup {
			return nil, fmt.Errorf("the built-in official.sum lists %s twice (INT-17)", f[1])
		}
		out[f[1]] = f[0]
	}
	return out, nil
}

func fileSHA(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func driverEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func resolveConnection(name string, conn map[string]any, lookup func(string) (string, bool), probs *problems, s *scrubber) (any, protocol.Secrets) {
	secrets := protocol.Secrets{}
	if conn == nil {
		return nil, secrets
	}
	out := map[string]any{}
	for _, k := range sortedKeys(conn) {
		if !strings.HasSuffix(k, "_env") {
			out[k] = conn[k]
		}
	}
	for _, k := range sortedKeys(conn) {
		if !strings.HasSuffix(k, "_env") {
			continue
		}
		key := strings.TrimSuffix(k, "_env")
		env, ok := conn[k].(string)
		switch {
		case key == "":
			probs.add(ExitInvalid, "binding: drivers.%s: %q isn't a valid key; name the value before _env", name, k)
		case !ok || env == "":
			probs.add(ExitInvalid, "binding: drivers.%s.%s must name an environment variable", name, k)
		case out[key] != nil:
			probs.add(ExitInvalid, "binding: drivers.%s has both %s and %s; use one", name, key, k)
		default:
			v, ok := lookup(env)
			if !ok || v == "" {
				probs.add(ExitInvalid, "binding: drivers.%s.%s: environment variable %s is not set", name, k, env)
				continue
			}
			secrets[key] = v
			s.add(v)
		}
	}
	return out, secrets
}

func checkValue(sch any, v any) []string {
	if sch == nil {
		if v == nil {
			return nil
		}
		return []string{"$: the driver takes no value here; leave it out"}
	}
	return schema.Check(sch, v)
}

func openSession(ctx context.Context, cfg *model.Config, b *model.Binding, opt Options, s *scrubber) (*session, error) {
	sess := &session{opt: opt, hosts: map[string]*host{}, scrub: s}
	var probs problems
	sums, err := pins()
	if err != nil {
		probs.add(ExitIntegrity, "%v", err)
		return nil, probs.err(s)
	}
	for _, name := range sortedKeys(cfg.Drivers) {
		dc := cfg.Drivers[name]
		if !officialName.MatchString(dc.Official) {
			probs.add(ExitInvalid, "proctor.yaml: drivers.%s: official %q must name an official driver, such as http", name, dc.Official)
			continue
		}
		file := "proctor-driver-" + dc.Official
		h := &host{name: name, official: dc.Official, path: filepath.Join(opt.DriverDir, file)}
		sha, err := fileSHA(h.path)
		if err != nil {
			probs.add(ExitError, "driver %s: cannot read %s: %v", name, h.path, err)
			continue
		}
		h.sha = sha
		want, pinned := sums[file]
		switch {
		case pinned && want != sha:
			probs.add(ExitIntegrity, "driver %s: %s has checksum %s, but this proctor expects %s (INT-17)", name, h.path, sha, want)
			continue
		case !pinned && opt.Authoritative:
			probs.add(ExitIntegrity, "driver %s: %s isn't pinned in this proctor's official.sum, and an authoritative run refuses unpinned drivers (INT-17)", name, file)
			continue
		case pinned:
			h.origin = OriginOfficial
		default:
			h.origin = OriginUnpinned
		}
		sess.hosts[name] = h
	}
	conns := map[string]any{}
	secrets := map[string]protocol.Secrets{}
	if b != nil {
		for _, name := range sortedKeys(b.Drivers) {
			if _, ok := cfg.Drivers[name]; !ok {
				probs.add(ExitInvalid, "binding: driver %q is not declared in proctor.yaml", name)
			}
		}
		for _, name := range sortedKeys(cfg.Drivers) {
			conns[name], secrets[name] = resolveConnection(name, b.Drivers[name], opt.LookupEnv, &probs, s)
		}
		checkBindingAppLogs(cfg, b, &probs)
	}
	if err := probs.err(s); err != nil {
		return nil, err
	}
	for _, h := range sess.ordered() {
		sec := secrets[h.name]
		if sec == nil {
			sec = protocol.Secrets{}
		}
		sess.start(ctx, h, cfg.Drivers[h.name].Policy, conns[h.name], sec, &probs)
	}
	if err := probs.err(s); err != nil {
		sess.close()
		return nil, err
	}
	return sess, nil
}

func checkBindingAppLogs(cfg *model.Config, b *model.Binding, probs *problems) {
	switch {
	case cfg.AppLogs == nil && b.AppLogs != nil:
		probs.add(ExitInvalid, "binding: app_logs is set, but proctor.yaml has no app_logs, so the application's log would not be checked; remove app_logs from the binding or add it to proctor.yaml (EXE-10)")
	case cfg.AppLogs == nil:
	case b.AppLogs == nil || b.AppLogs.File == "":
		probs.add(ExitInvalid, "binding: app_logs.file is required, because proctor.yaml has app_logs: give the path of the application's log file (EXE-10)")
	case !filepath.IsAbs(b.AppLogs.File):
		probs.add(ExitInvalid, "binding: app_logs.file %q must be an absolute path", b.AppLogs.File)
	}
}

func (sess *session) ordered() []*host {
	out := make([]*host, 0, len(sess.hosts))
	for _, k := range sortedKeys(sess.hosts) {
		out = append(out, sess.hosts[k])
	}
	return out
}

func (sess *session) callCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, sess.opt.CallTimeout+callMargin)
}

func (sess *session) start(ctx context.Context, h *host, policy, conn any, secrets protocol.Secrets, probs *problems) {
	c, err := protocol.Start(ctx, h.path, nil, driverEnv())
	if err != nil {
		probs.add(ExitError, "driver %s: cannot start %s: %v", h.name, h.path, err)
		return
	}
	h.client = c
	cctx, cancel := sess.callCtx(ctx)
	err = c.Call(cctx, protocol.MethodHello, protocol.HelloParams{Protocol: protocol.Version, CoreVersion: CoreVersion}, &h.hello)
	cancel()
	if err != nil {
		probs.add(ExitError, "driver %s: hello failed: %v%s", h.name, err, stderrHint(c))
		return
	}
	if msgs := checkHello(h); len(msgs) > 0 {
		for _, m := range msgs {
			probs.add(ExitError, "driver %s: %s", h.name, m)
		}
		return
	}
	bad := false
	for _, m := range checkValue(h.hello.PolicySchema, policy) {
		probs.add(ExitInvalid, "proctor.yaml: drivers.%s.policy%s", h.name, strings.TrimPrefix(m, "$"))
		bad = true
	}
	for _, m := range checkValue(h.hello.ConnectionSchema, conn) {
		probs.add(ExitInvalid, "binding: drivers.%s%s", h.name, strings.TrimPrefix(m, "$"))
		bad = true
	}
	if bad {
		return
	}
	cctx, cancel = sess.callCtx(ctx)
	err = c.Call(cctx, protocol.MethodConfigure, protocol.ConfigureParams{Policy: policy, Connection: conn, Secrets: secrets}, nil)
	cancel()
	if err != nil {
		code := ExitInvalid
		if brokenClient(err) || asProtocolError(err).Data.Cause == protocol.CauseDriverFailed {
			code = ExitError
		}
		probs.add(code, "driver %s: configure was refused: %v", h.name, err)
	}
}

func stderrHint(c *protocol.Client) string {
	b := strings.TrimSpace(string(c.Stderr()))
	if b == "" {
		return ""
	}
	if i := strings.LastIndexByte(b, '\n'); i >= 0 {
		b = b[i+1:]
	}
	if len(b) > 200 {
		b = b[:200]
	}
	return "; its last log line: " + b
}

func checkHello(h *host) []string {
	var msgs []string
	r := h.hello
	if r.Protocol != protocol.Version {
		msgs = append(msgs, fmt.Sprintf("speaks protocol %d; this proctor speaks protocol %d", r.Protocol, protocol.Version))
	}
	for what, sch := range map[string]any{"policy_schema": r.PolicySchema, "connection_schema": r.ConnectionSchema} {
		if sch == nil {
			continue
		}
		for _, e := range schema.CheckSchema(sch) {
			msgs = append(msgs, what+": "+e)
		}
	}
	h.ops = map[string]protocol.Operation{}
	for _, op := range r.Operations {
		where := fmt.Sprintf("operation %q", op.Name)
		if op.Name == "" {
			msgs = append(msgs, "an operation has no name")
			continue
		}
		if _, dup := h.ops[op.Name]; dup {
			msgs = append(msgs, where+" is listed twice")
			continue
		}
		switch op.Kind {
		case protocol.KindAct, protocol.KindObserve, protocol.KindByArgs:
		default:
			msgs = append(msgs, fmt.Sprintf("%s: unknown kind %q", where, op.Kind))
		}
		switch op.Credential {
		case protocol.CredentialNone, protocol.CredentialOptional, protocol.CredentialRequired:
		default:
			msgs = append(msgs, fmt.Sprintf("%s: unknown credential %q", where, op.Credential))
		}
		for what, sch := range map[string]any{"args_schema": op.ArgsSchema, "fact_schema": op.FactSchema} {
			if sch == nil {
				continue
			}
			for _, e := range schema.CheckSchema(sch) {
				msgs = append(msgs, where+": "+what+": "+e)
			}
		}
		for _, p := range sortedKeys(op.Escape) {
			if _, err := expr.ParsePath(p); err != nil {
				msgs = append(msgs, fmt.Sprintf("%s: escape: %v", where, err))
			}
			if op.Escape[p] != protocol.EscapePathSegment {
				msgs = append(msgs, fmt.Sprintf("%s: escape %s: unknown escaping %q (this proctor knows %q)", where, p, op.Escape[p], protocol.EscapePathSegment))
			}
		}
		for _, p := range op.Redact {
			if _, err := expr.ParsePath(p); err != nil {
				msgs = append(msgs, fmt.Sprintf("%s: redact: %v", where, err))
			}
		}
		for _, p := range op.Data {
			if _, err := expr.ParsePath(p); err != nil {
				msgs = append(msgs, fmt.Sprintf("%s: data: %v", where, err))
			}
		}
		h.ops[op.Name] = op
	}
	sort.Strings(msgs)
	return msgs
}

func (sess *session) close() {
	for _, h := range sess.ordered() {
		if h.closed || h.client == nil {
			continue
		}
		h.closed = true
		_ = h.client.Close()
		raw := sess.scrub.clean(string(h.client.Stderr()))
		h.dropped = h.client.StderrDropped()
		if len(raw) > stderrKeep {
			cut := len(raw) - stderrKeep
			if i := strings.IndexByte(raw[cut:], '\n'); i >= 0 {
				cut += i + 1
			}
			h.dropped += int64(cut)
			raw = raw[cut:]
		}
		h.stderr = raw
	}
}

func (sess *session) infos() []DriverInfo {
	var out []DriverInfo
	for _, h := range sess.ordered() {
		d := DriverInfo{Name: h.name, Official: h.official, Version: h.hello.Version, Origin: h.origin, SHA256: h.sha,
			Proofs: h.proofs, Stderr: h.stderr, StderrDropped: h.dropped}
		out = append(out, d)
	}
	return out
}

func Describe(ctx context.Context, cfg *model.Config, specs []SpecFile, b *model.Binding, opt Options) (map[model.StepRef]model.StepInfo, []DriverInfo, error) {
	opt, err := opt.withDefaults()
	if err != nil {
		return nil, nil, &SetupError{ExitCode: ExitError, Msgs: []string{err.Error()}}
	}
	s := &scrubber{}
	sess, err := openSession(ctx, cfg, b, opt, s)
	if err != nil {
		return nil, nil, err
	}
	infos, err := sess.describe(ctx, specs)
	sess.close()
	if err != nil {
		return nil, nil, err
	}
	return infos, sess.infos(), nil
}

func (sess *session) describe(ctx context.Context, specs []SpecFile) (map[model.StepRef]model.StepInfo, error) {
	out := map[model.StepRef]model.StepInfo{}
	var probs problems
	for _, f := range specs {
		if f.Spec == nil {
			continue
		}
		for _, sc := range f.Spec.Scenarios {
			for _, part := range []struct {
				phase string
				steps []model.Step
			}{{"setup", sc.Setup}, {"steps", sc.Steps}} {
				phase := part.phase
				for i, st := range part.steps {
					if name, _, ok := model.SplitDo(st.Do); !ok || sess.hosts[name] == nil {
						continue
					}
					info, err := sess.validateStep(ctx, st, phase == "setup")
					if err != nil {
						probs.add(ExitError, "%s: scenario %s %s[%d]: %v", f.Path, sc.ID, phase, i, err)
						continue
					}
					out[model.StepRef{Spec: f.Path, Scenario: sc.ID, Phase: phase, Index: i}] = info
				}
			}
		}
	}
	if err := probs.err(sess.scrub); err != nil {
		return nil, err
	}
	return out, nil
}

func (sess *session) validateStep(ctx context.Context, st model.Step, setup bool) (model.StepInfo, error) {
	info := model.StepInfo{StateCheck: true}
	problem := func(rule, path, format string, args ...any) {
		info.Problems = append(info.Problems, model.Problem{Rule: rule, Path: path, Message: sess.scrub.clean(fmt.Sprintf(format, args...))})
	}
	name, opName, ok := model.SplitDo(st.Do)
	if !ok {
		problem("EXE-01", "do", "do %q must be <driver>.<operation>", st.Do)
		return info, nil
	}
	h := sess.hosts[name]
	if h == nil {
		problem("EXE-01", "do", "driver %q isn't declared in proctor.yaml", name)
		return info, nil
	}
	op, ok := h.ops[opName]
	if !ok {
		problem("EXE-01", "do", "driver %s has no operation %q (it has %s)", name, opName, strings.Join(sortedKeys(h.ops), ", "))
		return info, nil
	}
	info.OpensHandle, info.UsesHandle = op.OpensHandle, op.UsesHandle
	switch {
	case st.Variant != "" && st.Identity == "":
		problem("EXE-01", "variant", "variant %q needs an identity", st.Variant)
	case st.Identity != "" && op.Credential == protocol.CredentialNone:
		problem("EXE-01", "identity", "operation %s takes no credential, so it can't have an identity", st.Do)
	case st.Identity == "" && op.Credential == protocol.CredentialRequired:
		problem("EXE-01", "identity", "operation %s needs a credential; name an identity", st.Do)
	}
	var argProblems []string
	if op.ArgsSchema == nil {
		if st.With != nil {
			argProblems = []string{"$: operation " + st.Do + " takes no arguments; leave with out"}
		}
	} else {
		argProblems = schema.Check(op.ArgsSchema, st.With, schema.AllowRefs)
	}
	for _, m := range argProblems {
		problem("EXE-01", "with", "with%s", strings.TrimPrefix(m, "$"))
	}
	var res protocol.ValidateResult
	cctx, cancel := sess.callCtx(ctx)
	err := h.client.Call(cctx, protocol.MethodValidate, protocol.ValidateParams{Op: opName, Args: st.With, Identity: st.Identity,
		Variant: st.Variant, Expect: st.Expect, Setup: setup, Save: st.Save}, &res)
	cancel()
	if err != nil {
		return info, fmt.Errorf("driver %s: validate failed: %v", name, err)
	}
	for _, p := range res.Problems {
		info.Problems = append(info.Problems, model.Problem{Rule: p.Rule, Message: sess.scrub.clean(p.Message), Path: p.Path})
	}
	switch op.Kind {
	case protocol.KindByArgs:
		switch res.Kind {
		case protocol.KindAct, protocol.KindObserve:
			info.Kind = res.Kind
		default:
			info.Kind = protocol.KindAct
			problem("EXE-01", "do", "driver %s answered kind %q for %s; expected act or observe", name, res.Kind, st.Do)
		}
	default:
		info.Kind = op.Kind
	}
	info.Destructive, info.FixedTarget = res.Destructive, res.FixedTarget
	info.Data = append([]string(nil), op.Data...)
	if res.StateCheck != nil {
		info.StateCheck = *res.StateCheck
	}
	seen := map[string]bool{}
	for _, n := range res.Refs {
		seen[n] = true
	}
	for _, n := range refsIn(st.With) {
		seen[n] = true
	}
	info.Refs = sortedKeys(seen)
	return info, nil
}

func refsIn(v any) []string {
	n, err := normalizeJSON(v)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var walk func(any)
	scan := func(s string) {
		for _, m := range refPattern.FindAllStringSubmatch(s, -1) {
			seen[m[1]] = true
		}
	}
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			scan(x)
		case map[string]any:
			for k, e := range x {
				scan(k)
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(n)
	return sortedKeys(seen)
}

func asProtocolError(err error) *protocol.Error {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe
	}
	return protocol.Errorf(protocol.CauseDriverFailed, "%v", err)
}

func brokenClient(err error) bool {
	return errors.Is(err, protocol.ErrDriverFailed) || errors.Is(err, protocol.ErrTimeout)
}
