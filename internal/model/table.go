package model

import (
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Table struct {
	ID           string            `yaml:"id"`
	Name         string            `yaml:"name"`
	Setup        []Step            `yaml:"setup"`
	SetupFrom    string            `yaml:"setup_from"`
	Do           string            `yaml:"do"`
	Identity     string            `yaml:"identity"`
	Variant      string            `yaml:"variant"`
	With         yaml.Node         `yaml:"with,omitempty"`
	Expect       yaml.Node         `yaml:"expect"`
	Columns      []string          `yaml:"columns"`
	Rows         []yaml.Node       `yaml:"rows"`
	NoStateCheck string            `yaml:"no_state_check"`
	Waive        map[string]string `yaml:"waive"`
	ExpectLogs   []LogExpectation  `yaml:"expect_logs,omitempty"`
}

type TableCase struct {
	Table     string
	Group     string
	SetupFrom string
	Columns   []string
	Cells     []string
	First     bool
	Canon     []byte
}

var (
	rowRef     = regexp.MustCompile(`\$\{row\.([^}]*)\}`)
	wholeRef   = regexp.MustCompile(`^\$\{row\.([^}]*)\}$`)
	columnName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
)

func (s *Spec) ExpandTables(decode func([]byte, any) error) []Problem {
	var probs []Problem
	add := func(rule, format string, args ...any) {
		probs = append(probs, Problem{Rule: rule, Message: fmt.Sprintf(format, args...)})
	}
	group := map[string]string{}
	setup := map[string][]Step{}
	firstSeen := map[string]bool{}
	for ti := range s.Tables {
		t := &s.Tables[ti]
		where := fmt.Sprintf("tables[%d]", ti)
		if t.ID != "" {
			where = "table " + t.ID
		}
		g, st := t.ID, t.Setup
		if t.SetupFrom != "" {
			if len(t.Setup) > 0 {
				add("TBL-04", "%s: has both setup and setup_from; use one", where)
			}
			if og, ok := group[t.SetupFrom]; ok {
				g, st = og, setup[t.SetupFrom]
			} else {
				add("TBL-04", "%s: setup_from %q isn't a table defined earlier in this specification", where, t.SetupFrom)
			}
		}
		if t.ID != "" {
			if _, dup := group[t.ID]; dup {
				add("INT-02", "%s: table id %q is used twice in this specification", where, t.ID)
			}
			group[t.ID], setup[t.ID] = g, st
		}
		if strings.TrimSpace(t.Name) == "" {
			add("EXE-01", "%s: name is required", where)
		}
		if strings.TrimSpace(t.Do) == "" {
			add("EXE-01", "%s: do is required", where)
			continue
		}
		if rowRef.MatchString(t.Do) {
			add("TBL-05", "%s: ${row...} can't be used in do", where)
			continue
		}
		if t.Expect.Kind != yaml.MappingNode {
			add("EXE-01", "%s: expect is required, as a mapping", where)
			continue
		}
		if len(t.Columns) == 0 {
			add("TBL-01", "%s: columns are required; the first column is the case id", where)
			continue
		}
		colIndex := map[string]int{}
		for i, c := range t.Columns {
			if !columnName.MatchString(c) {
				add("TBL-01", "%s: invalid column name %q (lower case letters, digits and _)", where, c)
			}
			if _, dup := colIndex[c]; dup {
				add("TBL-01", "%s: column %q is listed twice", where, c)
			}
			colIndex[c] = i
		}
		used := map[string]bool{}
		bad := false
		texts := []*yaml.Node{{Kind: yaml.ScalarNode, Value: t.Identity}, {Kind: yaml.ScalarNode, Value: t.Variant}}
		for _, n := range append(texts, &t.With, &t.Expect) {
			walkScalars(n, func(v *yaml.Node, isKey bool) {
				for _, m := range rowRef.FindAllStringSubmatch(v.Value, -1) {
					if isKey {
						add("TBL-05", "%s: ${row.%s} can't be used in a key", where, m[1])
						bad = true
						continue
					}
					if i, ok := colIndex[m[1]]; !ok || i == 0 {
						if ok {
							add("TBL-05", "%s: ${row.%s} is the case id column; it can't be used in the step", where, m[1])
						} else {
							add("TBL-05", "%s: ${row.%s} isn't one of the columns", where, m[1])
						}
						bad = true
						continue
					}
					used[m[1]] = true
				}
			})
		}
		for _, c := range t.Columns[1:] {
			if !used[c] {
				add("TBL-01", "%s: column %q isn't used by identity, variant, with or expect", where, c)
				bad = true
			}
		}
		if len(t.Rows) == 0 {
			add("TBL-01", "%s: at least one row is required", where)
			continue
		}
		if bad {
			continue
		}
		base := *t
		base.Rows = nil
		for ri := range t.Rows {
			row := &t.Rows[ri]
			rw := fmt.Sprintf("%s rows[%d]", where, ri)
			if row.Kind != yaml.SequenceNode {
				add("TBL-03", "%s: a row is a list of cells, one per column", rw)
				continue
			}
			if len(row.Content) != len(t.Columns) {
				add("TBL-03", "%s: has %d cells, but the table has %d columns", rw, len(row.Content), len(t.Columns))
				continue
			}
			idCell := row.Content[0]
			if idCell.Kind != yaml.ScalarNode || idCell.Value == "" {
				add("TBL-01", "%s: the first cell must be the case id", rw)
				continue
			}
			caseID := idCell.Value
			cells := map[string]*yaml.Node{}
			display := make([]string, len(row.Content))
			ok := true
			for i, c := range row.Content {
				cells[t.Columns[i]] = c
				if c.Kind == yaml.ScalarNode {
					display[i] = c.Value
				} else {
					b, err := yaml.Marshal(flow(c))
					if err != nil {
						ok = false
					}
					display[i] = strings.TrimSpace(string(b))
				}
				if rowRef.MatchString(display[i]) {
					add("TBL-05", "%s: a cell can't refer to another cell (%s)", rw, display[i])
					ok = false
				}
			}
			if !ok {
				continue
			}
			step := Step{ID: "case", Do: t.Do, NoStateCheck: t.NoStateCheck, Waive: t.Waive, Expect: &Expect{}}
			var err error
			if step.Identity, err = substituteText(t.Identity, cells); err != nil {
				add("TBL-05", "case %s: identity: %v", caseID, err)
				continue
			}
			if step.Variant, err = substituteText(t.Variant, cells); err != nil {
				add("TBL-05", "case %s: variant: %v", caseID, err)
				continue
			}
			if t.With.Kind != 0 {
				with, err := substitute(&t.With, cells)
				if err != nil {
					add("TBL-05", "case %s: with: %v", caseID, err)
					continue
				}
				if err := decodeNode(with, &step.With, decode); err != nil {
					add("INT-15", "case %s: with: %v", caseID, err)
					continue
				}
			}
			exp, err := substitute(&t.Expect, cells)
			if err != nil {
				add("TBL-05", "case %s: expect: %v", caseID, err)
				continue
			}
			if err := decodeNode(exp, step.Expect, decode); err != nil {
				add("INT-15", "case %s: expect: %v", caseID, err)
				continue
			}
			canon, err := yaml.Marshal(struct {
				Table Table      `yaml:"table"`
				Setup []Step     `yaml:"setup"`
				Row   *yaml.Node `yaml:"row"`
			}{cleanTable(base), st, clean(row)})
			if err != nil {
				add("INT-15", "case %s: %v", caseID, err)
				continue
			}
			s.Scenarios = append(s.Scenarios, Scenario{
				ID:         caseID,
				Name:       t.Name,
				Setup:      st,
				Steps:      []Step{step},
				ExpectLogs: t.ExpectLogs,
				Case: &TableCase{Table: t.ID, Group: g, SetupFrom: t.SetupFrom, Columns: t.Columns, Cells: display,
					First: !firstSeen[g], Canon: canon},
			})
			firstSeen[g] = true
		}
	}
	return probs
}

func walkScalars(n *yaml.Node, f func(*yaml.Node, bool)) {
	switch n.Kind {
	case yaml.ScalarNode:
		f(n, false)
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			f(n.Content[i], true)
			walkScalars(n.Content[i+1], f)
		}
	default:
		for _, c := range n.Content {
			walkScalars(c, f)
		}
	}
}

func substitute(n *yaml.Node, cells map[string]*yaml.Node) (*yaml.Node, error) {
	if n.Kind == yaml.ScalarNode {
		if m := wholeRef.FindStringSubmatch(n.Value); m != nil {
			return clean(cells[m[1]]), nil
		}
		var err error
		v := rowRef.ReplaceAllStringFunc(n.Value, func(ref string) string {
			c := cells[rowRef.FindStringSubmatch(ref)[1]]
			if c.Kind != yaml.ScalarNode {
				err = fmt.Errorf("%s is part of a longer text, so its cell must be a single value, not a list or mapping", ref)
				return ref
			}
			return c.Value
		})
		if err != nil {
			return nil, err
		}
		out := clean(n)
		if v != n.Value {
			out.Value, out.Tag, out.Style = v, "!!str", 0
		}
		return out, nil
	}
	out := &yaml.Node{Kind: n.Kind, Tag: n.Tag, Style: n.Style}
	for i, c := range n.Content {
		if n.Kind == yaml.MappingNode && i%2 == 0 {
			out.Content = append(out.Content, clean(c))
			continue
		}
		sc, err := substitute(c, cells)
		if err != nil {
			return nil, err
		}
		out.Content = append(out.Content, sc)
	}
	return out, nil
}

func substituteText(s string, cells map[string]*yaml.Node) (string, error) {
	out, err := substitute(&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}, cells)
	if err != nil {
		return "", err
	}
	if out.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("%s must be a single value, not a list or mapping", s)
	}
	return out.Value, nil
}

func clean(n *yaml.Node) *yaml.Node {
	out := &yaml.Node{Kind: n.Kind, Tag: n.Tag, Value: n.Value, Style: n.Style &^ yaml.FlowStyle}
	for _, c := range n.Content {
		out.Content = append(out.Content, clean(c))
	}
	return out
}

func flow(n *yaml.Node) *yaml.Node {
	out := clean(n)
	var set func(*yaml.Node)
	set = func(m *yaml.Node) {
		if m.Kind == yaml.MappingNode || m.Kind == yaml.SequenceNode {
			m.Style |= yaml.FlowStyle
		}
		for _, c := range m.Content {
			set(c)
		}
	}
	set(out)
	return out
}

func cleanTable(t Table) Table {
	if t.With.Kind != 0 {
		t.With = *clean(&t.With)
	}
	t.Expect = *clean(&t.Expect)
	return t
}

func decodeNode(n *yaml.Node, v any, decode func([]byte, any) error) error {
	b, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	return decode(b, v)
}
