package integrity

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"proctor/internal/expr"
	"proctor/internal/model"
)

const (
	phaseSetup   = "setup"
	phaseSteps   = "steps"
	phaseCleanup = "cleanup"
)

var waivable = map[string]bool{"FIX-05": true, "HTTP-09": true, "EXE-09": true}

const defaultEvery = 500 * time.Millisecond

type stepAt struct {
	ref     model.StepRef
	step    *model.Step
	label   string
	setup   bool
	cleanup bool
	quiet   bool
}

func scenarioWhere(sc *model.Scenario) string {
	if sc.Case != nil {
		return fmt.Sprintf("table %s case %s", sc.Case.Label(), sc.ID)
	}
	return "scenario " + sc.ID
}

func stepsOf(p string, sc *model.Scenario) []stepAt {
	where := scenarioWhere(sc)
	quiet := sc.Case != nil && !sc.Case.First
	out := make([]stepAt, 0, len(sc.Setup)+len(sc.Steps)+len(sc.Cleanup))
	for j := range sc.Setup {
		label := fmt.Sprintf("%s setup[%d]", where, j)
		if sc.Case != nil {
			label = fmt.Sprintf("table %s setup[%d]", sc.Case.Group, j)
		}
		out = append(out, stepAt{
			ref:  model.StepRef{Spec: p, Scenario: sc.ID, Phase: phaseSetup, Index: j},
			step: &sc.Setup[j], label: label, setup: true, quiet: quiet,
		})
	}
	for j := range sc.Steps {
		out = append(out, stepAt{
			ref:  model.StepRef{Spec: p, Scenario: sc.ID, Phase: phaseSteps, Index: j},
			step: &sc.Steps[j], label: fmt.Sprintf("%s steps[%d]", where, j),
		})
	}
	for j := range sc.Cleanup {
		label := fmt.Sprintf("%s cleanup[%d]", where, j)
		if sc.Case != nil {
			label = fmt.Sprintf("table %s cleanup[%d]", sc.Case.Label(), j)
		}
		out = append(out, stepAt{
			ref:  model.StepRef{Spec: p, Scenario: sc.ID, Phase: phaseCleanup, Index: j},
			step: &sc.Cleanup[j], label: label, cleanup: true, quiet: sc.Case != nil && !sc.Case.FirstRow,
		})
	}
	return out
}

func (r *Report) validateSpec(sf SpecFile, c *model.Config, seen map[string]string) {
	p, s := sf.Path, sf.Spec
	if s.Version != 2 {
		r.add(Invalid, "INT-15", p, "version must be 2, got %d", s.Version)
	}
	if len(s.Scenarios) == 0 && len(s.Tables) == 0 {
		r.add(Violation, "INT-04", p, "the specification defines no scenarios")
	}
	logTables := map[string]bool{}
	local := map[string]string{}
	for i := range s.Scenarios {
		sc := &s.Scenarios[i]
		laterRow := sc.Case != nil && !sc.Case.FirstRow
		where := fmt.Sprintf("scenarios[%d]", i)
		if sc.Case != nil {
			where = fmt.Sprintf("table %s case", sc.Case.Label())
		}
		if !idPattern.MatchString(sc.ID) {
			r.add(Invalid, "INT-02", p, "%s: invalid id %q", where, sc.ID)
		} else if other, dup := seen[sc.ID]; dup {
			kind := "scenario"
			if sc.Case != nil {
				kind = "case"
			}
			if first, here := local[sc.ID]; here {
				r.add(Invalid, "INT-02", p, "%s: %s id %q is also used by %s in this file; the drivers' descriptions of steps are keyed by file and id, so theirs can't be told apart, and the checks that use them are skipped for every scenario with this id and every table with a case of it until the id is unique", where, kind, sc.ID, first)
			} else {
				r.add(Invalid, "INT-02", p, "%s: %s id %q is also used in %s", where, kind, sc.ID, other)
			}
		} else {
			seen[sc.ID] = p
			r.ScenarioIDs = append(r.ScenarioIDs, sc.ID)
		}
		if _, here := local[sc.ID]; !here {
			local[sc.ID] = where
			if sc.Case != nil {
				local[sc.ID] = "table " + sc.Case.Label()
			}
		}
		where = scenarioWhere(sc)
		if strings.TrimSpace(sc.Name) == "" && sc.Case == nil {
			r.add(Invalid, "EXE-01", p, "%s: name is required", where)
		}
		if len(sc.Steps) == 0 {
			r.add(Invalid, "EXE-01", p, "%s: at least one verification step is required", where)
		}
		if sc.Cleanup != nil && len(sc.Cleanup) == 0 && !laterRow {
			cleanupWhere := where
			if sc.Case != nil {
				cleanupWhere = "table " + sc.Case.Label()
			}
			r.add(Invalid, "EXE-14", p, "%s: cleanup lists no steps; remove it", cleanupWhere)
		}
		switch {
		case sc.Case == nil:
			r.checkExpectLogs(p, where, sc.ExpectLogs, c)
		case !logTables[sc.Case.Key]:
			logTables[sc.Case.Key] = true
			r.checkExpectLogs(p, "table "+sc.Case.Label(), sc.ExpectLogs, c)
		}
		ids := map[string]bool{}
		saved := map[string]bool{}
		hs := handleScope{opened: map[string]string{}, later: map[string]string{}}
		for _, at := range stepsOf(p, sc) {
			if at.step.As != "" {
				if _, seen := hs.later[at.step.As]; !seen {
					hs.later[at.step.As] = at.step.ID
				}
			}
		}
		for _, at := range stepsOf(p, sc) {
			r.quietly(at.quiet, func() {
				r.validateStep(p, at, sc, c, ids, saved, laterRow && !at.setup)
				r.checkHandles(p, at, sc, hs)
			})
			for name := range at.step.Save {
				saved[name] = true
			}
		}
	}
}

func sharedRefs(s *model.Spec) map[int]bool {
	count := map[string]int{}
	for i := range s.Scenarios {
		count[s.Scenarios[i].ID]++
	}
	tables := map[string]bool{}
	for i := range s.Scenarios {
		if sc := &s.Scenarios[i]; count[sc.ID] > 1 && sc.Case != nil {
			tables[sc.Case.Key] = true
		}
	}
	out := map[int]bool{}
	for i := range s.Scenarios {
		if sc := &s.Scenarios[i]; count[sc.ID] > 1 || sc.Case != nil && tables[sc.Case.Key] {
			out[i] = true
		}
	}
	return out
}

func (r *Report) checkExpectLogs(p, where string, list []model.LogExpectation, c *model.Config) {
	if list == nil {
		return
	}
	if c.AppLogs == nil {
		r.add(Invalid, "EXE-10", p, "%s: expect_logs needs app_logs in proctor.yaml; without it the application's log isn't read, so nothing could be expected", where)
		return
	}
	if len(list) == 0 {
		r.add(Invalid, "EXE-10", p, "%s: expect_logs lists no entries; remove it", where)
		return
	}
	levels := c.AppLogs.EffectiveLevels()
	seen := map[string]int{}
	for i, e := range list {
		at := fmt.Sprintf("%s: expect_logs[%d]", where, i)
		switch {
		case strings.TrimSpace(e.Level) == "":
			r.add(Invalid, "EXE-10", p, "%s: level is required", at)
		case !contains(levels, strings.ToUpper(e.Level)):
			r.add(Invalid, "EXE-10", p, "%s: level %s isn't one of app_logs.levels (%s), so no line at it could fail the scenario", at, e.Level, strings.Join(levels, ", "))
		}
		if e.Message == "" {
			r.add(Invalid, "EXE-10", p, "%s: message is required, the exact text of the log line's %s field", at, c.AppLogs.MessageKey())
		}
		if e.Count != nil && *e.Count < 1 {
			r.add(Invalid, "EXE-10", p, "%s: count must be at least 1, got %d", at, *e.Count)
		}
		if j, dup := seen[e.Key()]; dup {
			r.add(Invalid, "EXE-10", p, "%s: repeats expect_logs[%d] (same level, ignoring case, and message)", at, j)
		} else {
			seen[e.Key()] = i
		}
	}
}

func (r *Report) validateStep(p string, at stepAt, sc *model.Scenario, c *model.Config, ids, saved map[string]bool, tableChecked bool) {
	st := at.step
	where := at.label
	if !idPattern.MatchString(st.ID) {
		r.add(Invalid, "INT-02", p, "%s: invalid step id %q", where, st.ID)
	} else if ids[st.ID] {
		r.add(Invalid, "INT-02", p, "%s: duplicate step id %q", where, st.ID)
	}
	ids[st.ID] = true
	where = fmt.Sprintf("%s (%s)", where, st.ID)
	if at.cleanup {
		r.checkCleanupStep(p, where, st, sc.Case != nil)
	}
	if st.NoStateCheck != "" && strings.TrimSpace(st.NoStateCheck) == "" && !tableChecked && !at.cleanup {
		r.add(Invalid, "EXE-07", p, "%s: no_state_check must state a reason", where)
	}
	if st.NoStateCheck != "" && at.setup {
		r.add(Invalid, "EXE-07", p, "%s: no_state_check is only for verification steps", where)
	}
	switch driver, _, ok := model.SplitDo(st.Do); {
	case strings.TrimSpace(st.Do) == "":
		r.add(Invalid, "EXE-01", p, "%s: do is required (<driver>.<operation>)", where)
	case !ok:
		r.add(Invalid, "EXE-01", p, "%s: do %q must be <driver>.<operation>", where, st.Do)
	default:
		if _, configured := c.Drivers[driver]; !configured {
			r.add(Invalid, "INT-12", p, "%s: do names driver %q, which proctor.yaml doesn't configure", where, driver)
		}
	}
	if st.With != nil {
		if err := checkJSONValue(st.With); err != nil {
			r.add(Invalid, "EXE-01", p, "%s: with can't be sent as JSON: %v", where, err)
		}
	}
	r.checkIdentity(p, where, at, c)
	if !at.cleanup {
		r.checkTime(p, where, st, tableChecked)
	}
	if !tableChecked {
		r.checkTimeout(p, where, st, c)
	}
	r.checkExpect(p, where, st.Expect)
	for _, v := range expectValues(st.Expect) {
		names, err := references(v)
		if err != nil {
			r.add(Invalid, "EXE-02", p, "%s: expect: %v", where, err)
			continue
		}
		for _, n := range names {
			r.checkRef(p, where+": expect", n, saved, c, sc, false)
		}
	}
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
	rules := sortedKeys(st.Waive)
	if tableChecked {
		rules = nil
	}
	for _, rule := range rules {
		w := st.Waive[rule]
		switch {
		case !waivable[rule]:
			r.add(Invalid, "EXE-07", p, "%s: waive names %q; only FIX-05, HTTP-09 and EXE-09 can be waived (EXE-07 uses no_state_check)", where, rule)
		case strings.TrimSpace(w.Reason) == "":
			r.add(Invalid, rule, p, "%s: the %s waiver must state a reason", where, rule)
		}
		if !w.Listed {
			continue
		}
		named := map[string]bool{}
		switch {
		case sc.Case == nil || at.setup:
			r.add(Invalid, rule, p, "%s: the %s waiver names rows, which only a table's step has; write the reason alone", where, rule)
		case len(w.Rows) == 0:
			r.add(Invalid, rule, p, "%s: the %s waiver's rows is empty; name the rows it covers, or write the reason alone if it covers every row", where, rule)
		default:
			for _, row := range w.Rows {
				if named[row] {
					r.add(Invalid, rule, p, "%s: the %s waiver names row %q twice", where, rule, row)
				}
				named[row] = true
			}
		}
	}
}

type handleScope struct {
	opened map[string]string
	later  map[string]string
}

func (r *Report) checkHandles(p string, at stepAt, sc *model.Scenario, hs handleScope) {
	st := at.step
	if st.As == "" && st.On == "" {
		return
	}
	where := fmt.Sprintf("%s (%s)", at.label, st.ID)
	driver, _, _ := model.SplitDo(st.Do)
	for _, f := range []struct{ key, name string }{{"as", st.As}, {"on", st.On}} {
		if f.name != "" && !model.HandleName.MatchString(f.name) {
			r.add(Invalid, "HND-01", p, "%s: %s %q isn't a valid handle name (a lower-case letter, then lower-case letters, digits or _)", where, f.key, f.name)
		}
	}
	if st.As != "" && st.On != "" {
		r.add(Invalid, "HND-04", p, "%s: a step can't have both as and on; it either opens a handle or uses one", where)
	}
	if sc.Case != nil {
		r.add(Invalid, "HND-08", p, "%s: a table's setup and cleanup can't open or use handles, because the table's cases can't use them", where)
	}
	if st.On != "" {
		d, ok := hs.opened[st.On]
		switch {
		case ok && d != driver:
			r.add(Invalid, "HND-07", p, "%s: on %q names a handle of driver %s, but this step's driver is %s; a handle can only be used by the driver that opened it", where, st.On, d, driver)
		case ok:
		case hs.later[st.On] != "" && hs.later[st.On] != st.ID:
			r.add(Invalid, "HND-03", p, "%s: on %q names a handle that is opened only later, by step %s; open it with as in an earlier step", where, st.On, hs.later[st.On])
		default:
			r.add(Invalid, "HND-03", p, "%s: on %q doesn't name a handle opened with as by an earlier step of this scenario", where, st.On)
		}
	}
	if st.As != "" {
		if _, dup := hs.opened[st.As]; dup {
			r.add(Invalid, "HND-02", p, "%s: as %q is already used by an earlier step of this scenario; each handle needs its own name", where, st.As)
		} else {
			hs.opened[st.As] = driver
		}
		if hasTime(st) {
			r.add(Invalid, "HND-09", p, "%s: as can't be used with eventually, consistently or never, because every sample would open another handle", where)
		}
	}
}

func (r *Report) checkIdentity(p, where string, at stepAt, c *model.Config) {
	st := at.step
	id, declared := c.Identities[st.Identity]
	if st.Identity != "" && !declared {
		r.add(Invalid, "INT-12", p, "%s: identity %q is not declared in proctor.yaml", where, st.Identity)
	}
	if st.Variant == "" {
		return
	}
	switch {
	case st.Identity == "":
		r.add(Invalid, "AUTH-06", p, "%s: variant %q needs an identity", where, st.Variant)
	case declared && !contains(id.Variants, st.Variant) && !contains(id.AcceptedVariants, st.Variant):
		r.add(Invalid, "AUTH-06", p, "%s: identity %q has no credential variant %q", where, st.Identity, st.Variant)
	}
	switch {
	case at.setup:
		r.add(Invalid, "AUTH-06", p, "%s: a credential variant is for negative tests, not setup", where)
	case at.cleanup:
		r.add(Invalid, "AUTH-06", p, "%s: a credential variant is for negative tests, not cleanup", where)
	}
}

func (r *Report) checkCleanupStep(p, where string, st *model.Step, table bool) {
	if hasTime(st) {
		r.add(Invalid, "EXE-14", p, "%s: eventually, consistently and never can't be used in cleanup; a cleanup step acts once, and checks that wait for an outcome belong in steps", where)
	}
	if len(st.Save) > 0 {
		r.add(Invalid, "EXE-14", p, "%s: save can't be used in cleanup; no step runs after cleanup to use the value", where)
	}
	if st.NoStateCheck != "" {
		r.add(Invalid, "EXE-14", p, "%s: no_state_check means nothing in cleanup, since cleanup steps need no state check (EXE-07); remove it", where)
	}
	if st.As != "" {
		r.add(Invalid, "EXE-14", p, "%s: as can't be used in cleanup; a handle opened there could never be used, since nothing runs after cleanup", where)
	}
	vals := append([]any{st.With}, expectValues(st.Expect)...)
	seen := map[string]bool{}
	for _, v := range vals {
		names, err := references(v)
		if err != nil {
			continue
		}
		for _, n := range names {
			if table && strings.HasPrefix(n, "row.") && !seen[n] {
				seen[n] = true
				r.add(Invalid, "TBL-05", p, "%s: ${%s} can't be used in cleanup, which is written once for the scenario or table and isn't filled in per row", where, n)
			}
		}
	}
}

func CleanupNeedsGuard(sc *model.Scenario, i int, in model.StepInfo, known bool) bool {
	if !known {
		return true
	}
	if in.Kind != kindAct || !in.Destructive {
		return false
	}
	if waived(stepAt{ref: model.StepRef{Scenario: sc.ID, Phase: phaseCleanup, Index: i}, step: &sc.Cleanup[i]}, "EXE-09") {
		return false
	}
	saved := map[string]bool{}
	for _, st := range append(append([]model.Step{}, sc.Setup...), sc.Steps...) {
		for name := range st.Save {
			saved[name] = true
		}
	}
	return described{info: in, runScope: targetsRunScoped(in.ExactTargets, saved)}.fixedTarget()
}

func hasTime(st *model.Step) bool {
	return st.Eventually != nil || st.Consistently != nil || st.Never != nil
}

func (r *Report) checkTimeout(p, where string, st *model.Step, c *model.Config) {
	if st.Timeout == "" {
		return
	}
	d, err := parseDuration(st.Timeout)
	switch {
	case err != nil:
		r.add(Invalid, "TIME-13", p, "%s: timeout: %v", where, err)
	case d > r.limits.maxCall:
		note := ""
		if c.TimeLimits == nil || c.TimeLimits.MaxCall == "" {
			note = ", the default when proctor.yaml doesn't set it"
		}
		r.add(Invalid, "TIME-13", p, "%s: timeout is %s, longer than time_limits.max_call (%s%s); raise max_call in proctor.yaml, where review sees it, or shorten the timeout", where, d, r.limits.maxCall, note)
	}
}

func (r *Report) checkTime(p, where string, st *model.Step, laterRow bool) {
	type construct struct{ name, field, window, every string }
	var used []construct
	if st.Eventually != nil {
		used = append(used, construct{"eventually", "within", st.Eventually.Within, st.Eventually.Every})
	}
	if st.Consistently != nil {
		used = append(used, construct{"consistently", "for", st.Consistently.For, st.Consistently.Every})
	}
	if st.Never != nil {
		used = append(used, construct{"never", "for", st.Never.For, st.Never.Every})
	}
	if len(used) == 0 {
		return
	}
	if len(used) > 1 {
		if !laterRow {
			r.add(Invalid, "EXE-01", p, "%s: a step can use only one of eventually, consistently and never", where)
		}
		return
	}
	t := used[0]
	if st.Expect.Count() == 0 {
		r.add(Invalid, "EXE-03", p, "%s: %s needs expect with at least one expectation to sample", where, t.name)
	}
	if t.name != "eventually" && len(st.Save) > 0 {
		r.add(Invalid, "TIME-11", p, "%s: save can't be used with %s; only eventually saves, from its passing sample", where, t.name)
	}
	if laterRow {
		return
	}
	window, werr := parseDuration(t.window)
	if t.window == "" {
		r.add(Invalid, "TIME-07", p, "%s: %s.%s is required", where, t.name, t.field)
	} else if werr != nil {
		r.add(Invalid, "TIME-07", p, "%s: %s.%s: %v", where, t.name, t.field, werr)
	} else if window > r.limits.maxWindow {
		r.add(Invalid, "TIME-07", p, "%s: %s.%s is %s, longer than time_limits.max_window (%s)", where, t.name, t.field, window, r.limits.maxWindow)
	}
	every, label := defaultEvery, fmt.Sprintf("%s.every (default %s)", t.name, defaultEvery)
	var eerr error
	if t.every != "" {
		label = t.name + ".every"
		if every, eerr = parseDuration(t.every); eerr != nil {
			r.add(Invalid, "TIME-07", p, "%s: %s: %v", where, label, eerr)
		}
	}
	if eerr != nil {
		return
	}
	if every < r.limits.minInterval {
		r.add(Invalid, "TIME-07", p, "%s: %s is %s, shorter than time_limits.min_interval (%s)", where, label, every, r.limits.minInterval)
	}
	if werr != nil || t.window == "" {
		return
	}
	if every*2 > window {
		r.add(Invalid, "TIME-07", p, "%s: %s is %s, more than half of %s.%s (%s)", where, label, every, t.name, t.field, window)
		return
	}
	samples := int64(window/every) + 1
	if window%every != 0 {
		samples++
	}
	if samples > int64(r.limits.maxSamples) {
		r.add(Invalid, "TIME-07", p, "%s: %s.%s %s sampled every %s schedules %d samples, more than time_limits.max_samples (%d)",
			where, t.name, t.field, window, every, samples, r.limits.maxSamples)
	}
}

func (r *Report) checkExpect(p, where string, ex *model.Expect) {
	if ex == nil {
		return
	}
	in := map[string][]string{}
	note := func(matcher, k string) {
		if _, err := expr.ParsePath(k); err != nil {
			r.add(Invalid, "EXE-03", p, "%s: %s: %v", where, matcher, err)
		}
		in[k] = append(in[k], matcher)
	}
	for _, k := range sortedKeys(ex.Equal) {
		note("equal", k)
		if err := checkJSONValue(ex.Equal[k]); err != nil {
			r.add(Invalid, "EXE-03", p, "%s: equal: expected value for %s isn't a JSON value: %v", where, k, err)
		}
	}
	for _, k := range sortedKeys(ex.Contains) {
		note("contains", k)
		if _, isText := ex.Contains[k].(string); !isText {
			r.add(Invalid, "EXE-03", p, "%s: contains: the expected text for %s must be text", where, k)
		}
	}
	for _, k := range sortedKeys(ex.Matches) {
		note("matches", k)
		pattern, isText := ex.Matches[k].(string)
		if !isText {
			r.add(Invalid, "EXE-12", p, "%s: matches: the pattern for %s must be text", where, k)
			continue
		}
		if _, err := model.MatchPattern(pattern); err != nil {
			r.add(Invalid, "EXE-12", p, "%s: matches %s: %v", where, k, err)
		}
	}
	for _, k := range sortedKeys(ex.Excludes) {
		note("excludes", k)
		texts, multi, ok := model.ExcludedTexts(ex.Excludes[k])
		switch {
		case !ok:
			r.add(Invalid, "EXE-12", p, "%s: excludes: the text for %s must be text or a list of texts", where, k)
			continue
		case multi && len(texts) == 0:
			r.add(Invalid, "EXE-12", p, "%s: excludes of %s lists no texts", where, k)
		}
		seen := map[string]bool{}
		for _, t := range texts {
			switch {
			case t == "":
				r.add(Invalid, "EXE-12", p, "%s: excludes of %s has an empty text, which every text contains, so the step could never pass", where, k)
			case seen[t]:
				r.add(Invalid, "EXE-12", p, "%s: excludes of %s lists %q twice", where, k, t)
			}
			seen[t] = true
		}
	}
	for _, k := range sortedKeys(ex.Differs) {
		note("differs", k)
		if err := checkJSONValue(ex.Differs[k]); err != nil {
			r.add(Invalid, "EXE-12", p, "%s: differs: the value for %s isn't a JSON value: %v", where, k, err)
		}
	}
	for _, list := range []struct {
		name  string
		paths []string
	}{{"present", ex.Present}, {"absent", ex.Absent}} {
		for _, k := range list.paths {
			if contains(in[k], list.name) {
				r.add(Invalid, "EXE-03", p, "%s: %s lists %s twice", where, list.name, k)
			} else if list.name == "present" {
				for _, m := range []string{"equal", "contains", "matches", "excludes", "differs"} {
					if contains(in[k], m) {
						r.add(Invalid, "EXE-03", p, "%s: %s is asserted by both %s and present", where, k, m)
						break
					}
				}
			}
			note(list.name, k)
		}
	}
	for _, k := range sortedKeys(ex.Keys) {
		note("keys", k)
		seen := map[string]bool{}
		for _, name := range ex.Keys[k] {
			if seen[name] {
				r.add(Invalid, "EXE-03", p, "%s: keys of %s lists %q twice", where, k, name)
			}
			seen[name] = true
		}
	}
	for _, k := range sortedKeys(ex.Length) {
		note("length", k)
		r.checkWhole(p, where, "length of "+k, ex.Length[k])
	}
	for _, k := range sortedKeys(ex.Unordered) {
		note("unordered", k)
		if err := checkJSONValue(ex.Unordered[k]); err != nil {
			r.add(Invalid, "EXE-03", p, "%s: unordered: expected values for %s aren't JSON values: %v", where, k, err)
		}
	}
	for _, m := range []struct {
		name string
		vals map[string]model.Claims[map[string]any]
	}{{"any", ex.Any}, {"none", ex.None}} {
		for _, k := range sortedKeys(m.vals) {
			note(m.name, k)
			claims := m.vals[k]
			if claims.Multi && len(claims.List) == 0 {
				r.add(Invalid, "EXE-03", p, "%s: %s of %s lists no claims", where, m.name, k)
			}
			seen := map[string]int{}
			for i, claim := range claims.All() {
				of := m.name + " of " + k + claimLabel(claims.Multi, i)
				if j, dup := seen[canonicalJSON(claim)]; dup {
					r.add(Invalid, "EXE-03", p, "%s: %s repeats claim[%d]", where, of, j)
				}
				seen[canonicalJSON(claim)] = i
				for _, sub := range sortedKeys(claim) {
					if strings.TrimSpace(sub) == "" {
						r.add(Invalid, "EXE-03", p, "%s: %s has an empty field name", where, of)
					} else if _, err := expr.ParseRelative(sub); err != nil {
						r.add(Invalid, "EXE-03", p, "%s: %s: claim key %s: %v", where, of, sub, err)
					}
					if err := checkJSONValue(claim[sub]); err != nil {
						r.add(Invalid, "EXE-03", p, "%s: %s: expected value for %s isn't a JSON value: %v", where, of, sub, err)
					}
				}
			}
		}
	}
	for _, k := range sortedKeys(ex.None) {
		anyClaims, ok := ex.Any[k]
		if !ok {
			continue
		}
		for i, n := range ex.None[k].All() {
			for j, a := range anyClaims.All() {
				if claimWithin(n, a) {
					r.add(Invalid, "EXE-03", p, "%s: none of %s%s forbids every element that any of %s%s requires, so the step can never pass", where, k, claimLabel(ex.None[k].Multi, i), k, claimLabel(anyClaims.Multi, j))
				}
			}
		}
	}
	for _, k := range sortedKeys(ex.Counts) {
		note("count", k)
		claims := ex.Counts[k]
		if claims.Multi && len(claims.List) == 0 {
			r.add(Invalid, "EXE-03", p, "%s: count of %s lists no claims", where, k)
		}
		seen := map[string]int{}
		for i, cm := range claims.All() {
			of := "count of " + k + claimLabel(claims.Multi, i)
			if j, dup := seen[canonicalJSON(cm.Where)]; dup && cm.Where != nil {
				r.add(Invalid, "EXE-03", p, "%s: %s counts the same where as claim[%d]", where, of, j)
			}
			seen[canonicalJSON(cm.Where)] = i
			if cm.Where == nil {
				r.add(Invalid, "EXE-03", p, "%s: %s needs where, the fields an element must have to be counted", where, of)
			}
			for _, sub := range sortedKeys(cm.Where) {
				if strings.TrimSpace(sub) == "" {
					r.add(Invalid, "EXE-03", p, "%s: %s has an empty field name in where", where, of)
				} else if _, err := expr.ParseRelative(sub); err != nil {
					r.add(Invalid, "EXE-03", p, "%s: %s: where key %s: %v", where, of, sub, err)
				}
				if err := checkJSONValue(cm.Where[sub]); err != nil {
					r.add(Invalid, "EXE-03", p, "%s: %s: expected value for %s isn't a JSON value: %v", where, of, sub, err)
				}
			}
			r.checkWhole(p, where, of+": is", cm.Is)
		}
	}
	for _, k := range sortedKeys(in) {
		if !contains(in[k], "absent") {
			continue
		}
		for _, other := range in[k] {
			if other != "absent" {
				r.add(Invalid, "EXE-03", p, "%s: %s is both absent and in %s", where, k, other)
			}
		}
	}
}

func (r *Report) checkWhole(p, where, what string, v any) {
	switch n := v.(type) {
	case int:
		if n < 0 {
			r.add(Invalid, "EXE-03", p, "%s: %s can't be negative", where, what)
		}
	case string:
		if names, err := references(n); err != nil || len(names) != 1 || n != "${"+names[0]+"}" {
			r.add(Invalid, "EXE-03", p, "%s: %s must be an integer or one ${name} reference", where, what)
		}
	default:
		r.add(Invalid, "EXE-03", p, "%s: %s must be an integer or one ${name} reference", where, what)
	}
}

func expectValues(ex *model.Expect) []any {
	if ex == nil {
		return nil
	}
	var out []any
	for _, k := range sortedKeys(ex.Equal) {
		out = append(out, ex.Equal[k])
	}
	for _, k := range sortedKeys(ex.Length) {
		out = append(out, ex.Length[k])
	}
	for _, k := range sortedKeys(ex.Unordered) {
		out = append(out, ex.Unordered[k])
	}
	for _, m := range []map[string]model.Claims[map[string]any]{ex.Any, ex.None} {
		for _, k := range sortedKeys(m) {
			for _, claim := range m[k].All() {
				out = append(out, claim)
			}
		}
	}
	for _, k := range sortedKeys(ex.Contains) {
		out = append(out, ex.Contains[k])
	}
	for _, k := range sortedKeys(ex.Counts) {
		for _, cm := range ex.Counts[k].All() {
			out = append(out, cm.Where, cm.Is)
		}
	}
	for _, k := range sortedKeys(ex.Excludes) {
		out = append(out, ex.Excludes[k])
	}
	for _, k := range sortedKeys(ex.Differs) {
		out = append(out, ex.Differs[k])
	}
	return out
}

func assertedValues(ex *model.Expect) []any {
	if ex == nil {
		return nil
	}
	var out []any
	for _, k := range sortedKeys(ex.Equal) {
		out = append(out, ex.Equal[k])
	}
	for _, k := range sortedKeys(ex.Unordered) {
		out = append(out, ex.Unordered[k])
	}
	for _, m := range []map[string]model.Claims[map[string]any]{ex.Any, ex.None} {
		for _, k := range sortedKeys(m) {
			for _, claim := range m[k].All() {
				for _, sub := range sortedKeys(claim) {
					out = append(out, claim[sub])
				}
			}
		}
	}
	for _, k := range sortedKeys(ex.Contains) {
		out = append(out, ex.Contains[k])
	}
	for _, k := range sortedKeys(ex.Counts) {
		for _, cm := range ex.Counts[k].All() {
			for _, sub := range sortedKeys(cm.Where) {
				out = append(out, cm.Where[sub])
			}
		}
	}
	return out
}

func claimLabel(multi bool, i int) string {
	if !multi {
		return ""
	}
	return fmt.Sprintf(" claim[%d]", i)
}

func claimWithin(small, large map[string]any) bool {
	for k, v := range small {
		w, ok := large[k]
		if !ok || canonicalJSON(v) != canonicalJSON(w) {
			return false
		}
	}
	return true
}

func canonicalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

var refPattern = regexp.MustCompile(`\$\{([^}]*)\}`)

func references(v any) ([]string, error) {
	var out []string
	var walk func(any) error
	text := func(s string) error {
		ms := refPattern.FindAllStringSubmatch(s, -1)
		if strings.Count(s, "${") != len(ms) {
			return fmt.Errorf("unclosed ${ in %q", s)
		}
		for _, m := range ms {
			out = append(out, m[1])
		}
		return nil
	}
	walk = func(v any) error {
		switch t := v.(type) {
		case string:
			return text(t)
		case map[string]any:
			for _, k := range sortedKeys(t) {
				if err := text(k); err != nil {
					return err
				}
				if err := walk(t[k]); err != nil {
					return err
				}
			}
		case []any:
			for _, x := range t {
				if err := walk(x); err != nil {
					return err
				}
			}
		}
		return nil
	}
	err := walk(v)
	return out, err
}

func (r *Report) checkRef(p, where, name string, saved map[string]bool, c *model.Config, sc *model.Scenario, inWith bool) {
	switch {
	case name == expr.RunID:
	case expr.VarName(name):
		if !saved[name] {
			r.add(Invalid, "EXE-02", p, "%s: ${%s} isn't saved by an earlier step of this scenario", where, name)
		}
	case expr.IsFill(name):
		if !inWith {
			r.add(Invalid, "EXE-02", p, "%s: ${%s} may be used only in with", where, name)
		}
	case expr.IsSecret(name):
		switch {
		case !inWith:
			r.add(Invalid, "AUTH-07", p, "%s: ${%s} may be used only in with", where, name)
		case !contains(c.Secrets, expr.SecretName(name)):
			r.add(Invalid, "AUTH-07", p, "%s: secret %q is not declared in proctor.yaml", where, expr.SecretName(name))
		}
	case strings.HasPrefix(name, "row."):
		col := strings.TrimPrefix(name, "row.")
		if sc.Case == nil {
			r.add(Invalid, "TBL-05", p, "%s: ${%s} can be used only in a table", where, name)
		} else if i := indexOf(sc.Case.Columns, col); i < 1 {
			r.add(Invalid, "TBL-05", p, "%s: ${%s} isn't one of the table's columns", where, name)
		}
	default:
		ident, value, ok := identityRef(name)
		if !ok {
			r.add(Invalid, "EXE-02", p, "%s: unsupported reference ${%s}: supported are ${run.id}, ${fill(N)}, ${secret.<name>}, ${<identity>.<value>} and ${name} for values saved by earlier steps", where, name)
			return
		}
		if _, declared := c.Identities[ident]; !declared || reservedNames[ident] {
			r.add(Invalid, "EXE-02", p, "%s: ${%s} names identity %q, which proctor.yaml doesn't declare", where, name, ident)
		} else if !expr.VarName(value) {
			r.add(Invalid, "EXE-02", p, "%s: ${%s}: %q isn't a valid identity value name", where, name, value)
		}
	}
}

func identityRef(name string) (ident, value string, ok bool) {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return "", "", false
	}
	return name[:i], name[i+1:], true
}

func (r *Report) checkSetupExercised(checked map[model.StepRef]bool) {
	type use struct{ spec, scenario, step, desc string }
	verified := map[string]bool{}
	setup := map[string]use{}
	var order []string
	for _, sf := range r.Specs {
		shared := sharedRefs(sf.Spec)
		for k, sc := range sf.Spec.Scenarios {
			for i := range sc.Steps {
				if key, _, ok := operation(&sc.Steps[i]); ok {
					verified[key] = true
				}
			}
			if shared[k] {
				continue
			}
			for i := range sc.Setup {
				key, desc, ok := operation(&sc.Setup[i])
				if !ok || checked[model.StepRef{Spec: sf.Path, Scenario: sc.ID, Phase: phaseSetup, Index: i}] {
					continue
				}
				if _, seen := setup[key]; !seen {
					owner := "scenario " + sc.ID
					if sc.Case != nil {
						owner = "table " + sc.Case.Group
					}
					setup[key] = use{sf.Path, owner, sc.Setup[i].ID, desc}
					order = append(order, key)
				}
			}
		}
	}
	for _, key := range order {
		if !verified[key] {
			u := setup[key]
			r.add(Invalid, "FIX-08", u.spec, "%s setup step %s uses %s, but no verification step in the set exercises that operation with that outcome, and it isn't a non-destructive act followed in its scenario by a verification step that checks state on the same driver; a defect in it would be reported only as a setup ERROR",
				u.scenario, u.step, u.desc)
		}
	}
}

var topLevelPath = regexp.MustCompile(`^\$\.[A-Za-z_][A-Za-z0-9_-]*$`)

func operation(st *model.Step) (key, desc string, ok bool) {
	if strings.TrimSpace(st.Do) == "" {
		return "", "", false
	}
	parts := []string{st.Do}
	if m, isMap := st.With.(map[string]any); isMap {
		for _, k := range sortedKeys(m) {
			if v, scalar := scalarText(m[k]); scalar {
				parts = append(parts, k+"="+v)
			}
		}
	}
	var outcome []string
	if st.Expect != nil {
		for _, k := range sortedKeys(st.Expect.Equal) {
			if !topLevelPath.MatchString(k) {
				continue
			}
			if v, scalar := scalarText(st.Expect.Equal[k]); scalar {
				outcome = append(outcome, k+"="+v)
			}
		}
	}
	desc = strings.Join(parts, " ")
	if len(outcome) > 0 {
		desc += " expecting " + strings.Join(outcome, " ")
	}
	return strings.Join(append(parts, outcome...), "\x00"), desc, true
}

func scalarText(v any) (string, bool) {
	switch v.(type) {
	case map[string]any, []any:
		return "", false
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	return refPattern.ReplaceAllString(string(b), "{}"), true
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

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func contains(list []string, s string) bool {
	return indexOf(list, s) >= 0
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}
