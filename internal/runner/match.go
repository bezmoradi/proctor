package runner

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"proctor/internal/expr"
	"proctor/internal/model"
)

type subCheck struct {
	key  string
	path expr.Path
	perr error
	want any
}

type check struct {
	kind    string
	target  string
	path    expr.Path
	perr    error
	want    any
	subs    []subCheck
	n       int
	claim   *int
	pattern *regexp.Regexp
}

func newCheck(kind, target string) check {
	c := check{kind: kind, target: target}
	c.path, c.perr = expr.ParsePath(target)
	return c
}

func (r *Runner) prepare(ex *model.Expect, vars expr.Vars) ([]check, error) {
	if ex == nil {
		return nil, nil
	}
	var out []check
	for _, key := range sortedKeys(ex.Equal) {
		want, err := vars.Value(ex.Equal[key])
		if err != nil {
			return nil, fmt.Errorf("equal %s: %v", key, err)
		}
		c := newCheck("equal", key)
		c.want = want
		out = append(out, c)
	}
	for _, key := range ex.Present {
		out = append(out, newCheck("present", key))
	}
	for _, key := range ex.Absent {
		out = append(out, newCheck("absent", key))
	}
	for _, key := range sortedKeys(ex.Keys) {
		want := make([]string, 0, len(ex.Keys[key]))
		for _, k := range ex.Keys[key] {
			t, err := vars.Text(k, nil)
			if err != nil {
				return nil, fmt.Errorf("keys %s: %v", key, err)
			}
			want = append(want, t)
		}
		sort.Strings(want)
		c := newCheck("keys", key)
		c.want = want
		out = append(out, c)
	}
	for _, key := range sortedKeys(ex.Length) {
		n, err := lengthValue(ex.Length[key], vars)
		if err != nil {
			return nil, fmt.Errorf("length of %s: %v", key, err)
		}
		c := newCheck("length", key)
		c.want = n
		out = append(out, c)
	}
	for _, key := range sortedKeys(ex.Unordered) {
		want, err := vars.Value(anyList(ex.Unordered[key]))
		if err != nil {
			return nil, fmt.Errorf("unordered %s: %v", key, err)
		}
		c := newCheck("unordered", key)
		c.want = want
		out = append(out, c)
	}
	for _, kind := range []string{"any", "none"} {
		m := ex.Any
		if kind == "none" {
			m = ex.None
		}
		for _, key := range sortedKeys(m) {
			for i, claim := range m[key].All() {
				c := newCheck(kind, key)
				c.claim = claimIndex(m[key].Multi, i)
				want, err := whereChecks(&c, claim, vars)
				if err != nil {
					return nil, fmt.Errorf("%s %s%s: %v", kind, key, claimLabel(c.claim), err)
				}
				c.want = want
				out = append(out, c)
			}
		}
	}
	for _, key := range sortedKeys(ex.Contains) {
		text, isText := ex.Contains[key].(string)
		if !isText {
			return nil, fmt.Errorf("contains %s: the expected text must be text", key)
		}
		want, err := vars.Text(text, nil)
		if err != nil {
			return nil, fmt.Errorf("contains %s: %v", key, err)
		}
		c := newCheck("contains", key)
		c.want = want
		out = append(out, c)
	}
	for _, key := range sortedKeys(ex.Matches) {
		text, isText := ex.Matches[key].(string)
		if !isText {
			return nil, fmt.Errorf("matches %s: the pattern must be text", key)
		}
		re, err := model.MatchPattern(text)
		if err != nil {
			return nil, fmt.Errorf("matches %s: %v", key, err)
		}
		c := newCheck("matches", key)
		c.want, c.pattern = text, re
		out = append(out, c)
	}
	for _, key := range sortedKeys(ex.Excludes) {
		texts, multi, ok := model.ExcludedTexts(ex.Excludes[key])
		if !ok {
			return nil, fmt.Errorf("excludes %s: the text must be text or a list of texts", key)
		}
		for i, text := range texts {
			c := newCheck("excludes", key)
			c.claim = claimIndex(multi, i)
			want, err := vars.Text(text, nil)
			if err != nil {
				return nil, fmt.Errorf("excludes %s%s: %v", key, claimLabel(c.claim), err)
			}
			if want == "" {
				return nil, fmt.Errorf("excludes %s%s: the text is empty, and every text contains it", key, claimLabel(c.claim))
			}
			c.want = want
			out = append(out, c)
		}
	}
	for _, key := range sortedKeys(ex.Differs) {
		want, err := vars.Value(ex.Differs[key])
		if err != nil {
			return nil, fmt.Errorf("differs %s: %v", key, err)
		}
		c := newCheck("differs", key)
		c.want = want
		out = append(out, c)
	}
	for _, key := range sortedKeys(ex.Counts) {
		for i, cm := range ex.Counts[key].All() {
			c := newCheck("count", key)
			c.claim = claimIndex(ex.Counts[key].Multi, i)
			where, err := whereChecks(&c, cm.Where, vars)
			if err != nil {
				return nil, fmt.Errorf("count %s%s: %v", key, claimLabel(c.claim), err)
			}
			if c.n, err = lengthValue(cm.Is, vars); err != nil {
				return nil, fmt.Errorf("count %s%s: is: %v", key, claimLabel(c.claim), err)
			}
			c.want = map[string]any{"where": where, "is": c.n}
			out = append(out, c)
		}
	}
	return out, nil
}

func claimIndex(multi bool, i int) *int {
	if !multi {
		return nil
	}
	return &i
}

func claimLabel(claim *int) string {
	if claim == nil {
		return ""
	}
	return fmt.Sprintf(" claim[%d]", *claim)
}

func whereChecks(c *check, m map[string]any, vars expr.Vars) (map[string]any, error) {
	want := map[string]any{}
	for _, sk := range sortedKeys(m) {
		v, err := vars.Value(m[sk])
		if err != nil {
			return nil, fmt.Errorf("%s: %v", sk, err)
		}
		p, perr := expr.ParseRelative(sk)
		c.subs = append(c.subs, subCheck{key: sk, path: p, perr: perr, want: v})
		want[sk] = v
	}
	return want, nil
}

func anyList(l []any) any {
	if l == nil {
		return []any{}
	}
	return l
}

func lengthValue(v any, vars expr.Vars) (int, error) {
	x, err := vars.Value(v)
	if err != nil {
		return 0, err
	}
	bad := fmt.Errorf("%s is not a non-negative whole number", expr.ToText(x))
	switch n := x.(type) {
	case int:
		if n < 0 {
			return 0, bad
		}
		return n, nil
	case int64:
		if n < 0 || n > math.MaxInt32 {
			return 0, bad
		}
		return int(n), nil
	case uint64:
		if n > math.MaxInt32 {
			return 0, bad
		}
		return int(n), nil
	case json.Number:
		i, err := strconv.Atoi(n.String())
		if err != nil || i < 0 {
			return 0, bad
		}
		return i, nil
	}
	return 0, bad
}

func sameMultiset(want, got []any) bool {
	if len(want) != len(got) {
		return false
	}
	used := make([]bool, len(got))
	for _, w := range want {
		found := false
		for i, g := range got {
			if !used[i] && expr.Equal(w, g) {
				used[i], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

type missingKey struct {
	key, part string
	whole     bool
}

func keyInNoElement(subs []subCheck, list []any) (missingKey, bool) {
	if len(list) == 0 {
		return missingKey{}, false
	}
	for _, s := range subs {
		found := false
		for _, el := range list {
			if _, ok := s.path.Select(el); ok {
				found = true
				break
			}
		}
		if !found {
			return missingPart(s, list), true
		}
	}
	return missingKey{}, false
}

func missingPart(s subCheck, list []any) missingKey {
	n := len(s.path.Segments())
	held := 0
	for _, el := range list {
		for held < n-1 {
			if _, ok := s.path.Prefix(held + 1).Select(el); !ok {
				break
			}
			held++
		}
	}
	return missingKey{key: s.key, part: s.path.Prefix(held + 1).Relative(), whole: held+1 >= n}
}

func vacuousMessage(m missingKey, target string) string {
	head := fmt.Sprintf("claim key %s is in no element of %s; the claim can't fail (EXE-11)", m.key, target)
	if m.whole {
		return head + ": check the key against an element, or add a claim that some element has it (any, or count with is above 0)"
	}
	return fmt.Sprintf("%s: no element has %s. A missing %s isn't read as \"not this value\", since the claim would then pass whenever the elements stop carrying %s; add a claim that some element has it (any, or count with is above 0, on %s), or, if no element should, assert what the elements are instead (for example count them by type)", head, m.part, m.part, m.part, m.key)
}

func matchesAll(subs []subCheck, el any) bool {
	for _, s := range subs {
		got, ok := s.path.Select(el)
		if !ok || !expr.Equal(s.want, got) {
			return false
		}
	}
	return true
}

const notPresent = "not present in the fact"

func excludeInList(a *Assertion, target, want string, list []any) {
	if len(list) == 0 {
		a.Result, a.vacuous = Fail, true
		a.Message = fmt.Sprintf("%s is an empty list, so excludes can't be judged: no element could hold the text. Assert that the list has elements too, or that it is empty with length", target)
		return
	}
	for i, el := range list {
		text, isText := el.(string)
		if !isText {
			a.Result, a.Message = Fail, fmt.Sprintf("element [%d] is not text", i)
			return
		}
		if at := strings.Index(text, want); at >= 0 {
			a.Actual = text
			a.Result, a.Message = Fail, fmt.Sprintf("element [%d] contains the excluded text at byte %d of %d", i, at, len(text))
			return
		}
	}
}

func (r *Runner) judge(c check, fact any, red []string, op string) Assertion {
	a := Assertion{Kind: c.kind, Target: c.target, Claim: c.claim, Result: Pass}
	elem := c.target + "[0]"
	fail := func(msg string) { a.Result, a.Message = Fail, msg }
	if c.perr != nil {
		fail(c.perr.Error())
		return a
	}
	if why, bad := invalidBody(c, fact); bad {
		a.Expected, a.vacuous = c.want, true
		fail(fmt.Sprintf("the response body isn't valid JSON (body_invalid: %s), so %s on %s can't be judged (HTTP-10); its text is in $.%s", why, c.kind, c.target, cutTextName))
		return r.redactAssertion(a, c, elem, red)
	}
	got, ok := c.path.Select(fact)
	if kept, mark, cut := truncatedText(c, fact, got); cut {
		a.Expected, a.Present, a.CutAt, a.vacuous = c.want, true, kept, true
		fail(fmt.Sprintf("%s was cut at %d bytes (%s: true): %s", c.target, kept, mark, cutAdvice(mark, c.kind)))
		return r.redactAssertion(a, c, elem, red)
	}
	if list, why, over := overflowed(c, fact, op); over {
		a.Expected, a.Present, a.Overflow, a.vacuous = c.want, ok, true, true
		if isStream(fact) {
			fail(fmt.Sprintf("the driver left frames out of %s (%s: true%s), so %s on %s can't be judged: a frame left out could change it. On a stream that overflowed only any, present and checks on a frame that is there are judged; assert on a shorter stream", list, overflowMarkName, why, c.kind, c.target))
		} else {
			fail(fmt.Sprintf("the driver left records out of %s (%s: true%s), so %s on %s can't be judged: a record left out could change it. When records were left out only any, present and checks on a record that is there are judged; narrow the read so fewer records match", list, overflowMarkName, why, c.kind, c.target))
		}
		return r.redactAssertion(a, c, elem, red)
	}
	switch c.kind {
	case "equal":
		a.Expected = c.want
		if !ok {
			fail(notPresent)
			break
		}
		a.Present, a.Actual = true, got
		if !expr.Equal(c.want, got) {
			fail("value differs")
			if c.target == statusPath {
				a.Response = r.responseCause(fact, red)
			}
		}
	case "differs":
		a.Expected = c.want
		if !ok {
			fail(notPresent)
			break
		}
		a.Present, a.Actual = true, got
		if expr.Equal(c.want, got) {
			fail("the value is the one it must differ from")
		}
	case "matches", "excludes":
		want := c.want.(string)
		a.Expected = want
		text, isText := got.(string)
		list, isList := got.([]any)
		switch {
		case !ok:
			fail(notPresent)
		case c.kind == "excludes" && isList:
			a.Present, a.Actual = true, list
			excludeInList(&a, c.target, want, list)
		case !isText:
			a.Present, a.Actual = true, got
			fail("not text")
		default:
			a.Present, a.Actual = true, text
			if c.kind == "matches" && !c.pattern.MatchString(text) {
				fail("doesn't match the pattern, which must match the whole text")
			}
			if at := strings.Index(text, want); c.kind == "excludes" && at >= 0 {
				fail(fmt.Sprintf("contains the excluded text at byte %d of %d", at, len(text)))
			}
		}
	case "present":
		if !ok {
			fail(notPresent)
			break
		}
		a.Present = true
	case "absent":
		if ok {
			a.Present, a.Actual = true, got
			fail("present in the fact, expected absent")
		}
	case "keys":
		want := c.want.([]string)
		a.Expected = want
		obj, isObj := got.(map[string]any)
		switch {
		case !ok:
			fail(notPresent)
		case !isObj:
			a.Present, a.Actual = true, got
			fail("not an object")
		default:
			have := sortedKeys(obj)
			a.Present, a.Actual = true, have
			var extra, missing []string
			for _, k := range have {
				if !contains(want, k) {
					extra = append(extra, k)
				}
			}
			for _, k := range want {
				if _, ok := obj[k]; !ok {
					missing = append(missing, k)
				}
			}
			if len(extra)+len(missing) > 0 {
				fail(fmt.Sprintf("unexpected keys %v, missing keys %v", extra, missing))
			}
		}
	case "length":
		want := c.want.(int)
		a.Expected = want
		list, isList := got.([]any)
		switch {
		case !ok:
			fail(notPresent)
		case !isList:
			a.Present, a.Actual = true, got
			fail("not a list")
		default:
			a.Present, a.Actual = true, len(list)
			if len(list) != want {
				fail(fmt.Sprintf("expected length %d, got %d", want, len(list)))
			}
		}
	case "unordered":
		want, _ := c.want.([]any)
		a.Expected = want
		list, isList := got.([]any)
		switch {
		case !ok:
			fail(notPresent)
		case !isList:
			a.Present, a.Actual = true, got
			fail("not a list")
		default:
			a.Present, a.Actual = true, list
			if !sameMultiset(want, list) {
				fail("values differ (order ignored)")
			}
		}
	case "contains":
		want := c.want.(string)
		a.Expected = want
		text, isText := got.(string)
		switch {
		case !ok:
			fail(notPresent)
		case !isText:
			a.Present, a.Actual = true, got
			fail("not text")
		default:
			a.Present, a.Actual = true, text
			if !strings.Contains(text, want) {
				fail("doesn't contain the expected text")
			}
		}
	case "count":
		a.Expected = c.want
		for _, s := range c.subs {
			if s.perr != nil {
				fail(fmt.Sprintf("%s: %v", s.key, s.perr))
				return r.redactAssertion(a, c, elem, red)
			}
		}
		list, isList := got.([]any)
		switch {
		case !ok:
			fail(notPresent)
		case !isList:
			a.Present, a.Actual = true, got
			fail("not a list")
		default:
			a.Present = true
			var at []string
			for i, el := range list {
				if matchesAll(c.subs, el) {
					at = append(at, fmt.Sprintf("[%d]", i))
				}
			}
			a.Actual = len(at)
			matched := fmt.Sprintf("none of the %d elements has these values", len(list))
			if len(at) > 0 {
				matched = fmt.Sprintf("%d of the %d elements have these values: %s", len(at), len(list), strings.Join(at, ", "))
			}
			key, vacuous := keyInNoElement(c.subs, list)
			switch {
			case c.n == 0 && vacuous:
				a.vacuous = true
				fail(vacuousMessage(key, c.target))
			case len(at) == c.n:
				a.Message = matched
			default:
				fail(fmt.Sprintf("%s; expected %d", matched, c.n))
			}
		}
	case "any", "none":
		a.Expected = c.want
		for _, s := range c.subs {
			if s.perr != nil {
				fail(fmt.Sprintf("%s: %v", s.key, s.perr))
				return r.redactAssertion(a, c, elem, red)
			}
		}
		list, isList := got.([]any)
		switch {
		case !ok:
			fail(notPresent)
		case !isList:
			a.Present, a.Actual = true, got
			fail("not a list")
		default:
			a.Present = true
			idx := -1
			for i, el := range list {
				if matchesAll(c.subs, el) {
					idx = i
					break
				}
			}
			switch {
			case c.kind == "any" && idx < 0:
				a.Actual = list
				fail(fmt.Sprintf("none of the %d elements has these values", len(list)))
			case c.kind == "any":
				a.Actual, elem = list[idx], fmt.Sprintf("%s[%d]", c.target, idx)
				a.Message = fmt.Sprintf("element [%d] has these values", idx)
			case idx >= 0:
				a.Actual, elem = list[idx], fmt.Sprintf("%s[%d]", c.target, idx)
				fail(fmt.Sprintf("element [%d] has these values, expected none", idx))
			default:
				if key, vacuous := keyInNoElement(c.subs, list); vacuous {
					a.Actual, a.vacuous = list, true
					fail(vacuousMessage(key, c.target))
				}
			}
		}
	}
	return r.redactAssertion(a, c, elem, red)
}

const (
	statusPath      = "$.status"
	cutTextName     = "body_text"
	cutMarkName     = "body_truncated"
	plainTextName   = "text"
	cutTextSuffix   = "_text"
	cutMarkSuffix   = "_truncated"
	bodyName        = "body"
	invalidMarkName = "body_invalid"
	responseSlack   = 64 << 10
)

const (
	overflowMarkName = "overflow"
	overflowWhyName  = "overflow_reason"
	countFieldName   = "count"
	closedFieldName  = "closed"
)

var overflowJudged = map[string]bool{"any": true, "present": true}

func isStream(fact any) bool {
	obj, _ := fact.(map[string]any)
	_, has := obj[closedFieldName]
	return has
}

func overflowed(c check, fact any, op string) (string, string, bool) {
	segs := c.path.Segments()
	obj, _ := fact.(map[string]any)
	if len(segs) == 0 || segs[0].IsIndex || obj[overflowMarkName] != true {
		return "", "", false
	}
	name := segs[0].Name
	_, isList := obj[name].([]any)
	if !isList && name != countFieldName {
		return "", "", false
	}
	at := expr.Child("$", name)
	if _, own := obj[op].([]any); !isList && own {
		at = expr.Child("$", op)
	} else if !isList {
		for _, sibling := range sortedKeys(obj) {
			if _, ok := obj[sibling].([]any); ok {
				at = expr.Child("$", sibling)
				break
			}
		}
	}
	why := ""
	if text, isText := obj[overflowWhyName].(string); isText && text != "" {
		why = "; " + text
	}
	switch {
	case len(segs) == 1 && !overflowJudged[c.kind]:
		return at, why, true
	case len(segs) > 1 && isList && c.kind == "absent":
		if _, there := c.path.Prefix(2).Select(fact); !there {
			return at, why, true
		}
	}
	return "", "", false
}

func invalidBody(c check, fact any) (string, bool) {
	segs := c.path.Segments()
	if len(segs) == 0 || segs[0].IsIndex || segs[0].Name != bodyName {
		return "", false
	}
	obj, _ := fact.(map[string]any)
	why, has := obj[invalidMarkName]
	if !has {
		return "", false
	}
	text, _ := why.(string)
	return text, true
}

var textMatchers = map[string]bool{"equal": true, "contains": true, "matches": true, "excludes": true, "differs": true}

func truncatedText(c check, fact, got any) (int, string, bool) {
	text, isText := got.(string)
	segs := c.path.Segments()
	if !textMatchers[c.kind] || !isText || len(segs) == 0 {
		return 0, "", false
	}
	last := segs[len(segs)-1]
	if last.IsIndex {
		return 0, "", false
	}
	mark, ok := cutMarkOf(last.Name)
	if !ok {
		return 0, "", false
	}
	parent, _ := c.path.Prefix(len(segs) - 1).Select(fact)
	obj, _ := parent.(map[string]any)
	if obj[mark] != true {
		return 0, "", false
	}
	return len(text), mark, true
}

func cutMarkOf(name string) (string, bool) {
	switch {
	case name == plainTextName:
		return plainTextName + cutMarkSuffix, true
	case strings.HasSuffix(name, cutTextSuffix) && len(name) > len(cutTextSuffix):
		return strings.TrimSuffix(name, cutTextSuffix) + cutMarkSuffix, true
	}
	return "", false
}

func cutAdvice(mark, kind string) string {
	if mark == cutMarkName {
		return fmt.Sprintf("the driver keeps only the start of a longer body, so %s can't judge the text after the cut; assert on a smaller response", kind)
	}
	return fmt.Sprintf("the driver keeps only the start of a longer text, so %s can't judge the text after the cut; assert on a shorter text", kind)
}

func (r *Runner) responseCause(fact any, red []string) string {
	obj, _ := fact.(map[string]any)
	if body, isObj := obj["body"].(map[string]any); isObj {
		var parts []string
		for _, k := range []string{"error", "message"} {
			if v, has := body[k]; has {
				parts = append(parts, k+" "+compactJSON(r.redactValue("$.body."+k, v, red)))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, ", ")
		}
	}
	text := ""
	switch {
	case obj[cutTextName] != nil:
		text, _ = r.redactValue("$."+cutTextName, obj[cutTextName], red).(string)
	case obj["body"] != nil:
		text = compactJSON(r.redactValue("$.body", obj["body"], red))
	}
	if text == "" {
		return ""
	}
	if limit := r.excerpt + responseSlack; len(text) > limit {
		text = cut(text, limit)
	}
	return "body " + text
}

func compactJSON(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func (r *Runner) redactAssertion(a Assertion, c check, elem string, red []string) Assertion {
	switch c.kind {
	case "keys":
		switch {
		case r.listedPath(c.target, red):
			if a.Expected != nil {
				a.Expected = redacted
			}
			if a.Actual != nil {
				a.Actual = redacted
			}
		case r.sensitiveTarget(c.target):
			if a.Expected != nil {
				a.Expected = redacted
			}
			if _, names := a.Actual.([]string); names {
				a.Actual = redacted
			} else {
				a.Actual = r.redactValue(c.target, a.Actual, red)
			}
		}
		return a
	case "any", "none":
		if r.listedPath(c.target, red) {
			if a.Expected != nil {
				a.Expected = redacted
			}
			if a.Actual != nil {
				a.Actual = redacted
			}
			return a
		}
		if want, ok := a.Expected.(map[string]any); ok {
			a.Expected = r.redactWhere(c.target, want, red)
		}
		if _, isList := a.Actual.([]any); isList {
			elem = c.target
		}
		a.Actual = r.redactValue(elem, a.Actual, red)
		return a
	case "count":
		want, isCount := a.Expected.(map[string]any)
		switch {
		case r.listedPath(c.target, red):
			a.Expected = redacted
		case isCount:
			where, _ := want["where"].(map[string]any)
			a.Expected = map[string]any{"where": r.redactWhere(c.target, where, red), "is": want["is"]}
		}
		if _, isCount := a.Actual.(int); !isCount {
			a.Actual = r.redactValue(c.target, a.Actual, red)
		}
		return a
	}
	a.Expected = r.redactValue(c.target, a.Expected, red)
	a.Actual = r.redactValue(c.target, a.Actual, red)
	return a
}

func (r *Runner) redactWhere(target string, want map[string]any, red []string) map[string]any {
	out := map[string]any{}
	for k, v := range want {
		out[k] = r.redactValue(target+"[0]"+whereSuffix(k), v, red)
	}
	return out
}

func whereSuffix(k string) string {
	if p, err := expr.ParseRelative(k); err == nil {
		return strings.TrimPrefix(p.String(), "$")
	}
	if strings.HasPrefix(k, "$") {
		return strings.TrimPrefix(k, "$")
	}
	return "." + k
}

type redactPath struct {
	canonical, dotted string
}

func (p redactPath) child(name string) redactPath {
	return redactPath{canonical: expr.Child(p.canonical, name), dotted: p.dotted + "." + name}
}

func (p redactPath) index(i int) redactPath {
	at := fmt.Sprintf("[%d]", i)
	return redactPath{canonical: p.canonical + at, dotted: p.dotted + at}
}

func (r *Runner) listedAt(p redactPath, red []string) bool {
	return r.listedPath(p.canonical, red) || r.listedPath(p.dotted, red)
}

func (r *Runner) sensitiveAt(p redactPath) bool {
	return sensitivePath(p.canonical) || sensitivePath(p.dotted) || r.headerKeyPath(p.canonical)
}

func (r *Runner) sensitiveTarget(p string) bool {
	return sensitivePath(p) || r.headerKeyPath(p)
}

func (r *Runner) headerKeyPath(p string) bool {
	parsed, err := expr.ParsePath(p)
	if err != nil {
		return false
	}
	for _, seg := range parsed.Segments() {
		if !seg.IsIndex && dottedHeader(seg.Name, r.headers) {
			return true
		}
	}
	return false
}

func dottedHeader(name string, headers map[string]bool) bool {
	if !strings.Contains(name, ".") {
		return false
	}
	for _, part := range strings.Split(strings.ToLower(name), ".") {
		if credentialHeaders[part] || headers[part] {
			return true
		}
	}
	return false
}

func newRedactPath(path string) redactPath {
	p, err := expr.ParsePath(path)
	if err != nil {
		return redactPath{canonical: path, dotted: path}
	}
	dotted := "$"
	for _, seg := range p.Segments() {
		if seg.IsIndex {
			dotted += fmt.Sprintf("[%d]", seg.Index)
		} else {
			dotted += "." + seg.Name
		}
	}
	return redactPath{canonical: path, dotted: dotted}
}

func (r *Runner) redactValue(path string, v any, red []string) any {
	return r.redactAt(newRedactPath(path), v, red)
}

func (r *Runner) redactAt(path redactPath, v any, red []string) any {
	if v == nil {
		return nil
	}
	if r.listedAt(path, red) {
		return redacted
	}
	switch x := v.(type) {
	case string:
		if r.sensitiveAt(path) {
			return redacted
		}
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = r.redactAt(path.child(k), e, red)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = r.redactAt(path.index(i), e, red)
		}
		return out
	}
	return v
}

func (r *Runner) scrubRedacted(path string, v any, red []string) {
	r.scrubAt(newRedactPath(path), v, red)
}

func (r *Runner) scrubAt(path redactPath, v any, red []string) {
	if r.listedAt(path, red) {
		for _, s := range textLeaves(v) {
			r.scrub.add(s)
		}
		return
	}
	switch x := v.(type) {
	case string:
		if r.sensitiveAt(path) {
			r.scrub.add(x)
		}
	case map[string]any:
		for k, e := range x {
			r.scrubAt(path.child(k), e, red)
		}
	case []any:
		for i, e := range x {
			r.scrubAt(path.index(i), e, red)
		}
	}
}

func textLeaves(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case map[string]any:
		var out []string
		for _, e := range x {
			out = append(out, textLeaves(e)...)
		}
		return out
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, textLeaves(e)...)
		}
		return out
	}
	return nil
}

func (r *Runner) listedPath(p string, red []string) bool {
	if r.headerPath(p) {
		return true
	}
	for q := range r.redactJS {
		if pathUnder(p, q) {
			return true
		}
	}
	for _, q := range red {
		if pathUnder(p, q) {
			return true
		}
	}
	return false
}

func (r *Runner) headerPath(p string) bool {
	if len(r.headers) == 0 {
		return false
	}
	parsed, err := expr.ParsePath(p)
	if err != nil {
		return false
	}
	for _, seg := range parsed.Segments() {
		if !seg.IsIndex && r.headers[strings.ToLower(seg.Name)] {
			return true
		}
	}
	return false
}

func (r *Runner) scrubHeaders(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if r.headers[strings.ToLower(k)] || dottedHeader(k, r.headers) {
				for _, s := range textLeaves(e) {
					r.scrub.add(s)
				}
				continue
			}
			r.scrubHeaders(e)
		}
	case []any:
		for _, e := range x {
			r.scrubHeaders(e)
		}
	}
}

func (r *Runner) redactExcerpt(text string) string {
	if len(r.headers) == 0 || text == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line == "" {
			break
		}
		name, _, ok := strings.Cut(line, ": ")
		if ok && r.headers[strings.ToLower(name)] {
			lines[i] = name + ": " + redacted
		}
	}
	return strings.Join(lines, "\n")
}

func pathUnder(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+".") || strings.HasPrefix(p, root+"[")
}

func sensitivePath(p string) bool {
	for _, part := range strings.FieldsFunc(p, func(c rune) bool { return c == '.' || c == '[' }) {
		if sensitiveName(part) {
			return true
		}
	}
	return false
}

func sensitiveName(name string) bool {
	last := strings.ToLower(name)
	for _, w := range []string{"password", "secret", "token", "api_key"} {
		if strings.Contains(last, w) {
			return true
		}
	}
	return false
}
