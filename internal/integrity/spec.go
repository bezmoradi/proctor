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
	phaseSetup = "setup"
	phaseSteps = "steps"
)

var waivable = map[string]bool{"FIX-05": true, "HTTP-09": true, "EXE-09": true}

const defaultEvery = 500 * time.Millisecond

type stepAt struct {
	ref   model.StepRef
	step  *model.Step
	label string
	setup bool
	quiet bool
}

func scenarioWhere(sc *model.Scenario) string {
	if sc.Case != nil {
		return fmt.Sprintf("table %s case %s", sc.Case.Table, sc.ID)
	}
	return "scenario " + sc.ID
}

func stepsOf(p string, sc *model.Scenario) []stepAt {
	where := scenarioWhere(sc)
	quiet := sc.Case != nil && !sc.Case.First
	out := make([]stepAt, 0, len(sc.Setup)+len(sc.Steps))
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
	for i := range s.Scenarios {
		sc := &s.Scenarios[i]
		where := fmt.Sprintf("scenarios[%d]", i)
		if sc.Case != nil {
			where = fmt.Sprintf("table %s case", sc.Case.Table)
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
		where = scenarioWhere(sc)
		if strings.TrimSpace(sc.Name) == "" && sc.Case == nil {
			r.add(Invalid, "EXE-01", p, "%s: name is required", where)
		}
		if len(sc.Steps) == 0 {
			r.add(Invalid, "EXE-01", p, "%s: at least one verification step is required", where)
		}
		ids := map[string]bool{}
		saved := map[string]bool{}
		for _, at := range stepsOf(p, sc) {
			r.quietly(at.quiet, func() {
				r.validateStep(p, at, sc, c, ids, saved)
			})
			for name := range at.step.Save {
				saved[name] = true
			}
		}
	}
}

func (r *Report) validateStep(p string, at stepAt, sc *model.Scenario, c *model.Config, ids, saved map[string]bool) {
	st := at.step
	where := at.label
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
	r.checkTime(p, where, st)
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
	for _, rule := range sortedKeys(st.Waive) {
		switch {
		case !waivable[rule]:
			r.add(Invalid, "EXE-07", p, "%s: waive names %q; only FIX-05, HTTP-09 and EXE-09 can be waived (EXE-07 uses no_state_check)", where, rule)
		case strings.TrimSpace(st.Waive[rule]) == "":
			r.add(Invalid, rule, p, "%s: the %s waiver must state a reason", where, rule)
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
	if at.setup {
		r.add(Invalid, "AUTH-06", p, "%s: a credential variant is for negative tests, not setup", where)
	}
}

func hasTime(st *model.Step) bool {
	return st.Eventually != nil || st.Consistently != nil || st.Never != nil
}

func (r *Report) checkTime(p, where string, st *model.Step) {
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
		r.add(Invalid, "EXE-01", p, "%s: a step can use only one of eventually, consistently and never", where)
		return
	}
	t := used[0]
	if st.Expect.Count() == 0 {
		r.add(Invalid, "EXE-03", p, "%s: %s needs expect with at least one expectation to sample", where, t.name)
	}
	if t.name != "eventually" && len(st.Save) > 0 {
		r.add(Invalid, "TIME-11", p, "%s: save can't be used with %s; only eventually saves, from its passing sample", where, t.name)
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
	for _, list := range []struct {
		name  string
		paths []string
	}{{"present", ex.Present}, {"absent", ex.Absent}} {
		for _, k := range list.paths {
			if contains(in[k], list.name) {
				r.add(Invalid, "EXE-03", p, "%s: %s lists %s twice", where, list.name, k)
			} else if list.name == "present" && contains(in[k], "equal") {
				r.add(Invalid, "EXE-03", p, "%s: %s is asserted by both equal and present", where, k)
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
		switch v := ex.Length[k].(type) {
		case int:
			if v < 0 {
				r.add(Invalid, "EXE-03", p, "%s: length of %s can't be negative", where, k)
			}
		case string:
			if names, err := references(v); err != nil || len(names) != 1 || v != "${"+names[0]+"}" {
				r.add(Invalid, "EXE-03", p, "%s: length of %s must be an integer or one ${name} reference", where, k)
			}
		default:
			r.add(Invalid, "EXE-03", p, "%s: length of %s must be an integer or one ${name} reference", where, k)
		}
	}
	for _, k := range sortedKeys(ex.Unordered) {
		note("unordered", k)
		if err := checkJSONValue(ex.Unordered[k]); err != nil {
			r.add(Invalid, "EXE-03", p, "%s: unordered: expected values for %s aren't JSON values: %v", where, k, err)
		}
	}
	for _, m := range []struct {
		name string
		vals map[string]map[string]any
	}{{"any", ex.Any}, {"none", ex.None}} {
		for _, k := range sortedKeys(m.vals) {
			note(m.name, k)
			for _, sub := range sortedKeys(m.vals[k]) {
				if strings.TrimSpace(sub) == "" {
					r.add(Invalid, "EXE-03", p, "%s: %s of %s has an empty field name", where, m.name, k)
				}
				if err := checkJSONValue(m.vals[k][sub]); err != nil {
					r.add(Invalid, "EXE-03", p, "%s: %s of %s: expected value for %s isn't a JSON value: %v", where, m.name, k, sub, err)
				}
			}
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
	for _, m := range []map[string]map[string]any{ex.Any, ex.None} {
		for _, k := range sortedKeys(m) {
			out = append(out, m[k])
		}
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
	for _, m := range []map[string]map[string]any{ex.Any, ex.None} {
		for _, k := range sortedKeys(m) {
			for _, sub := range sortedKeys(m[k]) {
				out = append(out, m[k][sub])
			}
		}
	}
	return out
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

func (r *Report) checkSetupExercised() {
	type use struct{ spec, scenario, step, desc string }
	verified := map[string]bool{}
	setup := map[string]use{}
	var order []string
	for _, sf := range r.Specs {
		for _, sc := range sf.Spec.Scenarios {
			for i := range sc.Steps {
				if key, _, ok := operation(&sc.Steps[i]); ok {
					verified[key] = true
				}
			}
			for i := range sc.Setup {
				key, desc, ok := operation(&sc.Setup[i])
				if !ok {
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
			r.add(Invalid, "FIX-08", u.spec, "%s setup step %s uses %s, but no verification step in the set exercises that operation with that outcome; a defect in it would be reported only as a setup ERROR",
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
