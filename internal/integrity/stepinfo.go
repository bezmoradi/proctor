package integrity

import (
	"fmt"
	"strings"

	"proctor/internal/expr"
	"proctor/internal/model"
)

const (
	kindAct     = "act"
	kindObserve = "observe"
)

type described struct {
	at    stepAt
	info  model.StepInfo
	known bool
	fired map[string]bool
}

func (d described) acts() bool { return d.known && d.info.Kind == kindAct }

func (d described) observes() bool {
	return d.known && d.info.Kind == kindObserve && d.at.step.Expect.Count() > 0
}

func (r *Report) ApplyStepInfo(info map[model.StepRef]model.StepInfo) {
	c := r.Config
	if c == nil {
		return
	}
	checked := map[model.StepRef]bool{}
	for _, sf := range r.Specs {
		tables := map[string][]described{}
		var order []string
		for i := range sf.Spec.Scenarios {
			sc := &sf.Spec.Scenarios[i]
			ds := r.describe(sf.Path, sc, info, c)
			where := scenarioWhere(sc)
			r.checkStateChecks(sf.Path, where, ds, c)
			markCheckedSetup(ds, c, checked)
			if sc.Case == nil {
				r.checkRunScoped(sf.Path, where, ds)
				continue
			}
			t := sc.Case.Table
			if _, ok := tables[t]; !ok {
				order = append(order, t)
				tables[t] = ds
				continue
			}
			for _, d := range ds {
				if !d.at.setup {
					tables[t] = append(tables[t], d)
				}
			}
		}
		for _, t := range order {
			mark := len(r.Findings)
			r.checkRunScoped(sf.Path, "table "+t, tables[t])
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
	r.checkSetupExercised(checked)
}

func markCheckedSetup(ds []described, c *model.Config, checked map[model.StepRef]bool) {
	for i, d := range ds {
		if !d.at.setup || !d.acts() || d.info.Destructive {
			continue
		}
		driver, _, _ := model.SplitDo(d.at.step.Do)
		for _, later := range ds[i+1:] {
			laterDriver, _, _ := model.SplitDo(later.at.step.Do)
			if !later.at.setup && laterDriver == driver && later.checksState(c) {
				checked[d.at.ref] = true
				break
			}
		}
	}
}

func waived(st *model.Step, rule string) bool {
	return waivable[rule] && strings.TrimSpace(st.Waive[rule]) != ""
}

func (r *Report) describe(p string, sc *model.Scenario, info map[model.StepRef]model.StepInfo, c *model.Config) []described {
	ats := stepsOf(p, sc)
	out := make([]described, len(ats))
	saved := map[string]bool{}
	for k, at := range ats {
		d := described{at: at, fired: map[string]bool{}}
		r.quietly(at.quiet, func() {
			st := at.step
			where := fmt.Sprintf("%s (%s)", at.label, st.ID)
			driver, _, doOK := model.SplitDo(st.Do)
			in, found := info[at.ref]
			if !found {
				if _, configured := c.Drivers[driver]; doOK && configured {
					r.add(Invalid, "EXE-01", p, "%s: driver %q gave no description of this step", where, driver)
				}
				return
			}
			for _, pr := range in.Problems {
				rule := strings.TrimSpace(pr.Rule)
				if rule == "" {
					rule = "EXE-01"
				}
				if rule == "AUTH-06" && acceptedVariant(st, c) {
					continue
				}
				d.fired[rule] = true
				if waived(st, rule) {
					continue
				}
				msg := pr.Message
				if pr.Path != "" {
					msg = pr.Path + ": " + msg
				}
				r.add(Invalid, rule, p, "%s: %s", where, msg)
			}
			switch in.Kind {
			case kindAct, kindObserve:
				d.info, d.known = in, true
				r.checkHandleUse(p, where, st, in)
			case "":
				if len(in.Problems) == 0 {
					r.add(Invalid, "EXE-01", p, "%s: driver %q gave no kind for this step; a step must act or observe", where, driver)
				}
			default:
				r.add(Invalid, "EXE-01", p, "%s: driver %q described this step as %q; a step must act or observe", where, driver, in.Kind)
			}
			seenRef := map[string]bool{}
			for _, name := range in.Refs {
				if seenRef[name] {
					continue
				}
				seenRef[name] = true
				r.checkRef(p, where+": with", name, saved, c, sc, true)
			}
			if !d.known {
				return
			}
			if st.Expect.Count() == 0 {
				if in.Kind == kindAct {
					r.add(Invalid, "EXE-03", p, "%s: this step acts, so it must state at least one expectation", where)
				} else {
					r.add(Invalid, "EXE-03", p, "%s: every step must state at least one expectation", where)
				}
			}
			if in.Kind == kindAct && hasTime(st) {
				r.add(Invalid, "TIME-11", p, "%s: eventually, consistently and never are only for steps that observe; this step acts", where)
			}
		})
		for name := range at.step.Save {
			saved[name] = true
		}
		out[k] = d
	}
	return out
}

func (r *Report) checkHandleUse(p, where string, st *model.Step, in model.StepInfo) {
	switch {
	case st.As != "" && !in.OpensHandle:
		r.add(Invalid, "HND-05", p, "%s: as %q: operation %s doesn't open a handle, so the step can't name one", where, st.As, st.Do)
	case st.As == "" && in.OpensHandle:
		r.add(Invalid, "HND-05", p, "%s: operation %s opens a handle; name it with as, so later steps can use it with on", where, st.Do)
	}
	switch {
	case st.On != "" && !in.UsesHandle:
		r.add(Invalid, "HND-06", p, "%s: on %q: operation %s doesn't use a handle, so the step can't name one", where, st.On, st.Do)
	case st.On == "" && in.UsesHandle:
		r.add(Invalid, "HND-06", p, "%s: operation %s works on a handle; name the handle with on, as opened by an earlier step's as", where, st.Do)
	}
}

func acceptedVariant(st *model.Step, c *model.Config) bool {
	return st.Variant != "" && contains(c.Identities[st.Identity].AcceptedVariants, st.Variant)
}

func refusedVariant(st *model.Step, c *model.Config) bool {
	return st.Variant != "" && !contains(c.Identities[st.Identity].AcceptedVariants, st.Variant)
}

func (d described) checksState(c *model.Config) bool {
	if !d.observes() || refusedVariant(d.at.step, c) {
		return false
	}
	return d.info.StateCheck || acceptedVariant(d.at.step, c)
}

func (r *Report) checkStateChecks(p, where string, ds []described, c *model.Config) {
	for i, d := range ds {
		if d.at.setup || !d.known {
			continue
		}
		st := d.at.step
		if !d.acts() {
			if st.NoStateCheck != "" {
				r.add(Invalid, "EXE-07", p, "%s step %s: no_state_check is only for steps that act", where, st.ID)
			}
			continue
		}
		checked := false
		for _, later := range ds[i+1:] {
			if !later.at.setup && later.checksState(c) {
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
			r.add(Invalid, "EXE-07", p, "%s step %s acts, but no later step of the scenario checks the state it changed (add a later step that observes it with at least one expectation and an accepted credential, and that its driver counts as a state check, which a step expecting to be denied isn't; or set no_state_check to the reason none is possible)",
				where, st.ID)
		}
	}
}

func (r *Report) checkRunScoped(p, where string, ds []described) {
	for i, d := range ds {
		if !d.known {
			continue
		}
		st := d.at.step
		fired := map[string]bool{}
		for rule := range d.fired {
			fired[rule] = true
		}
		var msgs []string
		if d.acts() {
			for _, v := range dataLeaves(st.With, d.info.Data) {
				if v == "" || strings.Contains(v, "${"+expr.RunID+"}") || refersToSaved(v) {
					continue
				}
				for _, later := range ds[i+1:] {
					if asserts(later.at.step, v) {
						fired["FIX-05"] = true
						msgs = append(msgs, fmt.Sprintf("%s step %s sends %q and step %s expects it, but it doesn't contain ${run.id} or a saved value, so data from an earlier run could match (use ${run.id} in the value, or waive FIX-05 with the reason it can't vary)", where, st.ID, v, later.at.step.ID))
						break
					}
				}
			}
		}
		unguarded := false
		if d.acts() && d.info.Destructive && d.info.FixedTarget {
			unguarded = true
			for _, earlier := range ds[:i] {
				if earlier.observes() {
					unguarded = false
					break
				}
			}
			if unguarded {
				fired["EXE-09"] = true
			}
		}
		for _, rule := range sortedKeys(st.Waive) {
			if waived(st, rule) && !fired[rule] {
				r.add(Invalid, rule, p, "%s step %s: waives %s, but the rule doesn't apply to this step; remove the waiver", where, st.ID, rule)
			}
		}
		if !waived(st, "FIX-05") {
			for _, m := range msgs {
				r.add(Invalid, "FIX-05", p, "%s", m)
			}
		}
		if unguarded && !waived(st, "EXE-09") {
			r.add(Invalid, "EXE-09", p, "%s step %s acts destructively on a fixed target (%s), which the scenario didn't create and may hold data that isn't the test's; check it first with an earlier step that observes it (for example that it doesn't exist), use a target with a saved value or ${run.id}, or waive EXE-09 with the reason it's safe",
				where, st.ID, st.Do)
		}
	}
}

func dataLeaves(with any, data []string) []string {
	if len(data) == 0 {
		return stringLeaves(with)
	}
	var out []string
	for _, p := range data {
		path, err := expr.ParsePath(p)
		if err != nil {
			return stringLeaves(with)
		}
		if v, ok := path.Select(with); ok {
			out = append(out, stringLeaves(v)...)
		}
	}
	return out
}

func refersToSaved(v string) bool {
	for _, m := range refPattern.FindAllStringSubmatch(v, -1) {
		if expr.VarName(m[1]) {
			return true
		}
	}
	return false
}

func asserts(st *model.Step, v string) bool {
	for _, x := range assertedValues(st.Expect) {
		for _, s := range stringLeaves(x) {
			if s == v {
				return true
			}
		}
	}
	if st.Expect != nil {
		for _, x := range st.Expect.Contains {
			if s, isText := x.(string); isText && s != "" && strings.Contains(v, s) {
				return true
			}
		}
	}
	return false
}
