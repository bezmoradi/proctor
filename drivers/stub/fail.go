package stub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/proctor/protocol"
	"github.com/bezmoradi/proctor/protocol/schema"
)

const (
	MinDelay   = time.Millisecond
	MaxDelay   = 10 * time.Minute
	MinFor     = time.Second
	MaxFor     = time.Hour
	DefaultFor = 10 * time.Minute
	MaxTimes   = 10000
)

var headerNameRE = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")

var forbiddenHeaders = map[string]bool{"content-length": true, "transfer-encoding": true, "connection": true}

var FailArgsSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"route":   map[string]any{"type": "string"},
		"status":  map[string]any{"type": "integer", "minimum": 200, "maximum": 599},
		"headers": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"json":    true,
		"text":    map[string]any{"type": "string"},
		"reset":   map[string]any{"type": "boolean"},
		"delay":   map[string]any{"type": "string"},
		"times":   map[string]any{"type": "integer", "minimum": 1, "maximum": MaxTimes},
		"for":     map[string]any{"type": "string"},
	},
	"required":             []string{"route"},
	"additionalProperties": false,
}

var FailFactSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"id":         map[string]any{"type": "string"},
		"route":      map[string]any{"type": "string"},
		"active":     map[string]any{"type": "boolean"},
		"expires_at": map[string]any{"type": "string"},
		"last_seq":   map[string]any{"type": "integer", "minimum": 0},
	},
	"required":             []string{"id", "route", "active", "expires_at", "last_seq"},
	"additionalProperties": false,
}

func validateFail(p protocol.ValidateParams) protocol.ValidateResult {
	r := protocol.ValidateResult{Kind: protocol.KindAct, Targets: []string{RoutesTarget}}
	pr := problems{&r}
	m, ok := p.Args.(map[string]any)
	if !ok {
		pr.add("EXE-01", "$.with", "with must be an object naming the route and the fault: status, reset: true or delay")
		return r
	}
	for _, msg := range schema.Check(FailArgsSchema, m, schema.AllowRefs) {
		pr.add("EXE-01", "$.with", "%s", msg)
	}
	if v, ok := m["route"].(string); ok {
		switch {
		case strings.Contains(v, "${"):
			pr.add("STUB-01", "$.with.route", "route takes a route id or %q, not a reference, so the step's target is fixed", AllRoutes)
		case v != AllRoutes && !idRE.MatchString(v):
			pr.add("STUB-01", "$.with.route", "route %q is neither a route id (^[a-z][a-z0-9_]{0,62}$) nor %q for every request", v, AllRoutes)
		default:
			r.Targets = []string{routeTarget(v)}
		}
	}
	for _, key := range []string{"status", "reset", "delay", "times", "for"} {
		if s, ok := m[key].(string); ok && strings.Contains(s, "${") {
			pr.add("STUB-01", "$.with."+key, "%s can't be a reference; write it out", key)
		}
	}
	_, hasStatus := m["status"]
	_, hasReset := m["reset"]
	_, hasDelay := m["delay"]
	_, hasHeaders := m["headers"]
	_, hasJSON := m["json"]
	_, hasText := m["text"]
	switch {
	case hasReset && m["reset"] != true:
		pr.add("STUB-01", "$.with.reset", "reset takes only true; leave it out for a fault that answers")
	case hasReset && (hasStatus || hasHeaders || hasJSON || hasText):
		pr.add("STUB-01", "$.with", "reset: true closes the connection without an answer, so it can't come with status, headers, json or text")
	case !hasStatus && (hasHeaders || hasJSON || hasText):
		pr.add("STUB-01", "$.with", "headers, json and text describe the fault's answer, so they need a status")
	case !hasStatus && !hasReset && !hasDelay:
		pr.add("STUB-01", "$.with", "give the fault: status (with optional headers and json or text), reset: true, or delay, alone or before either")
	}
	if hasJSON && hasText {
		pr.add("STUB-01", "$.with", "give json or text, not both")
	}
	if n, ok := m["status"].(json.Number); ok && (n.String() == "204" || n.String() == "304") && (hasJSON || hasText) {
		pr.add("STUB-01", "$.with", "a %s answer has no body; remove json or text", n)
	}
	if s, ok := m["delay"].(string); ok && !strings.Contains(s, "${") {
		if d, err := time.ParseDuration(s); err != nil || d < MinDelay || d > MaxDelay {
			pr.add("STUB-01", "$.with.delay", "delay %q must be a duration from 1ms to 10m, such as 5s", s)
		}
	}
	if s, ok := m["for"].(string); ok && !strings.Contains(s, "${") {
		if d, err := time.ParseDuration(s); err != nil || d < MinFor || d > MaxFor {
			pr.add("STUB-01", "$.with.for", "for %q must be a duration from 1s to 1h, such as 5m", s)
		}
	}
	if h, ok := m["headers"].(map[string]any); ok {
		for _, name := range sortedNames(h) {
			at := "$.with.headers." + name
			switch {
			case !headerNameRE.MatchString(name):
				pr.add("STUB-01", at, "%q is not a header name", name)
			case forbiddenHeaders[strings.ToLower(name)]:
				pr.add("STUB-01", at, "a fault can't set %s; the sim sets it", name)
			}
			if s, ok := h[name].(string); ok {
				checkRefs(pr, "HTTP-02", at, s)
				if strings.ContainsAny(s, "\r\n") {
					pr.add("STUB-01", at, "the value holds a line break")
				}
			}
		}
	}
	if s, ok := m["text"].(string); ok {
		checkRefs(pr, "HTTP-02", "$.with.text", s)
	}
	if v, ok := m["json"]; ok {
		walkStrings(v, func(s string) { checkRefs(pr, "HTTP-02", "$.with.json", s) })
	}
	r.Refs = refs(p.Args)
	return r
}

func walkStrings(v any, f func(string)) {
	switch x := v.(type) {
	case string:
		f(x)
	case map[string]any:
		for k, e := range x {
			f(k)
			walkStrings(e, f)
		}
	case []any:
		for _, e := range x {
			walkStrings(e, f)
		}
	}
}

func sortedNames(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type failArgs struct {
	Route   string            `json:"route"`
	Status  *int              `json:"status"`
	Headers map[string]string `json:"headers"`
	JSON    json.RawMessage   `json:"json"`
	Text    *string           `json:"text"`
	Reset   *bool             `json:"reset"`
	Delay   *string           `json:"delay"`
	Times   *int              `json:"times"`
	For     *string           `json:"for"`
}

type faultRequest struct {
	Route   string            `json:"route"`
	Status  int               `json:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	JSON    json.RawMessage   `json:"json,omitempty"`
	Text    *string           `json:"text,omitempty"`
	DelayMS int64             `json:"delay_ms,omitempty"`
	Reset   bool              `json:"reset,omitempty"`
	Times   int               `json:"times,omitempty"`
	TTLMS   int64             `json:"ttl_ms"`
}

func (d *Driver) fail(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	start := time.Now()
	var a failArgs
	if err := protocol.Decode(p.Args, &a); err != nil {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "fail: %v", err)
	}
	req := faultRequest{Route: a.Route, Headers: a.Headers, JSON: a.JSON, Text: a.Text, TTLMS: DefaultFor.Milliseconds()}
	var parts []string
	if a.Delay != nil {
		dl, err := time.ParseDuration(*a.Delay)
		if err != nil || dl < MinDelay || dl > MaxDelay {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "STUB-01: delay %q must be a duration from 1ms to 10m", *a.Delay)
		}
		req.DelayMS = dl.Milliseconds()
		parts = append(parts, "delay "+dl.String())
	}
	if a.Status != nil {
		req.Status = *a.Status
		parts = append(parts, "status "+strconv.Itoa(*a.Status))
	}
	if a.Reset != nil && *a.Reset {
		req.Reset = true
		parts = append(parts, "reset")
	}
	if a.Times != nil {
		req.Times = *a.Times
		parts = append(parts, fmt.Sprintf("%d times", *a.Times))
	}
	if a.For != nil {
		dur, err := time.ParseDuration(*a.For)
		if err != nil || dur < MinFor || dur > MaxFor {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "STUB-01: for %q must be a duration from 1s to 1h", *a.For)
		}
		req.TTLMS = dur.Milliseconds()
	}
	parts = append(parts, "for "+(time.Duration(req.TTLMS)*time.Millisecond).String())
	payload, err := json.Marshal(req)
	if err != nil {
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "fail: %v", err)
	}
	status, body, err := d.do(ctx, http.MethodPost, FaultsPath, payload)
	if err != nil {
		return protocol.CallResult{}, err
	}
	se := readSimError(body)
	switch {
	case status == http.StatusConflict && se.Fault != nil:
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "STUB-03: a fault on route %s would overlap the active fault %s on route %s, which expires at %s; a fault an earlier scenario or run left behind blocks a new one until it expires or is deleted with DELETE %s/%s", a.Route, se.Fault.ID, se.Fault.Route, se.Fault.ExpiresAt, FaultsPath, se.Fault.ID)
	case status == http.StatusNotFound && se.Code == "unknown_route":
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "STUB-02: %s", se.Error)
	case status == http.StatusNotFound:
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseUnreadableResponse, "POST %s answered 404; the endpoint must be %s", FaultsPath, SimName)
	case status == http.StatusBadRequest:
		return protocol.CallResult{}, protocol.Lastingf(protocol.CauseInvalidRequest, "STUB-01: the sim refused the fault: %s", se.Error)
	case status != http.StatusCreated:
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnreadableResponse, "POST %s answered %d: %s", FaultsPath, status, se.Error)
	}
	var out struct {
		ID        string  `json:"id"`
		Route     string  `json:"route"`
		ExpiresAt string  `json:"expires_at"`
		LastSeq   *uint64 `json:"last_seq"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.ID == "" || out.LastSeq == nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnreadableResponse, "POST %s: the answer is not a fault from %s", FaultsPath, SimName)
	}
	d.open[out.ID] = out.Route
	return protocol.CallResult{
		Fact:   map[string]any{"id": out.ID, "route": out.Route, "active": true, "expires_at": out.ExpiresAt, "last_seq": *out.LastSeq},
		Handle: out.ID,
		Evidence: protocol.Evidence{
			Summary:        fmt.Sprintf("fail (route %s: %s) -> fault %s until %s, after request %d", out.Route, strings.Join(parts, ", "), out.ID, out.ExpiresAt, *out.LastSeq),
			RequestExcerpt: "POST " + FaultsPath + "\n" + string(payload),
			DurationMS:     time.Since(start).Milliseconds(),
		},
	}, nil
}
