package stub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/proctor/protocol"
	"github.com/bezmoradi/proctor/protocol/schema"
)

var stringLists = map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}

var ReceivedArgsSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"route":  map[string]any{"type": "string"},
		"method": map[string]any{"type": "string", "enum": methods},
		"path":   map[string]any{"type": "string"},
		"after":  map[string]any{"type": []string{"integer", "string"}, "minimum": 0},
	},
	"additionalProperties": false,
}

var requestSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"seq":            map[string]any{"type": "integer", "minimum": 1},
		"at":             map[string]any{"type": "string"},
		"route":          map[string]any{"type": "string"},
		"method":         map[string]any{"type": "string"},
		"host":           map[string]any{"type": "string"},
		"path":           map[string]any{"type": "string"},
		"query":          stringLists,
		"headers":        stringLists,
		"body":           true,
		"body_text":      map[string]any{"type": "string"},
		"body_truncated": map[string]any{"type": "boolean"},
		"body_invalid":   map[string]any{"type": "string"},
		"body_bytes":     map[string]any{"type": "integer", "minimum": 0},
		"status":         map[string]any{"type": "integer", "minimum": 0},
		"fault":          map[string]any{"type": "boolean"},
	},
	"required":             []string{"seq", "at", "route", "method", "host", "path", "query", "headers", "body_truncated", "body_bytes", "status", "fault"},
	"additionalProperties": false,
}

var ReceivedFactSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"count":           map[string]any{"type": "integer", "minimum": 0},
		"requests":        map[string]any{"type": "array", "items": requestSchema},
		"unmatched":       map[string]any{"type": "integer", "minimum": 0},
		"last_seq":        map[string]any{"type": "integer", "minimum": 0},
		"overflow":        map[string]any{"type": "boolean"},
		"overflow_reason": map[string]any{"type": "string"},
	},
	"required":             []string{"count", "requests", "unmatched", "last_seq", "overflow"},
	"additionalProperties": false,
}

func validateReceived(p protocol.ValidateParams) protocol.ValidateResult {
	yes := true
	r := protocol.ValidateResult{Kind: protocol.KindObserve, StateCheck: &yes, Targets: []string{RoutesTarget}}
	pr := problems{&r}
	if p.Setup {
		pr.add("FIX-03", "$.do", "reading the sim's request log is a verification step; setup goes through the application")
	}
	if p.Args != nil {
		if _, ok := p.Args.(map[string]any); !ok {
			pr.add("EXE-01", "$.with", "with must be an object with optional route, method, path and after")
			return r
		}
	}
	for _, msg := range schema.Check(ReceivedArgsSchema, argsOrEmpty(p.Args), schema.AllowRefs) {
		pr.add("EXE-01", "$.with", "%s", msg)
	}
	m, _ := p.Args.(map[string]any)
	if v, ok := m["route"].(string); ok {
		switch {
		case strings.Contains(v, "${"):
			pr.add("EXE-01", "$.with.route", "route takes a route id from the route file, not a reference, so the step's target is fixed")
		case !idRE.MatchString(v):
			pr.add("EXE-01", "$.with.route", "route %q is not a route id (^[a-z][a-z0-9_]{0,62}$); leave route out to read every request", v)
		default:
			r.Targets = []string{routeTarget(v)}
		}
	}
	if v, ok := m["method"].(string); ok && strings.Contains(v, "${") {
		pr.add("EXE-01", "$.with.method", "method takes one of %s, not a reference", strings.Join(methods, ", "))
	}
	if v, ok := m["path"].(string); ok {
		names := checkRefs(pr, "HTTP-02", "$.with.path", v)
		whole := len(names) == 1 && v == "${"+names[0]+"}"
		if !whole && !strings.HasPrefix(v, "/") {
			pr.add("EXE-01", "$.with.path", "path %q must be the request's escaped path, starting with /", v)
		}
		if strings.ContainsAny(v, "?#") {
			pr.add("EXE-01", "$.with.path", "path %q can't hold ? or #; the log keeps the query apart", v)
		}
	}
	if v, ok := m["after"].(string); ok {
		names := checkRefs(pr, "HTTP-02", "$.with.after", v)
		if len(names) != 1 || v != "${"+names[0]+"}" || names[0] == "run.id" {
			pr.add("EXE-01", "$.with.after", "after takes a whole number or a saved mark written as the whole value, such as \"${mark}\" saved from $.last_seq")
		}
	}
	r.Refs = refs(p.Args)
	return r
}

type receivedArgs struct {
	Route  *string `json:"route"`
	Method *string `json:"method"`
	Path   *string `json:"path"`
	After  any     `json:"after"`
}

type receivedAnswer struct {
	LastSeq      *uint64           `json:"last_seq"`
	FirstKeptSeq uint64            `json:"first_kept_seq"`
	More         bool              `json:"more"`
	Total        int               `json:"total"`
	Unmatched    int               `json:"unmatched"`
	Recorded     int               `json:"recorded"`
	Requests     []json.RawMessage `json:"requests"`
}

func afterValue(v any) (uint64, error) {
	var s string
	switch x := v.(type) {
	case nil:
		return 0, nil
	case json.Number:
		s = x.String()
	case string:
		s = strings.TrimSpace(x)
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return 0, fmt.Errorf("after must be a whole number, not %T", v)
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("after %q must be a whole number of 0 or more, such as the last_seq a mark saved", s)
	}
	return n, nil
}

func (d *Driver) received(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	start := time.Now()
	var a receivedArgs
	if p.Args != nil {
		if err := protocol.Decode(p.Args, &a); err != nil {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "received: %v", err)
		}
	}
	after, err := afterValue(a.After)
	if err != nil {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "received: %v", err)
	}
	q := url.Values{}
	var scope []string
	if a.Route != nil {
		rs, err := d.routes(ctx)
		if err != nil {
			return protocol.CallResult{}, err
		}
		if !rs.has(*a.Route) {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "STUB-02: route %q is not in the sim's route file (sha256 %s), so no request can match it; its routes are %s", *a.Route, rs.SHA256, rs.ids())
		}
		q.Set("route", *a.Route)
		scope = append(scope, "route "+*a.Route)
	}
	if a.Method != nil {
		q.Set("method", *a.Method)
		scope = append(scope, "method "+*a.Method)
	}
	if a.Path != nil {
		q.Set("path", *a.Path)
		scope = append(scope, "path "+*a.Path)
	}
	if a.After != nil {
		q.Set("after", strconv.FormatUint(after, 10))
		scope = append(scope, fmt.Sprintf("after %d", after))
	}
	target := ReceivedPath
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	status, body, err := d.do(ctx, http.MethodGet, target, nil)
	if err != nil {
		return protocol.CallResult{}, err
	}
	switch {
	case status == http.StatusBadRequest && readSimError(body).Code == "unknown_route":
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "STUB-02: %s", readSimError(body).Error)
	case status == http.StatusNotFound:
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseUnreadableResponse, "GET %s answered 404; the endpoint must be %s", ReceivedPath, SimName)
	case status != http.StatusOK:
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnreadableResponse, "GET %s answered %d: %s", ReceivedPath, status, readSimError(body).Error)
	}
	var ans receivedAnswer
	if err := json.Unmarshal(body, &ans); err != nil || ans.LastSeq == nil || ans.Requests == nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnreadableResponse, "GET %s: the answer is not %s's request log", ReceivedPath, SimName)
	}
	requests := make([]any, 0, len(ans.Requests))
	var cut []string
	for _, raw := range ans.Requests {
		v, err := decodeNumber(raw)
		if err != nil {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnreadableResponse, "GET %s: a request in the answer can't be read: %v", ReceivedPath, err)
		}
		if m, ok := v.(map[string]any); ok && m["body_truncated"] == true {
			cut = append(cut, fmt.Sprint(m["seq"]))
		}
		requests = append(requests, v)
	}
	var reasons []string
	if ans.FirstKeptSeq > 0 && after < ans.FirstKeptSeq-1 {
		reasons = append(reasons, fmt.Sprintf("requests %d to %d were dropped from the sim's log, which keeps the newest 10,000 requests and 128 MiB", after+1, ans.FirstKeptSeq-1))
	}
	if ans.More {
		reasons = append(reasons, fmt.Sprintf("%d requests are in scope, and the sim answers at most 100 (12 MiB) at once", ans.Total))
	}
	if len(cut) > 0 {
		reasons = append(reasons, fmt.Sprintf("the body of request %s was cut at 64 KiB (body_truncated)", strings.Join(cut, ", ")))
	}
	fact := map[string]any{
		"count":     len(requests),
		"requests":  requests,
		"unmatched": ans.Unmatched,
		"last_seq":  *ans.LastSeq,
		"overflow":  len(reasons) > 0,
	}
	if len(reasons) > 0 {
		fact["overflow_reason"] = strings.Join(reasons, "; ") + "; narrow the read with after, route or path"
	}
	what := "every request"
	if len(scope) > 0 {
		what = strings.Join(scope, ", ")
	}
	noun := "requests"
	if len(requests) == 1 {
		noun = "request"
	}
	summary := fmt.Sprintf("received (%s) -> %d %s of %d recorded", what, len(requests), noun, ans.Recorded)
	if ans.Unmatched > 0 {
		summary += fmt.Sprintf(", %d unmatched", ans.Unmatched)
	}
	if len(reasons) > 0 {
		summary += " (overflow)"
	}
	return protocol.CallResult{
		Fact: fact,
		Evidence: protocol.Evidence{
			Summary:        summary,
			RequestExcerpt: "GET " + target,
			DurationMS:     time.Since(start).Milliseconds(),
		},
	}, nil
}

func decodeNumber(raw []byte) (any, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}
