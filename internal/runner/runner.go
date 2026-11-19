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

	"proctor/internal/applogs"
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
	ReasonAppLogs             = "app_logs"
	ReasonCleanupFailed       = "cleanup_failed"
	ReasonCleanupUnguarded    = "cleanup_unguarded"
	ReasonHandleNotOpen       = "handle_not_open"
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
	CauseAppLogsUnreadable  = "app_logs_unreadable"
	CauseAppLogsUnparsed    = "app_logs_unparsed"
	CauseAppLogsUnmapped    = "app_logs_unmapped_level"
)

const (
	OriginOfficial = "official"
	OriginUnpinned = "unpinned"
)

const (
	PhaseSetup        = "setup"
	PhaseVerification = "verification"
	PhaseCleanup      = "cleanup"
)

const redacted = "[redacted]"

const (
	defaultCallTimeout  = 10 * time.Second
	defaultReadyTimeout = 30 * time.Second
	defaultEvery        = 500 * time.Millisecond
	defaultExcerpt      = 4096
	callMargin          = time.Second
	readyPause          = 250 * time.Millisecond
	logFlushWait        = 300 * time.Millisecond
	logLinesShown       = 50
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
	Response string `json:"response,omitempty"`
	CutAt    int    `json:"cut_at,omitempty"`
	Overflow bool   `json:"overflow,omitempty"`
	vacuous  bool
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

	AfterSkippedAct bool `json:"after_skipped_act,omitempty"`

	rec *stepRecord
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
	AppLogs *AppLogResult  `json:"app_logs,omitempty"`

	FirstFailure   string            `json:"first_failure,omitempty"`
	Cleanup        Result            `json:"cleanup,omitempty"`
	CleanupFailure string            `json:"cleanup_failure,omitempty"`
	Transcript     *TranscriptResult `json:"transcript,omitempty"`
	ApprovedLog    *TranscriptResult `json:"approved_log,omitempty"`

	rec      *scenarioRecord
	logRec   *logRecord
	logVars  *scenarioRecord
	tableKey string
}

const AppLogCheck = "app-logs"

type AppLogResult struct {
	Check           string           `json:"check"`
	Result          Result           `json:"result"`
	Cause           string           `json:"cause,omitempty"`
	Message         string           `json:"message,omitempty"`
	Note            string           `json:"note,omitempty"`
	LinesRead       int              `json:"lines_read"`
	LinesParsed     int              `json:"lines_parsed"`
	UnparsedCount   int              `json:"unparsed_count"`
	FirstUnparsed   string           `json:"first_unparsed,omitempty"`
	UnmappedCount   int              `json:"unmapped_count"`
	UnmappedLevel   string           `json:"unmapped_level,omitempty"`
	FirstUnmapped   string           `json:"first_unmapped,omitempty"`
	Unexpected      []AppLogLine     `json:"unexpected,omitempty"`
	UnexpectedCount int              `json:"unexpected_count"`
	Expected        []AppLogExpected `json:"expected,omitempty"`

	window []string
	kept   bool
}

type AppLogLine struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Time    string `json:"time,omitempty"`
	Line    string `json:"line"`
}

type AppLogExpected struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Count   *int   `json:"count,omitempty"`
	Seen    int    `json:"seen"`
	Result  Result `json:"result"`
	Problem string `json:"problem,omitempty"`
}

type TableRow struct {
	Case   string   `json:"case"`
	Cells  []string `json:"cells"`
	Result Result   `json:"result"`
}

type TableResult struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Spec        string            `json:"specification"`
	SetupFrom   string            `json:"setup_from,omitempty"`
	Columns     []string          `json:"columns"`
	Rows        []TableRow        `json:"rows"`
	Result      Result            `json:"result"`
	Reason      string            `json:"reason,omitempty"`
	AppLogs     *AppLogResult     `json:"app_logs,omitempty"`
	ApprovedLog *TranscriptResult `json:"approved_log,omitempty"`

	key    string
	logRec *logRecord
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
	Received   string           `json:"received,omitempty"`
}

const (
	ExitError     = 2
	ExitInvalid   = 3
	ExitIntegrity = 4
)

var identityKeys = map[string]bool{"id": true, "user_id": true}

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
	RunID         string
	Received      ReceivedWriter
}

type Progress struct {
	Index, Total int
	ID           string
	Table        string
	Done         bool
	Result       Result
	Cleanup      Result
	Elapsed      time.Duration
}

func (o Options) withDefaults(cfg *model.Config) (Options, error) {
	var tl *model.TimeLimits
	if cfg != nil {
		tl = cfg.TimeLimits
	}
	limit, err := tl.MaxCallLimit()
	if err != nil {
		limit = model.DefaultMaxCall
	}
	if o.CallTimeout <= 0 {
		o.CallTimeout = min(defaultCallTimeout, limit)
	}
	if o.CallTimeout > limit {
		return o, &SetupError{ExitCode: ExitInvalid, Msgs: []string{fmt.Sprintf("--call-timeout %s is longer than time_limits.max_call (%s%s); raise max_call in proctor.yaml, where review sees it, or give the slow steps their own timeout (TIME-13)", o.CallTimeout, limit, defaultNote(tl))}}
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
	checked  map[string]string
	logs     applogs.Source
	logRules applogs.Rules
	logLoose bool
}

func defaultNote(tl *model.TimeLimits) string {
	if tl == nil || tl.MaxCall == "" {
		return ", the default when proctor.yaml doesn't set it"
	}
	return ""
}

func credKey(identity, variant string) string { return identity + "\x00" + variant }

func New(cfg *model.Config, specs []SpecFile, b *model.Binding, opt Options) (*Runner, error) {
	opt, err := opt.withDefaults(cfg)
	if err != nil {
		return nil, asSetup(err)
	}
	if b == nil {
		return nil, &SetupError{ExitCode: ExitInvalid, Msgs: []string{"a binding is required to run specifications"}}
	}
	r := &Runner{cfg: cfg, specs: specs, b: b, opt: opt, creds: map[string]protocol.Credential{}, secrets: map[string]string{},
		values: map[string]string{}, redactJS: map[string]bool{}, excerpt: defaultExcerpt, scrub: &scrubber{}}
	if cfg.Redaction != nil {
		for _, p := range cfg.Redaction.JSONPaths {
			r.redactJS[p] = true
			if c, ok := expr.Canonical(p); ok {
				r.redactJS[c] = true
			}
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
	if cfg.AppLogs != nil && b.AppLogs != nil {
		src := applogs.File{Path: b.AppLogs.File}
		if _, err := src.Mark(); err != nil {
			sess.close()
			return nil, &SetupError{ExitCode: ExitError, Msgs: []string{r.scrub.clean(fmt.Sprintf("app_logs: cannot read the application's log: %v", err))}}
		}
		r.logs = src
		r.logRules = applogs.Rules{Format: cfg.AppLogs.FormatName(), Levels: cfg.AppLogs.EffectiveLevels(), LevelField: cfg.AppLogs.LevelKey(),
			MessageField: cfg.AppLogs.MessageKey(), Numbers: cfg.AppLogs.NumberLevels()}
		r.logLoose = cfg.AppLogs.UnparsedAllowed()
	}
	infos := opt.Described
	if infos == nil {
		infos, err = sess.describe(context.Background(), specs)
		if err != nil {
			sess.close()
			return nil, err
		}
	}
	checkDescribed(cfg, specs, infos, sess.entries(), &probs)
	if err := probs.err(r.scrub); err != nil {
		sess.close()
		return nil, err
	}
	r.info = infos
	r.checked = map[string]string{}
	for _, in := range infos {
		for _, f := range in.Files {
			r.checked[f.Path] = f.SHA256
		}
	}
	return r, nil
}

func checkDescribed(cfg *model.Config, specs []SpecFile, infos map[model.StepRef]model.StepInfo, entries map[string][]model.PolicyEntry, probs *problems) {
	rep := &integrity.Report{Config: cfg}
	for _, f := range specs {
		if f.Spec != nil {
			rep.Specs = append(rep.Specs, integrity.SpecFile{Path: f.Path, Spec: f.Spec})
		}
	}
	rep.ApplyStepInfo(infos, entries)
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
	holder := map[string]string{}
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
			v := b.Identities[name].Values[k]
			r.values[name+"."+k] = v
			if !identityKeys[k] {
				continue
			}
			if other, taken := holder[k+"\x00"+v]; taken {
				bad("binding: identities %q and %q have the same %s value; each identity needs its own, or a scenario that keeps one user from another's data tests a user against themselves (AUTH-03)", other, name, k)
				continue
			}
			holder[k+"\x00"+v] = name
		}
	}
	for _, ref := range r.usedValues() {
		v, ok := b.Identities[ref[0]].Values[ref[1]]
		switch {
		case !ok:
			bad("binding: identity %q has no value %q, which a step uses as ${%s.%s}; add it under the identity's values (AUTH-03)", ref[0], ref[1], ref[0], ref[1])
		case strings.TrimSpace(v) == "":
			bad("binding: identity %q: value %q, which a step uses as ${%s.%s}, is empty (AUTH-03)", ref[0], ref[1], ref[0], ref[1])
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
	return append(append(append([]model.Step{}, sc.Setup...), sc.Steps...), sc.Cleanup...)
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

func (r *Runner) usedValues() [][2]string {
	seen := map[[2]string]bool{}
	for _, f := range r.specs {
		if f.Spec == nil {
			continue
		}
		for _, sc := range f.Spec.Scenarios {
			for _, st := range allSteps(sc) {
				for _, v := range []any{st.With, st.Expect} {
					for _, n := range refsIn(v) {
						i := strings.LastIndexByte(n, '.')
						if i <= 0 || i == len(n)-1 || n == expr.RunID || expr.IsSecret(n) {
							continue
						}
						if _, declared := r.cfg.Identities[n[:i]]; declared {
							seen[[2]string{n[:i], n[i+1:]}] = true
						}
					}
				}
			}
		}
	}
	out := make([][2]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0]+"\x00"+out[i][1] < out[j][0]+"\x00"+out[j][1] })
	return out
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
	runID := r.opt.RunID
	if runID == "" {
		runID = NewRunID()
	}
	out := &Outcome{Target: r.b.Environment, RunID: runID}
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
		var table *tableWindow
		for i, sc := range f.Spec.Scenarios {
			n++
			began := time.Now()
			tableLabel := ""
			if sc.Case != nil {
				tableLabel = sc.Case.Label()
			}
			r.progress(Progress{Index: n, Total: total, ID: sc.ID, Table: tableLabel})
			var res ScenarioResult
			switch {
			case stopAll != "":
				res = notRunScenario(p, sc, stopAll)
			case ctx.Err() != nil:
				res = notRunScenario(p, sc, ReasonAborted)
			case sc.Case != nil:
				key := p + "\x00" + sc.Case.GroupKey
				if groups[key] == nil {
					groups[key] = &tableGroup{}
				}
				if table == nil && r.logs != nil {
					table = &tableWindow{w: r.openLogs()}
				}
				res = r.tableCase(ctx, p, sc, out.RunID, groups[key])
			default:
				w := r.openLogs()
				res = r.scenario(ctx, p, sc, out.RunID)
				if w != nil {
					res.AppLogs = r.closeLogs(ctx, w, sc.ExpectLogs, sc.ApprovedLogWanted())
					applyLogs(res.AppLogs, &res.Result, &res.Reason, &res.Cause)
				}
				if sc.ApprovedLogWanted() {
					res.logRec = newLogRecord(res.AppLogs)
				}
			}
			if sc.Case != nil {
				res.Table, res.tableKey = sc.Case.Table, sc.Case.Key
				for i := range res.Steps {
					res.Steps[i].Case = sc.ID
				}
				out.addTableRow(p, sc, res)
			}
			out.Scenarios = append(out.Scenarios, res)
			done := Progress{Index: n, Total: total, ID: sc.ID, Table: tableLabel, Done: true, Result: res.Result, Cleanup: res.Cleanup, Elapsed: time.Since(began)}
			if table == nil {
				r.progress(done)
				continue
			}
			table.rows = append(table.rows, len(out.Scenarios)-1)
			table.done = append(table.done, done)
			if next := i + 1; next < len(f.Spec.Scenarios) && f.Spec.Scenarios[next].Case != nil && f.Spec.Scenarios[next].Case.Key == sc.Case.Key {
				continue
			}
			al := r.closeLogs(ctx, table.w, sc.ExpectLogs, sc.ApprovedLogWanted())
			out.applyTableLogs(p, sc.Case.Key, table.rows, al)
			if sc.ApprovedLogWanted() {
				out.keepTableLog(p, sc.Case.Key, table.rows, al)
			}
			for j, ev := range table.done {
				ev.Result = out.Scenarios[table.rows[j]].Result
				r.progress(ev)
			}
			table = nil
		}
	}
	r.sess.close()
	out.Drivers = r.sess.infos()
	own := ownResults(out)
	r.applyTranscripts(out)
	r.applyApprovedLogs(out, own)
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
	ran       bool
	ok        bool
	cause     string
	vars      expr.Vars
	setup     []StepResult
	rowFailed map[string]bool
}

func (r *Runner) tableCase(ctx context.Context, p string, sc model.Scenario, runID string, g *tableGroup) ScenarioResult {
	hs := newHandleSet()
	res, vars := r.tableCaseSteps(ctx, p, sc, runID, g, hs)
	cleanVars := vars
	if cleanVars == nil {
		cleanVars = expr.Vars{}
		for k, v := range g.vars {
			cleanVars[k] = v
		}
	}
	if g.rowFailed == nil {
		g.rowFailed = map[string]bool{}
	}
	r.cleanup(ctx, p, sc, &res, cleanVars, hs, g.ok && !g.rowFailed[sc.Case.Key])
	if res.Result != Pass {
		g.rowFailed[sc.Case.Key] = true
	}
	res.Handles = r.closeHandles(ctx, hs)
	r.recordTranscript(&res, sc, g.setup, vars)
	r.keepLogVars(&res, sc, vars)
	return res
}

func (r *Runner) tableCaseSteps(ctx context.Context, p string, sc model.Scenario, runID string, g *tableGroup, hs *handleSet) (ScenarioResult, expr.Vars) {
	res := ScenarioResult{ID: sc.ID, Name: sc.Name, Spec: p, Result: Pass}
	keep := r.wantsTranscript(sc)
	if !g.ran {
		g.ran, g.ok = true, true
		g.vars = r.baseVars(runID + "-" + sc.Case.RunScope)
		for _, st := range sc.Setup {
			if !g.ok {
				res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: PhaseSetup, Result: NotRun, Reason: ReasonSetupFailed})
				continue
			}
			sr := r.step(ctx, st, recording(StepResult{ID: st.ID, Phase: PhaseSetup}, keep), g.vars, hs)
			if sr.Result != Pass {
				sr.Result, sr.Reason = Error, ReasonSetupFailed
				g.ok, g.cause = false, sr.Cause
			}
			res.Steps = append(res.Steps, sr)
		}
		g.setup = setupResults(res.Steps)
	}
	if !g.ok {
		res.Result, res.Reason, res.Cause = NotRun, ReasonSetupFailed, g.cause
		for _, st := range sc.Steps {
			res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: PhaseVerification, Result: NotRun, Reason: ReasonSetupFailed})
		}
		return res, nil
	}
	vars := expr.Vars{}
	for k, v := range g.vars {
		vars[k] = v
	}
	stop, failed, skippedAct := "", false, false
	for i, st := range sc.Steps {
		if stop != "" {
			res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: PhaseVerification, Result: NotRun, Reason: stop})
			continue
		}
		if failed {
			if reason, msg := r.afterFailure(p, sc.ID, i, st, vars, hs); reason != "" {
				skippedAct = skippedAct || !r.observes(p, sc.ID, i)
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
		sr := r.step(ctx, st, recording(StepResult{ID: st.ID, Phase: PhaseVerification}, keep), vars, hs)
		switch sr.Result {
		case Fail:
			noteFailure(&res, &sr, skippedAct)
			res.Result, failed = Fail, true
		case Error:
			if !failed {
				res.Result, res.Cause = Error, sr.Cause
			}
			stop = ReasonEarlierStepErrored
		}
		res.Steps = append(res.Steps, sr)
	}
	return res, vars
}

func (r *Runner) scenario(ctx context.Context, p string, sc model.Scenario, runID string) ScenarioResult {
	res := ScenarioResult{ID: sc.ID, Name: sc.Name, Spec: p, Result: Pass}
	vars := r.baseVars(runID + "-" + sc.ID)
	hs := newHandleSet()
	keep := r.wantsTranscript(sc)
	stop, failed, skippedAct := "", false, false
	run := func(i int, st model.Step, phase string) {
		if stop != "" {
			res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: phase, Result: NotRun, Reason: stop})
			return
		}
		if failed {
			if reason, msg := r.afterFailure(p, sc.ID, i, st, vars, hs); reason != "" {
				skippedAct = skippedAct || !r.observes(p, sc.ID, i)
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
		sr := r.step(ctx, st, recording(StepResult{ID: st.ID, Phase: phase}, keep), vars, hs)
		switch {
		case phase == PhaseSetup && sr.Result != Pass:
			sr.Result, sr.Reason = Error, ReasonSetupFailed
			res.Result, res.Reason, res.Cause = Error, ReasonSetupFailed, sr.Cause
			stop = ReasonSetupFailed
		case sr.Result == Fail:
			noteFailure(&res, &sr, skippedAct)
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
	r.cleanup(ctx, p, sc, &res, vars, hs, true)
	res.Handles = r.closeHandles(ctx, hs)
	r.recordTranscript(&res, sc, setupResults(res.Steps), vars)
	r.keepLogVars(&res, sc, vars)
	return res
}

func (r *Runner) observes(p, scenario string, i int) bool {
	in, ok := r.info[model.StepRef{Spec: p, Scenario: scenario, Phase: "steps", Index: i}]
	return ok && in.Kind == protocol.KindObserve
}

func noteFailure(res *ScenarioResult, sr *StepResult, skippedAct bool) {
	if res.FirstFailure == "" {
		res.FirstFailure = sr.ID
	}
	sr.AfterSkippedAct = skippedAct
}

func (r *Runner) afterFailure(p, scenario string, i int, st model.Step, vars expr.Vars, hs *handleSet) (string, string) {
	if !r.observes(p, scenario, i) {
		return ReasonEarlierStepFailed, ""
	}
	if st.On != "" && hs.open[st.On] == nil {
		return ReasonEarlierStepFailed, fmt.Sprintf("on %q: the handle isn't open, because an earlier step failed or didn't run", st.On)
	}
	return missingSaved(st, vars)
}

func missingSaved(st model.Step, vars expr.Vars) (string, string) {
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
	for _, st := range sc.Cleanup {
		res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: PhaseCleanup, Result: NotRun, Reason: reason})
	}
	if len(sc.Cleanup) > 0 {
		res.Cleanup, res.CleanupFailure = NotRun, sc.Cleanup[0].ID
	}
	return res
}

func (r *Runner) cleanup(ctx context.Context, p string, sc model.Scenario, res *ScenarioResult, vars expr.Vars, hs *handleSet, earlierPassed bool) {
	if len(sc.Cleanup) == 0 {
		return
	}
	keep := r.wantsTranscript(sc)
	clean := earlierPassed
	for _, sr := range res.Steps {
		if sr.Result != Pass {
			clean = false
		}
	}
	res.Cleanup = Pass
	cause := ""
	for i, st := range sc.Cleanup {
		sr := StepResult{ID: st.ID, Phase: PhaseCleanup}
		reason, msg := r.cleanupBlocked(p, sc, i, st, vars, hs, clean)
		switch {
		case ctx.Err() != nil:
			sr.Result, sr.Reason, sr.Message = NotRun, ReasonAborted, "the run was interrupted, which stops the drivers, so nothing was sent; what the scenario created may remain"
		case reason != "":
			sr.Result, sr.Reason, sr.Message = NotRun, reason, msg
		default:
			sr = r.step(ctx, st, recording(sr, keep), vars, hs)
		}
		if sr.Result != Pass {
			clean = false
			if res.CleanupFailure == "" {
				res.CleanupFailure, cause = st.ID, sr.Cause
			}
			if tableRank[sr.Result] > tableRank[res.Cleanup] {
				res.Cleanup = sr.Result
			}
		}
		res.Steps = append(res.Steps, sr)
	}
	switch {
	case res.Result != Pass || res.Cleanup == Pass:
	case ctx.Err() != nil:
		res.Result, res.Reason, res.Cause = Error, ReasonAborted, ""
	default:
		res.Result, res.Reason, res.Cause = Error, ReasonCleanupFailed, cause
	}
}

func (r *Runner) cleanupBlocked(p string, sc model.Scenario, i int, st model.Step, vars expr.Vars, hs *handleSet, clean bool) (string, string) {
	if st.On != "" && hs.open[st.On] == nil {
		return ReasonHandleNotOpen, fmt.Sprintf("on %q: the handle isn't open, because the step that opens it failed or didn't run, so nothing was sent", st.On)
	}
	if reason, msg := missingSaved(st, vars); reason != "" {
		return reason, msg + "; nothing was sent"
	}
	in, known := r.info[model.StepRef{Spec: p, Scenario: sc.ID, Phase: "cleanup", Index: i}]
	if !clean && integrity.CleanupNeedsGuard(&sc, i, in, known) {
		return ReasonCleanupUnguarded, "this step acts destructively on a fixed target, which EXE-09 accepted only because an earlier step guards it; an earlier step (in a table, also one of an earlier row) didn't pass, so the guard isn't proven for this run and nothing was sent"
	}
	return "", ""
}

var tableRank = map[Result]int{Pass: 0, NotRun: 1, Error: 2, Fail: 3}

func (o *Outcome) addTableRow(p string, sc model.Scenario, res ScenarioResult) {
	var t *TableResult
	for i := range o.Tables {
		if o.Tables[i].key == sc.Case.Key && o.Tables[i].Spec == p {
			t = &o.Tables[i]
		}
	}
	if t == nil {
		o.Tables = append(o.Tables, TableResult{ID: sc.Case.Table, Name: sc.Name, Spec: p, SetupFrom: sc.Case.SetupFrom,
			Columns: sc.Case.Columns, Result: Pass, key: sc.Case.Key})
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
