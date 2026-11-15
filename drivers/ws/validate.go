package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"proctor/protocol"
)

const statusPath = "$.status"

type validation struct {
	r protocol.ValidateResult
}

func (v *validation) add(rule, path, format string, args ...any) {
	v.r.Problems = append(v.r.Problems, protocol.Problem{Rule: rule, Message: fmt.Sprintf(format, args...), Path: path})
}

func (d *Driver) Validate(ctx context.Context, p protocol.ValidateParams) (protocol.ValidateResult, error) {
	v := &validation{r: protocol.ValidateResult{Kind: protocol.KindAct, Problems: []protocol.Problem{}, Refs: []string{}}}
	args, _ := p.Args.(map[string]any)
	v.r.Refs = uniqueSorted(refNames(p.Args))
	switch p.Op {
	case OpConnect:
		v.connect(args, p)
	case OpSend:
		v.send(args)
	case OpMessages:
		v.r.Kind = protocol.KindObserve
	default:
		v.add("EXE-01", "$.do", "the ws driver has no operation %q; it has %s, %s and %s", p.Op, OpConnect, OpSend, OpMessages)
	}
	return v.r, nil
}

func (v *validation) connect(args map[string]any, p protocol.ValidateParams) {
	if path, ok := args["path"].(string); ok {
		v.r.FixedTarget = !strings.Contains(path, "${")
		if err := checkPath(path, false); err != nil {
			v.add("WS-01", "$.with.path", "%v", err)
		}
	}
	query, _ := args["query"].(map[string]any)
	headers, _ := args["headers"].(map[string]any)
	v.checkHeaders(headers)
	if noOrigin, _ := args["no_origin"].(bool); noOrigin {
		for name := range headers {
			if strings.EqualFold(name, "origin") {
				v.add("EXE-01", "$.with.no_origin", "no_origin sends the handshake without an Origin header, but headers also sets %s", name)
			}
		}
	}
	if list, ok := args["subprotocols"].([]any); ok {
		for i, raw := range list {
			if sp, ok := raw.(string); ok && !strings.Contains(sp, "${") && !isToken(sp) {
				v.add("EXE-01", fmt.Sprintf("$.with.subprotocols[%d]", i), "subprotocol %q is not a valid token", sp)
			}
		}
	}
	if raw, has := args["auth"]; has {
		if s, ok := raw.(string); ok {
			v.checkAuth(s, query, headers, p.Identity)
		}
	}
	for _, key := range []string{"path", "query", "subprotocols", "auth"} {
		v.onlyInData("$.with."+key, args[key])
	}
	v.onlyInData("$.expect", p.Expect)
	v.checkVariant(p)
}

func (v *validation) checkHeaders(headers map[string]any) {
	seen := map[string]string{}
	for _, name := range sortedKeys(headers) {
		at := "$.with.headers"
		if rule, why := forbiddenHeader(name); rule != "" {
			v.add(rule, at, "header %q can't be set by a specification: %s", name, why)
			continue
		}
		if !isToken(name) {
			v.add("EXE-01", at, "header name %q is not a valid HTTP header name", name)
			continue
		}
		lower := strings.ToLower(name)
		if prev, dup := seen[lower]; dup {
			v.add("EXE-01", at, "headers %q and %q name the same header (header names ignore case)", prev, name)
		}
		seen[lower] = name
		if s, ok := headers[name].(string); ok && hasControl(s) {
			v.add("EXE-01", at, "header %s contains a line break or control character", name)
		}
	}
}

func (v *validation) checkAuth(s string, query, headers map[string]any, identity string) {
	at := "$.with.auth"
	place, err := parseAuth(s)
	if err != nil || s == "" {
		if err == nil {
			err = fmt.Errorf("auth is empty; leave it out to use the subprotocol")
		}
		v.add("WS-03", at, "%v", err)
		return
	}
	if identity == "" {
		v.add("WS-03", at, "auth %q says where the credential goes, but the step names no identity", s)
	}
	switch place.kind {
	case "query":
		if _, taken := query[place.name]; taken {
			v.add("WS-03", at, "auth %q puts the credential in query parameter %s, which query also sets", s, place.name)
		}
	case "header":
		for name := range headers {
			if strings.EqualFold(name, place.name) {
				v.add("WS-03", at, "auth %q puts the credential in header %s, which headers also sets", s, place.name)
			}
		}
	}
}

func (v *validation) onlyInData(at string, value any) {
	reported := map[string]bool{}
	for _, n := range refNames(value) {
		if (isFill(n) || isSecret(n)) && !reported[n] {
			reported[n] = true
			v.add("HTTP-02", at, "${%s} may be used only in handshake headers or in sent frames", n)
		}
	}
}

func (v *validation) checkVariant(p protocol.ValidateParams) {
	if p.Variant == "" {
		return
	}
	if p.Setup {
		v.add("AUTH-06", "$.variant", "a credential variant is for negative tests, not setup")
		return
	}
	ex, _ := p.Expect.(map[string]any)
	equal, _ := ex["equal"].(map[string]any)
	raw, has := equal[statusPath]
	n, _ := raw.(json.Number)
	if !has || (n.String() != "401" && n.String() != "403") {
		v.add("AUTH-06", "$.expect.equal", "a connect with credential variant %q must expect the handshake to be refused with 401 or 403 in expect.equal[%q]", p.Variant, statusPath)
	}
}

func (v *validation) send(args map[string]any) {
	_, hasJSON := args["json"]
	_, hasText := args["text"]
	if hasJSON == hasText {
		v.add("WS-02", "$.with", "send needs exactly one of json and text")
	}
}
