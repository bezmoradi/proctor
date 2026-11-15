package integrity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"proctor/internal/expr"
	"proctor/internal/gitrepo"
	"proctor/internal/model"
	"proctor/internal/strictyaml"
)

type Kind int

const (
	Invalid   Kind = 3
	Violation Kind = 4
)

func (k Kind) String() string {
	switch k {
	case Violation:
		return "integrity_violation"
	}
	return "invalid"
}

type Finding struct {
	Kind    Kind   `json:"kind"`
	Rule    string `json:"rule"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

type Report struct {
	Commit        string            `json:"commit,omitempty"`
	ConfigPath    string            `json:"config"`
	ProtectedRoot string            `json:"protected_root"`
	Specs         []string          `json:"specifications"`
	ScenarioIDs   []string          `json:"scenario_ids"`
	Unlisted      []string          `json:"unlisted"`
	Hashes        map[string]string `json:"sha256"`
	Findings      []Finding         `json:"findings"`

	Config   *model.Config          `json:"-"`
	Manifest *model.Manifest        `json:"-"`
	Parsed   map[string]*model.Spec `json:"-"`
}

func (r *Report) ExitCode() int {
	code := 0
	for _, f := range r.Findings {
		if int(f.Kind) > code {
			code = int(f.Kind)
		}
	}
	return code
}

func (r *Report) Has(k Kind) bool {
	for _, f := range r.Findings {
		if f.Kind == k {
			return true
		}
	}
	return false
}

func (r *Report) add(k Kind, rule, p, format string, args ...any) {
	r.Findings = append(r.Findings, Finding{Kind: k, Rule: rule, Path: p, Message: fmt.Sprintf(format, args...)})
}

type Source interface {
	ReadFile(p string) (data []byte, ok bool, err error)
}

var CodeownersPaths = []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}

var repoMetaPaths = []string{".gitattributes", ".gitmodules", ".lfsconfig"}

func Protected(p, root string, extra []string) bool {
	if strings.HasPrefix(p, root+"/") || strings.HasPrefix(p, ".github/") {
		return true
	}
	for _, m := range append(append([]string(nil), repoMetaPaths...), CodeownersPaths...) {
		if p == m {
			return true
		}
	}
	for _, e := range extra {
		if p == e || (strings.HasSuffix(e, "/") && strings.HasPrefix(p, e)) {
			return true
		}
	}
	return false
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

var identityTypes = map[string]bool{"none": true, "bearer": true, "basic": true, "oauth2_client_credentials": true}

var adapterNames = map[string]bool{"http": true, "postgres": true}

var parkedAdapters = map[string]bool{"temporal": true, "logs": true}

var httpMethods = map[string]bool{"GET": true, "HEAD": true, "OPTIONS": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}

var stateChanging = map[string]bool{"POST": true, "PUT": true, "PATCH": true, "DELETE": true}

var objectPattern = regexp.MustCompile(`^([a-z_][a-z0-9_$]*\.)?[a-z_][a-z0-9_$]*$`)

func checkRelPath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("path is empty")
	case strings.HasPrefix(p, "/"):
		return fmt.Errorf("path %q is absolute", p)
	case strings.ContainsAny(p, "\\\x00"):
		return fmt.Errorf("path %q contains a backslash or NUL", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return fmt.Errorf("path %q leaves its directory", p)
		}
	}
	if path.Clean(p) != p || p == "." {
		return fmt.Errorf("path %q is not in clean form (expected %q)", p, path.Clean(p))
	}
	return nil
}

func Load(src Source, configPath string) *Report {
	r := &Report{ConfigPath: configPath, Hashes: map[string]string{}, Parsed: map[string]*model.Spec{}}
	if err := checkRelPath(configPath); err != nil {
		r.add(Invalid, "MODE-01", configPath, "configuration path: %v", err)
		return r
	}
	root := path.Dir(configPath)
	if root == "." {
		r.add(Invalid, "MODE-01", configPath, "proctor.yaml must sit inside a protected root directory, not at the repository root")
		return r
	}
	r.ProtectedRoot = root

	var cfg model.Config
	if !r.decode(src, configPath, &cfg) {
		return r
	}
	r.Config = &cfg
	r.validateConfig(&cfg)
	if cfg.Manifest == "" {
		return r
	}
	if err := checkRelPath(cfg.Manifest); err != nil {
		r.add(Violation, "INT-01", configPath, "manifest path: %v", err)
		return r
	}
	manifestPath := root + "/" + cfg.Manifest
	var man model.Manifest
	if !r.decode(src, manifestPath, &man) {
		return r
	}
	r.Manifest = &man
	if man.Version != 1 {
		r.add(Invalid, "INT-15", manifestPath, "version must be 1, got %d", man.Version)
	}
	if len(man.Specifications) == 0 {
		r.add(Violation, "INT-04", manifestPath, "the manifest lists no specifications")
	}

	seenSpecID := map[string]bool{}
	seenPath := map[string]bool{}
	seenScenario := map[string]string{}
	for i, e := range man.Specifications {
		where := fmt.Sprintf("%s: specifications[%d]", manifestPath, i)
		if !idPattern.MatchString(e.ID) {
			r.add(Invalid, "INT-02", manifestPath, "%s: invalid id %q", where, e.ID)
		} else if seenSpecID[e.ID] {
			r.add(Invalid, "INT-02", manifestPath, "%s: duplicate specification id %q", where, e.ID)
		}
		seenSpecID[e.ID] = true
		if err := checkRelPath(e.Path); err != nil {
			r.add(Violation, "INT-01", manifestPath, "%s: %v", where, err)
			continue
		}
		full := root + "/" + e.Path
		if seenPath[full] {
			r.add(Invalid, "INT-02", manifestPath, "%s: %s is listed twice", where, full)
			continue
		}
		seenPath[full] = true
		data, ok, err := src.ReadFile(full)
		if err != nil {
			r.add(Violation, "INT-07", full, "cannot read: %v", err)
			continue
		}
		if !ok {
			r.add(Violation, "INT-01", full, "listed in the manifest but not found (paths must match byte for byte)")
			continue
		}
		r.Specs = append(r.Specs, full)
		r.Hashes[full] = sha(data)
		var spec model.Spec
		if err := strictyaml.Decode(data, &spec); err != nil {
			r.add(Invalid, "INT-15", full, "%v", err)
			continue
		}
		for _, pr := range spec.ExpandTables(strictyaml.Decode) {
			r.add(Invalid, pr.Rule, full, "%s", pr.Msg)
		}
		r.Parsed[full] = &spec
		r.validateSpec(full, &spec, &cfg, seenScenario)
	}
	r.checkSetupExercised()
	return r
}

func (r *Report) decode(src Source, p string, v any) bool {
	data, ok, err := src.ReadFile(p)
	switch {
	case err != nil:
		r.add(Violation, "INT-07", p, "cannot read: %v", err)
		return false
	case !ok:
		r.add(Violation, "INT-01", p, "not found")
		return false
	}
	r.Hashes[p] = sha(data)
	if err := strictyaml.Decode(data, v); err != nil {
		r.add(Invalid, "INT-15", p, "%v", err)
		return false
	}
	return true
}

func (r *Report) validateConfig(c *model.Config) {
	p := r.ConfigPath
	if c.Version != 1 {
		r.add(Invalid, "INT-15", p, "version must be 1, got %d", c.Version)
	}
	if c.Manifest == "" {
		r.add(Invalid, "INT-15", p, "manifest is required")
	}
	for name, a := range c.Adapters {
		if parkedAdapters[name] {
			r.add(Invalid, "INT-12", p, "adapter %q isn't available: Temporal and log evidence (Phase 5) is parked", name)
			continue
		}
		if !adapterNames[name] {
			r.add(Invalid, "INT-12", p, "unknown adapter %q", name)
		}
		if a != "enabled" && a != "disabled" {
			r.add(Invalid, "INT-12", p, "adapter %q must be enabled or disabled, got %q", name, a)
		}
	}
	if c.IdentityCheck != nil {
		r.add(Invalid, "AUTH-04", p, "identity_check isn't available: the identity check (AUTH-04) is parked, so it would never run")
	}
	if c.Adapters["http"] == "disabled" {
		r.add(Invalid, "INT-12", p, "the http adapter can't be disabled: every Phase 0 scenario uses it")
	}
	for _, name := range sortedKeys(c.Identities) {
		if !idPattern.MatchString(name) {
			r.add(Invalid, "INT-12", p, "invalid identity name %q", name)
		}
		id := c.Identities[name]
		if !identityTypes[id.Type] {
			r.add(Invalid, "AUTH-01", p, "identity %q has unsupported type %q", name, id.Type)
		}
		if id.Type == "none" && len(id.Variants) > 0 {
			r.add(Invalid, "AUTH-06", p, "identity %q has type none, so it has no credential to vary", name)
		}
		seen := map[string]bool{}
		if id.Type == "none" && len(id.AcceptedVariants) > 0 {
			r.add(Invalid, "AUTH-06", p, "identity %q has type none, so it has no credential to vary", name)
		}
		for _, v := range append(append([]string{}, id.Variants...), id.AcceptedVariants...) {
			if !idPattern.MatchString(v) {
				r.add(Invalid, "AUTH-06", p, "identity %q: invalid credential variant name %q", name, v)
			} else if seen[v] {
				r.add(Invalid, "AUTH-06", p, "identity %q: credential variant %q is listed twice", name, v)
			}
			seen[v] = true
		}
	}
	seenSecret := map[string]bool{}
	for _, s := range c.Secrets {
		if !expr.VarName(s) || seenSecret[s] {
			r.add(Invalid, "AUTH-07", p, "secrets: invalid or repeated name %q", s)
		}
		seenSecret[s] = true
	}
	if len(c.Connections.Postgres) > 0 && c.Adapters["postgres"] == "disabled" {
		r.add(Invalid, "INT-12", p, "connections.postgres is set but the postgres adapter is disabled")
	}
	for _, name := range sortedKeys(c.Connections.Postgres) {
		pc := c.Connections.Postgres[name]
		if !idPattern.MatchString(name) {
			r.add(Invalid, "INT-12", p, "invalid PostgreSQL connection name %q", name)
		}
		for _, sch := range pc.Schemas {
			if !objectPattern.MatchString(sch) || strings.Contains(sch, ".") {
				r.add(Invalid, "DB-03", p, "connection %q: invalid schema name %q (lower-case, unquoted identifiers only)", name, sch)
			}
		}
		if len(pc.Objects) == 0 {
			r.add(Invalid, "DB-07", p, "connection %q declares no objects; list the tables and views its checks may read", name)
		}
		for _, o := range pc.Objects {
			if !objectPattern.MatchString(o) {
				r.add(Invalid, "DB-07", p, "connection %q: invalid object name %q (schema.name or name, lower-case, unquoted)", name, o)
			} else if len(pc.Schemas) == 0 && !strings.Contains(o, ".") {
				r.add(Invalid, "DB-03", p, "connection %q declares no schemas, so object %q must be schema-qualified", name, o)
			}
		}
	}
	for _, e := range c.ProtectedPaths {
		if err := checkRelPath(strings.TrimSuffix(e, "/")); err != nil {
			r.add(Invalid, "INT-13", p, "protected_paths: %v", err)
		}
	}
}

func (r *Report) validateSpec(p string, s *model.Spec, c *model.Config, seen map[string]string) {
	if s.Version != 1 {
		r.add(Invalid, "INT-15", p, "version must be 1, got %d", s.Version)
	}
	if len(s.Scenarios) == 0 && len(s.Tables) == 0 {
		r.add(Violation, "INT-04", p, "the specification defines no scenarios")
	}
	tableCases := map[string][]model.Scenario{}
	var tableOrder []string
	for i, sc := range s.Scenarios {
		where := fmt.Sprintf("scenarios[%d]", i)
		if sc.Case != nil {
			where = fmt.Sprintf("table %s case", sc.Case.Table)
			if len(tableCases[sc.Case.Table]) == 0 {
				tableOrder = append(tableOrder, sc.Case.Table)
			}
			tableCases[sc.Case.Table] = append(tableCases[sc.Case.Table], sc)
		}
		if !idPattern.MatchString(sc.ID) {
			r.add(Invalid, "INT-02", p, "%s: invalid id %q", where, sc.ID)
		} else if other, dup := seen[sc.ID]; dup {
			kind := "scenario"
			if sc.Case != nil {
				kind = "case"
			}
			r.add(Invalid, "INT-02", p, "%s: %s id %q is also used in %s", where, kind, sc.ID, other)
		} else {
			seen[sc.ID] = p
			r.ScenarioIDs = append(r.ScenarioIDs, sc.ID)
		}
		where = fmt.Sprintf("scenario %s", sc.ID)
		if sc.Case != nil {
			where = fmt.Sprintf("table %s case %s", sc.Case.Table, sc.ID)
		}
		if strings.TrimSpace(sc.Name) == "" && sc.Case == nil {
			r.add(Invalid, "EXE-01", p, "%s: name is required", where)
		}
		if len(sc.Steps) == 0 {
			r.add(Invalid, "EXE-01", p, "%s: at least one verification step is required", where)
		}
		stepIDs := map[string]bool{}
		saved := map[string]bool{expr.RunID: true}
		mark := len(r.Findings)
		for j, st := range sc.Setup {
			label := fmt.Sprintf("%s setup[%d]", where, j)
			if sc.Case != nil {
				label = fmt.Sprintf("table %s setup[%d]", sc.Case.Group, j)
			}
			r.validateStep(p, label, st, true, c, stepIDs, saved)
		}
		if sc.Case != nil && !sc.Case.First {
			r.Findings = r.Findings[:mark]
		}
		for j, st := range sc.Steps {
			r.validateStep(p, fmt.Sprintf("%s steps[%d]", where, j), st, false, c, stepIDs, saved)
		}
		r.checkStateChecks(p, where, sc)
		if sc.Case == nil {
			r.checkRunScoped(p, where, sc)
		}
	}
	for _, id := range tableOrder {
		cases := tableCases[id]
		whole := model.Scenario{ID: id, Setup: cases[0].Setup}
		for _, sc := range cases {
			whole.Steps = append(whole.Steps, sc.Steps...)
		}
		mark := len(r.Findings)
		r.checkRunScoped(p, "table "+id, whole)
		seen := map[Finding]bool{}
		kept := r.Findings[:mark]
		for _, f := range r.Findings[mark:] {
			if !seen[f] {
				seen[f] = true
				kept = append(kept, f)
			}
		}
		r.Findings = kept
	}
}

func (r *Report) checkStateChecks(p, where string, sc model.Scenario) {
	for i, st := range sc.Steps {
		if st.Request == nil || !stateChanging[st.Request.Method] {
			if st.NoStateCheck != "" {
				r.add(Invalid, "EXE-07", p, "%s step %s: no_state_check is only for POST, PUT, PATCH and DELETE verification steps", where, st.ID)
			}
			continue
		}
		checked := false
		for _, later := range sc.Steps[i+1:] {
			if isStateCheck(later) {
				checked = true
				break
			}
		}
		if strings.TrimSpace(st.NoStateCheck) != "" {
			if checked {
				r.add(Invalid, "EXE-07", p, "%s step %s: no_state_check waives a state check, but a later step already checks state; remove the waiver", where, st.ID)
			}
			continue
		}
		if !checked {
			r.add(Invalid, "EXE-07", p, "%s step %s: %s changes state, but no later step of the scenario checks it (add a database check or a GET that isn't expected to be refused, or set no_state_check to the reason none is possible)",
				where, st.ID, st.Request.Method)
		}
	}
}

func isStateCheck(st model.Step) bool {
	if st.Verify != nil && st.Verify.Postgres != nil {
		return true
	}
	if st.Request == nil || st.Request.Method != "GET" || st.Expect == nil || st.Expect.Status == nil {
		return false
	}
	s := *st.Expect.Status
	return s != 401 && s != 403
}

var waivable = map[string]bool{"FIX-05": true, "HTTP-09": true, "EXE-09": true}

var destructive = map[string]bool{"PUT": true, "PATCH": true, "DELETE": true}

func (r *Report) checkRunScoped(p, where string, sc model.Scenario) {
	all := append(append([]model.Step{}, sc.Setup...), sc.Steps...)
	for i, st := range all {
		fired := map[string]bool{}
		var msgs []string
		if st.Request != nil && st.Request.Body != nil {
			for _, v := range stringLeaves(st.Request.Body) {
				if v == "" || strings.Contains(v, "${"+expr.RunID+"}") || refersToSaved(v) {
					continue
				}
				for _, later := range all[i+1:] {
					if expects(later, v) {
						fired["FIX-05"] = true
						msgs = append(msgs, fmt.Sprintf("%s step %s sends %q and step %s expects it, but it doesn't contain ${run.id}, so data from an earlier run could match (use ${run.id} in the value, or waive FIX-05 with the reason it can't vary)", where, st.ID, v, later.ID))
						break
					}
				}
			}
		}
		unfiltered := st.Request != nil && st.Request.Method == "GET" && st.Expect != nil && len(st.Expect.Length) > 0 && len(st.Request.Query) == 0
		unguarded := false
		if st.Request != nil && destructive[st.Request.Method] && !strings.Contains(st.Request.Path, "${") {
			unguarded = true
			for _, earlier := range all[:i] {
				if earlier.Verify != nil && earlier.Verify.Postgres != nil {
					unguarded = false
					break
				}
			}
		}
		rules := make([]string, 0, len(st.Waive))
		for rule := range st.Waive {
			rules = append(rules, rule)
		}
		sort.Strings(rules)
		for _, rule := range rules {
			reason := st.Waive[rule]
			switch {
			case !waivable[rule]:
				r.add(Invalid, "EXE-07", p, "%s step %s: waive names %q; only FIX-05, HTTP-09 and EXE-09 can be waived (EXE-07 uses no_state_check)", where, st.ID, rule)
			case strings.TrimSpace(reason) == "":
				r.add(Invalid, rule, p, "%s step %s: the %s waiver must state a reason", where, st.ID, rule)
			case rule == "FIX-05" && !fired["FIX-05"], rule == "HTTP-09" && !unfiltered, rule == "EXE-09" && !unguarded:
				r.add(Invalid, rule, p, "%s step %s: waives %s, but the rule doesn't apply to this step; remove the waiver", where, st.ID, rule)
			}
		}
		if fired["FIX-05"] && strings.TrimSpace(st.Waive["FIX-05"]) == "" {
			for _, m := range msgs {
				r.add(Invalid, "FIX-05", p, "%s", m)
			}
		}
		if unguarded && strings.TrimSpace(st.Waive["EXE-09"]) == "" {
			r.add(Invalid, "EXE-09", p, "%s step %s sends %s to the fixed path %s, which the scenario didn't create and may hold data that isn't the test's; check it in the database first (an earlier verify.postgres step, for example that the id doesn't exist), use a path with a saved value or ${run.id}, or waive EXE-09 with the reason it's safe", where, st.ID, st.Request.Method, st.Request.Path)
		}
		if unfiltered && strings.TrimSpace(st.Waive["HTTP-09"]) == "" {
			r.add(Invalid, "HTTP-09", p, "%s step %s asserts a list length on a request with no query parameters; the list may include records the scenario didn't create (filter the request, or waive HTTP-09 with the reason it can't)", where, st.ID)
		}
	}
}

func refersToSaved(v string) bool {
	for _, m := range refAny.FindAllString(v, -1) {
		name := m[2 : len(m)-1]
		if name != expr.RunID && !expr.IsFill(name) && !expr.IsSecret(name) {
			return true
		}
	}
	return false
}

func expects(st model.Step, v string) bool {
	var vals []any
	if st.Expect != nil {
		for _, x := range st.Expect.JSON {
			vals = append(vals, x)
		}
	}
	if st.Verify != nil && st.Verify.Postgres != nil {
		pc := st.Verify.Postgres
		vals = append(vals, pc.Args...)
		if pc.Expect != nil {
			for _, x := range pc.Expect.FirstRow {
				vals = append(vals, x)
			}
		}
	}
	for _, x := range vals {
		for _, s := range stringLeaves(x) {
			if s == v {
				return true
			}
		}
	}
	return false
}

func stringLeaves(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case map[string]any:
		var out []string
		for _, k := range sortedKeys(t) {
			out = append(out, stringLeaves(t[k])...)
		}
		return out
	case []any:
		var out []string
		for _, x := range t {
			out = append(out, stringLeaves(x)...)
		}
		return out
	}
	return nil
}

func (r *Report) checkSetupExercised() {
	type op struct {
		method, path string
		status       int
	}
	verified := map[op]bool{}
	type use struct{ spec, scenario, step string }
	setup := map[op]use{}
	var order []op
	for _, p := range r.Specs {
		s := r.Parsed[p]
		if s == nil {
			continue
		}
		for _, sc := range s.Scenarios {
			for _, st := range sc.Steps {
				if st.Request != nil && st.Expect != nil && st.Expect.Status != nil {
					verified[op{st.Request.Method, template(st.Request.Path), *st.Expect.Status}] = true
				}
			}
			for _, st := range sc.Setup {
				if st.Request != nil && st.Expect != nil && st.Expect.Status != nil {
					o := op{st.Request.Method, template(st.Request.Path), *st.Expect.Status}
					if _, ok := setup[o]; !ok {
						owner := "scenario " + sc.ID
						if sc.Case != nil {
							owner = "table " + sc.Case.Group
						}
						setup[o] = use{p, owner, st.ID}
						order = append(order, o)
					}
				}
			}
		}
	}
	for _, o := range order {
		if !verified[o] {
			u := setup[o]
			r.add(Invalid, "FIX-08", u.spec, "%s setup step %s uses %s %s expecting %d, but no verification step in the set exercises that operation with that status; a defect in it would be reported only as a setup ERROR",
				u.scenario, u.step, o.method, o.path, o.status)
		}
	}
}

var refAny = regexp.MustCompile(`\$\{[^}]*\}`)

func template(p string) string { return refAny.ReplaceAllString(p, "{}") }

func (r *Report) validateStep(p, where string, st model.Step, setup bool, c *model.Config, ids, saved map[string]bool) {
	if !idPattern.MatchString(st.ID) {
		r.add(Invalid, "INT-02", p, "%s: invalid step id %q", where, st.ID)
	} else if ids[st.ID] {
		r.add(Invalid, "INT-02", p, "%s: duplicate step id %q", where, st.ID)
	}
	ids[st.ID] = true
	where = fmt.Sprintf("%s (%s)", where, st.ID)
	if st.NoStateCheck != "" && strings.TrimSpace(st.NoStateCheck) == "" {
		r.add(Invalid, "EXE-07", p, "%s: no_state_check must state a reason", where)
	}
	if st.NoStateCheck != "" && setup {
		r.add(Invalid, "EXE-07", p, "%s: no_state_check is only for verification steps", where)
	}
	switch {
	case st.Request != nil && st.Verify != nil:
		r.add(Invalid, "EXE-01", p, "%s: a step has either request or verify, not both", where)
		return
	case st.Verify != nil:
		r.validateVerify(p, where, st, setup, c, saved)
		return
	case st.Request == nil:
		r.add(Invalid, "EXE-01", p, "%s: request or verify is required", where)
	default:
		if !httpMethods[st.Request.Method] {
			r.add(Invalid, "EXE-01", p, "%s: unsupported method %q", where, st.Request.Method)
		}
		if err := checkRequestPath(st.Request.Path); err != nil {
			r.add(Invalid, "HTTP-05", p, "%s: %v", where, err)
		}
		for name := range st.Request.Headers {
			if rule, why := forbiddenHeader(name); rule != "" {
				r.add(Invalid, rule, p, "%s: header %q can't be set by a specification: %s", where, name, why)
			}
		}
		if st.Request.Method == "HEAD" && st.Request.Body != nil {
			r.add(Invalid, "EXE-01", p, "%s: a HEAD request can't send a body", where)
		}
		if st.Request.Body != nil {
			if err := checkJSONValue(st.Request.Body); err != nil {
				r.add(Invalid, "EXE-01", p, "%s: body can't be sent as JSON: %v", where, err)
			}
		}
		r.checkRefs(p, where, saved, st.Request.Path, st.Request.Query)

		r.checkRequestRefs(p, where, saved, c, st.Request.Headers, st.Request.Body)
		id, declared := c.Identities[st.Request.Identity]
		if st.Request.Identity == "" {
			r.add(Invalid, "INT-12", p, "%s: identity is required (use an identity of type none for anonymous requests)", where)
		} else if !declared {
			r.add(Invalid, "INT-12", p, "%s: identity %q is not declared in proctor.yaml", where, st.Request.Identity)
		}
		if cr := st.Request.Credential; cr != "" {
			accepted := contains(id.AcceptedVariants, cr)
			if declared && !accepted && !contains(id.Variants, cr) {
				r.add(Invalid, "AUTH-06", p, "%s: identity %q has no credential variant %q", where, st.Request.Identity, cr)
			}
			if setup {
				r.add(Invalid, "AUTH-06", p, "%s: a credential variant is for negative tests, not setup", where)
			} else if !accepted && st.Expect != nil && st.Expect.Status != nil && *st.Expect.Status != 401 && *st.Expect.Status != 403 {
				r.add(Invalid, "AUTH-06", p, "%s: a request with credential variant %q must expect 401 or 403, not %d", where, cr, *st.Expect.Status)
			}
		}
	}
	if st.Expect == nil || st.Expect.Status == nil {
		r.add(Invalid, "EXE-03", p, "%s: expect.status is required", where)
		return
	}
	status := *st.Expect.Status
	if status < 100 || status > 599 {
		r.add(Invalid, "EXE-03", p, "%s: status %d is not an HTTP status", where, status)
	}
	if st.Request != nil && st.Request.Method == "HEAD" {
		ex := st.Expect
		if len(ex.JSON) > 0 || len(ex.Present) > 0 || len(ex.Absent) > 0 || len(ex.Length) > 0 || len(ex.Keys) > 0 || len(st.Save) > 0 {
			r.add(Invalid, "EXE-03", p, "%s: a HEAD response has no body, so it can only assert status and headers", where)
		}
	}
	for _, k := range sortedKeys(st.Expect.JSON) {
		if _, err := expr.ParsePath(k); err != nil {
			r.add(Invalid, "EXE-03", p, "%s: %v", where, err)
		}
		if err := checkJSONValue(st.Expect.JSON[k]); err != nil {
			r.add(Invalid, "EXE-03", p, "%s: expected value for %s isn't a JSON value: %v", where, k, err)
		}
	}
	asserted := map[string]string{}
	for _, k := range sortedKeys(st.Expect.JSON) {
		asserted[k] = "json"
	}
	for _, list := range []struct {
		name  string
		paths []string
	}{{"present", st.Expect.Present}, {"absent", st.Expect.Absent}} {
		for _, k := range list.paths {
			if _, err := expr.ParsePath(k); err != nil {
				r.add(Invalid, "HTTP-08", p, "%s: %s: %v", where, list.name, err)
			}
			if prev, dup := asserted[k]; dup {
				r.add(Invalid, "HTTP-08", p, "%s: %s is asserted by both %s and %s", where, k, prev, list.name)
			}
			asserted[k] = list.name
		}
	}
	for _, k := range sortedKeys(st.Expect.Keys) {
		if _, err := expr.ParsePath(k); err != nil {
			r.add(Invalid, "HTTP-08", p, "%s: keys: %v", where, err)
		}
		seenKey := map[string]bool{}
		for _, name := range st.Expect.Keys[k] {
			if seenKey[name] {
				r.add(Invalid, "HTTP-08", p, "%s: keys of %s lists %q twice", where, k, name)
			}
			seenKey[name] = true
		}
		if asserted[k] == "absent" {
			r.add(Invalid, "HTTP-08", p, "%s: %s is both absent and given keys", where, k)
		}
	}
	for _, k := range sortedKeys(st.Expect.Length) {
		if _, err := expr.ParsePath(k); err != nil {
			r.add(Invalid, "HTTP-09", p, "%s: length: %v", where, err)
		}
		switch v := st.Expect.Length[k].(type) {
		case int:
			if v < 0 {
				r.add(Invalid, "HTTP-09", p, "%s: length of %s can't be negative", where, k)
			}
		case string:
			if names, err := expr.References(v); err != nil || len(names) != 1 || v != "${"+names[0]+"}" {
				r.add(Invalid, "HTTP-09", p, "%s: length of %s must be an integer or one ${name} reference", where, k)
			}
		default:
			r.add(Invalid, "HTTP-09", p, "%s: length of %s must be an integer or one ${name} reference", where, k)
		}
		if asserted[k] == "absent" {
			r.add(Invalid, "HTTP-08", p, "%s: %s is both absent and given a length", where, k)
		}
	}
	for _, name := range sortedKeys(st.Expect.HeaderSets) {
		vals := st.Expect.HeaderSets[name]
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, " :\r\n") {
			r.add(Invalid, "HTTP-08", p, "%s: header_sets: invalid header name %q", where, name)
		}
		for other := range st.Expect.Headers {
			if strings.EqualFold(other, name) {
				r.add(Invalid, "HTTP-08", p, "%s: header %s is in both headers and header_sets", where, name)
			}
		}
		for _, h := range st.Expect.AbsentHeaders {
			if strings.EqualFold(h, name) {
				r.add(Invalid, "HTTP-08", p, "%s: header %s is both in header_sets and absent", where, name)
			}
		}
		if len(vals) == 0 {
			r.add(Invalid, "HTTP-08", p, "%s: header_sets.%s needs at least one value", where, name)
		}
		seen := map[string]bool{}
		for _, v := range vals {
			switch {
			case strings.TrimSpace(v) != v || v == "":
				r.add(Invalid, "HTTP-08", p, "%s: header_sets.%s: value %q is empty or has spaces around it", where, name, v)
			case strings.ContainsAny(v, ",\r\n"):
				r.add(Invalid, "HTTP-08", p, "%s: header_sets.%s: value %q can't contain a comma or line break; list each value separately", where, name, v)
			case seen[v]:
				r.add(Invalid, "HTTP-08", p, "%s: header_sets.%s lists %q twice", where, name, v)
			}
			seen[v] = true
		}
	}
	for _, h := range st.Expect.AbsentHeaders {
		if strings.TrimSpace(h) == "" || strings.ContainsAny(h, " :\r\n") {
			r.add(Invalid, "HTTP-08", p, "%s: absent_headers: invalid header name %q", where, h)
		}
		for name := range st.Expect.Headers {
			if strings.EqualFold(name, h) {
				r.add(Invalid, "HTTP-08", p, "%s: header %s is both expected and absent", where, h)
			}
		}
	}
	sets := map[string]any{}
	for k, v := range st.Expect.HeaderSets {
		list := make([]any, len(v))
		for i, x := range v {
			list[i] = x
		}
		sets[k] = list
	}
	r.checkRefs(p, where, saved, st.Expect.JSON, st.Expect.Headers, st.Expect.Length, sets)
	for _, name := range sortedKeys(st.Save) {
		if !expr.VarName(name) {
			r.add(Invalid, "EXE-02", p, "%s: save name %q must be a letter or _ followed by letters, digits or _", where, name)
		} else if saved[name] {
			r.add(Invalid, "EXE-02", p, "%s: %q is already saved by an earlier step", where, name)
		}
		if _, err := expr.ParsePath(st.Save[name]); err != nil {
			r.add(Invalid, "EXE-02", p, "%s: save %s: %v", where, name, err)
		}
	}
	defer func() {
		for name := range st.Save {
			saved[name] = true
		}
	}()
	if !setup && status >= 200 && status < 300 && len(st.Expect.JSON) == 0 && len(st.Expect.Headers) == 0 &&
		len(st.Expect.Present) == 0 && len(st.Expect.Absent) == 0 && len(st.Expect.Length) == 0 && len(st.Expect.Keys) == 0 &&
		len(st.Expect.AbsentHeaders) == 0 && len(st.Expect.HeaderSets) == 0 {
		r.add(Invalid, "EXE-03", p, "%s: a verification step expecting %d must also assert a body field or header", where, status)
	}
}

func (r *Report) validateVerify(p, where string, st model.Step, setup bool, c *model.Config, saved map[string]bool) {
	if st.Verify.Postgres == nil {
		r.add(Invalid, "EXE-01", p, "%s: verify needs postgres", where)
		return
	}
	if setup {
		r.add(Invalid, "FIX-03", p, "%s: database checks are verification steps; setup goes through the application", where)
	}
	if st.Expect != nil {
		r.add(Invalid, "EXE-01", p, "%s: a database check's expectation goes in verify.postgres.expect", where)
	}
	pc := st.Verify.Postgres
	defer r.validateRowSaves(p, where, st, saved)
	if _, ok := c.Connections.Postgres[pc.Connection]; !ok {
		r.add(Invalid, "INT-12", p, "%s: PostgreSQL connection %q is not declared in proctor.yaml", where, pc.Connection)
	}
	if strings.TrimSpace(pc.Query) == "" {
		r.add(Invalid, "DB-01", p, "%s: query is required", where)
	}
	if strings.Contains(pc.Query, "${") {
		r.add(Invalid, "DB-01", p, "%s: query can't contain ${...}; pass values as args ($1, $2, ...)", where)
	}
	if strings.ContainsRune(pc.Query, 0) {
		r.add(Invalid, "DB-01", p, "%s: query contains a NUL character", where)
	}
	for i, a := range pc.Args {
		switch a.(type) {
		case nil, string, bool, int, int64, uint64, float64:
		default:
			r.add(Invalid, "DB-01", p, "%s: args[%d] must be a string, number, boolean or null", where, i)
		}
	}
	r.checkRefs(p, where, saved, pc.Args)
	ex := pc.Expect
	if ex == nil || (ex.Rows == nil && ex.FirstRow == nil) {
		r.add(Invalid, "EXE-03", p, "%s: verify.postgres.expect needs rows, first_row or both", where)
		return
	}
	if ex.Rows != nil && *ex.Rows < 0 {
		r.add(Invalid, "EXE-03", p, "%s: rows can't be negative", where)
	}
	if ex.Rows != nil && *ex.Rows == 0 && ex.FirstRow != nil {
		r.add(Invalid, "EXE-03", p, "%s: first_row can't be expected when rows is 0", where)
	}
	if ex.FirstRow != nil && len(ex.FirstRow) == 0 {
		r.add(Invalid, "EXE-03", p, "%s: first_row is empty", where)
	}
	for _, k := range sortedKeys(ex.FirstRow) {
		if err := checkJSONValue(ex.FirstRow[k]); err != nil {
			r.add(Invalid, "EXE-03", p, "%s: first_row.%s isn't a JSON value: %v", where, k, err)
		}
	}
	r.checkRefs(p, where, saved, ex.FirstRow)
}

func (r *Report) validateRowSaves(p, where string, st model.Step, saved map[string]bool) {
	if len(st.Save) == 0 {
		return
	}
	ex := st.Verify.Postgres.Expect
	if ex == nil || ex.Rows == nil || *ex.Rows != 1 {
		r.add(Invalid, "EXE-02", p, "%s: a database check can save values only when it expects rows: 1", where)
	}
	for _, name := range sortedKeys(st.Save) {
		if !expr.VarName(name) {
			r.add(Invalid, "EXE-02", p, "%s: save name %q must be a letter or _ followed by letters, digits or _", where, name)
		} else if saved[name] {
			r.add(Invalid, "EXE-02", p, "%s: %q is already saved by an earlier step", where, name)
		}
		if !expr.VarName(st.Save[name]) {
			r.add(Invalid, "EXE-02", p, "%s: save %s: %q is not a column name", where, name, st.Save[name])
		}
	}
	for name := range st.Save {
		saved[name] = true
	}
}

func checkJSONValue(v any) error {
	if f, ok := v.(float64); ok && (f != f || f > 1.7976931348623157e308 || f < -1.7976931348623157e308) {
		return fmt.Errorf("%v is not a JSON number", f)
	}
	switch t := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(t) {
			if err := checkJSONValue(t[k]); err != nil {
				return err
			}
		}
	case []any:
		for _, x := range t {
			if err := checkJSONValue(x); err != nil {
				return err
			}
		}
	}
	_, err := json.Marshal(v)
	return err
}

func (r *Report) checkRefs(p, where string, saved map[string]bool, values ...any) {
	for _, v := range values {
		var names []string
		var err error
		switch t := v.(type) {
		case map[string]string:
			m := make(map[string]any, len(t))
			for k, x := range t {
				m[k] = x
			}
			names, err = expr.ReferencesIn(m)
		case map[string]any:
			names, err = expr.ReferencesIn(t)
		default:
			names, err = expr.ReferencesIn(t)
		}
		if err != nil {
			r.add(Invalid, "HTTP-02", p, "%s: %v", where, err)
			continue
		}
		for _, n := range names {
			switch {
			case saved[n]:
			case expr.IsFill(n) || expr.IsSecret(n):
				r.add(Invalid, "HTTP-02", p, "%s: ${%s} may be used only in a request body or request headers", where, n)
			case !saved[n]:
				r.add(Invalid, "HTTP-02", p, "%s: ${%s} isn't saved by an earlier step of this scenario", where, n)
			}
		}
	}
}

func (r *Report) checkRequestRefs(p, where string, saved map[string]bool, c *model.Config, values ...any) {
	allowed := map[string]bool{}
	for k, v := range saved {
		allowed[k] = v
	}
	for _, v := range values {
		var names []string
		switch t := v.(type) {
		case map[string]string:
			m := make(map[string]any, len(t))
			for k, x := range t {
				m[k] = x
			}
			names, _ = expr.ReferencesIn(m)
		default:
			names, _ = expr.ReferencesIn(t)
		}
		for _, n := range names {
			switch {
			case expr.IsFill(n):
				allowed[n] = true
			case expr.IsSecret(n):
				if contains(c.Secrets, expr.SecretName(n)) {
					allowed[n] = true
				} else {
					r.add(Invalid, "AUTH-07", p, "%s: secret %q is not declared in proctor.yaml", where, expr.SecretName(n))
					allowed[n] = true
				}
			}
		}
	}
	r.checkRefs(p, where, allowed, append([]any{}, values...)...)
}

func checkRequestPath(p string) error {
	switch {
	case !strings.HasPrefix(p, "/"):
		return fmt.Errorf("path %q must start with /", p)
	case strings.HasPrefix(p, "//"):
		return fmt.Errorf("path %q starts with //, which names a host", p)
	case strings.ContainsAny(p, "?#\\"):
		return fmt.Errorf("path %q contains ?, # or \\; put query parameters in query", p)
	}
	for _, r := range p {
		if r <= ' ' || r == 0x7f {
			return fmt.Errorf("path %q contains a space or control character", p)
		}
	}
	lower := strings.ToLower(p)
	if strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") {
		return fmt.Errorf("path %q contains a percent-encoded / or \\", p)
	}
	for _, seg := range strings.Split(p[1:], "/") {
		dec, err := url.PathUnescape(seg)
		if err != nil {
			return fmt.Errorf("path %q: %v", p, err)
		}
		if dec == "." || dec == ".." {
			return fmt.Errorf("path %q contains a . or .. segment", p)
		}
	}
	return nil
}

func forbiddenHeader(name string) (rule, why string) {
	switch strings.ToLower(name) {
	case "host":
		return "HTTP-05", "the target comes from the binding"
	case "user-agent":
		return "HTTP-04", "the User-Agent comes from the binding"
	case "authorization", "proxy-authorization", "cookie":
		return "AUTH-02", "credentials come from identities, never from specifications"
	case "content-length", "transfer-encoding", "connection":
		return "EXE-01", "the HTTP client sets it"
	}
	return "", ""
}

func CheckCommit(repo *gitrepo.Repo, commit, configPath string) *Report {
	src, err := NewCommitSource(repo, commit)
	if err != nil {
		r := &Report{ConfigPath: configPath, Hashes: map[string]string{}}
		r.add(Violation, "INT-08", "", "%v", err)
		return r
	}
	oid, tree := src.oid, src.tree
	r := Load(src, configPath)
	r.Commit = oid
	if r.ProtectedRoot == "" {
		return r
	}
	var extra []string
	if r.Config != nil {
		extra = r.Config.ProtectedPaths
	}

	reported := map[string]bool{}
	for _, d := range protectedDirs(r.ProtectedRoot, extra) {
		if e, ok := tree[d]; ok && !reported[d] {
			reported[d] = true
			r.add(Violation, "INT-13", d, "should be a directory holding protected inputs, but is a %s (mode %s)", kindOf(e), e.Mode)
		}
	}

	var protectedPaths []string
	codeowners := 0
	for _, p := range sortedKeys(tree) {
		e := tree[p]
		if !Protected(p, r.ProtectedRoot, extra) {
			continue
		}
		protectedPaths = append(protectedPaths, p)
		if !e.Regular() {
			r.add(Violation, "INT-13", p, "protected input is a %s (mode %s); only regular files are allowed", kindOf(e), e.Mode)
			continue
		}
		data, err := repo.Blob(e.OID)
		if err != nil {
			r.add(Violation, "INT-07", p, "cannot read blob: %v", err)
			continue
		}
		if bytes.HasPrefix(data, []byte("version https://git-lfs.github.com/spec/")) {
			r.add(Violation, "INT-13", p, "protected input is a Git LFS pointer")
		}
		for _, c := range CodeownersPaths {
			if p == c {
				codeowners++
			}
		}
		if isSpecCandidate(p, r) && !contains(r.Specs, p) {
			r.Unlisted = append(r.Unlisted, p)
		}
	}
	if codeowners > 1 {
		r.add(Violation, "INT-11", "", "more than one CODEOWNERS file (%s); keep exactly one", strings.Join(CodeownersPaths, ", "))
	}

	attrs, err := repo.Attributes(oid, protectedPaths)
	if err != nil {
		r.add(Violation, "INT-14", "", "cannot evaluate Git attributes: %v", err)
	}
	for _, p := range protectedPaths {
		for _, a := range sortedKeys(attrs[p]) {
			if forbiddenAttr(a) {
				r.add(Violation, "INT-14", p, "Git attribute %q is %s on a protected path", a, attrs[p][a])
			}
		}
	}
	return r
}

func protectedDirs(root string, extra []string) []string {
	var dirs []string
	addWithAncestors := func(d string) {
		for ; d != "." && d != "" && d != "/"; d = path.Dir(d) {
			dirs = append(dirs, d)
		}
	}
	addWithAncestors(root)
	addWithAncestors(".github")
	for _, e := range extra {
		if strings.HasSuffix(e, "/") {
			addWithAncestors(strings.TrimSuffix(e, "/"))
		} else {
			addWithAncestors(path.Dir(e))
		}
	}
	return dirs
}

func forbiddenAttr(a string) bool {
	switch a {
	case "diff", "binary", "text", "eol", "crlf", "filter", "working-tree-encoding", "ident",
		"linguist-generated", "export-ignore", "export-subst":
		return true
	}
	return false
}

func isSpecCandidate(p string, r *Report) bool {
	if !strings.HasPrefix(p, r.ProtectedRoot+"/") {
		return false
	}
	if !strings.HasSuffix(p, ".yaml") && !strings.HasSuffix(p, ".yml") {
		return false
	}
	if p == r.ConfigPath || strings.HasPrefix(p, r.ProtectedRoot+"/environments/") {
		return false
	}
	if r.Config != nil && p == r.ProtectedRoot+"/"+r.Config.Manifest {
		return false
	}
	return true
}

func kindOf(e gitrepo.Entry) string {
	switch {
	case e.Mode == "120000":
		return "symlink"
	case e.Type == "commit":
		return "submodule"
	default:
		return e.Type
	}
}

type CommitSource struct {
	repo *gitrepo.Repo
	oid  string
	tree map[string]gitrepo.Entry
}

func NewCommitSource(repo *gitrepo.Repo, commit string) (*CommitSource, error) {
	oid, err := repo.ResolveCommit(commit)
	if err != nil {
		return nil, err
	}
	tree, err := repo.Tree(oid)
	if err != nil {
		return nil, err
	}
	return &CommitSource{repo: repo, oid: oid, tree: tree}, nil
}

func (s *CommitSource) ReadFile(p string) ([]byte, bool, error) {
	e, ok := s.tree[p]
	if !ok {
		return nil, false, nil
	}
	if !e.Regular() {
		return nil, false, fmt.Errorf("%s is a %s, not a regular file", p, kindOf(e))
	}
	data, err := s.repo.Blob(e.OID)
	return data, err == nil, err
}

type WorkTreeSource struct {
	Root string
}

func (s WorkTreeSource) ReadFile(p string) ([]byte, bool, error) {
	if err := checkRelPath(p); err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(filepath.Join(s.Root, filepath.FromSlash(p)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
