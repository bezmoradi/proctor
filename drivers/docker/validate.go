package docker

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/bezmoradi/proctor/protocol"
)

const (
	argsRule    = "DKR-03"
	maxLabels   = 20
	ruleUnknown = "EXE-01"
)

var (
	refRE    = regexp.MustCompile(`\$\{([^}]*)\}`)
	varName  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)
	fillRE   = regexp.MustCompile(`^fill\([0-9]+\)$`)
	secretRE = regexp.MustCompile(`^secret\.`)
)

type validation struct {
	r protocol.ValidateResult
}

func (v *validation) add(rule, path, format string, args ...any) {
	v.r.Problems = append(v.r.Problems, protocol.Problem{Rule: rule, Message: fmt.Sprintf(format, args...), Path: path})
}

func (d *Driver) Validate(ctx context.Context, p protocol.ValidateParams) (protocol.ValidateResult, error) {
	v := &validation{r: protocol.ValidateResult{Kind: protocol.KindObserve}}
	o, known := ops[p.Op]
	if !known {
		v.add(ruleUnknown, "$.do", "unknown operation %q; the docker driver has %s", p.Op, strings.Join(operations, ", "))
		return v.r, nil
	}
	if p.Setup {
		v.add("FIX-03", "$.do", "reading Docker is a verification step; setup goes through the application")
	}
	args, isObject := p.Args.(map[string]any)
	if o.list {
		v.r.Targets = []string{o.kind.plural}
	}
	switch {
	case p.Args == nil && o.list:
	case !isObject:
		if o.list {
			v.add(argsRule, "$.with", "with must be an object with optional labels and has_labels, or left out")
		} else {
			v.add(argsRule, "$.with", "with must be an object with name")
		}
	case o.list:
		v.checkListArgs(args)
	default:
		v.checkName(args)
		if name, ok := args["name"].(string); ok {
			v.r.Targets = []string{o.kind.single + "/" + refRE.ReplaceAllString(name, "{}")}
		}
	}
	v.r.Refs = refNames(p.Args)
	return v.r, nil
}

func (v *validation) checkName(args map[string]any) {
	raw, has := args["name"]
	name, isText := raw.(string)
	switch {
	case !has:
		v.add(argsRule, "$.with.name", "name is required: the exact name of the object, without Docker's leading /")
		return
	case !isText:
		v.add(argsRule, "$.with.name", "name must be text, not %s", show(raw))
		return
	}
	names, ok := v.refs("$.with.name", name)
	if !ok {
		return
	}
	if len(names) == 0 && !objectName.MatchString(name) {
		v.add(argsRule, "$.with.name", "name %q must match %s", name, objectName.String())
	}
}

func (v *validation) refs(at, s string) ([]string, bool) {
	names, err := references(s)
	if err != nil {
		v.add("HTTP-02", at, "%v", err)
		return nil, false
	}
	ok := true
	for _, n := range names {
		if fillRE.MatchString(n) || secretRE.MatchString(n) {
			v.add("HTTP-02", at, "${%s} may be used only in a request body or request headers, not in a Docker read", n)
			ok = false
		}
	}
	return names, ok
}

func (v *validation) checkListArgs(args map[string]any) {
	labels, hasLabels := args["labels"]
	lm, labelsAreObject := labels.(map[string]any)
	switch {
	case !hasLabels:
	case !labelsAreObject:
		v.add(argsRule, "$.with.labels", "labels must be an object of label keys and values, not %s", show(labels))
	case len(lm) == 0:
		v.add(argsRule, "$.with.labels", "labels is empty; leave it out to read every object in scope")
	case len(lm) > maxLabels:
		v.add(argsRule, "$.with.labels", "labels names %d keys; at most %d are allowed", len(lm), maxLabels)
	}
	for _, k := range sortedKeys(lm) {
		at := "$.with.labels"
		switch {
		case strings.Contains(k, "${"):
			v.add(argsRule, at, "label key %q must be literal; only values may hold ${...}", k)
			continue
		case !labelKey.MatchString(k):
			v.add(argsRule, at, "label key %q must match %s", k, labelKey.String())
			continue
		}
		val, isText := lm[k].(string)
		if !isText {
			v.add(argsRule, at, "the value of label %s must be text, not %s", k, show(lm[k]))
			continue
		}
		if hasControl(val) {
			v.add(argsRule, at, "the value of label %s can't hold a newline or another control character", k)
			continue
		}
		v.refs(at, val)
	}
	has, hasHas := args["has_labels"]
	list, isList := has.([]any)
	switch {
	case !hasHas:
	case !isList:
		v.add(argsRule, "$.with.has_labels", "has_labels must be a list of label keys, not %s", show(has))
	case len(list) == 0 || len(list) > maxLabels:
		v.add(argsRule, "$.with.has_labels", "has_labels must name 1 to %d label keys", maxLabels)
	}
	seen := map[string]bool{}
	for i, e := range list {
		at := fmt.Sprintf("$.with.has_labels[%d]", i)
		k, isText := e.(string)
		switch {
		case !isText:
			v.add(argsRule, at, "a label key must be text, not %s", show(e))
		case strings.Contains(k, "${"):
			v.add(argsRule, at, "label key %q must be literal", k)
		case !labelKey.MatchString(k):
			v.add(argsRule, at, "label key %q must match %s", k, labelKey.String())
		case seen[k]:
			v.add(argsRule, at, "label key %s is listed twice", k)
		case lm[k] != nil:
			v.add(argsRule, at, "label key %s is also in labels; name it once", k)
		}
		seen[k] = true
	}
	if len(lm)+len(list) > maxFilterTerms-1 {
		v.add(argsRule, "$.with", "labels and has_labels name %d keys together; at most %d are allowed", len(lm)+len(list), maxFilterTerms-1)
	}
}

func references(s string) ([]string, error) {
	var names []string
	for _, m := range refRE.FindAllStringSubmatch(s, -1) {
		if m[1] != "run.id" && !varName.MatchString(m[1]) && !fillRE.MatchString(m[1]) && !secretRE.MatchString(m[1]) {
			return nil, fmt.Errorf("unsupported reference ${%s}: supported are ${run.id}, ${<identity>.<value>} and ${name} for values saved by earlier steps", m[1])
		}
		names = append(names, m[1])
	}
	if strings.Count(s, "${") != len(names) {
		return nil, fmt.Errorf("unclosed ${ in %q", s)
	}
	return names, nil
}

func refNames(v any) []string {
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			for _, m := range refRE.FindAllStringSubmatch(x, -1) {
				seen[m[1]] = true
			}
		case map[string]any:
			for _, e := range x {
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
