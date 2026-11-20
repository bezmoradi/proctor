package model

import (
	"fmt"
	"sort"

	"proctor/internal/expr"
)

const pathSpellingRule = "EXE-13"

func (s *Spec) CanonicalPaths() []Problem {
	var probs []Problem
	for i := range s.Scenarios {
		sc := &s.Scenarios[i]
		quiet := sc.Case != nil && !sc.Case.FirstRow
		for j := range sc.Setup {
			where := fmt.Sprintf("scenario %s setup[%d] (%s)", sc.ID, j, sc.Setup[j].ID)
			if sc.Case != nil {
				where = fmt.Sprintf("table %s setup[%d] (%s)", sc.Case.Group, j, sc.Setup[j].ID)
			}
			probs = append(probs, canonicalStep(&sc.Setup[j], where)...)
		}
		for j := range sc.Steps {
			where := fmt.Sprintf("scenario %s steps[%d] (%s)", sc.ID, j, sc.Steps[j].ID)
			if sc.Case != nil {
				where = fmt.Sprintf("table %s step", sc.Case.Label())
			}
			if p := canonicalStep(&sc.Steps[j], where); !quiet {
				probs = append(probs, p...)
			}
		}
		for j := range sc.Cleanup {
			where := fmt.Sprintf("scenario %s cleanup[%d] (%s)", sc.ID, j, sc.Cleanup[j].ID)
			if sc.Case != nil {
				where = fmt.Sprintf("table %s cleanup[%d] (%s)", sc.Case.Label(), j, sc.Cleanup[j].ID)
			}
			if p := canonicalStep(&sc.Cleanup[j], where); !quiet {
				probs = append(probs, p...)
			}
		}
	}
	return probs
}

func canonicalStep(st *Step, where string) []Problem {
	var probs []Problem
	add := func(format string, args ...any) {
		probs = append(probs, Problem{Rule: pathSpellingRule, Message: where + ": " + fmt.Sprintf(format, args...)})
	}
	for name, p := range st.Save {
		if c, ok := expr.Canonical(p); ok {
			st.Save[name] = c
		}
	}
	ex := st.Expect
	if ex == nil {
		return nil
	}
	ex.Equal = canonicalKeys(ex.Equal, "equal", expr.Canonical, add)
	ex.Keys = canonicalKeys(ex.Keys, "keys", expr.Canonical, add)
	ex.Length = canonicalKeys(ex.Length, "length", expr.Canonical, add)
	ex.Unordered = canonicalKeys(ex.Unordered, "unordered", expr.Canonical, add)
	ex.Contains = canonicalKeys(ex.Contains, "contains", expr.Canonical, add)
	ex.Matches = canonicalKeys(ex.Matches, "matches", expr.Canonical, add)
	ex.Excludes = canonicalKeys(ex.Excludes, "excludes", expr.Canonical, add)
	ex.Differs = canonicalKeys(ex.Differs, "differs", expr.Canonical, add)
	for _, list := range []*[]string{&ex.Present, &ex.Absent} {
		for i, p := range *list {
			if c, ok := expr.Canonical(p); ok {
				(*list)[i] = c
			}
		}
	}
	for _, m := range []struct {
		name   string
		claims *map[string]Claims[map[string]any]
	}{{"any", &ex.Any}, {"none", &ex.None}} {
		*m.claims = canonicalKeys(*m.claims, m.name, expr.Canonical, add)
		for _, k := range sortedNames(*m.claims) {
			list := (*m.claims)[k].List
			for i := range list {
				list[i] = canonicalKeys(list[i], fmt.Sprintf("%s of %s%s", m.name, k, claimText((*m.claims)[k].Multi, i)), expr.CanonicalRelative, add)
			}
		}
	}
	ex.Counts = canonicalKeys(ex.Counts, "count", expr.Canonical, add)
	for _, k := range sortedNames(ex.Counts) {
		list := ex.Counts[k].List
		for i := range list {
			list[i].Where = canonicalKeys(list[i].Where, fmt.Sprintf("count of %s%s where", k, claimText(ex.Counts[k].Multi, i)), expr.CanonicalRelative, add)
		}
	}
	return probs
}

func canonicalKeys[V any](m map[string]V, what string, canon func(string) (string, bool), add func(string, ...any)) map[string]V {
	if m == nil {
		return nil
	}
	out := make(map[string]V, len(m))
	from := map[string]string{}
	for _, k := range sortedNames(m) {
		c, ok := canon(k)
		if !ok {
			c = k
		}
		if first, dup := from[c]; dup {
			add("%s names the path %s twice, as %s and as %s; write it once", what, c, first, k)
			continue
		}
		from[c] = k
		out[c] = m[k]
	}
	return out
}

func claimText(multi bool, i int) string {
	if !multi {
		return ""
	}
	return fmt.Sprintf(" claim[%d]", i)
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
