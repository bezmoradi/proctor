package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/bezmoradi/proctor/protocol"
	"github.com/bezmoradi/proctor/protocol/jsonpath"
)

const exitCodePath = "$.exit_code"

var (
	refRE     = regexp.MustCompile(`\$\{([^}]*)\}`)
	varName   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)
	savedName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

const runIDRef = "run.id"

type validation struct {
	r protocol.ValidateResult
}

func (v *validation) add(rule, path, format string, args ...any) {
	v.r.Problems = append(v.r.Problems, protocol.Problem{Rule: rule, Message: fmt.Sprintf(format, args...), Path: path})
}

func (d *driver) Validate(ctx context.Context, p protocol.ValidateParams) (protocol.ValidateResult, error) {
	v := &validation{r: protocol.ValidateResult{Kind: protocol.KindObserve, Problems: []protocol.Problem{}, Refs: []string{}}}
	if p.Op != opRun {
		v.add("EXE-01", "$.do", "the cli driver has no operation %q; it has only %s", p.Op, opRun)
		return v.r, nil
	}
	args, isObject := p.Args.(map[string]any)
	if !isObject {
		v.add(argsRule, "$.with", "with must be an object with command, and optionally args and stdin")
		return v.r, nil
	}
	name, isText := args["command"].(string)
	c := d.commands[name]
	switch {
	case !isText:
		v.add(argsRule, "$.with.command", "command must be the name of a command the policy declares, not %s", show(args["command"]))
		return v.r, nil
	case strings.Contains(name, "${"):
		v.add(argsRule, "$.with.command", "command %q must be written as declared; it can't hold ${...}", name)
		return v.r, nil
	case c == nil:
		v.add(argsRule, "$.with.command", "command %q isn't declared in the cli driver's policy (it declares %s); only declared commands run", name, strings.Join(sortedKeys(d.commands), ", "))
		return v.r, nil
	}
	v.r.Kind = c.Kind
	v.r.Uses = []string{entryKey(name)}
	extra := v.checkExtra(args, c)
	v.checkStdin(args)
	v.r.Refs = refNames(args)
	v.r.Targets = append([]string(nil), c.reported()...)
	if c.Kind == protocol.KindAct {
		v.r.Destructive = c.Destructive
		v.r.FixedTarget = !runScoped(v.r.Refs)
		if c.Destructive {
			v.r.ExactTargets = exactTargets(c, extra)
		}
	} else {
		v.r.ExactTargets = exactTargets(c, extra)
		if p.Setup {
			v.add("FIX-03", "$.do", "command %s observes, so it's a verification step; setup goes through an act command or the application", name)
		}
	}
	v.checkExpect(p, c)
	return v.r, nil
}

func exactTargets(c *command, extra []string) []string {
	out := make([]string, 0, len(c.reported()))
	for _, t := range c.reported() {
		if len(extra) == 0 {
			out = append(out, t)
			continue
		}
		out = append(out, t+"/"+strings.Join(extra, "/"))
	}
	return out
}

func (v *validation) checkExtra(args map[string]any, c *command) []string {
	raw, has := args["args"]
	if !has {
		return nil
	}
	list, isList := raw.([]any)
	switch {
	case !isList:
		v.add(argsRule, "$.with.args", "args must be a list of texts, not %s", show(raw))
		return nil
	case c.ExtraArgs == 0:
		v.add(argsRule, "$.with.args", "command %s takes no arguments from a step (extra_args is 0); its fixed args are %s", c.Name, show(c.Args))
		return nil
	case len(list) > c.ExtraArgs:
		v.add(argsRule, "$.with.args", "command %s takes at most %d arguments from a step (extra_args), not %d", c.Name, c.ExtraArgs, len(list))
		return nil
	}
	var out []string
	for i, e := range list {
		at := fmt.Sprintf("$.with.args[%d]", i)
		a, isText := e.(string)
		if !isText {
			v.add(argsRule, at, "an argument must be text, not %s", show(e))
			continue
		}
		if strings.ContainsRune(a, 0) || hasControl(a) {
			v.add(argsRule, at, "an argument can't hold a NUL, a line break or another control character")
		}
		v.refs(at, a)
		out = append(out, a)
	}
	return out
}

func (v *validation) checkStdin(args map[string]any) {
	raw, has := args["stdin"]
	if !has {
		return
	}
	s, isText := raw.(string)
	switch {
	case !isText:
		v.add(argsRule, "$.with.stdin", "stdin must be text, not %s", show(raw))
	case len(s) > stdinLimit:
		v.add(argsRule, "$.with.stdin", "stdin holds %d bytes; at most %d are allowed", len(s), stdinLimit)
	case strings.ContainsRune(s, 0):
		v.add(argsRule, "$.with.stdin", "stdin can't hold a NUL character")
	default:
		v.refs("$.with.stdin", s)
	}
}

func (v *validation) refs(at, s string) {
	matches := refRE.FindAllStringSubmatch(s, -1)
	if strings.Count(s, "${") != len(matches) {
		v.add(argsRule, at, "unclosed ${ in %q", s)
		return
	}
	for _, m := range matches {
		n := m[1]
		switch {
		case strings.HasPrefix(n, "secret."):
			v.add(argsRule, at, "${%s} can't be passed to a command; give it a secret through env and the binding's <NAME>_env", n)
		case strings.HasPrefix(n, "fill("):
			v.add(argsRule, at, "${%s} is only for an http body or headers", n)
		case n != runIDRef && !varName.MatchString(n):
			v.add(argsRule, at, "unsupported reference ${%s}: supported are ${run.id}, ${<identity>.<value>} and ${name} for values saved by earlier steps", n)
		}
	}
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

func runScoped(names []string) bool {
	for _, n := range names {
		if n == runIDRef || savedName.MatchString(n) {
			return true
		}
	}
	return false
}

func (v *validation) checkExpect(p protocol.ValidateParams, c *command) {
	no := false
	v.r.StateCheck = &no
	ex, _ := p.Expect.(map[string]any)
	equal, _ := ex["equal"].(map[string]any)
	var code any
	has := false
	others := 0
	for k, val := range equal {
		if canonical(k) == exitCodePath {
			code, has = val, true
		}
	}
	for matcher, raw := range ex {
		for _, path := range claimPaths(raw) {
			if matcher == "equal" && canonical(path) == exitCodePath {
				continue
			}
			others++
		}
	}
	n, isNumber := code.(json.Number)
	value, err := n.Int64()
	switch {
	case !has:
		v.add(expectRule, "$.expect.equal", "expect.equal[%q] is required: a cli step must say which exit code it expects", exitCodePath)
		return
	case !isNumber || err != nil || value < -1 || value > 255:
		v.add(expectRule, "$.expect.equal", "%s must be an exit code, 0 to 255 (or -1 for a command a signal stopped), not %s", exitCodePath, show(code))
		return
	}
	yes := true
	v.r.StateCheck = &yes
	if c.Kind == protocol.KindObserve && !p.Setup && others == 0 {
		v.add("EXE-03", "$.expect", "an observe command must assert something besides its exit code, such as $.stdout_json or $.stdout_text, since a command can exit as expected without reading anything")
	}
}

func claimPaths(raw any) []string {
	switch x := raw.(type) {
	case map[string]any:
		return sortedKeys(x)
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{x}
	}
	return nil
}

func canonical(p string) string {
	c, err := jsonpath.Canonical(p)
	if err != nil {
		return p
	}
	return c
}
