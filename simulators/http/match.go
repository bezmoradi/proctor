package simhttp

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/url"
	"strings"
)

type incoming struct {
	method  string
	segs    []string
	query   url.Values
	header  http.Header
	host    string
	body    any
	hasJSON bool
}

func requestSegments(escaped string) []string {
	raw := strings.Split(strings.TrimPrefix(escaped, "/"), "/")
	out := make([]string, len(raw))
	for i, s := range raw {
		if u, err := url.PathUnescape(s); err == nil {
			out[i] = u
		} else {
			out[i] = s
		}
	}
	return out
}

func (rs *Routes) match(in *incoming) (*Route, map[string]string) {
	for _, r := range rs.List {
		if params, ok := r.matches(in); ok {
			return r, params
		}
	}
	return nil, nil
}

func (r *Route) matches(in *incoming) (map[string]string, bool) {
	if in.method != r.Method || len(in.segs) != len(r.segs) {
		return nil, false
	}
	params := map[string]string{}
	for i, s := range r.segs {
		if s.param != "" {
			params[s.param] = in.segs[i]
			continue
		}
		if in.segs[i] != s.lit {
			return nil, false
		}
	}
	for _, q := range r.query {
		if !q.any(in.query[q.name]) {
			return nil, false
		}
	}
	for _, h := range r.headers {
		var values []string
		if h.name == "host" {
			values = []string{in.host}
		} else {
			values = in.header.Values(h.name)
		}
		if !h.any(values) {
			return nil, false
		}
	}
	if r.hasBody && (!in.hasJSON || !subset(r.body, in.body)) {
		return nil, false
	}
	return params, true
}

func (v valueMatch) any(values []string) bool {
	for _, s := range values {
		if v.re != nil && v.re.MatchString(s) || v.re == nil && s == v.exact {
			return true
		}
	}
	return false
}

func subset(want, got any) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok || !subset(wv, gv) {
				return false
			}
		}
		return true
	default:
		return jsonEqual(want, got)
	}
}

func jsonEqual(a, b any) bool {
	switch x := a.(type) {
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return false
		}
		ra, okA := new(big.Rat).SetString(x.String())
		rb, okB := new(big.Rat).SetString(y.String())
		return okA && okB && ra.Cmp(rb) == 0
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, ok := y[k]
			if !ok || !jsonEqual(xv, yv) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !jsonEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}
