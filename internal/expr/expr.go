package expr

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

type Path struct {
	src   string
	steps []step
}

type step struct {
	name  string
	index int
	isIdx bool
}

var nameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*`)

func ParsePath(s string) (Path, error) {
	p := Path{src: s}
	if !strings.HasPrefix(s, "$") {
		return p, fmt.Errorf("JSONPath %q must start with $", s)
	}
	rest := s[1:]
	for rest != "" {
		switch rest[0] {
		case '.':
			m := nameRE.FindString(rest[1:])
			if m == "" {
				return p, fmt.Errorf("JSONPath %q: expected a field name after '.'", s)
			}
			p.steps = append(p.steps, step{name: m})
			rest = rest[1+len(m):]
		case '[':
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return p, fmt.Errorf("JSONPath %q: unclosed '['", s)
			}
			n, err := strconv.Atoi(rest[1:end])
			if err != nil || n < 0 || rest[1] == '+' || (len(rest[1:end]) > 1 && rest[1] == '0') {
				return p, fmt.Errorf("JSONPath %q: only non-negative array indexes are supported in Phase 0", s)
			}
			p.steps = append(p.steps, step{index: n, isIdx: true})
			rest = rest[end+1:]
		default:
			return p, fmt.Errorf("JSONPath %q: unsupported syntax at %q (Phase 0 supports $, .name and [index])", s, rest)
		}
	}
	return p, nil
}

func (p Path) String() string { return p.src }

func (p Path) Select(doc any) (any, bool) {
	cur := doc
	for _, st := range p.steps {
		if st.isIdx {
			arr, ok := cur.([]any)
			if !ok || st.index >= len(arr) {
				return nil, false
			}
			cur = arr[st.index]
			continue
		}
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := obj[st.name]
		if !ok {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

var (
	refRE   = regexp.MustCompile(`\$\{([^}]*)\}`)
	varName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func VarName(s string) bool { return varName.MatchString(s) }

const RunID = "run.id"

func References(s string) ([]string, error) {
	var names []string
	for _, m := range refRE.FindAllStringSubmatch(s, -1) {
		if m[1] != RunID && !varName.MatchString(m[1]) && !IsFill(m[1]) && !IsSecret(m[1]) {
			return nil, fmt.Errorf("unsupported reference ${%s}: supported are ${run.id}, ${fill(N)}, ${secret.name} and ${name} for values saved by earlier steps", m[1])
		}
		names = append(names, m[1])
	}
	if strings.Count(s, "${") != len(names) {
		return nil, fmt.Errorf("unclosed ${ in %q", s)
	}
	return names, nil
}

func ReferencesIn(v any) ([]string, error) {
	var out []string
	var walk func(any) error
	walk = func(v any) error {
		switch t := v.(type) {
		case string:
			n, err := References(t)
			out = append(out, n...)
			return err
		case map[string]any:
			for k, x := range t {
				n, err := References(k)
				if err != nil {
					return err
				}
				out = append(out, n...)
				if err := walk(x); err != nil {
					return err
				}
			}
		case []any:
			for _, x := range t {
				if err := walk(x); err != nil {
					return err
				}
			}
		}
		return nil
	}
	err := walk(v)
	return out, err
}

var (
	fillRE   = regexp.MustCompile(`^fill\(([1-9][0-9]{0,7})\)$`)
	secretRE = regexp.MustCompile(`^secret\.([A-Za-z_][A-Za-z0-9_]*)$`)
)

const MaxFill = 16 << 20

func IsFill(ref string) bool {
	m := fillRE.FindStringSubmatch(ref)
	if m == nil {
		return false
	}
	n, _ := strconv.Atoi(m[1])
	return n <= MaxFill
}

func IsSecret(ref string) bool { return secretRE.MatchString(ref) }

func SecretName(ref string) string { return secretRE.FindStringSubmatch(ref)[1] }

func (v Vars) lookup(name string) (any, bool) {
	if m := fillRE.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n <= MaxFill {
			return strings.Repeat("x", n), true
		}
	}
	val, ok := v[name]
	return val, ok
}

type Vars map[string]any

func (v Vars) Value(t any) (any, error) {
	switch x := t.(type) {
	case string:
		if m := refRE.FindStringSubmatch(x); m != nil && m[0] == x {
			val, ok := v.lookup(m[1])
			if !ok {
				return nil, fmt.Errorf("${%s} has no saved value", m[1])
			}
			return val, nil
		}
		return v.Text(x, nil)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			nk, err := v.Text(k, nil)
			if err != nil {
				return nil, err
			}
			ne, err := v.Value(e)
			if err != nil {
				return nil, err
			}
			out[nk] = ne
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			ne, err := v.Value(e)
			if err != nil {
				return nil, err
			}
			out[i] = ne
		}
		return out, nil
	default:
		return t, nil
	}
}

func (v Vars) Text(t string, escape func(string) string) (string, error) {
	var err error
	out := refRE.ReplaceAllStringFunc(t, func(ref string) string {
		name := ref[2 : len(ref)-1]
		val, ok := v.lookup(name)
		if !ok {
			err = fmt.Errorf("${%s} has no saved value", name)
			return ""
		}
		s := ToText(val)
		if escape != nil {
			s = escape(s)
		}
		return s
	})
	return out, err
}

func ToText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func Equal(want, got any) bool {
	if wn, ok := number(want); ok {
		gn, ok := number(got)
		return ok && wn.Cmp(gn) == 0
	}
	switch w := want.(type) {
	case nil:
		return got == nil
	case bool:
		g, ok := got.(bool)
		return ok && g == w
	case string:
		g, ok := got.(string)
		return ok && g == w
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !Equal(w[i], g[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok || !Equal(wv, gv) {
				return false
			}
		}
		return true
	}
	return false
}

func number(v any) (*big.Rat, bool) {
	var s string
	switch n := v.(type) {
	case json.Number:
		s = n.String()
	case int:
		s = strconv.Itoa(n)
	case int64:
		s = strconv.FormatInt(n, 10)
	case uint64:
		s = strconv.FormatUint(n, 10)
	case float64:
		s = strconv.FormatFloat(n, 'g', -1, 64)
	default:
		return nil, false
	}
	r, ok := new(big.Rat).SetString(s)
	return r, ok
}
