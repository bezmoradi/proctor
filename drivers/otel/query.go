package otel

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bezmoradi/proctor/protocol"
)

type refused struct {
	Requests uint64 `json:"requests"`
	Spans    uint64 `json:"spans"`
	Logs     uint64 `json:"logs"`
}

type stats struct {
	Receiver string  `json:"receiver"`
	Version  string  `json:"version"`
	Spans    *int    `json:"spans"`
	Logs     *int    `json:"logs"`
	Refused  refused `json:"refused"`
}

type readyStats struct {
	Receiver string
	Version  string
	Spans    int
	Logs     int
}

type status struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type event struct {
	Name       string         `json:"name"`
	Time       string         `json:"time"`
	Attributes map[string]any `json:"attributes"`
}

type link struct {
	TraceID    string         `json:"trace_id"`
	SpanID     string         `json:"span_id"`
	Attributes map[string]any `json:"attributes"`
}

type dropped struct {
	Attributes uint32 `json:"attributes"`
	Events     uint32 `json:"events"`
	Links      uint32 `json:"links"`
}

type spanRecord struct {
	Seq          uint64         `json:"seq"`
	ReceivedAt   string         `json:"received_at"`
	Service      string         `json:"service"`
	Scope        string         `json:"scope"`
	Resource     map[string]any `json:"resource"`
	Attributes   map[string]any `json:"attributes"`
	TraceID      string         `json:"trace_id"`
	SpanID       string         `json:"span_id"`
	ParentSpanID string         `json:"parent_span_id"`
	Name         string         `json:"name"`
	Kind         string         `json:"kind"`
	Status       status         `json:"status"`
	StartTime    string         `json:"start_time"`
	EndTime      string         `json:"end_time"`
	DurationMS   json.Number    `json:"duration_ms"`
	Events       []event        `json:"events"`
	Links        []link         `json:"links"`
	Dropped      dropped        `json:"dropped"`
}

type logRecord struct {
	Seq            uint64         `json:"seq"`
	ReceivedAt     string         `json:"received_at"`
	Service        string         `json:"service"`
	Scope          string         `json:"scope"`
	Resource       map[string]any `json:"resource"`
	Attributes     map[string]any `json:"attributes"`
	Time           string         `json:"time"`
	ObservedTime   string         `json:"observed_time"`
	SeverityNumber int32          `json:"severity_number"`
	Severity       string         `json:"severity"`
	SeverityText   string         `json:"severity_text"`
	Body           any            `json:"body"`
	TraceID        string         `json:"trace_id"`
	SpanID         string         `json:"span_id"`
	EventName      string         `json:"event_name"`
}

type trace struct {
	TraceID    string   `json:"trace_id"`
	SpanCount  int      `json:"span_count"`
	Services   []string `json:"services"`
	RootCount  int      `json:"root_count"`
	ErrorCount int      `json:"error_count"`
}

type answer[R any] struct {
	Matched       *int     `json:"matched"`
	Records       *[]R     `json:"records"`
	Traces        []trace  `json:"traces"`
	Services      []string `json:"services"`
	AttributeKeys []string `json:"attribute_keys"`
	Stats         stats    `json:"stats"`
}

type filterArgs struct {
	TraceID           *string           `json:"trace_id,omitempty"`
	Service           *string           `json:"service,omitempty"`
	Name              *string           `json:"name,omitempty"`
	Kind              *string           `json:"kind,omitempty"`
	Status            *string           `json:"status,omitempty"`
	SeverityAtLeast   *string           `json:"severity_at_least,omitempty"`
	BodyContains      *string           `json:"body_contains,omitempty"`
	Attributes        map[string]any    `json:"attributes,omitempty"`
	AttributeContains map[string]string `json:"attribute_contains,omitempty"`
}

func (d *Driver) Call(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	start := time.Now()
	if p.Op != OpSpans && p.Op != OpLogs {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "unknown operation %q", p.Op)
	}
	if d.client == nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the otel driver has no connection")
	}
	var f filterArgs
	if m, ok := p.Args.(map[string]any); ok {
		for _, key := range []string{"trace_id", "service", "name", "kind", "status", "severity_at_least", "body_contains"} {
			if val, has := m[key]; has {
				if _, isText := val.(string); !isText {
					shown, _ := json.Marshal(val)
					return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s: %s must be text after inserting saved values, got %s", p.Op, key, shown)
				}
			}
		}
	}
	if p.Args != nil {
		if err := protocol.Decode(p.Args, &f); err != nil {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s: %v", p.Op, err)
		}
	}
	if p.Op == OpSpans && (f.SeverityAtLeast != nil || f.BodyContains != nil) {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "spans: severity_at_least and body_contains are filters of logs")
	}
	if p.Op == OpLogs && (f.Name != nil || f.Kind != nil || f.Status != nil) {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "logs: name, kind and status are filters of spans")
	}
	if f.TraceID != nil {
		t := strings.ToLower(strings.TrimSpace(*f.TraceID))
		if raw, err := hex.DecodeString(t); err != nil || len(raw) != 16 || strings.Trim(t, "0") == "" {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s: trace_id %q is not a trace id (32 hex characters, not all zeros)", p.Op, *f.TraceID)
		}
		f.TraceID = &t
	}
	body, err := json.Marshal(f)
	if err != nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s: the filter can't be written as JSON: %v", p.Op, err)
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	path := SpansPath
	if p.Op == OpLogs {
		path = LogsPath
	}
	raw, err := d.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return protocol.CallResult{}, err
	}
	excerpt := "POST " + path + "\n\n" + string(body)
	if p.Op == OpSpans {
		var a answer[spanRecord]
		if err := readAnswer(raw, path, &a); err != nil {
			return protocol.CallResult{}, err
		}
		spans := make([]any, 0, len(*a.Records))
		for _, r := range *a.Records {
			spans = append(spans, r.fact())
		}
		traces := make([]any, 0, len(a.Traces))
		for _, t := range a.Traces {
			traces = append(traces, map[string]any{"trace_id": t.TraceID, "span_count": t.SpanCount, "services": strList(t.Services), "root_count": t.RootCount, "error_count": t.ErrorCount})
		}
		fact := baseFact(*a.Matched, len(spans), a.Services, a.AttributeKeys, a.Stats.Refused)
		fact["spans"] = spans
		fact["traces"] = traces
		noun, tnoun := plural(*a.Matched, "span", "spans"), plural(len(traces), "trace", "traces")
		summary := fmt.Sprintf("spans (%s) -> %d %s of %d recorded in %d %s%s", describe(f), *a.Matched, noun, deref(a.Stats.Spans), len(traces), tnoun, overflowNote(fact))
		return protocol.CallResult{Fact: fact, Evidence: protocol.Evidence{Summary: summary, RequestExcerpt: excerpt, DurationMS: time.Since(start).Milliseconds()}}, nil
	}
	var a answer[logRecord]
	if err := readAnswer(raw, path, &a); err != nil {
		return protocol.CallResult{}, err
	}
	logs := make([]any, 0, len(*a.Records))
	for _, r := range *a.Records {
		logs = append(logs, r.fact())
	}
	fact := baseFact(*a.Matched, len(logs), a.Services, a.AttributeKeys, a.Stats.Refused)
	fact["logs"] = logs
	summary := fmt.Sprintf("logs (%s) -> %d %s of %d recorded%s", describe(f), *a.Matched, plural(*a.Matched, "log record", "log records"), deref(a.Stats.Logs), overflowNote(fact))
	return protocol.CallResult{Fact: fact, Evidence: protocol.Evidence{Summary: summary, RequestExcerpt: excerpt, DurationMS: time.Since(start).Milliseconds()}}, nil
}

func baseFact(matched, kept int, services, keys []string, r refused) map[string]any {
	fact := map[string]any{
		"count":          matched,
		"services":       strList(services),
		"attribute_keys": strList(keys),
		"overflow":       false,
	}
	var why []string
	if r.Requests+r.Spans+r.Logs > 0 {
		why = append(why, fmt.Sprintf("the receiver refused %d requests, %d spans and %d log records since it started, so records may be missing; restart it to clear this", r.Requests, r.Spans, r.Logs))
	}
	if matched > kept {
		why = append(why, fmt.Sprintf("%d records matched and the fact holds the first %d; filter more narrowly", matched, kept))
	}
	if len(why) > 0 {
		fact["overflow"] = true
		fact["overflow_reason"] = strings.Join(why, "; ")
	}
	return fact
}

func overflowNote(fact map[string]any) string {
	if fact["overflow"] == true {
		return " (overflow)"
	}
	return ""
}

func deref(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func describe(f filterArgs) string {
	var parts []string
	add := func(name string, v *string) {
		if v != nil {
			parts = append(parts, name+" "+*v)
		}
	}
	if f.TraceID != nil {
		parts = append(parts, "trace "+(*f.TraceID)[:8]+"…")
	}
	add("service", f.Service)
	add("name", f.Name)
	add("kind", f.Kind)
	add("status", f.Status)
	if f.SeverityAtLeast != nil {
		parts = append(parts, "severity at least "+*f.SeverityAtLeast)
	}
	if f.BodyContains != nil {
		parts = append(parts, "body text")
	}
	if len(f.Attributes) > 0 {
		parts = append(parts, "attributes "+strings.Join(sortedKeys(f.Attributes), ", "))
	}
	if len(f.AttributeContains) > 0 {
		parts = append(parts, "attribute text "+strings.Join(sortedKeys(f.AttributeContains), ", "))
	}
	if len(parts) == 0 {
		return "every record"
	}
	return strings.Join(parts, ", ")
}

func strList(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

func attrs(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func (r spanRecord) fact() map[string]any {
	events := make([]any, 0, len(r.Events))
	for _, e := range r.Events {
		events = append(events, map[string]any{"name": e.Name, "time": e.Time, "attributes": attrs(e.Attributes)})
	}
	links := make([]any, 0, len(r.Links))
	for _, l := range r.Links {
		links = append(links, map[string]any{"trace_id": l.TraceID, "span_id": l.SpanID, "attributes": attrs(l.Attributes)})
	}
	duration := r.DurationMS
	if duration == "" {
		duration = "0"
	}
	return map[string]any{
		"seq":            r.Seq,
		"received_at":    r.ReceivedAt,
		"service":        r.Service,
		"scope":          r.Scope,
		"resource":       attrs(r.Resource),
		"attributes":     attrs(r.Attributes),
		"trace_id":       r.TraceID,
		"span_id":        r.SpanID,
		"parent_span_id": r.ParentSpanID,
		"name":           r.Name,
		"kind":           r.Kind,
		"status":         map[string]any{"code": r.Status.Code, "message": r.Status.Message},
		"start_time":     r.StartTime,
		"end_time":       r.EndTime,
		"duration_ms":    duration,
		"events":         events,
		"links":          links,
		"dropped":        map[string]any{"attributes": r.Dropped.Attributes, "events": r.Dropped.Events, "links": r.Dropped.Links},
	}
}

func (r logRecord) fact() map[string]any {
	return map[string]any{
		"seq":             r.Seq,
		"received_at":     r.ReceivedAt,
		"service":         r.Service,
		"scope":           r.Scope,
		"resource":        attrs(r.Resource),
		"attributes":      attrs(r.Attributes),
		"time":            r.Time,
		"observed_time":   r.ObservedTime,
		"severity_number": r.SeverityNumber,
		"severity":        r.Severity,
		"severity_text":   r.SeverityText,
		"body":            r.Body,
		"trace_id":        r.TraceID,
		"span_id":         r.SpanID,
		"event_name":      r.EventName,
	}
}

func readAnswer[R any](raw []byte, path string, a *answer[R]) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(a); err != nil {
		return protocol.Errorf(protocol.CauseUnreadableResponse, "POST %s: the answer is not the receiver's records: %v", path, err)
	}
	if a.Matched == nil || a.Records == nil || a.Stats.Receiver != Receiver {
		return protocol.Errorf(protocol.CauseUnreadableResponse, "POST %s: the answer is not from %s", path, Receiver)
	}
	return nil
}

func (d *Driver) stats(ctx context.Context) (readyStats, error) {
	raw, err := d.do(ctx, http.MethodGet, StatsPath, nil)
	if err != nil {
		return readyStats{}, err
	}
	var st stats
	if err := json.Unmarshal(raw, &st); err != nil || st.Receiver != Receiver || st.Spans == nil || st.Logs == nil {
		return readyStats{}, protocol.Lastingf(protocol.CauseUnreadableResponse, "GET %s didn't answer as %s; the endpoint must be the receiver, not a collector", StatsPath, Receiver)
	}
	return readyStats{Receiver: st.Receiver, Version: st.Version, Spans: *st.Spans, Logs: *st.Logs}, nil
}

func (d *Driver) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, d.base.Scheme+"://"+d.base.Host+strings.TrimSuffix(d.base.EscapedPath(), "/")+path, rd)
	if err != nil {
		return nil, protocol.Lastingf(protocol.CauseInvalidRequest, "%s %s: %v", method, path, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, requestError(ctx, method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponse+1))
	if err != nil {
		return nil, requestError(ctx, method, path, err)
	}
	if len(raw) > MaxResponse {
		return nil, protocol.Errorf(protocol.CauseResponseTooLarge, "%s %s returned more than %d bytes; filter more narrowly", method, path, MaxResponse)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		detail := ""
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			detail = ": " + e.Error
		}
		if resp.StatusCode == http.StatusBadRequest && detail != "" {
			return nil, protocol.Errorf(protocol.CauseInvalidRequest, "%s %s answered 400%s", method, path, detail)
		}
		out := protocol.Errorf(protocol.CauseUnreadableResponse, "%s %s answered %d%s; the endpoint must be %s", method, path, resp.StatusCode, detail, Receiver)
		out.Data.Lasting = resp.StatusCode == http.StatusNotFound
		return nil, out
	}
	return raw, nil
}

func requestError(ctx context.Context, method, path string, err error) *protocol.Error {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || (errors.As(err, &ne) && ne.Timeout()) {
		return protocol.Errorf(protocol.CauseTimeout, "%s %s timed out", method, path)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return protocol.Errorf(protocol.CauseUnreachable, "%s %s failed: %v", method, path, err)
}
