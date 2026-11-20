package main

import (
	"github.com/bezmoradi/proctor/protocol"
)

func (d *driver) validateOpen(p protocol.ValidateParams) protocol.ValidateResult {
	v := &validation{r: protocol.ValidateResult{Kind: protocol.KindAct, Problems: []protocol.Problem{}, Refs: []string{}}}
	args, isObject := p.Args.(map[string]any)
	if !isObject {
		v.add("EXE-01", "$.with", "with must be an object with path")
		args = map[string]any{}
	}
	v.r.Refs = uniqueSorted(refNames(args))
	if path, ok := args["path"].(string); ok {
		v.r.Targets = []string{template(path)}
		if err := checkRequestPath(path); err != nil {
			v.add("HTTP-05", "$.with.path", "%v", err)
		}
	} else {
		v.add("HTTP-05", "$.with.path", "path must be text starting with /, got %s", show(args["path"]))
	}
	v.object(args, "query")
	headers := v.object(args, "headers")
	v.checkHeaders(headers)
	v.onlyInBodyOrHeaders("$.with.path", args["path"])
	v.onlyInBodyOrHeaders("$.with.query", args["query"])
	v.onlyInBodyOrHeaders("$.with.format", args["format"])
	var ex expectation
	if p.Expect != nil {
		if err := protocol.Decode(p.Expect, &ex); err != nil {
			v.add("EXE-03", "$.expect", "expect can't be read: %v", err)
			ex = expectation{}
		}
	}
	v.onlyInBodyOrHeaders("$.expect", []any{ex.Equal, ex.Length, ex.Unordered, ex.Any, ex.None, ex.Contains, ex.Count, ex.Matches, ex.Excludes, ex.Differs})
	v.checkClaims(ex)
	v.checkOpenStatus(ex, p)
	v.checkHeaderCase(ex)
	v.checkConflicts(ex)
	v.checkUnordered(ex)
	return v.r
}

func (v *validation) checkOpenStatus(ex expectation, p protocol.ValidateParams) {
	raw, has := ex.Equal[statusPath]
	code, valid := httpStatus(raw)
	switch {
	case !has:
		v.add("EXE-03", "$.expect.equal", "expect.equal[%q] is required: it says whether the stream opens (a 2xx) or is refused", statusPath)
	case !valid:
		v.add("EXE-03", "$.expect.equal", "status %s is not an HTTP status", show(raw))
	}
	if p.Variant == "" {
		return
	}
	switch {
	case p.Setup:
		v.add("AUTH-06", "$.variant", "a credential variant is for negative tests, not setup")
	case !has:
		v.add("AUTH-06", "$.expect.equal", "an open with credential variant %q must expect 401 or 403 in expect.equal[%q]", p.Variant, statusPath)
	case !valid || (code != 401 && code != 403):
		v.add("AUTH-06", "$.expect.equal", "an open with credential variant %q must expect 401 or 403, not %s", p.Variant, show(raw))
	}
}

func (d *driver) validateFrames(p protocol.ValidateParams) protocol.ValidateResult {
	v := &validation{r: protocol.ValidateResult{Kind: protocol.KindObserve, Problems: []protocol.Problem{}, Refs: []string{}}}
	v.r.Refs = uniqueSorted(refNames(p.Args))
	var ex expectation
	if p.Expect != nil {
		if err := protocol.Decode(p.Expect, &ex); err != nil {
			v.add("EXE-03", "$.expect", "expect can't be read: %v", err)
			ex = expectation{}
		}
	}
	v.checkClaims(ex)
	return v.r
}
