package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/bezmoradi/proctor/protocol"
)

var refRE = regexp.MustCompile(`\$\{([^}]*)\}`)

func (d *driver) Validate(ctx context.Context, p protocol.ValidateParams) (protocol.ValidateResult, error) {
	r := protocol.ValidateResult{Kind: protocol.KindObserve, Problems: []protocol.Problem{}}
	add := func(rule, path, format string, args ...any) {
		r.Problems = append(r.Problems, protocol.Problem{Rule: rule, Message: fmt.Sprintf(format, args...), Path: path})
	}
	switch p.Op {
	case opGet, opQuery:
	case opPut:
		r.Kind = protocol.KindAct
	default:
		add("EXE-01", "$.do", "the dynamodb driver has no operation %q; its operations are get, query and put", p.Op)
		return r, nil
	}
	r.Refs = refs(p.Args)
	args, ok := p.Args.(map[string]any)
	if !ok {
		add("EXE-01", "$.with", "with must be an object")
		return r, nil
	}
	switch t := args["table"].(type) {
	case string:
		if strings.Contains(t, "${") {
			add("DDB-01", "$.with.table", "table must name a table alias from the driver's policy literally, not through ${...}")
		} else if _, declared := d.tables[t]; !declared {
			add("DDB-01", "$.with.table", "table %q is not an alias in the dynamodb driver's policy in proctor.yaml (aliases: %s)", t, strings.Join(d.aliases, ", "))
		} else {
			r.Targets = []string{t}
		}
	default:
		add("DDB-01", "$.with.table", "table must name a table alias from the driver's policy")
	}
	field := "key"
	if p.Op == opPut {
		field = "item"
	}
	m, isObject := args[field].(map[string]any)
	switch {
	case !isObject || len(m) == 0:
		add("DDB-02", "$.with."+field, "%s must be an object with at least one attribute", field)
	case field == "key" && len(m) > 2:
		add("DDB-02", "$.with.key", "key names %d attributes; a key has a partition key and at most a sort key", len(m))
	}
	for name := range m {
		if name == "" {
			add("DDB-02", "$.with."+field, "%s has an attribute with an empty name", field)
		}
	}
	if _, has := args["index"]; has && p.Op == opGet {
		add("DDB-03", "$.with.index", "get reads a table by its primary key and can't use an index; use query to read an index")
	}
	if idx, has := args["index"].(string); has && p.Op == opQuery && strings.TrimSpace(idx) == "" {
		add("DDB-03", "$.with.index", "index must name an index of the table")
	}
	return r, nil
}

func refs(v any) []string {
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			for _, m := range refRE.FindAllStringSubmatch(x, -1) {
				seen[m[1]] = true
			}
		case map[string]any:
			for k, e := range x {
				walk(k)
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return sortedKeys(seen)
}
