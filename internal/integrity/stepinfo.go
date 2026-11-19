package integrity

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"proctor/internal/expr"
	"proctor/internal/model"
)

const (
	kindAct     = "act"
	kindObserve = "observe"
)

type described struct {
	at       stepAt
	info     model.StepInfo
	known    bool
	fired    map[string]bool
	runScope bool
}

func (d described) acts() bool { return d.known && d.info.Kind == kindAct }

func (d described) fixedTarget() bool {
	return d.info.FixedTarget || len(d.info.ExactTargets) > 0 && !d.runScope
}

func (d described) observes() bool {
	return d.known && d.info.Kind == kindObserve && d.at.step.Expect.Count() > 0
}

func (r *Report) ApplyStepInfo(info map[model.StepRef]model.StepInfo, entries map[string][]model.PolicyEntry) {
	c := r.Config
	if c == nil {
		return
	}
	for _, in := range info {
		for _, f := range in.Files {
			if r.Hashes != nil {
				r.Hashes[f.Path] = f.SHA256
			}
		}
	}
	checked := map[model.StepRef]bool{}
	used := map[string]map[string]bool{}
	relUsed := map[relationKey]bool{}
	var seen reportedTargets
	for _, sf := range r.Specs {
		tables := map[string][]described{}
		labels := map[string]string{}
		var order []string
		shared := sharedRefs(sf.Spec)
		for i := range sf.Spec.Scenarios {
			markDirectUses(sf.Path, &sf.Spec.Scenarios[i], info, c, used)
			if shared[i] {
				continue
			}
			sc := &sf.Spec.Scenarios[i]
			ds := r.describe(sf.Path, sc, info, c)
			for _, d := range ds {
				driver, _, _ := model.SplitDo(d.at.step.Do)
				seen.note(c.PolicyGroup(driver), d)
			}
			markShowsUses(ds, c, used, true)
			where := scenarioWhere(sc)
			r.checkStateChecks(sf.Path, where, ds, c, relUsed)
			markCheckedSetup(ds, c, checked)
			if sc.Case == nil {
				r.checkRunScoped(sf.Path, where, ds, c, false)
				continue
			}
			t := sc.Case.Key
			if _, ok := tables[t]; !ok {
				order = append(order, t)
				labels[t] = sc.Case.Label()
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
			r.checkRunScoped(sf.Path, "table "+labels[t], tables[t], c, true)
			markShowsUses(tables[t], c, used, false)
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
	r.checkEntriesUsed(entries, used, c)
	r.checkRelationsUsed(c, seen, relUsed)
}

func use(used map[string]map[string]bool, group, key string) {
	if used[group] == nil {
		used[group] = map[string]bool{}
	}
	used[group][key] = true
}

func markDirectUses(p string, sc *model.Scenario, info map[model.StepRef]model.StepInfo, c *model.Config, used map[string]map[string]bool) {
	for _, at := range stepsOf(p, sc) {
		driver, _, _ := model.SplitDo(at.step.Do)
		for _, k := range info[at.ref].Uses {
			use(used, c.PolicyGroup(driver), k)
		}
	}
}

func markShowsUses(ds []described, c *model.Config, used map[string]map[string]bool, later bool) {
	for i, act := range ds {
		if !act.acts() || len(act.info.Targets) == 0 {
			continue
		}
		actDriver, _, _ := model.SplitDo(act.at.step.Do)
		guarded := act.info.Destructive && act.fixedTarget()
		checked := later && (!act.at.setup || !act.info.Destructive)
		for j, read := range ds {
			switch {
			case j == i, j > i && (!checked || read.at.setup || read.at.cleanup), j < i && !guarded:
				continue
			}
			readDriver, _, _ := model.SplitDo(read.at.step.Do)
			if !read.observes() || refusedVariant(read.at.step, c) || !read.info.StateCheck || c.PolicyGroup(readDriver) != c.PolicyGroup(actDriver) {
				continue
			}
			for _, a := range act.info.Targets {
				if k, ok := read.info.ShowsEntries[a]; ok {
					use(used, c.PolicyGroup(readDriver), k)
				}
			}
		}
	}
}

func (r *Report) checkEntriesUsed(entries map[string][]model.PolicyEntry, used map[string]map[string]bool, c *model.Config) {
	reported := map[string]map[string]bool{}
	for _, name := range sortedKeys(entries) {
		group := c.PolicyGroup(name)
		for _, e := range entries[name] {
			if used[group][e.Key] || reported[group][e.Key] {
				continue
			}
			if reported[group] == nil {
				reported[group] = map[string]bool{}
			}
			reported[group][e.Key] = true
			r.add(Invalid, e.Rule, r.ConfigPath, "drivers.%s.policy: %s: no step of the listed specifications uses this entry, which needs %s; an entry nothing uses declares nothing today and would silently apply to a step added later, so remove it, or add the step it was written for", group, e.Key, e.Use)
		}
	}
}

func markCheckedSetup(ds []described, c *model.Config, checked map[model.StepRef]bool) {
	for i, d := range ds {
		if !d.at.setup || !d.acts() || d.info.Destructive {
			continue
		}
		driver, _, _ := model.SplitDo(d.at.step.Do)
		for _, later := range ds[i+1:] {
			laterDriver, _, _ := model.SplitDo(later.at.step.Do)
			if later.at.setup || later.at.cleanup || c.PolicyGroup(laterDriver) != c.PolicyGroup(driver) {
				continue
			}
			if counts, _, _ := stateCheckFor(d, later, c); counts {
				checked[d.at.ref] = true
				break
			}
		}
	}
}

func waived(at stepAt, rule string) bool {
	w, has := at.step.Waive[rule]
	return has && waivable[rule] && strings.TrimSpace(w.Reason) != "" && w.Covers(at.ref.Scenario)
}

func (r *Report) describe(p string, sc *model.Scenario, info map[model.StepRef]model.StepInfo, c *model.Config) []described {
	ats := stepsOf(p, sc)
	out := make([]described, len(ats))
	saved := map[string]bool{}
	type handle struct {
		driver  string
		targets []string
	}
	opened := map[string]handle{}
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
				if at.cleanup && (rule == "AUTH-06" && st.Variant != "" || rule == "FIX-03" && in.Kind == kindObserve) {
					continue
				}
				d.fired[rule] = true
				if waived(at, rule) {
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
				if h, ok := opened[st.On]; ok && in.UsesHandle && len(in.Targets) == 0 && h.driver == driver {
					d.info.Targets = append([]string(nil), h.targets...)
				}
				if in.OpensHandle && st.As != "" {
					opened[st.As] = handle{driver: driver, targets: append([]string(nil), in.Targets...)}
				}
				d.runScope = targetsRunScoped(in.ExactTargets, saved)
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
			if in.Kind == kindAct && hasTime(st) && !at.cleanup {
				r.add(Invalid, "TIME-11", p, "%s: eventually, consistently and never are only for steps that observe; this step acts", where)
			}
			if in.Kind == kindObserve && at.cleanup {
				r.add(Invalid, "EXE-14", p, "%s: this step observes, and cleanup takes only acts; an observation's expectations are checks of the scenario, so they belong in steps, where a mismatch fails it", where)
			}
		})
		for name := range at.step.Save {
			saved[name] = true
		}
		out[k] = d
	}
	return out
}

func targetsRunScoped(targets []string, saved map[string]bool) bool {
	for _, t := range targets {
		for _, m := range refPattern.FindAllStringSubmatch(t, -1) {
			if m[1] == expr.RunID || expr.VarName(m[1]) && saved[m[1]] {
				return true
			}
		}
	}
	return false
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

func stateCheckFor(act, later described, c *model.Config) (bool, string, []relationKey) {
	st := later.at.step
	switch {
	case !later.observes():
		return false, "", nil
	case refusedVariant(st, c):
		return false, fmt.Sprintf("step %s uses the refused credential variant %q", st.ID, st.Variant), nil
	case !later.info.StateCheck:
		return false, fmt.Sprintf("step %s expects an outcome its driver doesn't count as reading state (for http, a status that describes the request or a refusal, such as 400, 401, 403, 405, 415, 422, 429 or 5xx)", st.ID), nil
	}
	actDriver, _, _ := model.SplitDo(act.at.step.Do)
	laterDriver, _, _ := model.SplitDo(st.Do)
	if c.PolicyGroup(actDriver) != c.PolicyGroup(laterDriver) {
		other := fmt.Sprintf("step %s is on driver %s, which isn't driver %s and doesn't share its policy (policy_from)", st.ID, laterDriver, actDriver)
		switch {
		case len(act.info.Targets) == 0:
			return false, fmt.Sprintf("%s, and driver %s reports no target for step %s, so no relation in proctor.yaml can make a read on another driver count for it", other, actDriver, act.at.step.ID), nil
		case len(later.info.Targets) == 0:
			return false, fmt.Sprintf("%s, and it reports no target, so no relation in proctor.yaml can make it count", other), nil
		}
		if uses := relationsFor(act, later, c); len(uses) > 0 {
			return true, "", uses
		}
		return false, fmt.Sprintf("%s; it reads %s, and counts only through a relation in proctor.yaml saying that the act changes what it reads, such as relations: [%s] (declare it only if the code shows it)", other, strings.Join(later.info.Targets, ", "), suggestedRelation(act, later, c)), nil
	}
	switch {
	case len(act.info.Targets) == 0 && len(later.info.Targets) == 0:
		return true, "", nil
	case len(act.info.Targets) == 0:
		return false, fmt.Sprintf("driver %s reports no target for step %s to compare with step %s's %s", actDriver, act.at.step.ID, st.ID, strings.Join(later.info.Targets, ", ")), nil
	case len(later.info.Targets) == 0:
		return false, fmt.Sprintf("step %s reports no target to compare with %s", st.ID, strings.Join(act.info.Targets, ", ")), nil
	}
	for _, a := range act.info.Targets {
		if contains(later.info.Shows, a) {
			return true, "", nil
		}
		for _, o := range later.info.Targets {
			if a == o || segmentPrefix(a, o) || segmentPrefix(o, a) {
				return true, "", nil
			}
		}
	}
	return false, fmt.Sprintf("step %s reads %s, which isn't %s%s", st.ID, strings.Join(later.info.Targets, ", "), strings.Join(act.info.Targets, ", "), pathKin(act, "it")), nil
}

func pathKin(d described, read string) string {
	for _, t := range d.info.Targets {
		if !strings.HasPrefix(t, "/") {
			return ""
		}
	}
	return fmt.Sprintf(", a path above or below it, or a path its driver's policy says %s shows (shows)", read)
}

func segmentPrefix(prefix, p string) bool {
	prefix = strings.TrimSuffix(prefix, "/")
	return prefix != "" && strings.HasPrefix(p, prefix+"/")
}

func (r *Report) checkStateChecks(p, where string, ds []described, c *model.Config, relUsed map[relationKey]bool) {
	for i, d := range ds {
		if d.at.setup || d.at.cleanup || !d.known {
			continue
		}
		st := d.at.step
		if !d.acts() {
			if st.NoStateCheck != "" {
				r.add(Invalid, "EXE-07", p, "%s step %s: no_state_check is only for steps that act", where, st.ID)
			}
			continue
		}
		checker := ""
		var rejected []string
		for _, later := range ds[i+1:] {
			if later.at.setup || later.at.cleanup {
				continue
			}
			counts, why, uses := stateCheckFor(d, later, c)
			for _, u := range uses {
				relUsed[u] = true
			}
			switch {
			case counts && checker == "":
				checker = later.at.step.ID
			case !counts && checker == "" && why != "":
				rejected = append(rejected, why)
			}
		}
		if strings.TrimSpace(st.NoStateCheck) != "" {
			if checker != "" {
				r.add(Invalid, "EXE-07", p, "%s step %s: no_state_check waives a state check, but later step %s already checks the state it changed; remove the waiver", where, st.ID, checker)
			}
			continue
		}
		if checker == "" {
			reads := ""
			if len(d.info.Targets) > 0 {
				reads = fmt.Sprintf(", on the same driver or one sharing its policy (policy_from), that reads %s%s, or on another driver, that reads a target a relation in proctor.yaml says this act shows (relations)", strings.Join(d.info.Targets, ", "), pathKin(d, "the read"))
			}
			notCounted := ""
			if len(rejected) > 0 {
				notCounted = "; not counted: " + strings.Join(rejected, "; ")
			}
			r.add(Invalid, "EXE-07", p, "%s step %s acts, but no later step of the scenario checks the state it changed (add a later step that observes it%s, with at least one expectation, an accepted credential and an outcome its driver counts as reading state, which for http is a 2xx, 3xx, 404, 409, 410, 412 or 423 status; or set no_state_check to the reason none is possible)%s",
				where, st.ID, reads, notCounted)
		}
	}
}

func guardFor(act, earlier described, accepted bool, c *model.Config) (bool, string) {
	st := earlier.at.step
	actDriver, _, _ := model.SplitDo(act.at.step.Do)
	earlierDriver, _, _ := model.SplitDo(st.Do)
	sameGroup := c.PolicyGroup(actDriver) == c.PolicyGroup(earlierDriver)
	switch {
	case earlier.acts():
		related := sameGroup && earlier.info.Destructive && sameExactTarget(act, earlier)
		if related && !accepted && waived(earlier.at, "EXE-09") {
			name := st.ID
			if earlier.at.ref.Scenario != act.at.ref.Scenario {
				name = fmt.Sprintf("%s (row %s)", st.ID, earlier.at.ref.Scenario)
			}
			return false, fmt.Sprintf("step %s acts on the same target, but EXE-09 accepted it only through its waiver, and a waived act guards nothing (waive EXE-09 here too, with its own reason, or guard this step)", name)
		}
		return accepted && related, ""
	case !earlier.observes():
		return false, ""
	case !sameGroup:
		return false, fmt.Sprintf("step %s is on driver %s, which isn't driver %s and doesn't share its policy (policy_from)", st.ID, earlierDriver, actDriver)
	case refusedVariant(st, c):
		return false, fmt.Sprintf("step %s uses the refused credential variant %q", st.ID, st.Variant)
	case !earlier.info.StateCheck:
		return false, fmt.Sprintf("step %s expects an outcome its driver doesn't count as reading state (for http, a status that describes the request or a refusal, such as 400, 401, 403, 405, 415, 422, 429 or 5xx)", st.ID)
	case len(act.info.Targets) == 0:
		return false, fmt.Sprintf("driver %s reports no target for step %s to compare with", actDriver, act.at.step.ID)
	case len(earlier.info.Targets) == 0:
		return false, fmt.Sprintf("step %s reports no target to compare with %s", st.ID, strings.Join(act.info.Targets, ", "))
	}
	for _, a := range act.info.Targets {
		if contains(earlier.info.Shows, a) {
			return true, ""
		}
	}
	for _, a := range act.info.ExactTargets {
		for _, o := range earlier.info.ExactTargets {
			if a == o || segmentPrefix(a, o) || segmentPrefix(o, a) {
				return true, ""
			}
		}
	}
	return false, fmt.Sprintf("step %s reads %s, which isn't %s as written, a path above or below it, or a path its driver's policy says it shows (shows)", st.ID, strings.Join(writtenTargets(earlier), ", "), strings.Join(writtenTargets(act), ", "))
}

func sameExactTarget(a, b described) bool {
	for _, x := range a.info.ExactTargets {
		if contains(b.info.ExactTargets, x) {
			return true
		}
	}
	return false
}

func writtenTargets(d described) []string {
	targets := d.info.ExactTargets
	if len(targets) == 0 {
		targets = d.info.Targets
	}
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, brief(t, briefTarget))
	}
	return out
}

const briefTarget = 160

func brief(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := 0
	for i := range s {
		if i > n {
			break
		}
		cut = i
	}
	return s[:cut] + "..."
}

type writtenWaiver struct {
	phase string
	index int
	rule  string
}

func (r *Report) checkRunScoped(p, where string, ds []described, c *model.Config, table bool) {
	accepted := make([]bool, len(ds))
	rows := map[string]bool{}
	for _, d := range ds {
		if table && !d.at.setup {
			rows[d.at.ref.Scenario] = true
		}
	}
	checkedRows := map[writtenWaiver]bool{}
	notFiring := map[writtenWaiver][]string{}
	var plain []writtenWaiver
	for i, d := range ds {
		if !d.known {
			continue
		}
		st := d.at.step
		name := st.ID
		if table && !d.at.setup {
			name = fmt.Sprintf("%s (row %s)", st.ID, d.at.ref.Scenario)
		}
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
					if !asserts(later.at.step, v) {
						continue
					}
					fired["FIX-05"] = true
					if observedBefore(ds, i, later, v) {
						continue
					}
					msgs = append(msgs, fmt.Sprintf("%s step %s sends %q and step %s expects it, but it doesn't contain ${run.id} or a saved value, so data from an earlier run could match (use ${run.id} in the value; or, before step %s, add a step that does the same %s with the same inputs as step %s and expects each path where step %s expects it to hold something else: equal with another literal, absent, or none; or waive FIX-05 with the reason it can't vary)", where, name, v, later.at.step.ID, st.ID, later.at.step.Do, later.at.step.ID, later.at.step.ID))
					break
				}
			}
		}
		unguarded := false
		var notGuarding []string
		if d.acts() && d.info.Destructive && d.fixedTarget() {
			unguarded = true
			for j, earlier := range ds[:i] {
				guards, why := guardFor(d, earlier, accepted[j], c)
				if guards {
					unguarded = false
					break
				}
				if why != "" {
					notGuarding = append(notGuarding, why)
				}
			}
			if unguarded {
				fired["EXE-09"] = true
			}
			accepted[i] = !unguarded
		}
		for _, rule := range sortedKeys(st.Waive) {
			w := writtenWaiver{phase: d.at.ref.Phase, index: d.at.ref.Index, rule: rule}
			if table && !d.at.setup && !st.Waive[rule].Listed && !fired[rule] && waivable[rule] && strings.TrimSpace(st.Waive[rule].Reason) != "" {
				if _, seen := notFiring[w]; !seen {
					plain = append(plain, w)
				}
				notFiring[w] = append(notFiring[w], d.at.ref.Scenario)
				continue
			}
			r.checkWaiverUse(p, where, name, d, rule, fired[rule], table, rows, checkedRows)
		}
		if !waived(d.at, "FIX-05") {
			for _, m := range msgs {
				r.add(Invalid, "FIX-05", p, "%s", m)
			}
		}
		if unguarded && !waived(d.at, "EXE-09") {
			notCounted := ""
			if len(notGuarding) > 0 {
				notCounted = "; not counted: " + strings.Join(notGuarding, "; ")
			}
			r.add(Invalid, "EXE-09", p, "%s step %s acts destructively on a fixed target (%s %s), which the scenario didn't create and may hold data that isn't the test's: the target refers to neither ${run.id} nor a value saved earlier in the scenario, and an identity value, a secret or a literal is the same in every scenario and run. Check it first with an earlier step on the same driver, or one sharing its policy (policy_from), that reads it, with at least one expectation and an outcome its driver counts as reading state (for example that it doesn't exist): the same target as written, a path above or below it, or a read whose policy entry shows it (shows); or act on it only after an earlier destructive step on the same target that EXE-09 accepted without a waiver; or use a target with a saved value or ${run.id}; or waive EXE-09 with the reason it's safe%s",
				where, name, st.Do, strings.Join(writtenTargets(d), ", "), notCounted)
		}
	}
	for _, w := range plain {
		r.add(Invalid, w.rule, p, "%s step %s: waives %s for every row, but the rule doesn't apply to the step in %d of the table's %d rows (%s); name the rows it covers instead, as { rows: [<case ids>], reason: <text> }, or remove the waiver", where, tableStep(ds, w), w.rule, len(notFiring[w]), len(rows), someRows(notFiring[w]))
	}
}

const rowsShown = 10

func someRows(rows []string) string {
	if len(rows) <= rowsShown {
		return strings.Join(rows, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(rows[:rowsShown], ", "), len(rows)-rowsShown)
}

func tableStep(ds []described, w writtenWaiver) string {
	for _, d := range ds {
		if d.at.ref.Phase == w.phase && d.at.ref.Index == w.index {
			return d.at.step.ID
		}
	}
	return ""
}

func (r *Report) checkWaiverUse(p, where, name string, d described, rule string, fired, table bool, rows map[string]bool, checkedRows map[writtenWaiver]bool) {
	st := d.at.step
	w := st.Waive[rule]
	if !waivable[rule] || strings.TrimSpace(w.Reason) == "" || w.Listed && (!table || d.at.setup) {
		return
	}
	row := d.at.ref.Scenario
	key := writtenWaiver{phase: d.at.ref.Phase, index: d.at.ref.Index, rule: rule}
	if w.Listed && !checkedRows[key] {
		checkedRows[key] = true
		for _, named := range w.Rows {
			if !rows[named] {
				r.add(Invalid, rule, p, "%s step %s: the %s waiver names row %q, which the table doesn't have", where, st.ID, rule, named)
			}
		}
	}
	switch {
	case !w.Listed && !fired:
		r.add(Invalid, rule, p, "%s step %s: waives %s, but the rule doesn't apply to this step; remove the waiver", where, name, rule)
	case w.Listed && w.Covers(row) && !fired:
		r.add(Invalid, rule, p, "%s step %s: the %s waiver names row %s, but the rule doesn't apply to the step in that row; remove it from rows", where, name, rule, row)
	case w.Listed && !w.Covers(row) && fired:
		r.add(Invalid, rule, p, "%s step %s: %s applies to the step in this row, but the waiver's rows don't name it, so it isn't waived here; add the row to rows if the reason holds for it, or change the row", where, name, rule)
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
		for _, x := range st.Expect.Matches {
			if s, isText := x.(string); isText {
				if re, err := model.MatchPattern(s); err == nil && re.MatchString(v) {
					return true
				}
			}
		}
	}
	return false
}

type site struct {
	kind    string
	path    string
	text    string
	claim   map[string]any
	pattern *regexp.Regexp
}

const (
	siteValue   = "value"
	siteMember  = "member"
	siteText    = "text"
	siteClaim   = "claim"
	sitePattern = "pattern"
)

func sitesOf(ex *model.Expect, v string) ([]site, bool) {
	if ex == nil {
		return nil, true
	}
	var out []site
	ok := true
	for _, p := range sortedKeys(ex.Equal) {
		ok = valueSites(p, ex.Equal[p], v, &out) && ok
	}
	for _, p := range sortedKeys(ex.Unordered) {
		for _, e := range ex.Unordered[p] {
			if contains(stringLeaves(e), v) {
				out = append(out, site{kind: siteMember, path: p})
				break
			}
		}
	}
	for _, m := range []map[string]model.Claims[map[string]any]{ex.Any, ex.None} {
		for _, p := range sortedKeys(m) {
			for _, c := range m[p].All() {
				if contains(stringLeaves(c), v) {
					out = append(out, site{kind: siteClaim, path: p, claim: c})
				}
			}
		}
	}
	for _, p := range sortedKeys(ex.Counts) {
		for _, cm := range ex.Counts[p].All() {
			if contains(stringLeaves(cm.Where), v) {
				out = append(out, site{kind: siteClaim, path: p, claim: cm.Where})
			}
		}
	}
	for _, p := range sortedKeys(ex.Contains) {
		if t, isText := ex.Contains[p].(string); isText && t != "" && strings.Contains(v, t) {
			out = append(out, site{kind: siteText, path: p, text: t})
		}
	}
	for _, p := range sortedKeys(ex.Matches) {
		if t, isText := ex.Matches[p].(string); isText {
			if re, err := model.MatchPattern(t); err == nil && re.MatchString(v) {
				out = append(out, site{kind: sitePattern, path: p, pattern: re})
			}
		}
	}
	return out, ok
}

func valueSites(p string, x any, v string, out *[]site) bool {
	switch t := x.(type) {
	case string:
		if t == v {
			*out = append(*out, site{kind: siteValue, path: p})
		}
	case map[string]any:
		ok := true
		for _, k := range sortedKeys(t) {
			sub := expr.Child(p, k)
			if parsed, err := expr.ParsePath(sub); err != nil || parsed.String() != sub {
				if contains(stringLeaves(t[k]), v) {
					ok = false
				}
				continue
			}
			ok = valueSites(sub, t[k], v, out) && ok
		}
		return ok
	case []any:
		ok := true
		for i, e := range t {
			ok = valueSites(fmt.Sprintf("%s[%d]", p, i), e, v, out) && ok
		}
		return ok
	}
	return true
}

func observedBefore(ds []described, act int, later described, v string) bool {
	if strings.Contains(v, "${") || later.at.ref.Scenario != ds[act].at.ref.Scenario {
		return false
	}
	sites, ok := sitesOf(later.at.step.Expect, v)
	if !ok || len(sites) == 0 {
		return false
	}
	for _, s := range sites {
		proven := false
		for _, earlier := range ds[:act] {
			if earlier.at.ref.Scenario != ds[act].at.ref.Scenario || !sameRequest(earlier.at.step, later.at.step) || !earlier.observes() || earlier.at.step.Never != nil {
				continue
			}
			if s.ruledOutBy(earlier.at.step.Expect, v) {
				proven = true
				break
			}
		}
		if !proven {
			return false
		}
	}
	return true
}

func sameRequest(a, b *model.Step) bool {
	return a.Do == b.Do && a.On == b.On && reflect.DeepEqual(a.With, b.With)
}

func (s site) ruledOutBy(ex *model.Expect, v string) bool {
	for _, a := range ex.Absent {
		if pathWithin(s.path, a) {
			return true
		}
	}
	for _, a := range sortedKeys(ex.Equal) {
		if !pathWithin(s.path, a) {
			continue
		}
		rel, err := expr.ParsePath("$" + strings.TrimPrefix(s.path, a))
		if err != nil {
			continue
		}
		got, found := rel.Select(ex.Equal[a])
		if found && hasRef(got) {
			continue
		}
		if !found || s.differs(got, v) {
			return true
		}
	}
	if s.kind != siteClaim {
		return false
	}
	for _, n := range sortedKeys(ex.None) {
		if n != s.path {
			continue
		}
		for _, c := range ex.None[n].All() {
			if claimWithin(c, s.claim) {
				return true
			}
		}
	}
	for _, n := range sortedKeys(ex.Counts) {
		if n != s.path {
			continue
		}
		for _, cm := range ex.Counts[n].All() {
			if cm.Where != nil && zero(cm.Is) && claimWithin(cm.Where, s.claim) {
				return true
			}
		}
	}
	return false
}

func (s site) differs(got any, v string) bool {
	switch s.kind {
	case siteValue:
		t, isText := got.(string)
		return !isText || t != v
	case siteText:
		t, isText := got.(string)
		return !isText || !strings.Contains(t, s.text)
	case sitePattern:
		t, isText := got.(string)
		return !isText || !s.pattern.MatchString(t)
	case siteMember:
		list, isList := got.([]any)
		if !isList {
			return true
		}
		for _, e := range list {
			if contains(stringLeaves(e), v) {
				return false
			}
		}
		return true
	case siteClaim:
		list, isList := got.([]any)
		if !isList {
			return true
		}
		fixed := map[string]any{}
		for k, x := range s.claim {
			if !hasRef(x) {
				fixed[k] = x
			}
		}
		for _, e := range list {
			if matchesClaim(fixed, e) {
				return false
			}
		}
		return true
	}
	return false
}

func matchesClaim(claim map[string]any, el any) bool {
	for k, want := range claim {
		p, err := expr.ParseRelative(k)
		if err != nil {
			return true
		}
		got, ok := p.Select(el)
		if !ok || !expr.Equal(want, got) {
			return false
		}
	}
	return true
}

func pathWithin(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+".") || strings.HasPrefix(p, root+"[")
}

func hasRef(v any) bool {
	switch t := v.(type) {
	case string:
		return strings.Contains(t, "${")
	case map[string]any:
		for k, x := range t {
			if strings.Contains(k, "${") || hasRef(x) {
				return true
			}
		}
	case []any:
		for _, x := range t {
			if hasRef(x) {
				return true
			}
		}
	}
	return false
}

func zero(v any) bool {
	n, isInt := v.(int)
	return isInt && n == 0
}
