package integrity

import (
	"fmt"
	"strings"

	"proctor/internal/expr"
	"proctor/internal/model"
)

const invariantRule = "INV-01"

func (r *Report) validateInvariants(c *model.Config) {
	p := r.ConfigPath
	if c.Invariants != nil && len(c.Invariants) == 0 {
		r.add(Invalid, invariantRule, p, "invariants lists nothing; remove it")
	}
	seen := map[string]bool{}
	for i := range c.Invariants {
		st := &c.Invariants[i]
		where := fmt.Sprintf("invariants[%d] (%s)", i, st.ID)
		switch {
		case !idPattern.MatchString(st.ID):
			r.add(Invalid, invariantRule, p, "invariants[%d]: invalid id %q", i, st.ID)
		case seen[st.ID]:
			r.add(Invalid, invariantRule, p, "%s: the id is used twice", where)
		}
		seen[st.ID] = true
		switch driver, _, ok := model.SplitDo(st.Do); {
		case strings.TrimSpace(st.Do) == "":
			r.add(Invalid, invariantRule, p, "%s: do is required (<driver>.<operation>)", where)
		case !ok:
			r.add(Invalid, invariantRule, p, "%s: do %q must be <driver>.<operation>", where, st.Do)
		default:
			if _, configured := c.Drivers[driver]; !configured {
				r.add(Invalid, "INT-12", p, "%s: do names driver %q, which proctor.yaml doesn't configure", where, driver)
			}
		}
		if _, declared := c.Identities[st.Identity]; st.Identity != "" && !declared {
			r.add(Invalid, "INT-12", p, "%s: identity %q is not declared in proctor.yaml", where, st.Identity)
		}
		if st.Expect.Count() == 0 {
			r.add(Invalid, invariantRule, p, "%s: an invariant must state at least one expectation, the state that must hold after every scenario", where)
		}
		for _, bad := range []struct {
			set  bool
			name string
		}{
			{len(st.Save) > 0, "save"}, {st.As != "", "as"}, {st.On != "", "on"}, {st.Variant != "", "variant"},
			{st.Consistently != nil, "consistently"}, {st.Never != nil, "never"}, {st.NoStateCheck != "", "no_state_check"},
			{len(st.Waive) > 0, "waive"}, {st.HasUnlessPassed(), "unless_passed"},
		} {
			if bad.set {
				r.add(Invalid, invariantRule, p, "%s: %s can't be used in an invariant, a read that only observes the environment between scenarios", where, bad.name)
			}
		}
		r.checkExpect(p, where, st.Expect)
		r.checkTime(p, where, st, false)
		r.checkTimeout(p, where, st, c)
		for _, part := range []struct {
			v      any
			inWith bool
		}{{st.With, true}, {st.Expect, false}} {
			names, err := references(part.v)
			if err != nil {
				r.add(Invalid, "EXE-02", p, "%s: %v", where, err)
				continue
			}
			for _, n := range names {
				switch {
				case n == expr.RunID:
					r.add(Invalid, invariantRule, p, "%s: ${run.id} can't be used in an invariant, which belongs to no scenario, so it would match nothing and always hold", where)
				case expr.VarName(n):
					r.add(Invalid, invariantRule, p, "%s: ${%s} can't be used in an invariant: no step saves values for it", where, n)
				case strings.HasPrefix(n, "row."):
					r.add(Invalid, invariantRule, p, "%s: ${%s} can't be used in an invariant, which belongs to no table", where, n)
				default:
					r.checkRef(p, where, n, nil, c, &model.Scenario{}, part.inWith)
				}
			}
		}
	}
}

func (r *Report) applyInvariantInfo(c *model.Config, info map[model.StepRef]model.StepInfo, used map[string]map[string]bool) {
	for i := range c.Invariants {
		st := &c.Invariants[i]
		where := fmt.Sprintf("invariants[%d] (%s)", i, st.ID)
		driver, _, _ := model.SplitDo(st.Do)
		in, found := info[model.InvariantRef(i)]
		if !found {
			if _, configured := c.Drivers[driver]; configured {
				r.add(Invalid, "EXE-01", r.ConfigPath, "%s: driver %q gave no description of this step", where, driver)
			}
			continue
		}
		for _, pr := range in.Problems {
			rule := strings.TrimSpace(pr.Rule)
			if rule == "" {
				rule = "EXE-01"
			}
			msg := pr.Message
			if pr.Path != "" {
				msg = pr.Path + ": " + msg
			}
			r.add(Invalid, rule, r.ConfigPath, "%s: %s", where, msg)
		}
		if in.Kind != kindObserve {
			r.add(Invalid, invariantRule, r.ConfigPath, "%s: driver %q describes this step as %q; an invariant must only observe, so it can never change or hide what a scenario left behind", where, driver, in.Kind)
		}
		for _, k := range in.Uses {
			use(used, c.PolicyGroup(driver), k)
		}
	}
}
