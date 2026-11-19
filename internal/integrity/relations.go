package integrity

import (
	"fmt"
	"strings"
	"unicode"

	"proctor/internal/model"
)

const ruleRelations = "INT-20"

type relationKey struct {
	rel, show int
}

type groupTarget struct {
	group, target string
}

type reportedTargets struct {
	acts, reads map[string]map[string]bool
}

func (t *reportedTargets) note(group string, d described) {
	if !d.known {
		return
	}
	m := &t.reads
	if d.acts() {
		m = &t.acts
	}
	if *m == nil {
		*m = map[string]map[string]bool{}
	}
	if (*m)[group] == nil {
		(*m)[group] = map[string]bool{}
	}
	for _, x := range d.info.Targets {
		(*m)[group][x] = true
	}
}

func (r *Report) validateRelations(c *model.Config) {
	p := r.ConfigPath
	acts := map[groupTarget]int{}
	for i, rel := range c.Relations {
		where := fmt.Sprintf("relations[%d]", i)
		actOK := r.checkRelationEnd(c, where+".act", rel.Act)
		if len(rel.Shows) == 0 {
			r.add(Invalid, ruleRelations, p, "%s: shows is empty; list each { driver, target } on another driver whose reads show what the act changes", where)
		}
		if actOK {
			key := groupTarget{c.PolicyGroup(rel.Act.Driver), rel.Act.Target}
			if first, seen := acts[key]; seen {
				r.add(Invalid, ruleRelations, p, "%s: act %s is already the act of relations[%d]; list all its shows in one relation", where, rel.Act, first)
			} else {
				acts[key] = i
			}
		}
		shown := map[groupTarget]bool{}
		for j, sh := range rel.Shows {
			at := fmt.Sprintf("%s.shows[%d]", where, j)
			if !r.checkRelationEnd(c, at, sh) {
				continue
			}
			key := groupTarget{c.PolicyGroup(sh.Driver), sh.Target}
			switch {
			case actOK && key.group == c.PolicyGroup(rel.Act.Driver):
				r.add(Invalid, ruleRelations, p, "%s: %s is on driver %s, the act's own driver or one sharing its policy (policy_from); a relation is only for reads on another driver, since a read on the same driver counts when it reports the act's target, a target above or below it, or one its policy says it shows", at, sh, sh.Driver)
			case shown[key]:
				r.add(Invalid, ruleRelations, p, "%s: %s is listed twice in this relation", at, sh)
			}
			shown[key] = true
		}
	}
}

func (r *Report) checkRelationEnd(c *model.Config, at string, e model.RelationEnd) bool {
	p := r.ConfigPath
	ok := true
	switch _, declared := c.Drivers[e.Driver]; {
	case e.Driver == "":
		r.add(Invalid, ruleRelations, p, "%s: driver is required: the name of a driver in drivers", at)
		ok = false
	case !declared:
		r.add(Invalid, ruleRelations, p, "%s: driver %q isn't a driver in proctor.yaml (drivers: %s)", at, e.Driver, strings.Join(sortedKeys(c.Drivers), ", "))
		ok = false
	}
	if why := targetProblem(e.Target); why != "" {
		r.add(Invalid, ruleRelations, p, "%s: %s", at, why)
		ok = false
	}
	return ok
}

func targetProblem(t string) string {
	switch {
	case t == "":
		return "target is required: the target as the driver reports it (for http and ws the path with each ${...} written as {}, for dynamodb the table alias, for sqs the queue alias, for ses sent, for postgres the connection, for docker <kind>/<name> or the list operation's name)"
	case strings.Contains(t, "${"):
		return fmt.Sprintf("target %q has a ${...} reference; write {} where a step's target has one, since targets are compared with each reference replaced by {}", t)
	case strings.TrimSpace(t) != t || strings.IndexFunc(t, unicode.IsControl) >= 0:
		return fmt.Sprintf("target %q has a leading or trailing space or a control character", t)
	}
	return ""
}

func relationsFor(act, later described, c *model.Config) []relationKey {
	actDriver, _, _ := model.SplitDo(act.at.step.Do)
	laterDriver, _, _ := model.SplitDo(later.at.step.Do)
	actGroup, laterGroup := c.PolicyGroup(actDriver), c.PolicyGroup(laterDriver)
	var out []relationKey
	for i, rel := range c.Relations {
		if c.PolicyGroup(rel.Act.Driver) != actGroup || !contains(act.info.Targets, rel.Act.Target) {
			continue
		}
		for j, sh := range rel.Shows {
			if c.PolicyGroup(sh.Driver) == laterGroup && contains(later.info.Targets, sh.Target) {
				out = append(out, relationKey{i, j})
			}
		}
	}
	return out
}

func suggestedRelation(act, later described, c *model.Config) string {
	actDriver, _, _ := model.SplitDo(act.at.step.Do)
	laterDriver, _, _ := model.SplitDo(later.at.step.Do)
	rel := model.Relation{Act: model.RelationEnd{Driver: c.PolicyGroup(actDriver), Target: act.info.Targets[0]}}
	return rel.Entry(model.RelationEnd{Driver: c.PolicyGroup(laterDriver), Target: later.info.Targets[0]})
}

func (r *Report) checkRelationsUsed(c *model.Config, seen reportedTargets, used map[relationKey]bool) {
	p := r.ConfigPath
	for i, rel := range c.Relations {
		actGroup := c.PolicyGroup(rel.Act.Driver)
		if !usableEnd(c, rel.Act) {
			continue
		}
		if !seen.acts[actGroup][rel.Act.Target] {
			r.add(Invalid, ruleRelations, p, "relations[%d].act %s: no step that acts on driver %s, or one sharing its policy, reports this target (its acts report: %s); name the act's target as its driver reports it", i, rel.Act, actGroup, listOrNone(seen.acts[actGroup]))
			continue
		}
		for j, sh := range rel.Shows {
			group := c.PolicyGroup(sh.Driver)
			if !usableEnd(c, sh) || group == actGroup {
				continue
			}
			switch {
			case !seen.reads[group][sh.Target]:
				r.add(Invalid, ruleRelations, p, "relations[%d].shows[%d] %s: no step that observes on driver %s, or one sharing its policy, reports this target (its reads report: %s); name the read's target as its driver reports it", i, j, sh, group, listOrNone(seen.reads[group]))
			case !used[relationKey{i, j}]:
				r.add(Invalid, ruleRelations, p, "relations[%d].shows[%d]: no step of the listed specifications uses %s, which needs a step on driver %s that acts on %s and, later in its scenario, a step on driver %s that reads %s with at least one expectation and an outcome its driver counts as reading state; a relation nothing uses declares nothing today and would silently count a read added later, so remove it, or add the steps it was written for", i, j, rel.Entry(sh), actGroup, rel.Act.Target, group, sh.Target)
			}
		}
	}
}

func usableEnd(c *model.Config, e model.RelationEnd) bool {
	_, declared := c.Drivers[e.Driver]
	return declared && targetProblem(e.Target) == ""
}

func listOrNone(m map[string]bool) string {
	if len(m) == 0 {
		return "none"
	}
	return someRows(sortedKeys(m))
}
