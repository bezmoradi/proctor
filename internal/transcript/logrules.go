package transcript

import (
	"fmt"
	"regexp"
	"slices"

	"proctor/internal/model"
)

type LogRule struct {
	Name     string
	Pattern  *regexp.Regexp
	Field    string
	Numbered bool
}

func CompileLogRules(rules []model.LogScrubRule, fields []string, message string) ([]LogRule, []string) {
	var out []LogRule
	var probs []string
	if len(rules) > model.MaxScrubRules {
		probs = append(probs, fmt.Sprintf("app_logs.approved.scrub has %d rules, more than %d", len(rules), model.MaxScrubRules))
	}
	seen := map[string]bool{}
	for i, r := range rules {
		where := fmt.Sprintf("app_logs.approved.scrub[%d]", i)
		ok := true
		switch {
		case !model.ScrubName.MatchString(r.Name):
			probs = append(probs, fmt.Sprintf("%s: name %q must be lower-case letters and _", where, r.Name))
			ok = false
		case seen[r.Name]:
			probs = append(probs, fmt.Sprintf("%s: name %q is used by an earlier rule; each rule needs its own name, since its placeholders are <name-N>", where, r.Name))
			ok = false
		}
		seen[r.Name] = true
		rule := LogRule{Name: r.Name, Field: r.Field, Numbered: r.IsNumbered()}
		if r.Pattern == "" && r.Field == "" {
			probs = append(probs, fmt.Sprintf("%s: give a pattern, a field, or both", where))
			ok = false
		}
		if r.Pattern != "" {
			re, err := compilePattern(r.Pattern)
			if err != nil {
				probs = append(probs, fmt.Sprintf("%s: %v", where, err))
				ok = false
			}
			rule.Pattern = re
		}
		if r.Field != "" && r.Field != message && !slices.Contains(fields, r.Field) {
			probs = append(probs, fmt.Sprintf("%s: field %q is neither in app_logs.approved.fields nor the message field %q, so the rule would scrub nothing", where, r.Field, message))
			ok = false
		}
		if ok {
			out = append(out, rule)
		}
	}
	return out, probs
}

type Counter struct {
	c counter
}

func NewCounter(name string, numbered bool) *Counter {
	return &Counter{c: counter{name: name, numbered: numbered, seen: map[string]int{}}}
}

func (c *Counter) Whole(value string) string {
	return c.c.mark(value)
}

func (c *Counter) Matches(s string, re *regexp.Regexp) string {
	return replacePattern(s, re, &c.c)
}

func ReplaceSubs(s string, subs []Sub) string {
	for _, sub := range subs {
		if !sub.Number {
			s = replaceText(s, sub)
		}
	}
	return s
}
