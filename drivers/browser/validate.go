package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bezmoradi/proctor/protocol"
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
	case OpOpen:
		v.r.NoStateChange = true
		v.open(args, p)
	case OpFill:
		v.r.NoStateChange = true
		v.selector(args, "selector")
		if s, ok := args["value"].(string); ok && !strings.Contains(s, "${") {
			if typedControl(s) {
				v.add("BROWSER-05", "$.with.value", "value contains a control character other than a tab or a line break; fill types text, it doesn't press keys")
			}
			if len(s) > maxValue {
				v.add("BROWSER-05", "$.with.value", "value is %d bytes; fill types at most %d", len(s), maxValue)
			}
		}
		if s, ok := args["value"].(string); ok {
			for _, m := range refRE.FindAllStringSubmatch(s, -1) {
				if n, ok := fillSize(m[1]); ok && n > maxValue {
					v.add("BROWSER-05", "$.with.value", "${%s} is longer than the %d bytes fill types", m[1], maxValue)
				}
			}
		}
		v.onlyInData("$.with.selector", args["selector"])
	case OpClick:
		v.selector(args, "selector")
		if w, ok := args["wait_for"].(string); ok {
			switch {
			case strings.HasPrefix(w, "/"):
				if err := checkPath(w, false); err != nil {
					v.add("BROWSER-04", "$.with.wait_for", "wait_for: %v", err)
				}
			case strings.TrimSpace(w) == "":
				v.add("BROWSER-04", "$.with.wait_for", "wait_for is empty; give a CSS selector to wait for, or a path starting with /, or leave it out")
			}
		}
		v.onlyInData("$.with.selector", args["selector"])
		v.onlyInData("$.with.wait_for", args["wait_for"])
	case OpPage:
		v.r.Kind = protocol.KindObserve
		no := false
		v.r.StateCheck = &no
		v.page(args)
		v.onlyInData("$.with", p.Args)
	default:
		v.add("EXE-01", "$.do", "the browser driver has no operation %q; it has %s, %s, %s and %s", p.Op, OpOpen, OpFill, OpClick, OpPage)
	}
	v.onlyInData("$.expect", p.Expect)
	return v.r, nil
}

func (v *validation) open(args map[string]any, p protocol.ValidateParams) {
	if path, ok := args["path"].(string); ok {
		v.r.FixedTarget = !strings.Contains(path, "${")
		v.r.Targets = []string{"page" + refRE.ReplaceAllString(path, "{}")}
		if err := checkPath(path, false); err != nil {
			v.add("BROWSER-01", "$.with.path", "%v", err)
		}
	}
	query, _ := args["query"].(map[string]any)
	for _, k := range sortedKeys(query) {
		if k == "" || hasControl(k) {
			v.add("BROWSER-01", "$.with.query", "query parameter name %q is empty or has a control character", k)
		}
	}
	if raw, has := args["auth"]; has {
		if s, ok := raw.(string); ok {
			if _, err := parseAuth(s); err != nil {
				v.add("BROWSER-02", "$.with.auth", "%v", err)
			} else if p.Identity == "" {
				v.add("BROWSER-02", "$.with.auth", "auth %q says where the credential goes, but the step names no identity", s)
			}
		}
	}
	for _, key := range []string{"path", "query", "auth"} {
		v.onlyInData("$.with."+key, args[key])
	}
	v.checkVariant(p)
}

func (v *validation) selector(args map[string]any, key string) {
	if s, ok := args[key].(string); ok && strings.TrimSpace(s) == "" {
		v.add("BROWSER-03", "$.with."+key, "%s is empty; give a CSS selector", key)
	}
}

func (v *validation) page(args map[string]any) {
	sel, _ := args["selectors"].(map[string]any)
	if len(sel) > maxSelectors {
		v.add("BROWSER-03", "$.with.selectors", "selectors names %d elements; a read takes at most %d", len(sel), maxSelectors)
	}
	for _, name := range sortedKeys(sel) {
		if !nameRE.MatchString(name) {
			v.add("BROWSER-03", "$.with.selectors", "selector name %q must match ^[a-z][a-z0-9_]{0,62}$, so $.selectors.<name> reads it", name)
		}
		if s, ok := sel[name].(string); ok && strings.TrimSpace(s) == "" {
			v.add("BROWSER-03", "$.with.selectors."+name, "selector %s is empty; give a CSS selector", name)
		}
	}
}

func (v *validation) onlyInData(at string, value any) {
	reported := map[string]bool{}
	for _, n := range refNames(value) {
		if (isFill(n) || isSecret(n)) && !reported[n] {
			reported[n] = true
			v.add("HTTP-02", at, "${%s} may be used only in the value of %s.%s", n, Name, OpFill)
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
		v.add("AUTH-06", "$.expect.equal", "an open with credential variant %q must expect the page to be refused with 401 or 403 in expect.equal[%q]", p.Variant, statusPath)
	}
}

type authPlace struct {
	kind string
	name string
}

func parseAuth(s string) (authPlace, error) {
	switch {
	case s == "" || s == "header":
		return authPlace{kind: "header", name: "Authorization"}, nil
	case strings.HasPrefix(s, "header:"):
		name := strings.TrimPrefix(s, "header:")
		if !isToken(name) {
			return authPlace{}, fmt.Errorf("auth %q needs a valid HTTP header name after header:", s)
		}
		switch strings.ToLower(name) {
		case "cookie", "host", "content-length", "transfer-encoding", "connection":
			return authPlace{}, fmt.Errorf("auth %q names a header the browser owns; use cookie:<name> for a cookie", s)
		}
		return authPlace{kind: "header", name: name}, nil
	case strings.HasPrefix(s, "cookie:"):
		name := strings.TrimPrefix(s, "cookie:")
		if !isToken(name) {
			return authPlace{}, fmt.Errorf("auth %q needs a valid cookie name after cookie:", s)
		}
		return authPlace{kind: "cookie", name: name}, nil
	}
	return authPlace{}, fmt.Errorf("auth %q must be header (Authorization), header:<name> or cookie:<name>", s)
}
