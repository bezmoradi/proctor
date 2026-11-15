package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type Option int

const AllowRefs Option = 1

var (
	wholeRef = regexp.MustCompile(`^\$\{[^{}]+\}$`)
	plainKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	types    = []string{"object", "array", "string", "integer", "number", "boolean", "null"}
	ignored  = map[string]bool{"$schema": true, "$id": true, "$comment": true, "title": true, "description": true, "default": true, "examples": true}
)

type node struct {
	always     *bool
	types      []string
	props      map[string]*node
	required   []string
	additional *node
	items      *node
	enum       []any
	minimum    *big.Rat
	maximum    *big.Rat
	pattern    *regexp.Regexp
}

func IsRef(v any) bool {
	s, ok := v.(string)
	return ok && wholeRef.MatchString(s)
}

func CheckSchema(schema any) []string {
	_, errs := compileAny(schema)
	return errs
}

func Check(schema any, value any, opts ...Option) []string {
	n, errs := compileAny(schema)
	if len(errs) > 0 {
		return errs
	}
	v, err := normalize(value)
	if err != nil {
		return []string{"$: the value can't be written as JSON: " + err.Error()}
	}
	refs := false
	for _, o := range opts {
		if o == AllowRefs {
			refs = true
		}
	}
	var out []string
	n.check(v, "$", refs, &out)
	return out
}

func normalize(v any) (any, error) {
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

func compileAny(schema any) (*node, []string) {
	v, err := normalize(schema)
	if err != nil {
		return nil, []string{"schema: can't be written as JSON: " + err.Error()}
	}
	var errs []string
	n := compile(v, "$", &errs)
	return n, errs
}

func compile(v any, at string, errs *[]string) *node {
	bad := func(format string, args ...any) {
		*errs = append(*errs, fmt.Sprintf("schema at %s: ", at)+fmt.Sprintf(format, args...))
	}
	switch s := v.(type) {
	case bool:
		return &node{always: &s}
	case map[string]any:
		n := &node{}
		for _, k := range sortedKeys(s) {
			val := s[k]
			switch k {
			case "type":
				switch t := val.(type) {
				case string:
					n.types = []string{t}
				case []any:
					for _, e := range t {
						name, ok := e.(string)
						if !ok {
							bad("type must be a name or a list of names")
							continue
						}
						n.types = append(n.types, name)
					}
				default:
					bad("type must be a name or a list of names")
				}
				for _, t := range n.types {
					if !slices.Contains(types, t) {
						bad("unknown type %q", t)
					}
				}
			case "properties":
				m, ok := val.(map[string]any)
				if !ok {
					bad("properties must be an object")
					continue
				}
				n.props = map[string]*node{}
				for _, pk := range sortedKeys(m) {
					n.props[pk] = compile(m[pk], join(at, pk), errs)
				}
			case "required":
				l, ok := val.([]any)
				if !ok {
					bad("required must be a list of names")
					continue
				}
				for _, e := range l {
					name, ok := e.(string)
					if !ok {
						bad("required must be a list of names")
						continue
					}
					n.required = append(n.required, name)
				}
			case "additionalProperties":
				n.additional = compile(val, at+"{additionalProperties}", errs)
			case "items":
				n.items = compile(val, at+"[]", errs)
			case "enum":
				l, ok := val.([]any)
				if !ok {
					bad("enum must be a list")
					continue
				}
				n.enum = l
			case "minimum", "maximum":
				r, ok := number(val)
				if !ok {
					bad("%s must be a number", k)
					continue
				}
				if k == "minimum" {
					n.minimum = r
				} else {
					n.maximum = r
				}
			case "pattern":
				p, ok := val.(string)
				if !ok {
					bad("pattern must be text")
					continue
				}
				re, err := regexp.Compile(p)
				if err != nil {
					bad("pattern %q isn't a valid Go regular expression: %v", p, err)
					continue
				}
				n.pattern = re
			default:
				if !ignored[k] {
					bad("unsupported keyword %q", k)
				}
			}
		}
		return n
	default:
		bad("a schema must be an object or true or false")
		f := false
		return &node{always: &f}
	}
}

func (n *node) check(v any, at string, refs bool, out *[]string) {
	add := func(format string, args ...any) {
		*out = append(*out, at+": "+fmt.Sprintf(format, args...))
	}
	if n.always != nil {
		if !*n.always {
			add("no value is allowed here")
		}
		return
	}
	if refs && IsRef(v) {
		return
	}
	if len(n.types) > 0 && !n.matchesType(v) {
		add("expected %s, got %s", strings.Join(n.types, " or "), typeOf(v))
		return
	}
	if n.enum != nil {
		found := false
		for _, e := range n.enum {
			if equal(e, v) {
				found = true
				break
			}
		}
		if !found {
			add("must be one of %s", show(n.enum))
		}
	}
	if r, ok := number(v); ok {
		if n.minimum != nil && r.Cmp(n.minimum) < 0 {
			add("must be at least %s", n.minimum.RatString())
		}
		if n.maximum != nil && r.Cmp(n.maximum) > 0 {
			add("must be at most %s", n.maximum.RatString())
		}
	}
	if s, ok := v.(string); ok && n.pattern != nil && !n.pattern.MatchString(s) {
		add("must match the pattern %s", n.pattern.String())
	}
	switch val := v.(type) {
	case map[string]any:
		for _, r := range n.required {
			if _, ok := val[r]; !ok {
				add("missing required key %q", r)
			}
		}
		for _, k := range sortedKeys(val) {
			if p, ok := n.props[k]; ok {
				p.check(val[k], join(at, k), refs, out)
				continue
			}
			if n.additional == nil {
				continue
			}
			if n.additional.always != nil && !*n.additional.always {
				if len(n.props) > 0 {
					add("unknown key %q (allowed: %s)", k, strings.Join(sortedKeys(n.props), ", "))
				} else {
					add("unknown key %q (no keys are allowed)", k)
				}
				continue
			}
			n.additional.check(val[k], join(at, k), refs, out)
		}
	case []any:
		if n.items != nil {
			for i, e := range val {
				n.items.check(e, at+"["+strconv.Itoa(i)+"]", refs, out)
			}
		}
	}
}

func (n *node) matchesType(v any) bool {
	for _, t := range n.types {
		switch t {
		case "object":
			if _, ok := v.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := v.([]any); ok {
				return true
			}
		case "string":
			if _, ok := v.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
		case "null":
			if v == nil {
				return true
			}
		case "number":
			if _, ok := number(v); ok {
				return true
			}
		case "integer":
			if r, ok := number(v); ok && r.IsInt() {
				return true
			}
		}
	}
	return false
}

func typeOf(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case nil:
		return "null"
	}
	if r, ok := number(v); ok {
		if r.IsInt() {
			return "integer"
		}
		return "number"
	}
	return fmt.Sprintf("%T", v)
}

func number(v any) (*big.Rat, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return nil, false
	}
	return new(big.Rat).SetString(n.String())
}

func equal(a, b any) bool {
	if ra, ok := number(a); ok {
		rb, ok := number(b)
		return ok && ra.Cmp(rb) == 0
	}
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, ok := y[k]
			if !ok || !equal(xv, yv) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equal(x[i], y[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

func show(l []any) string {
	parts := make([]string, len(l))
	for i, e := range l {
		b, err := json.Marshal(e)
		if err != nil {
			parts[i] = fmt.Sprint(e)
			continue
		}
		parts[i] = string(b)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func join(at, key string) string {
	if plainKey.MatchString(key) {
		return at + "." + key
	}
	return at + "[" + strconv.Quote(key) + "]"
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
