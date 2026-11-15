package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"proctor/protocol"
)

const statusPath = "$.status"

type expectation struct {
	Equal     map[string]any            `json:"equal"`
	Present   []string                  `json:"present"`
	Absent    []string                  `json:"absent"`
	Keys      map[string][]string       `json:"keys"`
	Length    map[string]any            `json:"length"`
	Unordered map[string][]any          `json:"unordered"`
	Any       map[string]map[string]any `json:"any"`
	None      map[string]map[string]any `json:"none"`
}

func (e expectation) count() int {
	return len(e.Equal) + len(e.Present) + len(e.Absent) + len(e.Keys) + len(e.Length) + len(e.Unordered) + len(e.Any) + len(e.None)
}

func (e expectation) paths() []string {
	var out []string
	out = append(out, sortedKeys(e.Equal)...)
	out = append(out, e.Present...)
	out = append(out, e.Absent...)
	out = append(out, sortedKeys(e.Keys)...)
	out = append(out, sortedKeys(e.Length)...)
	out = append(out, sortedKeys(e.Unordered)...)
	out = append(out, sortedKeys(e.Any)...)
	out = append(out, sortedKeys(e.None)...)
	return out
}

type validation struct {
	r protocol.ValidateResult
}

func (v *validation) add(rule, path, format string, args ...any) {
	v.r.Problems = append(v.r.Problems, protocol.Problem{Rule: rule, Message: fmt.Sprintf(format, args...), Path: path})
}

func (d *driver) Validate(ctx context.Context, p protocol.ValidateParams) (protocol.ValidateResult, error) {
	v := &validation{r: protocol.ValidateResult{Kind: protocol.KindAct, Problems: []protocol.Problem{}, Refs: []string{}}}
	if p.Op != opRequest {
		v.add("EXE-01", "$.do", "the http driver has no operation %q; its only operation is %s", p.Op, opRequest)
		return v.r, nil
	}
	args, isObject := p.Args.(map[string]any)
	if !isObject {
		v.add("EXE-01", "$.with", "with must be an object with method and path")
		args = map[string]any{}
	}
	v.r.Refs = uniqueSorted(refNames(args))

	method, _ := args["method"].(string)
	if knownMethod(method) {
		if observes(method) {
			v.r.Kind = protocol.KindObserve
		}
		v.r.Destructive = destructive(method)
	} else {
		v.add("EXE-01", "$.with.method", "unsupported method %s", show(args["method"]))
	}

	if path, ok := args["path"].(string); ok {
		v.r.FixedTarget = !strings.Contains(path, "${")
		if err := checkRequestPath(path); err != nil {
			v.add("HTTP-05", "$.with.path", "%v", err)
		}
	} else {
		v.add("HTTP-05", "$.with.path", "path must be text starting with /, got %s", show(args["path"]))
	}

	query := v.object(args, "query")
	headers := v.object(args, "headers")
	v.checkHeaders(headers)

	body, hasBody := args["body"]
	if method == "HEAD" && hasBody && body != nil {
		v.add("EXE-01", "$.with.body", "a HEAD request can't send a body")
	}

	v.onlyInBodyOrHeaders("$.with.method", args["method"])
	v.onlyInBodyOrHeaders("$.with.path", args["path"])
	v.onlyInBodyOrHeaders("$.with.query", args["query"])

	var ex expectation
	if p.Expect != nil {
		if err := protocol.Decode(p.Expect, &ex); err != nil {
			v.add("EXE-03", "$.expect", "expect can't be read: %v", err)
			ex = expectation{}
		}
	}
	v.onlyInBodyOrHeaders("$.expect", []any{ex.Equal, ex.Length, ex.Unordered, ex.Any, ex.None})
	v.checkStatus(ex, p)
	if method == "HEAD" {
		v.checkHead(ex)
	}
	v.checkHeaderCase(ex)
	v.checkConflicts(ex)
	v.checkUnordered(ex)
	v.checkLength(ex)
	if method == "GET" && len(ex.Length) > 0 && len(query) == 0 {
		v.add("HTTP-09", "$.expect.length", "asserts a list length on a request with no query parameters; the list may include records the scenario didn't create (filter the request, or waive HTTP-09 with the reason it can't)")
	}
	return v.r, nil
}

func (v *validation) object(args map[string]any, key string) map[string]any {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		v.add("EXE-01", "$.with."+key, "%s must be an object of names and values, not %s", key, show(raw))
		return nil
	}
	return m
}

func (v *validation) checkHeaders(headers map[string]any) {
	seen := map[string]string{}
	for _, name := range sortedKeys(headers) {
		at := "$.with.headers"
		if rule, why := forbiddenHeader(name); rule != "" {
			v.add(rule, at, "header %q can't be set by a specification: %s", name, why)
			continue
		}
		if !validHeaderName(name) {
			v.add("EXE-01", at, "header name %q is not a valid HTTP header name", name)
			continue
		}
		lower := strings.ToLower(name)
		if prev, dup := seen[lower]; dup {
			v.add("EXE-01", at, "headers %q and %q name the same header (header names ignore case)", prev, name)
		}
		seen[lower] = name
		if s, ok := headers[name].(string); ok && strings.ContainsAny(s, "\r\n") {
			v.add("EXE-01", at, "header %s contains a line break", name)
		}
	}
}

func (v *validation) onlyInBodyOrHeaders(at string, value any) {
	reported := map[string]bool{}
	for _, n := range refNames(value) {
		if (isFill(n) || isSecret(n)) && !reported[n] {
			reported[n] = true
			v.add("HTTP-02", at, "${%s} may be used only in a request body or request headers", n)
		}
	}
}

func httpStatus(x any) (int, bool) {
	n, ok := x.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(n.String())
	if err != nil {
		return 0, false
	}
	return i, i >= 100 && i <= 599
}

func (v *validation) checkStatus(ex expectation, p protocol.ValidateParams) {
	raw, has := ex.Equal[statusPath]
	code, valid := httpStatus(raw)
	switch {
	case !has:
		v.add("EXE-03", "$.expect.equal", "expect.equal[%q] is required", statusPath)
	case !valid:
		v.add("EXE-03", "$.expect.equal", "status %s is not an HTTP status", show(raw))
	}
	if p.Variant != "" {
		switch {
		case p.Setup:
			v.add("AUTH-06", "$.variant", "a credential variant is for negative tests, not setup")
		case !has:
			v.add("AUTH-06", "$.expect.equal", "a request with credential variant %q must expect 401 or 403 in expect.equal[%q]", p.Variant, statusPath)
		case !valid || (code != 401 && code != 403):
			v.add("AUTH-06", "$.expect.equal", "a request with credential variant %q must expect 401 or 403, not %s", p.Variant, show(raw))
		}
	}
	if !p.Setup && valid && code >= 200 && code < 300 && ex.count() == 1 {
		v.add("EXE-03", "$.expect", "a verification step expecting %d must also assert a body field or header", code)
	}
}

func under(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+".") || strings.HasPrefix(p, root+"[")
}

func (v *validation) checkHead(ex expectation) {
	for _, p := range ex.paths() {
		if under(p, "$.body") || under(p, "$.body_text") || under(p, "$.body_invalid") {
			v.add("EXE-03", "$.expect", "a HEAD response has no body, so it can only assert status and headers")
			return
		}
	}
}

var headerRoots = []string{"$.headers", "$.header_lists"}

func headerPath(p string) (name, rest string, ok bool) {
	for _, root := range headerRoots {
		switch {
		case p == root:
			return "", "", true
		case strings.HasPrefix(p, root+"["):
			return "", p[len(root):], true
		case !strings.HasPrefix(p, root+"."):
			continue
		}
		tail := p[len(root)+1:]
		if i := strings.IndexAny(tail, ".["); i >= 0 {
			return tail[:i], tail[i:], true
		}
		return tail, "", true
	}
	return "", "", false
}

func headerRoot(p string) string {
	for _, root := range headerRoots {
		if under(p, root) {
			return root
		}
	}
	return ""
}

func (v *validation) checkHeaderCase(ex expectation) {
	seen := map[string]bool{}
	for _, p := range ex.paths() {
		name, _, ok := headerPath(p)
		if !ok || name == "" || seen[p] || name == strings.ToLower(name) {
			continue
		}
		seen[p] = true
		v.add("HTTP-08", "$.expect", "%s names header %q with capital letters; the driver reports header names in lower case, so write %s.%s", p, name, headerRoot(p), strings.ToLower(name))
	}
}

func headerKey(p string) (string, bool) {
	name, rest, ok := headerPath(p)
	if ok && name != "" && rest == "" {
		return "header " + strings.ToLower(name), true
	}
	return p, false
}

func (v *validation) checkConflicts(ex expectation) {
	asserted := map[string]string{}
	exact := map[string]string{}
	for _, k := range sortedKeys(ex.Equal) {
		key, _ := headerKey(k)
		asserted[key] = "equal"
		exact[k] = "equal"
	}
	for _, list := range []struct {
		name  string
		paths []string
	}{{"present", ex.Present}, {"absent", ex.Absent}} {
		for _, k := range list.paths {
			key, isHeader := headerKey(k)
			prev, dup := asserted[key]
			if same, ok := exact[k]; ok {
				prev, dup = same, true
			} else if dup && (prev == "absent") == (list.name == "absent") {
				dup = false
			}
			if dup {
				if isHeader && prev == "equal" && list.name == "absent" {
					name, _, _ := headerPath(k)
					v.add("HTTP-08", "$.expect."+list.name, "header %s is both expected and absent", name)
				} else {
					v.add("HTTP-08", "$.expect."+list.name, "%s is asserted by both %s and %s", k, prev, list.name)
				}
			}
			asserted[key] = list.name
			exact[k] = list.name
		}
	}
	for _, k := range ex.Absent {
		if name, rest, ok := headerPath(k); ok && rest == "" && !validHeaderName(name) {
			v.add("HTTP-08", "$.expect.absent", "absent: invalid header name %q", name)
		}
	}
	for _, k := range sortedKeys(ex.Keys) {
		seen := map[string]bool{}
		for _, name := range ex.Keys[k] {
			if seen[name] {
				v.add("HTTP-08", "$.expect.keys", "keys of %s lists %q twice", k, name)
			}
			seen[name] = true
		}
		if key, _ := headerKey(k); asserted[key] == "absent" {
			v.add("HTTP-08", "$.expect.keys", "%s is both absent and given keys", k)
		}
	}
	for _, k := range sortedKeys(ex.Length) {
		if key, _ := headerKey(k); asserted[key] == "absent" {
			v.add("HTTP-08", "$.expect.length", "%s is both absent and given a length", k)
		}
	}
}

func (v *validation) checkUnordered(ex expectation) {
	equal := map[string]bool{}
	for k := range ex.Equal {
		if key, ok := headerKey(k); ok {
			equal[key] = true
		}
	}
	absent := map[string]bool{}
	for _, k := range ex.Absent {
		if key, ok := headerKey(k); ok {
			absent[key] = true
		}
	}
	at := "$.expect.unordered"
	for _, k := range sortedKeys(ex.Unordered) {
		name, rest, ok := headerPath(k)
		if !ok {
			continue
		}
		if rest != "" || !validHeaderName(name) {
			v.add("HTTP-08", at, "unordered: invalid header name %q (name one header as %s.<name>)", strings.TrimPrefix(strings.TrimPrefix(k, headerRoot(k)), "."), headerRoot(k))
			continue
		}
		key, _ := headerKey(k)
		if equal[key] {
			v.add("HTTP-08", at, "header %s is in both equal and unordered", name)
		}
		if absent[key] {
			v.add("HTTP-08", at, "header %s is both in unordered and absent", name)
		}
		vals := ex.Unordered[k]
		if len(vals) == 0 {
			v.add("HTTP-08", at, "unordered %s needs at least one value", k)
		}
		seen := map[string]bool{}
		for _, raw := range vals {
			s, isText := raw.(string)
			switch {
			case !isText:
				v.add("HTTP-08", at, "unordered %s: value %s must be text", k, show(raw))
			case strings.TrimSpace(s) != s || s == "":
				v.add("HTTP-08", at, "unordered %s: value %q is empty or has spaces around it", k, s)
			case strings.ContainsAny(s, ",\r\n"):
				v.add("HTTP-08", at, "unordered %s: value %q can't contain a comma or line break; list each value separately", k, s)
			case seen[s]:
				v.add("HTTP-08", at, "unordered %s lists %q twice", k, s)
			}
			if isText {
				seen[s] = true
			}
		}
	}
}

func (v *validation) checkLength(ex expectation) {
	at := "$.expect.length"
	for _, k := range sortedKeys(ex.Length) {
		switch x := ex.Length[k].(type) {
		case json.Number:
			n, err := strconv.ParseInt(x.String(), 10, 64)
			switch {
			case err != nil:
				v.add("HTTP-09", at, "length of %s must be an integer or one ${name} reference", k)
			case n < 0:
				v.add("HTTP-09", at, "length of %s can't be negative", k)
			}
		case string:
			if !wholeRef.MatchString(x) || len(refRE.FindAllString(x, -1)) != 1 {
				v.add("HTTP-09", at, "length of %s must be an integer or one ${name} reference", k)
			}
		default:
			v.add("HTTP-09", at, "length of %s must be an integer or one ${name} reference", k)
		}
	}
}

func show(x any) string {
	if s, ok := x.(string); ok {
		return strconv.Quote(s)
	}
	if x == nil {
		return "nothing"
	}
	b, err := json.Marshal(x)
	if err != nil {
		return fmt.Sprintf("%v", x)
	}
	return string(b)
}
