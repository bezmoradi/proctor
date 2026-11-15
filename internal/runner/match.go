package runner

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

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
	kind   string
	target string
	path   expr.Path
	perr   error
	want   any
	subs   []subCheck
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
			c := newCheck(kind, key)
			want := map[string]any{}
			for _, sk := range sortedKeys(m[key]) {
				v, err := vars.Value(m[key][sk])
				if err != nil {
					return nil, fmt.Errorf("%s %s: %s: %v", kind, key, sk, err)
				}
				sp := sk
				if !strings.HasPrefix(sp, "$") {
					sp = "$." + sp
				}
				p, perr := expr.ParsePath(sp)
				c.subs = append(c.subs, subCheck{key: sk, path: p, perr: perr, want: v})
				want[sk] = v
			}
			c.want = want
			out = append(out, c)
		}
	}
	return out, nil
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

func lengthOf(v any) (int, bool) {
	switch x := v.(type) {
	case []any:
		return len(x), true
	case map[string]any:
		return len(x), true
	case string:
		return utf8.RuneCountInString(x), true
	}
	return 0, false
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

func (r *Runner) judge(c check, fact any, red []string) Assertion {
	a := Assertion{Kind: c.kind, Target: c.target, Result: Pass}
	elem := c.target + "[0]"
	fail := func(msg string) { a.Result, a.Message = Fail, msg }
	if c.perr != nil {
		fail(c.perr.Error())
		return a
	}
	got, ok := c.path.Select(fact)
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
		n, isLen := lengthOf(got)
		switch {
		case !ok:
			fail(notPresent)
		case !isLen:
			a.Present, a.Actual = true, got
			fail("not a list, an object or text")
		default:
			a.Present, a.Actual = true, n
			if n != want {
				fail(fmt.Sprintf("expected length %d, got %d", want, n))
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
			}
		}
	}
	return r.redactAssertion(a, c, elem, red)
}

func (r *Runner) redactAssertion(a Assertion, c check, elem string, red []string) Assertion {
	switch c.kind {
	case "keys":
		if r.redactPath(c.target, red) {
			if a.Expected != nil {
				a.Expected = redacted
			}
			if a.Actual != nil {
				a.Actual = redacted
			}
		}
		return a
	case "any", "none":
		if r.redactPath(c.target, red) {
			if a.Expected != nil {
				a.Expected = redacted
			}
			if a.Actual != nil {
				a.Actual = redacted
			}
			return a
		}
		if want, ok := a.Expected.(map[string]any); ok {
			out := map[string]any{}
			for k, v := range want {
				sp := k
				if !strings.HasPrefix(sp, "$") {
					sp = "$." + sp
				}
				out[k] = r.redactValue(c.target+"[0]"+strings.TrimPrefix(sp, "$"), v, red)
			}
			a.Expected = out
		}
		if _, isList := a.Actual.([]any); isList {
			elem = c.target
		}
		a.Actual = r.redactValue(elem, a.Actual, red)
		return a
	}
	a.Expected = r.redactValue(c.target, a.Expected, red)
	a.Actual = r.redactValue(c.target, a.Actual, red)
	return a
}

func (r *Runner) redactValue(path string, v any, red []string) any {
	if v == nil {
		return nil
	}
	if r.redactPath(path, red) {
		return redacted
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = r.redactValue(path+"."+k, e, red)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = r.redactValue(fmt.Sprintf("%s[%d]", path, i), e, red)
		}
		return out
	}
	return v
}

func (r *Runner) scrubRedacted(path string, v any, red []string) {
	if r.redactPath(path, red) {
		for _, s := range textLeaves(v) {
			r.scrub.add(s)
		}
		return
	}
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			r.scrubRedacted(path+"."+k, e, red)
		}
	case []any:
		for i, e := range x {
			r.scrubRedacted(fmt.Sprintf("%s[%d]", path, i), e, red)
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

func (r *Runner) redactPath(p string, red []string) bool {
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
	return sensitiveName(p[strings.LastIndexAny(p, ".[")+1:])
}

func pathUnder(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+".") || strings.HasPrefix(p, root+"[")
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
