package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"proctor/internal/expr"
	"proctor/internal/model"
)

const minLoose = 4

type Rule struct {
	Name     string
	Pattern  *regexp.Regexp
	Path     *expr.Path
	Numbered bool
}

func Compile(rules []model.ScrubRule) ([]Rule, []string) {
	var out []Rule
	var probs []string
	if len(rules) > model.MaxScrubRules {
		probs = append(probs, fmt.Sprintf("transcripts.scrub has %d rules, more than %d", len(rules), model.MaxScrubRules))
	}
	seen := map[string]bool{}
	for i, r := range rules {
		where := fmt.Sprintf("transcripts.scrub[%d]", i)
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
		rule := Rule{Name: r.Name, Numbered: r.IsNumbered()}
		if r.Pattern == "" && r.Path == "" {
			probs = append(probs, fmt.Sprintf("%s: give a pattern, a path, or both", where))
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
		if r.Path != "" {
			p, err := expr.ParsePath(r.Path)
			switch {
			case err != nil:
				probs = append(probs, fmt.Sprintf("%s: path: %v", where, err))
				ok = false
			case len(p.Segments()) == 0:
				probs = append(probs, fmt.Sprintf("%s: path %q names the whole fact, which would hide every value of every step; name the field to scrub, such as $.headers.date", where, r.Path))
				ok = false
			default:
				rule.Path = &p
			}
		}
		if ok {
			out = append(out, rule)
		}
	}
	return out, probs
}

func compilePattern(p string) (*regexp.Regexp, error) {
	if len(p) > model.MaxPatternBytes {
		return nil, fmt.Errorf("the pattern is %d bytes, longer than %d", len(p), model.MaxPatternBytes)
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, fmt.Errorf("pattern %q isn't a valid RE2 regular expression: %v", p, err)
	}
	if re.MatchString("") {
		return nil, fmt.Errorf("pattern %q matches empty text, so it would match everywhere; require at least one character", p)
	}
	return re, nil
}

type Sub struct {
	Text        string
	Number      bool
	Placeholder string
}

func OrderSubs(subs []Sub) []Sub {
	out := make([]Sub, 0, len(subs))
	seen := map[string]bool{}
	for _, s := range subs {
		k := strconv.FormatBool(s.Number) + "\x00" + s.Text
		if s.Text == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].Text) != len(out[j].Text) {
			return len(out[i].Text) > len(out[j].Text)
		}
		return out[i].Placeholder < out[j].Placeholder
	})
	return out
}

func boundary(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

func replaceText(s string, sub Sub) string {
	if len(sub.Text) >= minLoose {
		return strings.ReplaceAll(s, sub.Text, sub.Placeholder)
	}
	var b strings.Builder
	rest := s
	for {
		at := strings.Index(rest, sub.Text)
		if at < 0 {
			b.WriteString(rest)
			return b.String()
		}
		before, after := rune(' '), rune(' ')
		if at > 0 {
			before, _ = utf8.DecodeLastRuneInString(rest[:at])
		}
		if end := at + len(sub.Text); end < len(rest) {
			after, _ = utf8.DecodeRuneInString(rest[end:])
		}
		b.WriteString(rest[:at])
		if boundary(before) && boundary(after) {
			b.WriteString(sub.Placeholder)
		} else {
			b.WriteString(sub.Text)
		}
		rest = rest[at+len(sub.Text):]
	}
}

func walk(v any, text func(string) string, num func(json.Number) any) (any, error) {
	switch x := v.(type) {
	case string:
		return text(x), nil
	case json.Number:
		if num != nil {
			return num(x), nil
		}
		return x, nil
	case []any:
		out := make([]any, len(x))
		for i := range x {
			e, err := walk(x[i], text, num)
			if err != nil {
				return nil, err
			}
			out[i] = e
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for _, k := range sortedKeys(x) {
			e, err := walk(x[k], text, num)
			if err != nil {
				return nil, err
			}
			nk := text(k)
			if _, dup := out[nk]; dup {
				return nil, fmt.Errorf("scrubbing made two keys the same, %q; the transcript can't keep both", nk)
			}
			out[nk] = e
		}
		return out, nil
	}
	return v, nil
}

func Substitute(tree map[string]any, subs []Sub) (map[string]any, error) {
	return eachPayload(tree, func(_ string, v any) (any, error) {
		return walk(v, func(s string) string {
			for _, sub := range subs {
				if !sub.Number {
					s = replaceText(s, sub)
				}
			}
			return s
		}, func(n json.Number) any {
			for _, sub := range subs {
				if sub.Number && n.String() == sub.Text {
					return sub.Placeholder
				}
			}
			return n
		})
	})
}

var payloadKeys = []string{"fact", "with"}

func eachPayload(tree map[string]any, f func(key string, v any) (any, error)) (map[string]any, error) {
	steps, ok := tree["steps"].([]any)
	if !ok {
		return tree, nil
	}
	out := make(map[string]any, len(tree))
	for k, v := range tree {
		out[k] = v
	}
	newSteps := make([]any, len(steps))
	for i, st := range steps {
		sm, ok := st.(map[string]any)
		if !ok {
			newSteps[i] = st
			continue
		}
		copied := make(map[string]any, len(sm))
		for k, v := range sm {
			copied[k] = v
		}
		for _, k := range payloadKeys {
			v, has := copied[k]
			if !has {
				continue
			}
			nv, err := f(k, v)
			if err != nil {
				return nil, err
			}
			copied[k] = nv
		}
		newSteps[i] = copied
	}
	out["steps"] = newSteps
	return out, nil
}

type counter struct {
	name     string
	numbered bool
	seen     map[string]int
}

func (c *counter) mark(value string) string {
	if !c.numbered {
		return "<" + c.name + ">"
	}
	n, ok := c.seen[value]
	if !ok {
		n = len(c.seen) + 1
		c.seen[value] = n
	}
	return fmt.Sprintf("<%s-%d>", c.name, n)
}

func canonical(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return b.String()
}

func ApplyRules(tree map[string]any, rules []Rule) (map[string]any, error) {
	cur := tree
	for _, rule := range rules {
		c := &counter{name: rule.Name, numbered: rule.Numbered, seen: map[string]int{}}
		pattern := func(v any) (any, error) {
			return walk(v, func(s string) string { return replacePattern(s, rule.Pattern, c) }, nil)
		}
		var err error
		cur, err = eachPayload(cur, func(key string, v any) (any, error) {
			switch {
			case rule.Path == nil:
				return pattern(v)
			case key != "fact":
				return v, nil
			}
			return atPath(v, rule.Path.Segments(), func(at any) (any, error) {
				if rule.Pattern == nil {
					return c.mark(canonical(at)), nil
				}
				return pattern(at)
			})
		})
		if err != nil {
			return nil, err
		}
	}
	return cur, nil
}

func replacePattern(s string, re *regexp.Regexp, c *counter) string {
	return re.ReplaceAllStringFunc(s, func(m string) string {
		if m == "" {
			return m
		}
		return c.mark(m)
	})
}

func atPath(v any, segs []expr.Segment, f func(any) (any, error)) (any, error) {
	if len(segs) == 0 {
		return f(v)
	}
	s := segs[0]
	if s.IsIndex {
		arr, ok := v.([]any)
		if !ok || s.Index >= len(arr) {
			return v, nil
		}
		out := append([]any(nil), arr...)
		e, err := atPath(arr[s.Index], segs[1:], f)
		if err != nil {
			return nil, err
		}
		out[s.Index] = e
		return out, nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return v, nil
	}
	e, has := obj[s.Name]
	if !has {
		return v, nil
	}
	out := make(map[string]any, len(obj))
	for k, x := range obj {
		out[k] = x
	}
	ne, err := atPath(e, segs[1:], f)
	if err != nil {
		return nil, err
	}
	out[s.Name] = ne
	return out, nil
}

func Normalize(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func Build(doc Doc, subs []Sub, rules []Rule) ([]byte, error) {
	norm, err := Normalize(doc.Tree())
	if err != nil {
		return nil, err
	}
	m, ok := norm.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the transcript isn't a mapping")
	}
	if m, err = Substitute(m, OrderSubs(subs)); err != nil {
		return nil, err
	}
	if m, err = ApplyRules(m, rules); err != nil {
		return nil, err
	}
	return Render(m)
}
