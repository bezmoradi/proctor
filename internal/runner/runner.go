// Package runner executes specifications: scenarios of HTTP steps and
// read-only PostgreSQL checks against the target and connections named by an
// environment binding (prd-revised.md §11.1, §11.2, §11.7, §13.3, FIX-04,
// INT-03).
//
// It is Proctor's own runner (§19, SEC-13). Scenarios run one at a time in
// manifest order and then file order (EXE-04). Every scenario the loader
// found gets a result; nothing is skipped silently.
package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"proctor/internal/expr"
	"proctor/internal/integrity"
	"proctor/internal/model"
	"proctor/internal/postgres"
)

// Result is the outcome of a scenario, step or assertion (§13.3).
type Result string

const (
	Pass   Result = "PASS"
	Fail   Result = "FAIL"
	Error  Result = "ERROR"
	NotRun Result = "NOT_RUN"
)

// NOT_RUN and ERROR reasons (§13.3).
const (
	ReasonSetupFailed         = "setup_failed"
	ReasonEarlierStepFailed   = "earlier_step_failed"
	ReasonEarlierStepErrored  = "earlier_step_errored"
	ReasonEnvironmentNotReady = "environment_not_ready"
	ReasonAborted             = "aborted"
	ReasonTrustViolation      = "trust_violation"
)

// Causes say why a step is ERROR or FAIL (FIX-04). A setup step that FAILs
// becomes ERROR with reason setup_failed and keeps its cause, so a report
// tells an application defect (unexpected_response) from an environment
// problem (unreachable, timeout).
const (
	CauseUnexpectedResponse = "unexpected_response"
	CauseUnreachable        = "unreachable"
	CauseTimeout            = "timeout"
	CauseUnreadableResponse = "unreadable_response"
	CauseResponseTooLarge   = "response_too_large"
	CauseInvalidRequest     = "invalid_request"
)

// MaxBodyBytes bounds how much of a response Proctor reads.
const MaxBodyBytes = 10 << 20

const redacted = "[redacted]"

// Assertion is one evaluated expectation, with the evidence it was judged on.
type Assertion struct {
	Kind     string `json:"kind"` // status, header, body, json, present, absent, length, save, rows, first_row
	Target   string `json:"target,omitempty"`
	Expected any    `json:"expected,omitempty"`
	Actual   any    `json:"actual,omitempty"`
	Present  bool   `json:"present"`
	Result   Result `json:"result"`
	Message  string `json:"message,omitempty"`
}

// Request is what was sent, without credentials.
type Request struct {
	Method     string `json:"method"`
	URL        string `json:"url"`
	Identity   string `json:"identity"`
	Credential string `json:"credential,omitempty"`
}

// Check is a database check as run; arguments are not reported, since
// they can hold captured values.
type Check struct {
	Connection string `json:"connection"`
	Query      string `json:"query"`
}

// StepResult is one step's outcome.
type StepResult struct {
	ID         string      `json:"id"`
	Phase      string      `json:"phase"` // setup or verification
	Result     Result      `json:"result"`
	Reason     string      `json:"reason,omitempty"`
	Cause      string      `json:"cause,omitempty"`
	Message    string      `json:"message,omitempty"`
	Request    *Request    `json:"request,omitempty"`
	Check      *Check      `json:"check,omitempty"`
	Status     int         `json:"status,omitempty"`
	DurationMS float64     `json:"duration_ms,omitempty"`
	Assertions []Assertion `json:"assertions,omitempty"`
	Saved      []string    `json:"saved,omitempty"`
}

// ScenarioResult is one scenario's outcome. Its result is its worst step,
// except that a failed setup makes it ERROR (FIX-04).
type ScenarioResult struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Spec   string       `json:"specification"`
	Result Result       `json:"result"`
	Reason string       `json:"reason,omitempty"`
	Cause  string       `json:"cause,omitempty"`
	Steps  []StepResult `json:"steps"`
}

// Summary counts scenario results and evaluated assertions.
type Summary struct {
	Scenarios  int `json:"scenarios"`
	Passed     int `json:"passed"`
	Failed     int `json:"failed"`
	Errors     int `json:"errors"`
	NotRun     int `json:"not_run"`
	Assertions int `json:"assertions"`
}

// Outcome is a whole run.
type Outcome struct {
	RunID      string           `json:"run_id"`
	Target     string           `json:"target"`
	Readiness  string           `json:"readiness,omitempty"`
	Violations []string         `json:"violations,omitempty"`
	Scenarios  []ScenarioResult `json:"scenarios"`
	Summary    Summary          `json:"summary"`
	ExitCode   int              `json:"exit_code"`
	Result     string           `json:"result"`
}

// ConfigError is a binding or configuration problem found before any
// request is sent (exit 3).
type ConfigError struct{ Msgs []string }

func (e *ConfigError) Error() string { return strings.Join(e.Msgs, "; ") }

// Runner holds everything a run needs, checked by New.
type Runner struct {
	report       *integrity.Report
	base         *url.URL
	ua           string
	tokens       map[string]string // credKey -> bearer token; never reported
	client       *http.Client
	redactJS     map[string]bool
	redactH      map[string]bool
	ready        string
	readyFor     time.Duration
	queryTimeout time.Duration
	dsns         map[string]string // connection -> connection string; never reported
	pg           map[string]*postgres.Conn
	secrets      map[string]string // "secret.<name>" -> value; never reported
}

// Options tune a run. Zero values give the defaults.
type Options struct {
	// RequestTimeout bounds each request; 10s by default (OD-18 leaves the
	// v1 default open).
	RequestTimeout time.Duration
	// QueryTimeout bounds each database query; 5s by default.
	QueryTimeout time.Duration
	// LookupEnv reads credential variables; os.LookupEnv in production.
	LookupEnv func(string) (string, bool)
}

func credKey(identity, variant string) string { return identity + "\x00" + variant }

// New checks the binding against the loaded configuration. The report must
// have no findings of kind Invalid or Violation.
func New(report *integrity.Report, b *model.Binding, opt Options) (*Runner, error) {
	var msgs []string
	bad := func(format string, args ...any) { msgs = append(msgs, fmt.Sprintf(format, args...)) }

	base, err := url.Parse(b.Target.BaseURL)
	switch {
	case b.Target.BaseURL == "":
		bad("binding: target.base_url is required")
	case err != nil:
		bad("binding: target.base_url: %v", err)
	case base.Scheme != "http" && base.Scheme != "https":
		bad("binding: target.base_url must be an http or https URL")
	case base.Host == "":
		bad("binding: target.base_url has no host")
	case base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.ForceQuery:
		bad("binding: target.base_url can't contain credentials, a query or a fragment")
	}
	if strings.TrimSpace(b.Target.UserAgent) == "" {
		bad("binding: target.user_agent is required (HTTP-04)")
	} else if strings.ContainsAny(b.Target.UserAgent, "\r\n") {
		bad("binding: target.user_agent contains a line break")
	}

	lookup := opt.LookupEnv
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	cfg := report.Config
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
	}
	tokens := map[string]string{} // credKey -> whole Authorization value; never reported
	owner := map[string]string{}  // Authorization value -> who uses it, for AUTH-03
	load := func(who, key, env, prefix string) {
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
			tokens[key] = prefix + tok
		}
	}
	idents, variants, conns := used(report)
	for _, name := range idents {
		typ := cfg.Identities[name].Type
		switch typ {
		case "none":
		case "bearer":
			bi := b.Identities[name]
			if bi.TokenEnv == "" {
				bad("binding: identity %q (bearer) needs token_env", name)
				continue
			}
			load(fmt.Sprintf("identity %q", name), credKey(name, ""), bi.TokenEnv, "Bearer ")
		default:
			bad("identity %q: type %q is not supported yet (use none or bearer)", name, typ)
		}
	}
	for _, iv := range variants {
		bv, ok := b.Identities[iv[0]].Variants[iv[1]]
		who := fmt.Sprintf("identity %q variant %q", iv[0], iv[1])
		switch {
		case !ok || (bv.TokenEnv == "") == (bv.HeaderEnv == ""):
			bad("binding: identity %q needs exactly one of variants.%s.token_env or variants.%s.header_env", iv[0], iv[1], iv[1])
		case bv.TokenEnv != "":
			load(who, credKey(iv[0], iv[1]), bv.TokenEnv, "Bearer ")
		default:
			load(who, credKey(iv[0], iv[1]), bv.HeaderEnv, "")
		}
	}

	secrets := map[string]string{}
	for _, name := range sortedKeys(b.Secrets) {
		if !contains(cfg.Secrets, name) {
			bad("binding: secret %q is not declared in proctor.yaml", name)
		}
	}
	for _, name := range usedSecrets(report) {
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
		secrets["secret."+name] = v
	}

	dsns := map[string]string{}
	for _, name := range sortedKeys(b.Connections.Postgres) {
		if _, ok := cfg.Connections.Postgres[name]; !ok {
			bad("binding: PostgreSQL connection %q is not declared in proctor.yaml", name)
		}
	}
	for _, name := range conns {
		bc := b.Connections.Postgres[name]
		if bc.DSNEnv == "" {
			bad("binding: PostgreSQL connection %q needs dsn_env", name)
			continue
		}
		dsn, ok := lookup(bc.DSNEnv)
		if !ok || dsn == "" {
			bad("PostgreSQL connection %q: environment variable %s is not set", name, bc.DSNEnv)
			continue
		}
		dsns[name] = dsn
	}

	r := &Runner{report: report, base: base, ua: b.Target.UserAgent, tokens: tokens, dsns: dsns, secrets: secrets,
		pg:       map[string]*postgres.Conn{},
		redactJS: map[string]bool{}, redactH: map[string]bool{"authorization": true, "cookie": true, "set-cookie": true}}
	if cfg.Redaction != nil {
		for _, p := range cfg.Redaction.JSONPaths {
			r.redactJS[p] = true
		}
		for _, h := range cfg.Redaction.Headers {
			r.redactH[strings.ToLower(h)] = true
		}
	}
	r.readyFor = 30 * time.Second
	if rd := cfg.Readiness; rd != nil {
		r.ready = rd.Path
		if rd.StartupTimeout != "" {
			d, err := time.ParseDuration(rd.StartupTimeout)
			if err != nil || d <= 0 {
				bad("proctor.yaml: readiness.startup_timeout %q is not a positive duration", rd.StartupTimeout)
			} else {
				r.readyFor = d
			}
		}
		if rd.Path != "" && (!strings.HasPrefix(rd.Path, "/") || strings.HasPrefix(rd.Path, "//") || strings.ContainsAny(rd.Path, "?#")) {
			bad("proctor.yaml: readiness.path %q must be a path starting with a single /", rd.Path)
		}
	}
	if len(msgs) > 0 {
		return nil, &ConfigError{Msgs: msgs}
	}

	timeout := opt.RequestTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	r.queryTimeout = opt.QueryTimeout
	if r.queryTimeout <= 0 {
		r.queryTimeout = 5 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// SEC-07: requests go to the target only, never through a proxy taken
	// from the environment.
	transport.Proxy = nil
	r.client = &http.Client{
		Transport: transport,
		Timeout:   timeout,
		// HTTP-01: redirects are never followed; the response is judged as
		// received.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return r, nil
}

// used lists the identities, credential variants and PostgreSQL connections
// the loaded specifications use.
func used(rep *integrity.Report) (idents []string, variants [][2]string, conns []string) {
	seenI, seenV, seenC := map[string]bool{}, map[[2]string]bool{}, map[string]bool{}
	for _, p := range rep.Specs {
		s := rep.Parsed[p]
		if s == nil {
			continue
		}
		for _, sc := range s.Scenarios {
			for _, st := range append(append([]model.Step{}, sc.Setup...), sc.Steps...) {
				if st.Request != nil {
					seenI[st.Request.Identity] = true
					if st.Request.Credential != "" {
						seenV[[2]string{st.Request.Identity, st.Request.Credential}] = true
					}
				}
				if st.Verify != nil && st.Verify.Postgres != nil {
					seenC[st.Verify.Postgres.Connection] = true
				}
			}
		}
	}
	idents, conns = sortedKeys(seenI), sortedKeys(seenC)
	for v := range seenV {
		variants = append(variants, v)
	}
	sort.Slice(variants, func(i, j int) bool {
		return variants[i][0]+"\x00"+variants[i][1] < variants[j][0]+"\x00"+variants[j][1]
	})
	return idents, variants, conns
}

// NewRunID returns a fresh run ID: 12 lower-case hex digits from the
// operating system's random source, safe in paths, names and SQL text.
func NewRunID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand doesn't fail on supported platforms
	}
	return hex.EncodeToString(b)
}

// Run executes every scenario. It never returns early: a scenario that
// can't run still gets a NOT_RUN result with its reason (INT-03).
func (r *Runner) Run(ctx context.Context) *Outcome {
	out := &Outcome{Target: r.base.String(), RunID: NewRunID()}
	defer func() {
		for _, c := range r.pg {
			c.Close()
		}
	}()
	stopAll := ""
	if err := r.waitReady(ctx); err != nil {
		stopAll = ReasonEnvironmentNotReady
		out.Readiness = "not ready: " + err.Error()
	} else {
		if r.ready != "" || len(r.dsns) > 0 {
			out.Readiness = "ready"
		}
		v, err := r.checkRoles(ctx)
		switch {
		case err != nil:
			stopAll = ReasonEnvironmentNotReady
			out.Readiness = "not ready: " + err.Error()
		case len(v) > 0:
			// DB-04: a role that could change data, or read what
			// proctor.yaml doesn't declare, makes every check suspect.
			stopAll = ReasonTrustViolation
			out.Violations = v
		}
	}
	for _, p := range r.report.Specs {
		spec := r.report.Parsed[p]
		if spec == nil {
			continue
		}
		for _, sc := range spec.Scenarios {
			var res ScenarioResult
			switch {
			case stopAll != "":
				res = notRunScenario(p, sc, stopAll)
			case ctx.Err() != nil:
				res = notRunScenario(p, sc, ReasonAborted)
			default:
				res = r.scenario(ctx, p, sc, out.RunID)
			}
			out.Scenarios = append(out.Scenarios, res)
		}
	}
	out.finish()
	r.scrub(out)
	return out
}

// scrub replaces every credential, secret and connection string that
// appears anywhere in the outcome, for example because the application
// echoed one in a response Proctor asserted on (AUTH-02, SEC-02).
func (r *Runner) scrub(out *Outcome) {
	var values []string
	for _, v := range r.tokens {
		values = append(values, v, strings.TrimPrefix(v, "Bearer "))
	}
	for _, v := range r.secrets {
		values = append(values, v)
	}
	for _, v := range r.dsns {
		values = append(values, v)
	}
	// Longest first, so a value that contains another is replaced whole.
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	clean := func(s string) string {
		for _, v := range values {
			if len(v) >= 4 {
				s = strings.ReplaceAll(s, v, redacted)
			}
		}
		return s
	}
	var walk func(any) any
	walk = func(v any) any {
		switch t := v.(type) {
		case string:
			return clean(t)
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
		case map[string]any:
			for k, x := range t {
				t[k] = walk(x)
			}
		case []string:
			for i := range t {
				t[i] = clean(t[i])
			}
		}
		return v
	}
	out.Readiness = clean(out.Readiness)
	for i := range out.Violations {
		out.Violations[i] = clean(out.Violations[i])
	}
	for i := range out.Scenarios {
		for j := range out.Scenarios[i].Steps {
			st := &out.Scenarios[i].Steps[j]
			st.Message = clean(st.Message)
			if st.Request != nil {
				st.Request.URL = clean(st.Request.URL)
			}
			for k := range st.Assertions {
				a := &st.Assertions[k]
				a.Expected, a.Actual, a.Message, a.Target = walk(a.Expected), walk(a.Actual), clean(a.Message), clean(a.Target)
			}
		}
	}
}

// usedSecrets lists the secrets the loaded specifications reference.
func usedSecrets(rep *integrity.Report) []string {
	seen := map[string]bool{}
	for _, p := range rep.Specs {
		s := rep.Parsed[p]
		if s == nil {
			continue
		}
		for _, sc := range s.Scenarios {
			for _, st := range append(append([]model.Step{}, sc.Setup...), sc.Steps...) {
				if st.Request == nil {
					continue
				}
				hs := map[string]any{}
				for k, v := range st.Request.Headers {
					hs[k] = v
				}
				for _, v := range []any{st.Request.Body, hs} {
					names, _ := expr.ReferencesIn(v)
					for _, n := range names {
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
	// REP-07: a trust violation (4) first, then a behavioral failure (1),
	// then an error (2).
	switch {
	case len(o.Violations) > 0:
		o.ExitCode, o.Result = 4, "integrity_violation"
	case s.Failed > 0:
		o.ExitCode, o.Result = 1, "failed"
	case s.Errors > 0 || s.NotRun > 0 || s.Scenarios == 0:
		o.ExitCode, o.Result = 2, "error"
	default:
		o.ExitCode, o.Result = 0, "passed"
	}
}

// waitReady waits, within the startup timeout, for the readiness path to
// answer 2xx and for every PostgreSQL connection to answer SELECT 1 as the
// verification role (FIX-07).
func (r *Runner) waitReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, r.readyFor)
	defer cancel()
	pause := func(last string) error {
		select {
		case <-ctx.Done():
			return fmt.Errorf("not ready after %s: %s", r.readyFor, last)
		case <-time.After(250 * time.Millisecond):
			return nil
		}
	}
	if r.ready != "" {
		u := r.base.String() + r.ready
		last := "no response"
		for {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				return err
			}
			req.Header.Set("User-Agent", r.ua)
			resp, err := r.client.Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, MaxBodyBytes))
				resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					break
				}
				last = fmt.Sprintf("%s answered %d", r.ready, resp.StatusCode)
			} else {
				last = err.Error()
			}
			if err := pause(last); err != nil {
				return err
			}
		}
	}
	for _, name := range sortedKeys(r.dsns) {
		pol := r.report.Config.Connections.Postgres[name]
		for {
			c, err := postgres.Open(ctx, name, r.dsns[name], pol.Schemas, r.queryTimeout)
			if err == nil {
				if err = c.Ping(ctx, r.queryTimeout); err == nil {
					r.pg[name] = c
					break
				}
				c.Close()
			}
			if err := pause(err.Error()); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkRoles runs DB-04 and DB-07 on every open connection.
func (r *Runner) checkRoles(ctx context.Context) ([]string, error) {
	var out []string
	for _, name := range sortedKeys(r.pg) {
		pol := r.report.Config.Connections.Postgres[name]
		problems, err := r.pg[name].RoleProblems(ctx, pol.Objects, r.queryTimeout)
		if err != nil {
			return nil, fmt.Errorf("connection %s: checking the verification role: %v", name, err)
		}
		// A superuser can own thousands of objects; the first problems are
		// enough to act on, and the count says how many there are.
		const shown = 50
		for i, p := range problems {
			if i == shown {
				out = append(out, fmt.Sprintf("connection %s: and %d more problems with the verification role (DB-04)", name, len(problems)-shown))
				break
			}
			out = append(out, fmt.Sprintf("connection %s: %s (DB-04)", name, p))
		}
	}
	return out, nil
}

func notRunScenario(p string, sc model.Scenario, reason string) ScenarioResult {
	res := ScenarioResult{ID: sc.ID, Name: sc.Name, Spec: p, Result: NotRun, Reason: reason}
	for _, st := range sc.Setup {
		res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: "setup", Result: NotRun, Reason: reason})
	}
	for _, st := range sc.Steps {
		res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: "verification", Result: NotRun, Reason: reason})
	}
	return res
}

func (r *Runner) scenario(ctx context.Context, p string, sc model.Scenario, runID string) ScenarioResult {
	res := ScenarioResult{ID: sc.ID, Name: sc.Name, Spec: p, Result: Pass}
	// FIX-05: ${run.id} is unique per run and per scenario, so names made
	// with it collide neither with earlier runs nor with other scenarios.
	vars := expr.Vars{expr.RunID: runID + "-" + sc.ID}
	for k, v := range r.secrets {
		vars[k] = v
	}
	stop := "" // reason for the remaining steps, once one is set

	run := func(st model.Step, phase string) {
		if stop != "" {
			res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: phase, Result: NotRun, Reason: stop})
			return
		}
		if ctx.Err() != nil {
			stop = ReasonAborted
			res.Result, res.Reason = Error, ReasonAborted
			res.Steps = append(res.Steps, StepResult{ID: st.ID, Phase: phase, Result: NotRun, Reason: stop})
			return
		}
		sr := StepResult{ID: st.ID, Phase: phase}
		if st.Verify != nil {
			sr = r.pgStep(ctx, st, sr, vars)
		} else {
			sr = r.httpStep(ctx, st, sr, vars)
		}
		switch {
		case phase == "setup" && sr.Result != Pass:
			// FIX-04: a setup step that fails or can't run is ERROR; its
			// cause stays, so an application defect is told apart from
			// the environment.
			sr.Result, sr.Reason = Error, ReasonSetupFailed
			res.Result, res.Reason, res.Cause = Error, ReasonSetupFailed, sr.Cause
			stop = ReasonSetupFailed
		case sr.Result == Fail:
			res.Result = Fail
			stop = ReasonEarlierStepFailed
		case sr.Result == Error:
			res.Result, res.Cause = Error, sr.Cause
			stop = ReasonEarlierStepErrored
		}
		res.Steps = append(res.Steps, sr)
	}
	for _, st := range sc.Setup {
		run(st, "setup")
	}
	for _, st := range sc.Steps {
		run(st, "verification")
	}
	return res
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
