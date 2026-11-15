package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"proctor/internal/expr"
	"proctor/internal/integrity"
	"proctor/internal/model"
	"proctor/protocol"
)

type Result string

const (
	Pass   Result = "PASS"
	Fail   Result = "FAIL"
	Error  Result = "ERROR"
	NotRun Result = "NOT_RUN"
)

const (
	ReasonSetupFailed         = "setup_failed"
	ReasonEarlierStepFailed   = "earlier_step_failed"
	ReasonMissingSavedValue   = "missing_saved_value"
	ReasonEarlierStepErrored  = "earlier_step_errored"
	ReasonEnvironmentNotReady = "environment_not_ready"
	ReasonAborted             = "aborted"
	ReasonTrustViolation      = "trust_violation"
)

const (
	CauseUnexpectedResponse = "unexpected_response"
	CauseUnreachable        = protocol.CauseUnreachable
	CauseTimeout            = protocol.CauseTimeout
	CauseUnreadableResponse = protocol.CauseUnreadableResponse
	CauseResponseTooLarge   = protocol.CauseResponseTooLarge
	CauseInvalidRequest     = protocol.CauseInvalidRequest
	CauseQueryError         = protocol.CauseQueryError
	CauseUnsupportedResult  = protocol.CauseUnsupportedResult
	CauseDriverFailed       = protocol.CauseDriverFailed
	CauseNotConfigured      = protocol.CauseNotConfigured
)

const (
	OriginOfficial = "official"
	OriginUnpinned = "unpinned"
)

const (
	PhaseSetup        = "setup"
	PhaseVerification = "verification"
)

const redacted = "[redacted]"

const (
	defaultCallTimeout  = 10 * time.Second
	defaultReadyTimeout = 30 * time.Second
	defaultEvery        = 500 * time.Millisecond
	defaultExcerpt      = 4096
	callMargin          = time.Second
	readyPause          = 250 * time.Millisecond
	stderrKeep          = 64 << 10
)

var CoreVersion = "dev"

type Assertion struct {
	Kind     string `json:"kind"`
	Target   string `json:"target,omitempty"`
	Claim    *int   `json:"claim,omitempty"`
	Expected any    `json:"expected,omitempty"`
	Actual   any    `json:"actual,omitempty"`
	Present  bool   `json:"present"`
	Result   Result `json:"result"`
	Message  string `json:"message,omitempty"`
}

type Action struct {
	Driver   string `json:"driver"`
	Op       string `json:"op"`
	Identity string `json:"identity,omitempty"`
	Variant  string `json:"variant,omitempty"`
	As       string `json:"as,omitempty"`
	On       string `json:"on,omitempty"`
	Summary  string `json:"summary,omitempty"`
}

type Evidence struct {
	RequestExcerpt   string `json:"request_excerpt,omitempty"`
	ResponseExcerpt  string `json:"response_excerpt,omitempty"`
	DriverDurationMS int64  `json:"driver_duration_ms,omitempty"`
}

type StepResult struct {
	ID         string      `json:"id"`
	Case       string      `json:"case,omitempty"`
	Phase      string      `json:"phase"`
	Result     Result      `json:"result"`
	Reason     string      `json:"reason,omitempty"`
	Cause      string      `json:"cause,omitempty"`
	Message    string      `json:"message,omitempty"`
	Driver     string      `json:"driver,omitempty"`
	Origin     string      `json:"origin,omitempty"`
	Action     *Action     `json:"action,omitempty"`
	Evidence   *Evidence   `json:"evidence,omitempty"`
	DurationMS float64     `json:"duration_ms,omitempty"`
	Samples    int         `json:"samples,omitempty"`
	Assertions []Assertion `json:"assertions,omitempty"`
	Saved      []string    `json:"saved,omitempty"`
}

type ScenarioResult struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Spec    string         `json:"specification"`
	Table   string         `json:"table,omitempty"`
	Result  Result         `json:"result"`
	Reason  string         `json:"reason,omitempty"`
	Cause   string         `json:"cause,omitempty"`
	Steps   []StepResult   `json:"steps"`
	Handles []HandleRecord `json:"handles,omitempty"`
}

type TableRow struct {
	Case   string   `json:"case"`
	Cells  []string `json:"cells"`
	Result Result   `json:"result"`
}

type TableResult struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Spec      string     `json:"specification"`
	SetupFrom string     `json:"setup_from,omitempty"`
	Columns   []string   `json:"columns"`
	Rows      []TableRow `json:"rows"`
	Result    Result     `json:"result"`
	Reason    string     `json:"reason,omitempty"`
}

type Summary struct {
	Scenarios  int `json:"scenarios"`
	Passed     int `json:"passed"`
	Failed     int `json:"failed"`
	Errors     int `json:"errors"`
	NotRun     int `json:"not_run"`
	Assertions int `json:"assertions"`
}

type Proof struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type DriverInfo struct {
	Name          string  `json:"name"`
	Official      string  `json:"official"`
	Version       string  `json:"version"`
	Origin        string  `json:"origin"`
	SHA256        string  `json:"sha256"`
	Proofs        []Proof `json:"proofs,omitempty"`
	Stderr        string  `json:"stderr,omitempty"`
	StderrDropped int64   `json:"stderr_dropped_bytes,omitempty"`
}

type Outcome struct {
	RunID      string           `json:"run_id"`
	Target     string           `json:"target"`
	Readiness  string           `json:"readiness,omitempty"`
	Violations []string         `json:"violations,omitempty"`
	Drivers    []DriverInfo     `json:"drivers,omitempty"`
	Scenarios  []ScenarioResult `json:"scenarios"`
	Tables     []TableResult    `json:"tables,omitempty"`
	Summary    Summary          `json:"summary"`
	ExitCode   int              `json:"exit_code"`
	Result     string           `json:"result"`
}

const (
	ExitError     = 2
	ExitInvalid   = 3
	ExitIntegrity = 4
)

type SetupError struct {
	ExitCode int
	Msgs     []string
}

func (e *SetupError) Error() string { return strings.Join(e.Msgs, "; ") }

type problems struct {
	code int
	msgs []string
}

func (p *problems) add(code int, format string, args ...any) {
	if code > p.code {
		p.code = code
	}
	p.msgs = append(p.msgs, fmt.Sprintf(format, args...))
}

func (p *problems) err(s *scrubber) error {
	if len(p.msgs) == 0 {
		return nil
	}
	msgs := make([]string, len(p.msgs))
	for i, m := range p.msgs {
		msgs[i] = s.clean(m)
	}
	return &SetupError{ExitCode: p.code, Msgs: msgs}
}

type SpecFile struct {
	Path string
	Spec *model.Spec
}

type Options struct {
	CallTimeout   time.Duration
	ReadyTimeout  time.Duration
	LookupEnv     func(string) (string, bool)
	Authoritative bool
	DriverDir     string
	Described     map[model.StepRef]model.StepInfo
	Progress      func(Progress)
}

type Progress struct {
	Index, Total int
	ID           string
	Done         bool
	Result       Result
	Elapsed      time.Duration
}

func (o Options) withDefaults() (Options, error) {
	if o.CallTimeout <= 0 {
		o.CallTimeout = defaultCallTimeout
	}
	if o.ReadyTimeout <= 0 {
		o.ReadyTimeout = defaultReadyTimeout
	}
	if o.LookupEnv == nil {
		o.LookupEnv = func(string) (string, bool) { return "", false }
	}
	if o.DriverDir == "" {
		exe, err := os.Executable()
		if err != nil {
			return o, fmt.Errorf("cannot find the folder of the proctor program: %v", err)
		}
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		o.DriverDir = filepath.Dir(exe)
	}
	abs, err := filepath.Abs(o.DriverDir)
	if err != nil {
		return o, fmt.Errorf("driver folder %s: %v", o.DriverDir, err)
	}
	o.DriverDir = abs
	return o, nil
}

type Runner struct {
	cfg      *model.Config
	specs    []SpecFile
	b        *model.Binding
	opt      Options
	sess     *session
	creds    map[string]protocol.Credential
	secrets  map[string]string
	values   map[string]string
	redactJS map[string]bool
	excerpt  int
	scrub    *scrubber
	info     map[model.StepRef]model.StepInfo
}

func credKey(identity, variant string) string { return identity + "\x00" + variant }

func New(cfg *model.Config, specs []SpecFile, b *model.Binding, opt Options) (*Runner, error) {
	opt, err := opt.withDefaults()
	if err != nil {
		return nil, &SetupError{ExitCode: ExitError, Msgs: []string{err.Error()}}
	}
	if b == nil {
		return nil, &SetupError{ExitCode: ExitInvalid, Msgs: []string{"a binding is required to run specifications"}}
	}
	r := &Runner{cfg: cfg, specs: specs, b: b, opt: opt, creds: map[string]protocol.Credential{}, secrets: map[string]string{},
		values: map[string]string{}, redactJS: map[string]bool{}, excerpt: defaultExcerpt, scrub: &scrubber{}}
	if cfg.Redaction != nil {
		for _, p := range cfg.Redaction.JSONPaths {
			r.redactJS[p] = true
		}
	}
	if cfg.Report != nil && cfg.Report.ExcerptBytes > 0 {
		r.excerpt = cfg.Report.ExcerptBytes
	}
	var probs problems
	r.loadCredentials(&probs)
	if err := probs.err(r.scrub); err != nil {
		return nil, err
	}
	sess, err := openSession(context.Background(), cfg, b, opt, r.scrub)
	if err != nil {
		return nil, err
	}
	r.sess = sess
	infos := opt.Described
	if infos == nil {
		infos, err = sess.describe(context.Background(), specs)
		if err != nil {
			sess.close()
			return nil, err
		}
	}
	checkDescribed(cfg, specs, infos, &probs)
	if err := probs.err(r.scrub); err != nil {
		sess.close()
		return nil, err
	}
	r.info = infos
	return r, nil
}

func checkDescribed(cfg *model.Config, specs []SpecFile, infos map[model.StepRef]model.StepInfo, probs *problems) {
	rep := &integrity.Report{Config: cfg}
	for _, f := range specs {
		if f.Spec != nil {
			rep.Specs = append(rep.Specs, integrity.SpecFile{Path: f.Path, Spec: f.Spec})
		}
	}
	rep.ApplyStepInfo(infos)
	for _, f := range rep.Findings {
		code := ExitInvalid
		if f.Kind == integrity.Violation {
			code = ExitIntegrity
		}
		probs.add(code, "%s: %s", f.Rule, strings.TrimPrefix(f.Path+": ", ": ")+f.Message)
	}
}

func (r *Runner) loadCredentials(probs *problems) {
	bad := func(format string, args ...any) { probs.add(ExitInvalid, format, args...) }
	lookup := r.opt.LookupEnv
	cfg, b := r.cfg, r.b
	for _, name := range sortedKeys(b.Identities) {
		id, ok := cfg.Identities[name]
		if !ok {
			bad("binding: identity %q is not declared in proctor.yaml", name)
			continue
		}
		for _, v := range sortedKeys(b.Identities[name].Variants) {
			if !contains(id.Variants, v) && !contains(id.AcceptedVariants, v) {
				bad("binding: identity %q: credential variant %q is not declared in proctor.yaml", name, v)
			}
		}
		for _, k := range sortedKeys(b.Identities[name].Values) {
			if !expr.VarName(k) {
				bad("binding: identity %q: value name %q must be letters, digits and _", name, k)
				continue
			}
			r.values[name+"."+k] = b.Identities[name].Values[k]
		}
	}
	owner := map[string]string{}
	load := func(who, key, env, scheme, prefix, identity, variant string) {
		tok, ok := lookup(env)
		switch {
		case !ok || tok == "":
			bad("%s: environment variable %s is not set", who, env)
		case strings.ContainsAny(tok, "\r\n"):
			bad("%s: the value in %s contains a line break", who, env)
		case owner[prefix+tok] != "":
			bad("%s and %s send the same credential; each identity and credential variant needs its own (AUTH-03)", owner[prefix+tok], who)
		default:
			owner[prefix+tok] = who
			r.creds[key] = protocol.Credential{Identity: identity, Variant: variant, Scheme: scheme, Value: tok}
			r.scrub.add(tok)
		}
	}
	idents, variants := r.usedIdentities()
	for _, name := range idents {
		it, ok := cfg.Identities[name]
		if !ok {
			bad("identity %q is used by a step but not declared in proctor.yaml", name)
			continue
		}
		switch it.Type {
		case "none":
		case "bearer":
			bi := b.Identities[name]
			if bi.TokenEnv == "" {
				bad("binding: identity %q (bearer) needs token_env", name)
				continue
			}
			load(fmt.Sprintf("identity %q", name), credKey(name, ""), bi.TokenEnv, protocol.SchemeBearer, "Bearer ", name, "")
		default:
			bad("identity %q: type %q is not supported yet (use none or bearer)", name, it.Type)
		}
	}
	for _, iv := range variants {
		if _, ok := cfg.Identities[iv[0]]; !ok {
			continue
		}
		bv, ok := b.Identities[iv[0]].Variants[iv[1]]
		who := fmt.Sprintf("identity %q variant %q", iv[0], iv[1])
		switch {
		case !ok || (bv.TokenEnv == "") == (bv.HeaderEnv == ""):
			bad("binding: identity %q needs exactly one of variants.%s.token_env or variants.%s.header_env", iv[0], iv[1], iv[1])
		case bv.TokenEnv != "":
			load(who, credKey(iv[0], iv[1]), bv.TokenEnv, protocol.SchemeBearer, "Bearer ", iv[0], iv[1])
		default:
			load(who, credKey(iv[0], iv[1]), bv.HeaderEnv, protocol.SchemeHeader, "", iv[0], iv[1])
		}
	}
	for _, name := range sortedKeys(b.Secrets) {
		if !contains(cfg.Secrets, name) {
			bad("binding: secret %q is not declared in proctor.yaml", name)
		}
	}
	for _, name := range r.usedSecrets() {
		if !contains(cfg.Secrets, name) {
			bad("secret %q is used by a step but not declared in proctor.yaml", name)
			continue
		}
		bs, ok := b.Secrets[name]
		if !ok || bs.Env == "" {
			bad("binding: secret %q needs env", name)
			continue
		}
		v, ok := lookup(bs.Env)
		if !ok || v == "" {
			bad("secret %q: environment variable %s is not set", name, bs.Env)
			continue
		}
		r.secrets["secret."+name] = v
		r.scrub.add(v)
	}
}

func allSteps(sc model.Scenario) []model.Step {
	return append(append([]model.Step{}, sc.Setup...), sc.Steps...)
}

func (r *Runner) usedIdentities() (idents []string, variants [][2]string) {
	seenI, seenV := map[string]bool{}, map[[2]string]bool{}
	for _, f := range r.specs {
		if f.Spec == nil {
			continue
		}
		for _, sc := range f.Spec.Scenarios {
			for _, st := range allSteps(sc) {
				if st.Identity == "" {
					continue
				}
				seenI[st.Identity] = true
				if st.Variant != "" {
					seenV[[2]string{st.Identity, st.Variant}] = true
				}
			}
		}
	}
	idents = sortedKeys(seenI)
	for v := range seenV {
		variants = append(variants, v)
	}
	sort.Slice(variants, func(i, j int) bool {
		return variants[i][0]+"\x00"+variants[i][1] < variants[j][0]+"\x00"+variants[j][1]
	})
	return idents, variants
}

func (r *Runner) usedSecrets() []string {
	seen := map[string]bool{}
	for _, f := range r.specs {
		if f.Spec == nil {
			continue
		}
		for _, sc := range f.Spec.Scenarios {
			for _, st := range allSteps(sc) {
				for _, v := range []any{st.With, st.Expect} {
					for _, n := range refsIn(v) {
						if expr.IsSecret(n) {
							seen[expr.SecretName(n)] = true
						}
					}
				}
			}
		}
	}
	return sortedKeys(seen)
}

func NewRunID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (r *Runner) Close() {
	if r.sess != nil {
		r.sess.close()
	}
}

func (r *Runner) Run(ctx context.Context) *Outcome {
	out := &Outcome{Target: r.b.Environment, RunID: NewRunID()}
	stopAll := ""
	if msg := r.waitReady(ctx); msg != "" {
		stopAll = ReasonEnvironmentNotReady
		out.Readiness = "not ready: " + msg
	} else {
		out.Readiness = "ready"
		v, msg := r.prove(ctx)
		switch {
		case msg != "":
			stopAll = ReasonEnvironmentNotReady
			out.Readiness = "not ready: " + msg
		case len(v) > 0:
			stopAll = ReasonTrustViolation
			out.Violations = v
		}
	}
	groups := map[string]*tableGroup{}
	total, n := 0, 0
	for _, f := range r.specs {
		if f.Spec != nil {
			total += len(f.Spec.Scenarios)
		}
	}
	for _, f := range r.specs {
		if f.Spec == nil {
			continue
		}
		p := f.Path
		for _, sc := range f.Spec.Scenarios {
			n++
			began := time.Now()
			r.progress(Progress{Index: n, Total: total, ID: sc.ID})
			var res ScenarioResult
			switch {
			case stopAll != "":
				res = notRunScenario(p, sc, stopAll)
			case ctx.Err() != nil:
				res = notRunScenario(p, sc, ReasonAborted)
			case sc.Case != nil:
				key := p + "\x00" + sc.Case.Group
				if groups[key] == nil {
					groups[key] = &tableGroup{}
				}
				res = r.tableCase(ctx, p, sc, out.RunID, groups[key])
			default:
				res = r.scenario(ctx, p, sc, out.RunID)
			}
			if sc.Case != nil {
				res.Table = sc.Case.Table
				for i := range res.Steps {
					res.Steps[i].Case = sc.ID
				}
				out.addTableRow(p, sc, res)
			}
			out.Scenarios = append(out.Scenarios, res)
			r.progress(Progress{Index: n, Total: total, ID: sc.ID, Done: true, Result: res.Result, Elapsed: time.Since(began)})
		}
	}
	r.sess.close()
	out.Drivers = r.sess.infos()
	out.finish()
	r.scrubOutcome(out)
	return out
}

func (r *Runner) progress(p Progress) {
	if r.opt.Progress != nil {
		r.opt.Progress(p)
	}
}

func (r *Runner) baseVars(runID string) expr.Vars {
	vars := expr.Vars{expr.RunID: runID}
	for k, v := range r.secrets {
		vars[k] = v
	}
	for k, v := range r.values {
		vars[k] = v
	}
	return vars
}

func (r *Runner) waitReady(ctx context.Context) string {
	rctx, cancel := context.WithTimeout(ctx, r.opt.ReadyTimeout)
	defer cancel()
	for _, h := range r.sess.ordered() {
		last := "no answer"
		for {
			var res protocol.ReadyResult
			cctx, ccancel := context.WithTimeout(rctx, r.opt.CallTimeout+callMargin)
			err := h.client.Call(cctx, protocol.MethodReady, protocol.Empty{}, &res)
			ccancel()
			if err == nil && res.Ready {
				break
			}
			if err != nil {
				pe := asProtocolError(err)
				switch {
				case pe.Data.Lasting:
					return fmt.Sprintf("driver %s: %s; not retried, because waiting won't fix it", h.name, pe.Message)
				case rctx.Err() != nil && ctx.Err() == nil:
					return fmt.Sprintf("driver %s: not ready after %s: %s", h.name, r.opt.ReadyTimeout, last)
				case brokenClient(err):
					return fmt.Sprintf("driver %s: %s", h.name, pe.Message)
				}
				last = pe.Data.Cause + ": " + pe.Message
			} else {
				if res.Lasting {
					return fmt.Sprintf("driver %s: %s; not retried, because waiting won't fix it", h.name, res.Detail)
				}
				last = res.Detail
				if last == "" {
					last = "the driver reports it isn't ready"
				}
			}
			select {
			case <-rctx.Done():
				if ctx.Err() != nil {
					return fmt.Sprintf("driver %s: the run was interrupted while waiting: %s", h.name, last)
				}
				return fmt.Sprintf("driver %s: not ready after %s: %s", h.name, r.opt.ReadyTimeout, last)
			case <-time.After(readyPause):
			}
		}
	}
	return ""
}

func (r *Runner) prove(ctx context.Context) ([]string, string) {
	var out []string
	for _, h := range r.sess.ordered() {
		var res protocol.ProveResult
		cctx, cancel := context.WithTimeout(ctx, r.opt.CallTimeout+callMargin)
		err := h.client.Call(cctx, protocol.MethodProve, protocol.Empty{}, &res)
		cancel()
		if err != nil {
			pe := asProtocolError(err)
			return nil, fmt.Sprintf("driver %s: proving its guarantees: %s: %s", h.name, pe.Data.Cause, pe.Message)
		}
		const shown = 50
		failed := 0
		for _, p := range res.Proofs {
			h.proofs = append(h.proofs, Proof{Name: p.Name, Status: p.Status, Detail: p.Detail})
			switch p.Status {
			case protocol.ProofProven, protocol.ProofUnproven:
				continue
			}
			failed++
			if failed > shown {
				continue
			}
			if p.Status == protocol.ProofFailed {
				out = append(out, fmt.Sprintf("driver %s: proof %s failed: %s", h.name, p.Name, p.Detail))
			} else {
				out = append(out, fmt.Sprintf("driver %s: proof %s has unknown status %q, counted as failed: %s", h.name, p.Name, p.Status, p.Detail))
			}
		}
		if failed > shown {
			out = append(out, fmt.Sprintf("driver %s: and %d more failed proofs", h.name, failed-shown))
		}
	}
	return out, ""
}

type tableGroup struct {
	ran   bool
	ok    bool
	cause string
	vars  expr.Vars
}

func (r *Runner) tableCase(ctx context.Context, p string, sc model.Scenario, runID string, g *tableGroup) ScenarioResult {
	hs := newHandleSet()
	res := r.tableCaseSteps(ctx, p, sc, runID, g, hs)
	res.Handles = r.closeHandles(ctx, hs)
	return res
}

func (r *Runner) tableCaseSteps(ctx context.Context, p string, sc model.Scenario, runID string, g *tableGroup, hs *handleSet) ScenarioResult {
	res := ScenarioResult{ID: sc.ID, Name: sc.Name, Spec: p, Result: Pass}
	if !g.ran {
		g.ran, g.ok = true, true
		g.vars = r.baseVars(runID + "-" + sc.Case.Group)
		for _, st := range sc.Setup {
			if !g.ok {
				res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: PhaseSetup, Result: NotRun, Reason: ReasonSetupFailed})
				continue
			}
			sr := r.step(ctx, st, StepResult{ID: st.ID, Phase: PhaseSetup}, g.vars, hs)
			if sr.Result != Pass {
				sr.Result, sr.Reason = Error, ReasonSetupFailed
				g.ok, g.cause = false, sr.Cause
			}
			res.Steps = append(res.Steps, sr)
		}
	}
	if !g.ok {
		res.Result, res.Reason, res.Cause = NotRun, ReasonSetupFailed, g.cause
		for _, st := range sc.Steps {
			res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: PhaseVerification, Result: NotRun, Reason: ReasonSetupFailed})
		}
		return res
	}
	vars := expr.Vars{}
	for k, v := range g.vars {
		vars[k] = v
	}
	stop, failed := "", false
	for i, st := range sc.Steps {
		if stop != "" {
			res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: PhaseVerification, Result: NotRun, Reason: stop})
			continue
		}
		if failed {
			if reason, msg := r.afterFailure(p, sc.ID, i, st, vars, hs); reason != "" {
				res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: PhaseVerification, Result: NotRun, Reason: reason, Message: msg})
				continue
			}
		}
		if ctx.Err() != nil {
			stop = ReasonAborted
			if !failed {
				res.Result, res.Reason = Error, ReasonAborted
			}
			res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: PhaseVerification, Result: NotRun, Reason: stop})
			continue
		}
		sr := r.step(ctx, st, StepResult{ID: st.ID, Phase: PhaseVerification}, vars, hs)
		switch sr.Result {
		case Fail:
			res.Result, failed = Fail, true
		case Error:
			if !failed {
				res.Result, res.Cause = Error, sr.Cause
			}
			stop = ReasonEarlierStepErrored
		}
		res.Steps = append(res.Steps, sr)
	}
	return res
}

func (r *Runner) scenario(ctx context.Context, p string, sc model.Scenario, runID string) ScenarioResult {
	res := ScenarioResult{ID: sc.ID, Name: sc.Name, Spec: p, Result: Pass}
	vars := r.baseVars(runID + "-" + sc.ID)
	hs := newHandleSet()
	stop, failed := "", false
	run := func(i int, st model.Step, phase string) {
		if stop != "" {
			res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: phase, Result: NotRun, Reason: stop})
			return
		}
		if failed {
			if reason, msg := r.afterFailure(p, sc.ID, i, st, vars, hs); reason != "" {
				res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: phase, Result: NotRun, Reason: reason, Message: msg})
				return
			}
		}
		if ctx.Err() != nil {
			stop = ReasonAborted
			if !failed {
				res.Result, res.Reason = Error, ReasonAborted
			}
			res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: phase, Result: NotRun, Reason: stop})
			return
		}
		sr := r.step(ctx, st, StepResult{ID: st.ID, Phase: phase}, vars, hs)
		switch {
		case phase == PhaseSetup && sr.Result != Pass:
			sr.Result, sr.Reason = Error, ReasonSetupFailed
			res.Result, res.Reason, res.Cause = Error, ReasonSetupFailed, sr.Cause
			stop = ReasonSetupFailed
		case sr.Result == Fail:
			res.Result, failed = Fail, true
		case sr.Result == Error:
			if !failed {
				res.Result, res.Cause = Error, sr.Cause
			}
			stop = ReasonEarlierStepErrored
		}
		res.Steps = append(res.Steps, sr)
	}
	for i, st := range sc.Setup {
		run(i, st, PhaseSetup)
	}
	for i, st := range sc.Steps {
		run(i, st, PhaseVerification)
	}
	res.Handles = r.closeHandles(ctx, hs)
	return res
}

func (r *Runner) afterFailure(p, scenario string, i int, st model.Step, vars expr.Vars, hs *handleSet) (string, string) {
	in, ok := r.info[model.StepRef{Spec: p, Scenario: scenario, Phase: "steps", Index: i}]
	if !ok || in.Kind != protocol.KindObserve {
		return ReasonEarlierStepFailed, ""
	}
	if st.On != "" && hs.open[st.On] == nil {
		return ReasonEarlierStepFailed, fmt.Sprintf("on %q: the handle isn't open, because an earlier step failed or didn't run", st.On)
	}
	var missing []string
	for _, v := range []any{st.With, st.Expect} {
		for _, name := range refsIn(v) {
			ref := "${" + name + "}"
			if _, set := vars[name]; expr.VarName(name) && !set && !contains(missing, ref) {
				missing = append(missing, ref)
			}
		}
	}
	switch len(missing) {
	case 0:
		return "", ""
	case 1:
		return ReasonMissingSavedValue, missing[0] + " wasn't saved, because an earlier step failed or didn't run"
	}
	return ReasonMissingSavedValue, strings.Join(missing, ", ") + " weren't saved, because an earlier step failed or didn't run"
}

func notRunScenario(p string, sc model.Scenario, reason string) ScenarioResult {
	res := ScenarioResult{ID: sc.ID, Name: sc.Name, Spec: p, Result: NotRun, Reason: reason}
	for _, st := range sc.Setup {
		res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: PhaseSetup, Result: NotRun, Reason: reason})
	}
	for _, st := range sc.Steps {
		res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: PhaseVerification, Result: NotRun, Reason: reason})
	}
	return res
}

var tableRank = map[Result]int{Pass: 0, NotRun: 1, Error: 2, Fail: 3}

func (o *Outcome) addTableRow(p string, sc model.Scenario, res ScenarioResult) {
	var t *TableResult
	for i := range o.Tables {
		if o.Tables[i].ID == sc.Case.Table && o.Tables[i].Spec == p {
			t = &o.Tables[i]
		}
	}
	if t == nil {
		o.Tables = append(o.Tables, TableResult{ID: sc.Case.Table, Name: sc.Name, Spec: p, SetupFrom: sc.Case.SetupFrom,
			Columns: sc.Case.Columns, Result: Pass})
		t = &o.Tables[len(o.Tables)-1]
	}
	t.Rows = append(t.Rows, TableRow{Case: sc.ID, Cells: sc.Case.Cells, Result: res.Result})
	result := res.Result
	if res.Reason == ReasonSetupFailed {
		result = Error
		t.Reason = ReasonSetupFailed
	}
	if tableRank[result] > tableRank[t.Result] {
		t.Result = result
	}
}

func (o *Outcome) finish() {
	s := &o.Summary
	for _, sc := range o.Scenarios {
		s.Scenarios++
		switch sc.Result {
		case Pass:
			s.Passed++
		case Fail:
			s.Failed++
		case Error:
			s.Errors++
		default:
			s.NotRun++
		}
		for _, st := range sc.Steps {
			s.Assertions += len(st.Assertions)
		}
	}
	switch {
	case len(o.Violations) > 0:
		o.ExitCode, o.Result = ExitIntegrity, "integrity_violation"
	case s.Failed > 0:
		o.ExitCode, o.Result = 1, "failed"
	case s.Errors > 0 || s.NotRun > 0 || s.Scenarios == 0:
		o.ExitCode, o.Result = ExitError, "error"
	default:
		o.ExitCode, o.Result = 0, "passed"
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
