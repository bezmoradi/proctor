package model

import (
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"
)

type Waiver struct {
	Reason string
	Rows   []string
	Listed bool
}

func (w Waiver) Covers(caseID string) bool {
	return !w.Listed || slices.Contains(w.Rows, caseID)
}

func (w *Waiver) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		var reason string
		if err := n.Decode(&reason); err != nil {
			return err
		}
		*w = Waiver{Reason: reason}
		return nil
	case yaml.MappingNode:
	default:
		return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: a waiver is its reason, or { rows: [<case ids>], reason: <text> } on a table's step, not %s", n.Line, nodeKind(n))}}
	}
	var bad []string
	out := Waiver{Listed: true}
	hasRows := false
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		switch k.Value {
		case "reason":
			if v.Kind != yaml.ScalarNode {
				bad = append(bad, fmt.Sprintf("line %d: reason must be text, not %s", v.Line, nodeKind(v)))
				continue
			}
			if err := v.Decode(&out.Reason); err != nil {
				return err
			}
		case "rows":
			hasRows = true
			if v.Kind != yaml.SequenceNode {
				bad = append(bad, fmt.Sprintf("line %d: rows must be a list of the table's case ids, not %s", v.Line, nodeKind(v)))
				continue
			}
			out.Rows = []string{}
			for _, item := range v.Content {
				if item.Kind != yaml.ScalarNode || item.ShortTag() == "!!null" {
					bad = append(bad, fmt.Sprintf("line %d: each entry of rows must be a case id, not %s", item.Line, nodeKind(item)))
					continue
				}
				out.Rows = append(out.Rows, item.Value)
			}
		default:
			bad = append(bad, fmt.Sprintf("line %d: unknown key %q in a waiver (allowed: rows, reason)", k.Line, k.Value))
		}
	}
	if !hasRows && len(bad) == 0 {
		bad = append(bad, fmt.Sprintf("line %d: a waiver written as a mapping names its rows; write the reason alone for a waiver that applies to the step in every row", n.Line))
	}
	if len(bad) > 0 {
		return &yaml.TypeError{Errors: bad}
	}
	*w = out
	return nil
}

func (w Waiver) MarshalYAML() (any, error) {
	if !w.Listed {
		return w.Reason, nil
	}
	return struct {
		Rows   []string `yaml:"rows"`
		Reason string   `yaml:"reason"`
	}{w.Rows, w.Reason}, nil
}
