package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"proctor/protocol"
	"proctor/protocol/jsonpath"
)

const statusPath = "$.status"

type expectation struct {
	Equal     map[string]any      `json:"equal"`
	Present   []string            `json:"present"`
	Absent    []string            `json:"absent"`
	Keys      map[string][]string `json:"keys"`
	Length    map[string]any      `json:"length"`
	Unordered map[string][]any    `json:"unordered"`
	Any       map[string]any      `json:"any"`
	None      map[string]any      `json:"none"`
	Contains  map[string]any      `json:"contains"`
	Count     map[string]any      `json:"count"`
	Matches   map[string]any      `json:"matches"`
	Excludes  map[string]any      `json:"excludes"`
	Differs   map[string]any      `json:"differs"`
}

func (e expectation) count() int {
	return len(e.Equal) + len(e.Present) + len(e.Absent) + len(e.Keys) + len(e.Length) + len(e.Unordered) + len(e.Any) + len(e.None) + len(e.Contains) + len(e.Count) + len(e.Matches) + len(e.Excludes) + len(e.Differs)
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
	out = append(out, sortedKeys(e.Contains)...)
	out = append(out, sortedKeys(e.Count)...)
	out = append(out, sortedKeys(e.Matches)...)
	out = append(out, sortedKeys(e.Excludes)...)
	out = append(out, sortedKeys(e.Differs)...)
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
	switch p.Op {
	case opRequest:
	case opOpen:
		return d.validateOpen(p), nil
	case opFrames:
		return d.validateFrames(p), nil
	default:
		v.add("EXE-01", "$.do", "the http driver has no operation %q; it has %s, %s and %s", p.Op, opRequest, opOpen, opFrames)
		return v.r, nil
	}
	args, isObject := p.Args.(map[string]any)
	if !isObject {
		v.add("EXE-01", "$.with", "with must be an object with method and path")
		args = map[string]any{}
	}
	v.r.Refs = uniqueSorted(refNames(args))

	method, _ := args["method"].(string)
	literalPath, pathIsText := args["path"].(string)
	declared := readRequest{method: method, path: literalPath}
	readShows, declaredRead := d.policy.reads[declared]
	declaredRead = declaredRead && pathIsText
	declaredDestructive := pathIsText && d.policy.destructive[declared]
	switch {
	case declaredRead:
		v.r.Uses = []string{readEntry(declared)}
	case declaredDestructive:
		v.r.Uses = []string{destructiveEntry(declared)}
	}
	if knownMethod(method) {
		if observes(method) || declaredRead {
			v.r.Kind = protocol.KindObserve
		}
		v.r.Destructive = destructive(method) || declaredDestructive
	} else {
		v.add("EXE-01", "$.with.method", "unsupported method %s", show(args["method"]))
	}

	if path, ok := args["path"].(string); ok {
		exact, named := path, refNames(path)
		if declaredDestructive {
			exact, named = path+bodyTarget(args)+rawTarget(args, p.Files), refNames([]any{path, args["body"], args["body_text"]})
		}
		v.r.FixedTarget = !runScoped(named)
		v.r.Targets = []string{template(path)}
		v.r.ExactTargets = []string{exact}
		if err := checkRequestPath(path); err != nil {
			v.add("HTTP-05", "$.with.path", "%v", err)
		}
	} else {
		v.add("HTTP-05", "$.with.path", "path must be text starting with /, got %s", show(args["path"]))
	}

	query := v.object(args, "query")
	headers := v.object(args, "headers")
	v.checkHeaders(headers)

	v.checkBodyForms(args, method, headers, p.Files, d.policy.bodyLimit())

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
	v.onlyInBodyOrHeaders("$.expect", []any{ex.Equal, ex.Length, ex.Unordered, ex.Any, ex.None, ex.Contains, ex.Count, ex.Matches, ex.Excludes, ex.Differs})
	v.checkClaims(ex)
	v.checkStatus(ex, p)
	if method == "HEAD" {
		v.checkHead(ex, p.Save)
	}
	if v.r.Kind == protocol.KindObserve {
		if method == "OPTIONS" || !checksState(ex) {
			no := false
			v.r.StateCheck = &no
		}
		read := ""
		switch {
		case declaredRead:
			v.r.Shows, read = append([]string(nil), readShows...), declared.String()
		case method == showsMethod && pathIsText:
			v.r.Shows, read = append([]string(nil), d.policy.shows[template(literalPath)]...), showsMethod+" "+template(literalPath)
		}
		if len(v.r.Shows) > 0 {
			v.r.ShowsEntries = map[string]string{}
			for _, act := range v.r.Shows {
				v.r.ShowsEntries[act] = showsEntry(read, act)
			}
		}
	}
	v.checkHeaderCase(ex)
	v.checkConflicts(ex)
	v.checkUnordered(ex)
	if (method == "GET" || declaredRead) && len(ex.Length) > 0 && len(query) == 0 {
		v.add("HTTP-09", "$.expect.length", "asserts a list length on a request with no query parameters; the list may include records the scenario didn't create (filter the request, or waive HTTP-09 with the reason it can't)")
	}
	return v.r, nil
}

func runScoped(names []string) bool {
	for _, n := range names {
		if n == runIDRef || savedNameRE.MatchString(n) {
			return true
		}
	}
	return false
}

func bodyTarget(args map[string]any) string {
	var x any
	switch {
	case args["body"] != nil:
		x = args["body"]
	case args["body_text"] != nil:
		x = args["body_text"]
	default:
		return ""
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(x); err != nil {
		return " " + show(x)
	}
	return " " + strings.TrimSuffix(b.String(), "\n")
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

func (v *validation) checkClaims(ex expectation) {
	for _, m := range []struct {
		name   string
		claims map[string]any
	}{{"any", ex.Any}, {"none", ex.None}, {"count", ex.Count}} {
		for _, k := range sortedKeys(m.claims) {
			switch c := m.claims[k].(type) {
			case map[string]any, nil:
			case []any:
				for i, item := range c {
					if _, isObject := item.(map[string]any); !isObject {
						v.add("EXE-03", "$.expect."+m.name, "%s of %s: claim[%d] must be an object, not %s", m.name, k, i, show(item))
					}
				}
			default:
				v.add("EXE-03", "$.expect."+m.name, "%s of %s must be an object or a list of objects, not %s", m.name, k, show(c))
			}
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
	if p.Setup || !valid || code < 200 || code >= 300 {
		return
	}
	switch lone := loneBodyAbsence(ex); {
	case lone != "" && ex.count()-1 == 1:
		v.add("EXE-03", "$.expect.absent", "absent: [%s] alone doesn't show that the %d response has no body, since a body that isn't JSON is $.body_text and one that is JSON is $.body; list both, absent: [\"$.body\", \"$.body_text\"], or assert a header", lone, code)
	case ex.count() == 1 && code == 204:
		v.add("EXE-03", "$.expect", "a verification step expecting %d must also assert that the body is absent, absent: [\"$.body\", \"$.body_text\"], or a header", code)
	case ex.count() == 1:
		v.add("EXE-03", "$.expect", "a verification step expecting %d must also assert a body field or header", code)
	}
}

func loneBodyAbsence(ex expectation) string {
	hasBody, hasText := slices.Contains(ex.Absent, "$.body"), slices.Contains(ex.Absent, "$.body_text")
	switch {
	case hasBody && !hasText:
		return "$.body"
	case hasText && !hasBody:
		return "$.body_text"
	}
	return ""
}

func checksState(ex expectation) bool {
	code, valid := httpStatus(ex.Equal[statusPath])
	if !valid {
		return false
	}
	switch code {
	case 404, 409, 410, 412, 423:
		return true
	}
	return code >= 200 && code < 400
}

func under(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+".") || strings.HasPrefix(p, root+"[")
}

func inBody(p string) bool {
	return under(p, "$.body") || under(p, "$.body_text") || under(p, "$.body_invalid")
}

func (v *validation) checkHead(ex expectation, save map[string]string) {
	at := ""
	for _, p := range ex.paths() {
		if inBody(p) {
			at = "$.expect"
			break
		}
	}
	if at == "" {
		for _, name := range sortedKeys(save) {
			if inBody(save[name]) {
				at = "$.save"
				break
			}
		}
	}
	if at != "" {
		v.add("EXE-03", at, "a HEAD response has no body, so it can only assert status and headers")
	}
}

var headerRoots = []string{"$.headers", "$.header_lists"}

func headerPath(p string) (name, rest string, ok bool) {
	segs, err := jsonpath.Parse(p)
	if err != nil || len(segs) == 0 || segs[0].IsIndex || !slices.Contains(headerRoots, jsonpath.String(segs[:1])) {
		return "", "", false
	}
	switch {
	case len(segs) == 1:
		return "", "", true
	case segs[1].IsIndex:
		return "", jsonpath.String(segs[1:])[1:], true
	}
	return segs[1].Name, jsonpath.String(segs[2:])[1:], true
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
		v.add("HTTP-08", "$.expect", "%s names header %q with capital letters; the driver reports header names in lower case, so write %s", p, name, jsonpath.Child(headerRoot(p), strings.ToLower(name)))
	}
}

func headerKey(p string) (string, bool) {
	name, rest, ok := headerPath(p)
	if ok && name != "" && rest == "" {
		return "header " + strings.ToLower(name), true
	}
	return p, false
}

func byHeader(paths []string) map[string][]string {
	out := map[string][]string{}
	for _, p := range paths {
		if key, ok := headerKey(p); ok {
			out[key] = append(out[key], p)
		}
	}
	return out
}

func elsewhere(paths []string, p string) bool {
	for _, q := range paths {
		if q != p {
			return true
		}
	}
	return false
}

func (v *validation) checkConflicts(ex expectation) {
	equal := byHeader(sortedKeys(ex.Equal))
	present := byHeader(ex.Present)
	absent := byHeader(ex.Absent)
	contained := byHeader(append(append(append(sortedKeys(ex.Contains), sortedKeys(ex.Matches)...), sortedKeys(ex.Excludes)...), sortedKeys(ex.Differs)...))
	for _, k := range ex.Absent {
		key, isHeader := headerKey(k)
		if !isHeader {
			continue
		}
		name, _, _ := headerPath(k)
		switch {
		case elsewhere(equal[key], k), elsewhere(contained[key], k):
			v.add("HTTP-08", "$.expect.absent", "header %s is both expected and absent", name)
		case elsewhere(present[key], k):
			v.add("HTTP-08", "$.expect.absent", "%s is asserted by both present and absent", k)
		}
	}
	for _, k := range ex.Absent {
		if name, rest, ok := headerPath(k); ok && rest == "" && !validHeaderName(name) {
			v.add("HTTP-08", "$.expect.absent", "absent: invalid header name %q", name)
		}
	}
	for _, k := range sortedKeys(ex.Keys) {
		if key, isHeader := headerKey(k); isHeader && elsewhere(absent[key], k) {
			v.add("HTTP-08", "$.expect.keys", "%s is both absent and given keys", k)
		}
	}
	for _, k := range sortedKeys(ex.Length) {
		if key, isHeader := headerKey(k); isHeader && elsewhere(absent[key], k) {
			v.add("HTTP-08", "$.expect.length", "%s is both absent and given a length", k)
		}
	}
}

func (v *validation) checkUnordered(ex expectation) {
	equal := byHeader(sortedKeys(ex.Equal))
	absent := byHeader(ex.Absent)
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
		if len(equal[key]) > 0 {
			v.add("HTTP-08", at, "header %s is in both equal and unordered", name)
		}
		if elsewhere(absent[key], k) {
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
