// Package strictyaml decodes protected YAML (INT-15 in prd.md).
//
// It accepts exactly one document and rejects the YAML features that let a
// reviewer see something other than what runs: duplicate keys, anchors,
// aliases, merge keys, explicit tags, non-scalar keys and keys the target
// type doesn't define.
package strictyaml

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"
)

// Error describes why a document was rejected. Line is 0 when the parser
// gave no position.
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

// Decode checks data and decodes it into v, which must be a pointer.
func Decode(data []byte, v any) error {
	if _, err := Parse(data); err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return &Error{Msg: err.Error()}
	}
	return nil
}

// Parse checks data and returns its document node. Callers that need the
// node tree, such as the semantic diff, use it directly.
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
		return &Error{Line: n.Line, Msg: fmt.Sprintf("anchor &%s is not allowed", n.Anchor)}
	}
	if n.Kind == yaml.AliasNode {
		return &Error{Line: n.Line, Msg: "alias is not allowed"}
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
