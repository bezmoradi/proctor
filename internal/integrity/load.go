package integrity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"proctor/internal/expr"
	"proctor/internal/gitrepo"
	"proctor/internal/model"
	"proctor/internal/strictyaml"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

var driverName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

var programName = regexp.MustCompile(`^proctor-driver-[a-z][a-z0-9_-]*$`)

var platformKey = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+$`)

var sumHex = regexp.MustCompile(`^[0-9a-f]{64}$`)

var headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

var identityTypes = map[string]bool{"none": true, "bearer": true}

var parkedDrivers = map[string]bool{"temporal": true, "logs": true}

var reservedNames = map[string]bool{"run": true, "secret": true, "row": true, "fill": true}

type limits struct {
	minInterval time.Duration
	maxSamples  int
	maxWindow   time.Duration
	maxCall     time.Duration
}

var defaultLimits = limits{minInterval: 100 * time.Millisecond, maxSamples: 1000, maxWindow: 10 * time.Minute, maxCall: model.DefaultMaxCall}

func LoadConfig(src Source, configPath string) *Report {
	r := &Report{ConfigPath: configPath, Hashes: map[string]string{}, listed: map[string]bool{}, limits: defaultLimits, SpecIDs: map[string]string{}}
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
	cfg.SetFiles(protectedFiles{src: src, root: root})
	if data, ok, err := src.ReadFile(configPath); err == nil && ok {
		r.checkNullAppLogs(data)
	}
	r.validateConfig(&cfg)
	if cs, ok := src.(*CommitSource); ok {
		r.checkTrackedPrograms(&cfg, cs)
	}
	return r
}

func Load(src Source, configPath string) *Report {
	r := LoadConfig(src, configPath)
	if r.Config == nil {
		return r
	}
	cfg := r.Config
	root := r.ProtectedRoot
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
	if man.Version != 2 {
		r.add(Invalid, "INT-15", manifestPath, "version must be 2, got %d", man.Version)
	}
	if len(man.Specifications) == 0 {
		r.add(Violation, "INT-04", manifestPath, "the manifest lists no specifications")
	}

	seenSpecID := map[string]bool{}
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
		if r.listed[full] {
			r.add(Invalid, "INT-02", manifestPath, "%s: %s is listed twice", where, full)
			continue
		}
		r.listed[full] = true
		r.SpecIDs[full] = e.ID
		data, ok, err := src.ReadFile(full)
		if err != nil {
			r.add(Violation, "INT-07", full, "cannot read: %v", err)
			continue
		}
		if !ok {
			r.add(Violation, "INT-01", full, "listed in the manifest but not found (paths must match byte for byte)")
			continue
		}
		r.Hashes[full] = sha(data)
		var spec model.Spec
		if err := strictyaml.Decode(data, &spec); err != nil {
			r.add(Invalid, "INT-15", full, "%v", err)
			continue
		}
		for _, pr := range spec.ExpandTables(strictyaml.Decode) {
			r.add(Invalid, pr.Rule, full, "%s", pr.Message)
		}
		for _, pr := range spec.CanonicalPaths() {
			r.add(Invalid, pr.Rule, full, "%s", pr.Message)
		}
		sf := SpecFile{Path: full, Spec: &spec}
		r.Specs = append(r.Specs, sf)
		r.validateSpec(sf, cfg, seenScenario)
	}
	r.loadMutation(src, cfg)
	r.loadCoverage(src, cfg)
	r.loadApproved(src, cfg)
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
	if c.Version != 2 {
		r.add(Invalid, "INT-15", p, "version must be 2, got %d", c.Version)
	}
	if c.Manifest == "" {
		r.add(Invalid, "INT-15", p, "manifest is required")
	}
	for _, name := range sortedKeys(c.Drivers) {
		d := c.Drivers[name]
		if !driverName.MatchString(name) {
			r.add(Invalid, "INT-12", p, "invalid driver name %q (a lower-case letter, then lower-case letters, digits or _)", name)
		}
		switch {
		case d.Official != "" && d.Program != "":
			r.add(Invalid, "INT-12", p, "driver %q has both official and program; give official for a driver that ships with Proctor, or program for one kept in the repository", name)
		case d.Program != "":
			r.validateProgram(c, name, d)
		case d.Official == "":
			r.add(Invalid, "INT-12", p, "driver %q must name its official driver (official: <name>)", name)
		case !driverName.MatchString(d.Official):
			r.add(Invalid, "INT-12", p, "driver %q: invalid official driver name %q (a lower-case letter, then lower-case letters, digits or _)", name, d.Official)
		}
		if d.Official != "" && d.Program == "" && (d.SHA256 != nil || d.Operations != nil) {
			r.add(Invalid, "INT-12", p, "driver %q: sha256 and operations are only for a driver with program; an official driver's checksum is built into proctor", name)
		}
		switch {
		case name == "logs" || d.Official == "logs":
			r.add(Invalid, "INT-12", p, "driver %q isn't available: the logs driver (Phase 5) is parked; to fail scenarios on errors in the application's log, use app_logs in proctor.yaml (EXE-10) instead", name)
		case parkedDrivers[name] || parkedDrivers[d.Official]:
			r.add(Invalid, "INT-12", p, "driver %q isn't available: Temporal and log evidence (Phase 5) is parked", name)
		}
		if _, err := c.PolicyOf(name); err != nil {
			r.add(Invalid, "INT-18", p, "%v", err)
		}
	}
	for _, name := range sortedKeys(c.Identities) {
		if !idPattern.MatchString(name) {
			r.add(Invalid, "INT-12", p, "invalid identity name %q", name)
		} else if reservedNames[name] {
			r.add(Invalid, "INT-12", p, "identity name %q is reserved, because ${%s.<name>} has another meaning", name, name)
		}
		id := c.Identities[name]
		if !identityTypes[id.Type] {
			r.add(Invalid, "AUTH-01", p, "identity %q has type %q, which isn't supported: use none or bearer", name, id.Type)
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
	for _, e := range c.ProtectedPaths {
		if err := checkRelPath(strings.TrimSuffix(e, "/")); err != nil {
			r.add(Invalid, "INT-13", p, "protected_paths: %v", err)
		}
	}
	if c.Redaction != nil {
		for i, jp := range c.Redaction.JSONPaths {
			if _, err := expr.ParsePath(jp); err != nil {
				r.add(Invalid, "REP-06", p, "redaction.json_paths[%d]: %v", i, err)
			}
		}
		seenHeader := map[string]int{}
		for i, h := range c.Redaction.Headers {
			if !headerName.MatchString(h) {
				r.add(Invalid, "REP-06", p, "redaction.headers[%d]: %q is not a header name (one or more letters, digits and !#$%%&'*+-.^_`|~)", i, h)
				continue
			}
			if j, dup := seenHeader[strings.ToLower(h)]; dup {
				r.add(Invalid, "REP-06", p, "redaction.headers[%d]: %q repeats redaction.headers[%d]; header names ignore letter case", i, h, j)
				continue
			}
			seenHeader[strings.ToLower(h)] = i
		}
	}
	r.validateTimeLimits(c)
	r.validateAppLogs(c)
	r.validateApprovedLogs(c)
	r.validateTranscripts(c)
	r.validateRelations(c)
	r.validateIdentityCheck(c)
}

func (r *Report) validateProgram(c *model.Config, name string, d model.DriverConfig) {
	p := r.ConfigPath
	pathOK := false
	switch err := checkRelPath(d.Program); {
	case err != nil:
		r.add(Invalid, "INT-12", p, "driver %q: program: %v; give the program's path from the repository root, such as proctor/drivers/%s/bin/proctor-driver-%s", name, err, name, name)
	case strings.Contains(d.Program, "${"):
		r.add(Invalid, "INT-12", p, "driver %q: program %q can't use ${...}; name the file as written", name, d.Program)
	case !programName.MatchString(path.Base(d.Program)):
		r.add(Invalid, "INT-12", p, "driver %q: program %q must be a file named proctor-driver-<name>, where <name> is a lower-case letter, then lower-case letters, digits, _ or -", name, d.Program)
	default:
		pathOK = true
	}
	if len(d.SHA256) == 0 {
		r.add(Invalid, "INT-12", p, "driver %q: sha256 must pin the program's checksum for at least one platform, such as sha256: { linux/amd64: <64 hex digits> }", name)
	}
	for _, k := range slices.Sorted(maps.Keys(d.SHA256)) {
		if !platformKey.MatchString(k) {
			r.add(Invalid, "INT-12", p, "driver %q: sha256 key %q isn't a platform <os>/<arch>, such as linux/amd64 or darwin/arm64", name, k)
		}
		if !sumHex.MatchString(d.SHA256[k]) {
			r.add(Invalid, "INT-12", p, "driver %q: sha256 %s must be 64 lower-case hex digits, as shasum -a 256 prints them", name, k)
		}
	}
	if len(d.Operations) == 0 {
		r.add(Invalid, "INT-12", p, "driver %q: operations must list every operation of the program, each act or observe, such as operations: { read: observe }", name)
	}
	for _, op := range slices.Sorted(maps.Keys(d.Operations)) {
		if !driverName.MatchString(op) {
			r.add(Invalid, "INT-12", p, "driver %q: invalid operation name %q (a lower-case letter, then lower-case letters, digits or _)", name, op)
		}
		switch d.Operations[op] {
		case "act", "observe":
		default:
			r.add(Invalid, "INT-12", p, "driver %q: operation %q is %q; a repository driver's operation is act or observe", name, op, d.Operations[op])
		}
	}
	if pathOK && !ProtectedFolder(path.Dir(d.Program), r.ProtectedRoot, c.ProtectedPaths) {
		r.add(Violation, "INT-21", p, "driver %q: program %s is in %s, which isn't a protected folder, so its source could change without review; keep it under %s/, or list its folder in protected_paths with a trailing /", name, d.Program, path.Dir(d.Program), r.ProtectedRoot)
	}
}

func (r *Report) checkTrackedPrograms(c *model.Config, cs *CommitSource) {
	entries := make([]gitrepo.Entry, 0, len(cs.tree))
	for _, e := range cs.tree {
		entries = append(entries, e)
	}
	for _, name := range sortedKeys(c.Drivers) {
		prog := c.Drivers[name].Program
		if prog == "" || checkRelPath(prog) != nil {
			continue
		}
		if what, found := TrackedProgram(prog, entries, "the commit"); found {
			r.add(Violation, "INT-21", prog, "driver %q: %s; build it in the job from the reviewed source instead, so remove it with git rm --cached and ignore it", name, what)
		}
	}
}

func (r *Report) CheckTrackedWorkTree(index func() ([]gitrepo.Entry, error)) {
	if r.Config == nil {
		return
	}
	var entries []gitrepo.Entry
	var err error
	read := false
	for _, name := range sortedKeys(r.Config.Drivers) {
		prog := r.Config.Drivers[name].Program
		if prog == "" || checkRelPath(prog) != nil {
			continue
		}
		if !read {
			entries, err = index()
			read = true
		}
		if err != nil {
			r.add(Violation, "INT-21", prog, "driver %q: cannot tell whether Git tracks the program, so it isn't started: %v", name, err)
			continue
		}
		if what, found := TrackedProgram(prog, entries, "the index"); found {
			r.add(Violation, "INT-21", prog, "driver %q: %s; build it from the reviewed source instead, so remove it with git rm --cached and ignore it", name, what)
		}
	}
}

func (r *Report) DriverPathFindings() bool {
	for _, f := range r.Findings {
		if f.Rule == "INT-12" || f.Rule == "INT-21" {
			return true
		}
	}
	return false
}

func (r *Report) validateIdentityCheck(c *model.Config) {
	ic := c.IdentityCheck
	if ic == nil {
		return
	}
	p := r.ConfigPath
	if _, err := c.IdentityCheckDriver(); err != nil {
		r.add(Invalid, "AUTH-04", p, "%v", err)
	}
	switch {
	case ic.Path == "":
		r.add(Invalid, "AUTH-04", p, "identity_check.path is required: the path that answers with the calling user, such as /api/me")
	case strings.Contains(ic.Path, "${"):
		r.add(Invalid, "AUTH-04", p, "identity_check.path %q can't use ${...}: the check runs before any scenario, with nothing to insert", ic.Path)
	default:
		if err := identityCheckPath(ic.Path); err != nil {
			r.add(Invalid, "AUTH-04", p, "identity_check.path: %v", err)
		}
	}
	if ic.UserID == "" {
		r.add(Invalid, "AUTH-04", p, "identity_check.user_id is required: the path in the response body that holds the user's ID, such as $.id")
	} else if _, err := expr.ParsePath(ic.UserID); err != nil {
		r.add(Invalid, "AUTH-04", p, "identity_check.user_id: %v", err)
	}
}

func identityCheckPath(p string) error {
	switch {
	case !strings.HasPrefix(p, "/"):
		return fmt.Errorf("%q must start with /", p)
	case strings.HasPrefix(p, "//"):
		return fmt.Errorf("%q starts with //, which names a host", p)
	case strings.ContainsAny(p, "?#\\"):
		return fmt.Errorf("%q contains ?, # or \\; the check sends no query", p)
	}
	for _, c := range p {
		if c <= ' ' || c == 0x7f {
			return fmt.Errorf("%q contains a space or control character", p)
		}
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("%q contains a . or .. segment", p)
		}
	}
	return nil
}

func (r *Report) validateAppLogs(c *model.Config) {
	a := c.AppLogs
	if a == nil {
		return
	}
	p := r.ConfigPath
	if a.Levels != nil && len(a.Levels) == 0 {
		r.add(Invalid, "EXE-10", p, "app_logs.levels lists no levels, so no log line could fail a scenario; leave levels out for the default (%s)", strings.Join(model.DefaultAppLogLevels, ", "))
	}
	seen := map[string]bool{}
	for i, l := range a.Levels {
		u := strings.ToUpper(l)
		switch {
		case strings.TrimSpace(l) == "":
			r.add(Invalid, "EXE-10", p, "app_logs.levels[%d] is empty", i)
		case seen[u]:
			r.add(Invalid, "EXE-10", p, "app_logs.levels lists %s twice (levels are compared ignoring case)", l)
		}
		seen[u] = true
	}
	for _, f := range []struct{ key, value string }{{"level_field", a.LevelField}, {"message_field", a.MessageField}} {
		if f.value != "" && strings.TrimSpace(f.value) != f.value {
			r.add(Invalid, "EXE-10", p, "app_logs.%s %q has leading or trailing spaces", f.key, f.value)
		}
	}
	if a.LevelKey() == a.MessageKey() {
		r.add(Invalid, "EXE-10", p, "app_logs.level_field and app_logs.message_field both name %q", a.LevelKey())
	}
	switch a.Format {
	case "", model.AppLogFormatJSON, model.AppLogFormatLogfmt:
	default:
		r.add(Invalid, "EXE-10", p, "app_logs.format %q isn't json or logfmt", a.Format)
	}
	switch a.Unparsed {
	case "", model.AppLogUnparsedStrict, model.AppLogUnparsedAllow:
	default:
		r.add(Invalid, "EXE-10", p, "app_logs.unparsed %q isn't strict or allow", a.Unparsed)
	}
	if a.FlushWait != "" {
		d, err := time.ParseDuration(a.FlushWait)
		switch {
		case err != nil || d <= 0:
			r.add(Invalid, "EXE-10", p, "app_logs.flush_wait %q isn't a positive duration, such as 300ms or 1.5s", a.FlushWait)
		case d > model.MaxFlushWait:
			r.add(Invalid, "EXE-10", p, "app_logs.flush_wait %s is longer than %s, the most a scenario waits for the application's last log lines", d, model.MaxFlushWait)
		}
	}
	if a.LevelNumbers != nil && len(a.LevelNumbers) == 0 {
		r.add(Invalid, "EXE-10", p, "app_logs.level_numbers lists no numbers; leave it out if the log's levels are names")
	}
	for _, k := range slices.Sorted(maps.Keys(a.LevelNumbers)) {
		v := a.LevelNumbers[k]
		if _, ok := model.LevelNumber(k); !ok {
			r.add(Invalid, "EXE-10", p, "app_logs.level_numbers key %q isn't a whole number in decimal, such as 50 or -4, without a plus sign or leading zeros", k)
		}
		switch {
		case strings.TrimSpace(v) == "":
			r.add(Invalid, "EXE-10", p, "app_logs.level_numbers %s maps to no level; give the level's name, such as ERROR", k)
		case strings.TrimSpace(v) != v:
			r.add(Invalid, "EXE-10", p, "app_logs.level_numbers %s: %q has leading or trailing spaces", k, v)
		}
	}
}

func (r *Report) checkNullAppLogs(data []byte) {
	doc, err := strictyaml.Parse(data)
	if err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return
	}
	m := doc.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == "app_logs" && m.Content[i+1].ShortTag() == "!!null" {
			r.add(Invalid, "EXE-10", r.ConfigPath, "line %d: app_logs has no value; write app_logs: {} to check the application's log with the default settings, or remove the key", m.Content[i].Line)
		}
		if v := m.Content[i+1]; m.Content[i].Value == "app_logs" && v.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(v.Content); j += 2 {
				if v.Content[j].Value == "approved" && v.Content[j+1].ShortTag() == "!!null" {
					r.add(Invalid, "ALG-02", r.ConfigPath, "line %d: app_logs.approved has no value; give its fields, or remove the key", v.Content[j].Line)
				}
			}
		}
		if v := m.Content[i+1]; m.Content[i].Value == "dimensions" && (v.ShortTag() == "!!null" || (v.Kind == yaml.ScalarNode && v.Value == "")) {
			r.add(Invalid, model.CoverageRule, r.ConfigPath, "line %d: dimensions has no value; name the dimensions file, such as dimensions: dimensions.yaml, or remove the key", m.Content[i].Line)
		}
		if m.Content[i].Value == "transcripts" && m.Content[i+1].ShortTag() == "!!null" {
			r.add(Invalid, "TRN-03", r.ConfigPath, "line %d: transcripts has no value; give max_bytes or scrub, or remove the key for the defaults", m.Content[i].Line)
		}
		if m.Content[i].Value == "identity_check" && m.Content[i+1].ShortTag() == "!!null" {
			r.add(Invalid, "AUTH-04", r.ConfigPath, "line %d: identity_check has no value; give path and user_id, or remove the key", m.Content[i].Line)
		}
		if m.Content[i].Value == "mutation" && m.Content[i+1].ShortTag() == "!!null" {
			r.add(Invalid, "MUT-01", r.ConfigPath, "line %d: mutation has no value; give mutants, up, down and binding, or remove the key", m.Content[i].Line)
		}
	}
}

func (r *Report) validateTimeLimits(c *model.Config) {
	tl := c.TimeLimits
	if tl == nil {
		return
	}
	p := r.ConfigPath
	if tl.MinInterval != "" {
		if d, err := parseDuration(tl.MinInterval); err != nil {
			r.add(Invalid, "TIME-07", p, "time_limits.min_interval: %v", err)
		} else {
			r.limits.minInterval = d
		}
	}
	switch {
	case tl.MaxSamples < 0:
		r.add(Invalid, "TIME-07", p, "time_limits.max_samples can't be negative, got %d", tl.MaxSamples)
	case tl.MaxSamples > 0:
		r.limits.maxSamples = tl.MaxSamples
	}
	if tl.MaxWindow != "" {
		if d, err := parseDuration(tl.MaxWindow); err != nil {
			r.add(Invalid, "TIME-07", p, "time_limits.max_window: %v", err)
		} else {
			r.limits.maxWindow = d
		}
	}
	if r.limits.minInterval > r.limits.maxWindow {
		r.add(Invalid, "TIME-07", p, "time_limits.min_interval (%s) is longer than time_limits.max_window (%s)", r.limits.minInterval, r.limits.maxWindow)
	}
	if tl.MaxCall != "" {
		if d, err := parseDuration(tl.MaxCall); err != nil {
			r.add(Invalid, "TIME-13", p, "time_limits.max_call: %v", err)
		} else {
			r.limits.maxCall = d
		}
	}
}

var durationPattern = regexp.MustCompile(`^([0-9]+(\.[0-9]+)?(ms|s|m))+$`)

func parseDuration(s string) (time.Duration, error) {
	if !durationPattern.MatchString(s) {
		return 0, fmt.Errorf("%q is not a duration with a unit (ms, s or m), such as 500ms or 10s", s)
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration: %v", s, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%q must be longer than zero", s)
	}
	return d, nil
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
