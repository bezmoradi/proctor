package integrity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"proctor/internal/expr"
	"proctor/internal/model"
	"proctor/internal/strictyaml"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

var driverName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

var identityTypes = map[string]bool{"none": true, "bearer": true, "basic": true, "oauth2_client_credentials": true}

var parkedDrivers = map[string]bool{"temporal": true, "logs": true}

var reservedNames = map[string]bool{"run": true, "secret": true, "row": true, "fill": true}

type limits struct {
	minInterval time.Duration
	maxSamples  int
	maxWindow   time.Duration
}

var defaultLimits = limits{minInterval: 100 * time.Millisecond, maxSamples: 1000, maxWindow: 10 * time.Minute}

func Load(src Source, configPath string) *Report {
	r := &Report{ConfigPath: configPath, Hashes: map[string]string{}, listed: map[string]bool{}, limits: defaultLimits}
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
		sf := SpecFile{Path: full, Spec: &spec}
		r.Specs = append(r.Specs, sf)
		r.validateSpec(sf, &cfg, seenScenario)
	}
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
		case d.Official == "":
			r.add(Invalid, "INT-12", p, "driver %q must name its official driver (official: <name>)", name)
		case !driverName.MatchString(d.Official):
			r.add(Invalid, "INT-12", p, "driver %q: invalid official driver name %q (a lower-case letter, then lower-case letters, digits or _)", name, d.Official)
		}
		if parkedDrivers[name] || parkedDrivers[d.Official] {
			r.add(Invalid, "INT-12", p, "driver %q isn't available: Temporal and log evidence (Phase 5) is parked", name)
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
	}
	r.validateTimeLimits(c)
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
