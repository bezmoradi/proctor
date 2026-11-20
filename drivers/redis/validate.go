package redis

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/bezmoradi/proctor/protocol"
	"github.com/bezmoradi/proctor/protocol/schema"
)

var (
	refRE    = regexp.MustCompile(`\$\{([^}]*)\}`)
	savedRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	valueRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*$`)
	fillRE   = regexp.MustCompile(`^fill\(`)
	secretRE = regexp.MustCompile(`^secret\.`)
)

const errScoped = "redis.keys lists every key that matches, other scenarios' and earlier runs' included; give a match that holds ${run.id} or a value saved by an earlier step, such as session:${run.id}:*"

type validation struct {
	r protocol.ValidateResult
}

func (v *validation) add(rule, path, format string, args ...any) {
	v.r.Problems = append(v.r.Problems, protocol.Problem{Rule: rule, Message: fmt.Sprintf(format, args...), Path: path})
}

func (d *Driver) Validate(ctx context.Context, p protocol.ValidateParams) (protocol.ValidateResult, error) {
	v := &validation{r: protocol.ValidateResult{Kind: protocol.KindObserve}}
	var arg string
	var argsSchema map[string]any
	switch p.Op {
	case OpKey:
		arg, argsSchema = "key", KeyArgsSchema
	case OpKeys:
		arg, argsSchema = "match", KeysArgsSchema
	default:
		v.add("EXE-01", "$.do", "unknown operation %q; the redis driver has %q and %q", p.Op, OpKey, OpKeys)
		return v.r, nil
	}
	if p.Setup {
		v.add("FIX-03", "$.do", "reading Redis is a verification step; setup goes through the application")
	}
	for _, msg := range schema.Check(argsSchema, p.Args, schema.AllowRefs) {
		v.add(ruleArgs, "$.with", "%s", msg)
	}
	if matchers(p.Expect) == 0 {
		v.add("EXE-03", "$.expect", "a Redis check needs at least one expectation")
	}
	m, _ := p.Args.(map[string]any)
	s, isText := m[arg].(string)
	if !isText {
		return v.r, nil
	}
	v.r.Refs = refs(p.Args)
	v.r.Targets = []string{"key/" + refRE.ReplaceAllString(s, "{}")}
	at := "$.with." + arg
	g, scoped, ok := v.template(at, s, p.Op == OpKeys)
	if p.Op == OpKeys && !scoped {
		v.add(ruleScope, at, "%s", errScoped)
	}
	if !ok {
		return v.r, nil
	}
	cover := d.covering(g)
	if len(cover) == 0 {
		if p.Op == OpKey {
			v.add(rulePolicy, at, "key %q isn't inside the policy's key patterns (%s), whatever text each ${...} stands for; read a key inside one, or add a pattern that covers it to the redis driver's policy in proctor.yaml", s, d.policyText())
		} else {
			v.add(rulePolicy, at, "match %q can name keys outside the policy's key patterns (%s), with each ${...} standing for any text; every key it can match must lie inside one pattern, so narrow the match or add a pattern that covers it to the redis driver's policy in proctor.yaml", s, d.policyText())
		}
		return v.r, nil
	}
	for _, c := range cover {
		v.r.Uses = append(v.r.Uses, entryKey(c))
	}
	return v.r, nil
}

func (v *validation) template(at, s string, isGlob bool) (glob, bool, bool) {
	switch {
	case s == "":
		v.add(ruleArgs, at, "must not be empty")
		return nil, false, false
	case strings.ContainsFunc(s, unicode.IsControl):
		v.add(ruleArgs, at, "can't hold a newline or another control character")
		return nil, false, false
	}
	locs := refRE.FindAllStringSubmatchIndex(s, -1)
	if strings.Count(s, "${") != len(locs) {
		v.add(ruleArgs, at, "unclosed ${ in %q", s)
		return nil, false, false
	}
	var g glob
	scoped, ok := false, true
	part := func(p string) {
		if !isGlob {
			g = append(g, literal(p)...)
			return
		}
		pg, err := parseGlob(p)
		if err != nil {
			v.add(ruleArgs, at, "match %q: every [ needs its ], and ${...} can't stand inside [...]", s)
			ok = false
			return
		}
		g = append(g, pg...)
	}
	last := 0
	for _, loc := range locs {
		before := s[last:loc[0]]
		if isGlob && strings.HasSuffix(before, `\`) && (len(before)-len(strings.TrimRight(before, `\`)))%2 == 1 {
			v.add(ruleArgs, at, "match %q: a \\ can't come right before ${...}, since it would escape the inserted value's first character", s)
			ok = false
		}
		part(before)
		name := s[loc[2]:loc[3]]
		switch {
		case name == "run.id", savedRE.MatchString(name):
			scoped = true
		case valueRE.MatchString(name):
		case fillRE.MatchString(name), secretRE.MatchString(name):
			v.add("HTTP-02", at, "${%s} may be used only in a request body or request headers, not in a Redis read", name)
			ok = false
		default:
			v.add(ruleArgs, at, "unsupported reference ${%s}: use ${run.id}, ${<identity>.<value>} or a value saved by an earlier step", name)
			ok = false
		}
		if len(g) == 0 || g[len(g)-1].kind != tokStar {
			g = append(g, token{kind: tokStar})
		}
		last = loc[1]
	}
	part(s[last:])
	return g, scoped, ok
}

func matchers(expect any) int {
	m, _ := expect.(map[string]any)
	n := 0
	for _, v := range m {
		switch t := v.(type) {
		case map[string]any:
			n += len(t)
		case []any:
			n += len(t)
		}
	}
	return n
}

func refs(v any) []string {
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			for _, m := range refRE.FindAllStringSubmatch(x, -1) {
				seen[m[1]] = true
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
