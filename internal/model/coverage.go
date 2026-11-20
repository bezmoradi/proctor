package model

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	CoverageRule            = "TBL-07"
	DimensionsVersion       = 1
	MaxCoverageCombinations = 100000
	CoverageShown           = 50
)

var CoverageName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type Dimensions struct {
	Version    int                    `yaml:"version"`
	Dimensions map[string][]yaml.Node `yaml:"dimensions"`
	Sets       map[string]CoverageSet `yaml:"sets"`
}

type CoverageSet struct {
	Columns SetColumns          `yaml:"columns"`
	Except  []CoverageException `yaml:"except"`
}

type CoverageException struct {
	Cells  map[string]yaml.Node `yaml:"cells"`
	Reason string               `yaml:"reason"`
}

type SetColumns struct {
	Names     []string
	Dimension map[string]string
}

func (c *SetColumns) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: columns maps each table column to its dimension, such as { route: post_routes }, not %s", n.Line, nodeKind(n))}}
	}
	out := SetColumns{Dimension: map[string]string{}}
	var bad []string
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if v.Kind != yaml.ScalarNode || v.ShortTag() != "!!str" {
			bad = append(bad, fmt.Sprintf("line %d: column %q must name a dimension, not %s", v.Line, k.Value, nodeKind(v)))
			continue
		}
		out.Names = append(out.Names, k.Value)
		out.Dimension[k.Value] = v.Value
	}
	if len(bad) > 0 {
		return &yaml.TypeError{Errors: bad}
	}
	*c = out
	return nil
}

func (t *Table) CoverSet() (name string, given bool, err error) {
	switch {
	case t.Covers.Kind == 0:
		return "", false, nil
	case t.Covers.Kind != yaml.ScalarNode || t.Covers.ShortTag() != "!!str" || t.Covers.Value == "":
		return "", true, fmt.Errorf("covers must name a set in the dimensions file, not %s", nodeKind(&t.Covers))
	}
	return t.Covers.Value, true, nil
}

func CellKey(n *yaml.Node) (string, error) {
	switch {
	case n.Kind != yaml.ScalarNode:
		return "", fmt.Errorf("is %s, not a single value", nodeKind(n))
	case n.ShortTag() == "!!null":
		return "", fmt.Errorf("is null")
	}
	return n.ShortTag() + "\x00" + n.Value, nil
}

func ShowCell(key string) string {
	tag, value, _ := strings.Cut(key, "\x00")
	if tag == "!!str" {
		return strconv.Quote(value)
	}
	return value
}

func ShowTuple(columns []string, keys []string) string {
	parts := make([]string, len(columns))
	for i, c := range columns {
		parts[i] = c + "=" + ShowCell(keys[i])
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func TupleKey(keys []string) string {
	quoted := make([]string, len(keys))
	for i, k := range keys {
		quoted[i] = strconv.Quote(k)
	}
	return strings.Join(quoted, ",")
}

func (t *Table) StepColumns() map[string]bool {
	out := map[string]bool{}
	texts := []*yaml.Node{{Kind: yaml.ScalarNode, Value: t.Identity}, {Kind: yaml.ScalarNode, Value: t.Variant}}
	for _, n := range append(texts, &t.With) {
		walkScalars(n, func(v *yaml.Node, isKey bool) {
			if isKey {
				return
			}
			for _, m := range rowRef.FindAllStringSubmatch(v.Value, -1) {
				out[m[1]] = true
			}
		})
	}
	return out
}

func HoldsRowRef(s string) bool {
	return rowRef.MatchString(s)
}
