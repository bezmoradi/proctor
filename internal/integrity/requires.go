package integrity

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"proctor/internal/model"
)

type Build struct {
	Version  string
	Features []string
}

var Running Build

const requiresRule = "INT-22"

var (
	minVersion  = regexp.MustCompile(`^>=\s*(\d+)\.(\d+)\.(\d+)$`)
	coreVersion = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)`)
)

func (r *Report) checkRunningBuild(configPath string, data []byte) bool {
	var head struct {
		Requires *model.Requirements `yaml:"requires"`
	}
	if yaml.Unmarshal(data, &head) != nil || head.Requires == nil {
		return true
	}
	req := head.Requires
	ok := true
	refuse := func(format string, args ...any) {
		r.add(Invalid, requiresRule, configPath, format, args...)
		ok = false
	}
	if req.Proctor == "" && len(req.Features) == 0 {
		refuse("requires names neither proctor nor features; remove it, or say what the specifications need")
	}
	if req.Proctor != "" {
		want := minVersion.FindStringSubmatch(strings.TrimSpace(req.Proctor))
		have := coreVersion.FindStringSubmatch(Running.Version)
		switch {
		case want == nil:
			refuse("requires.proctor %q must be >= and a version, such as \">=0.5.0\"", req.Proctor)
		case have == nil || compareVersions(have[1:], want[1:]) < 0:
			refuse("this Proctor is %s, and proctor.yaml requires %s; install a release that meets it", Running.Version, req.Proctor)
		}
	}
	has := map[string]bool{}
	for _, f := range Running.Features {
		has[f] = true
	}
	seen := map[string]bool{}
	for _, f := range req.Features {
		switch {
		case seen[f]:
			refuse("requires.features lists %q twice", f)
		case !has[f]:
			refuse("this Proctor (%s) has no feature %q, which proctor.yaml requires; install a release whose proctor version --features lists it", Running.Version, f)
		}
		seen[f] = true
	}
	return ok
}

func compareVersions(a, b []string) int {
	for i := range a {
		x, _ := strconv.Atoi(a[i])
		y, _ := strconv.Atoi(b[i])
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func (r *Report) checkUsedFeatures(c *model.Config) {
	if c.Requires == nil || len(c.Requires.Features) == 0 {
		return
	}
	listed := map[string]bool{}
	for _, f := range c.Requires.Features {
		listed[f] = true
	}
	used := usedFeatures(c, r.Specs)
	names := make([]string, 0, len(used))
	for f := range used {
		names = append(names, f)
	}
	sort.Strings(names)
	for _, f := range names {
		if !listed[f] {
			r.add(Invalid, requiresRule, r.ConfigPath, "%s uses %s, but requires.features doesn't list it; add it, so an older Proctor is refused before it misreads the specifications", used[f], f)
		}
	}
}

func usedFeatures(c *model.Config, specs []SpecFile) map[string]string {
	used := map[string]string{}
	note := func(feature, where string) {
		if _, ok := used[feature]; !ok {
			used[feature] = where
		}
	}
	cfg := "proctor.yaml"
	if c.AppLogs != nil {
		note("app_logs", cfg+" app_logs")
	}
	if c.Mutation != nil {
		note("mutate", cfg+" mutation")
	}
	if len(c.Relations) > 0 {
		note("relations", cfg+" relations")
	}
	if c.IdentityCheck != nil {
		note("auth04.identity_check", cfg+" identity_check")
	}
	if len(c.Invariants) > 0 {
		note("invariants", cfg+" invariants")
	}
	for _, name := range sortedKeys(c.Drivers) {
		d := c.Drivers[name]
		where := fmt.Sprintf("%s driver %s", cfg, name)
		switch d.Official {
		case "browser", "cli", "docker", "otel", "redis", "stub":
			note(d.Official, where)
		}
		if d.Program != "" {
			note("drivers.repository", where)
		}
		if d.PolicyFrom != "" {
			note("policy_from", where)
		}
	}
	for _, sf := range specs {
		if sf.Spec == nil {
			continue
		}
		for _, t := range sf.Spec.Tables {
			where := fmt.Sprintf("%s table %s", sf.Path, t.ID)
			if t.Covers.Kind != 0 {
				note("tables.covers", where)
			}
			if t.Eventually != nil || t.Consistently != nil || t.Never != nil {
				note("table.time", where)
			}
		}
		for i := range sf.Spec.Scenarios {
			sc := &sf.Spec.Scenarios[i]
			where := fmt.Sprintf("%s %s", sf.Path, scenarioWhere(sc))
			if len(sc.Cleanup) > 0 {
				note("cleanup", where)
			}
			if sc.Transcript.Kind == yaml.ScalarNode && sc.Transcript.Value == "true" {
				note("transcripts", where)
			}
			if sc.ApprovedLog.Kind == yaml.ScalarNode && sc.ApprovedLog.Value == "true" {
				note("approved_logs", where)
			}
			if sc.Requires == model.RequiresDisposableEnvironment {
				note("requires.disposable_environment", where)
			}
			for k, part := range [][]model.Step{sc.Setup, sc.Steps, sc.Cleanup} {
				for j := range part {
					st := &part[j]
					stepWhere := where + " step " + st.ID
					if st.HasUnlessPassed() {
						note("cleanup.unless_passed", stepWhere)
					}
					if k == 2 && st.Eventually != nil {
						note("cleanup.eventually", stepWhere)
					}
					for _, sv := range st.Save {
						if sv.Capture != "" {
							note("save.capture", stepWhere)
						}
						if sv.Secret {
							note("save.secret", stepWhere)
						}
					}
					if name, op, ok := model.SplitDo(st.Do); ok && c.Drivers[name].Official == "http" {
						if with, isMap := st.With.(map[string]any); isMap && with["host"] != nil {
							note("http.virtual_hosts", stepWhere)
						}
						if op == "request" && (st.As != "" || st.On != "") {
							note("http.cookies", stepWhere)
						}
					}
					if st.Timeout != "" {
						note("step.timeout", stepWhere)
					}
					for _, w := range st.Waive {
						if w.Listed {
							note("waive.rows", stepWhere)
						}
					}
					if ex := st.Expect; ex != nil {
						if len(ex.Matches) > 0 {
							note("matchers.matches", stepWhere)
						}
						if len(ex.Excludes) > 0 {
							note("matchers.excludes", stepWhere)
						}
						if len(ex.Differs) > 0 {
							note("matchers.differs", stepWhere)
						}
					}
				}
			}
		}
	}
	return used
}
