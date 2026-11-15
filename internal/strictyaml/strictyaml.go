package strictyaml

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Error struct {
	Line int
	Msg  string
}

func (e *Error) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
	}
	return e.Msg
}

func Decode(data []byte, v any) error {
	if _, err := Parse(data); err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return &Error{Msg: explain(err, reflect.TypeOf(v))}
	}
	return nil
}

var (
	unknownField = regexp.MustCompile(`field (\S+) not found in type ([\w.\[\]*]+)`)
	badValue     = regexp.MustCompile("cannot unmarshal !!(\\w+)(?: `([^`]*)`)? into ([\\w.\\[\\]*]+)")
)

type schema struct {
	where map[string][]string
	keys  map[string][]string
	kinds map[string]string
}

func explain(err error, root reflect.Type) string {
	sc := schema{where: map[string][]string{}, keys: map[string][]string{}, kinds: map[string]string{}}
	sc.walk(root, "the top level", map[reflect.Type]bool{})
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "yaml: unmarshal errors:\n  ")
	msg = strings.ReplaceAll(msg, "\n  ", "; ")
	msg = unknownField.ReplaceAllStringFunc(msg, func(m string) string {
		g := unknownField.FindStringSubmatch(m)
		out := fmt.Sprintf("unknown key %q", g[1])
		if w := sc.where[g[2]]; len(w) > 0 {
			out += " in " + strings.Join(w, " or ")
		}
		if k := sc.keys[g[2]]; len(k) > 0 {
			out += " (allowed: " + strings.Join(k, ", ") + ")"
		}
		return out
	})
	msg = badValue.ReplaceAllStringFunc(msg, func(m string) string {
		g := badValue.FindStringSubmatch(m)
		want := sc.kinds[g[3]]
		if want == "" {
			want = "another kind of value"
		}
		got := map[string]string{"str": "text", "int": "a number", "float": "a number", "bool": "true or false", "seq": "a list", "map": "a mapping", "null": "null", "timestamp": "a date"}[g[1]]
		if got == "" {
			got = "!!" + g[1]
		}
		if g[2] != "" {
			got += " " + g[2]
		}
		return "expected " + want + ", got " + got
	})
	return msg
}

func (sc schema) walk(t reflect.Type, where string, seen map[reflect.Type]bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	name := t.String()
	if _, ok := sc.kinds[name]; !ok {
		sc.kinds[name] = kindName(t)
	}
	if t.Kind() == reflect.Struct && !contains(sc.where[name], where) {
		sc.where[name] = append(sc.where[name], where)
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		sc.walk(t.Elem(), where, seen)
	case reflect.Struct:
		if seen[t] {
			return
		}
		seen[t] = true
		if t.PkgPath() != "" && strings.HasPrefix(t.PkgPath(), "go.yaml.in/") {
			return
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
			if tag == "-" || !f.IsExported() {
				continue
			}
			if tag == "" {
				tag = strings.ToLower(f.Name)
			}
			sc.keys[name] = append(sc.keys[name], tag)
			sc.walk(f.Type, tag, seen)
		}
	}
}

func kindName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Struct, reflect.Map:
		return "a mapping"
	case reflect.Slice, reflect.Array:
		return "a list"
	case reflect.String:
		return "text"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "a whole number"
	case reflect.Float32, reflect.Float64:
		return "a number"
	}
	return "a value"
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func Parse(data []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, &Error{Msg: "empty document"}
		}
		return nil, &Error{Msg: err.Error()}
	}
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case err == nil:
		return nil, &Error{Line: extra.Line, Msg: "more than one YAML document"}
	case !errors.Is(err, io.EOF):
		return nil, &Error{Msg: err.Error()}
	}
	if err := check(&doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

func check(n *yaml.Node) error {
	if n.Anchor != "" {
		return &Error{Line: n.Line, Msg: fmt.Sprintf("anchor &%s is not allowed: one anchor edit could change many scenarios while the diff shows one line; write the repeated part out in full", n.Anchor)}
	}
	if n.Kind == yaml.AliasNode {
		return &Error{Line: n.Line, Msg: "alias is not allowed: one anchor edit could change many scenarios while the diff shows one line; write the repeated part out in full"}
	}
	if n.Style&yaml.TaggedStyle != 0 {
		return &Error{Line: n.Line, Msg: fmt.Sprintf("explicit tag %s is not allowed", n.Tag)}
	}
	if n.Kind == yaml.MappingNode {
		seen := make(map[string]int, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode {
				return &Error{Line: k.Line, Msg: "mapping keys must be scalars"}
			}
			if k.Tag == "!!merge" {
				return &Error{Line: k.Line, Msg: "merge key << is not allowed"}
			}
			if first, dup := seen[k.Value]; dup {
				return &Error{Line: k.Line, Msg: fmt.Sprintf("duplicate key %q (first at line %d)", k.Value, first)}
			}
			seen[k.Value] = k.Line
		}
	}
	for _, c := range n.Content {
		if err := check(c); err != nil {
			return err
		}
	}
	return nil
}
