package otel

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

const maxSafeInt = 1 << 53

type Status struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Event struct {
	Name       string         `json:"name"`
	Time       string         `json:"time"`
	Attributes map[string]any `json:"attributes"`
}

type Link struct {
	TraceID    string         `json:"trace_id"`
	SpanID     string         `json:"span_id"`
	Attributes map[string]any `json:"attributes"`
}

type Dropped struct {
	Attributes uint32 `json:"attributes"`
	Events     uint32 `json:"events"`
	Links      uint32 `json:"links"`
}

type Span struct {
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
	Status       Status         `json:"status"`
	StartTime    string         `json:"start_time"`
	EndTime      string         `json:"end_time"`
	DurationMS   float64        `json:"duration_ms"`
	Events       []Event        `json:"events"`
	Links        []Link         `json:"links"`
	Dropped      Dropped        `json:"dropped"`
	size         int64
}

type Log struct {
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
	size           int64
	level          int32
}

var spanKinds = map[tracepb.Span_SpanKind]string{
	tracepb.Span_SPAN_KIND_UNSPECIFIED: "unspecified",
	tracepb.Span_SPAN_KIND_INTERNAL:    "internal",
	tracepb.Span_SPAN_KIND_SERVER:      "server",
	tracepb.Span_SPAN_KIND_CLIENT:      "client",
	tracepb.Span_SPAN_KIND_PRODUCER:    "producer",
	tracepb.Span_SPAN_KIND_CONSUMER:    "consumer",
}

var statusCodes = map[tracepb.Status_StatusCode]string{
	tracepb.Status_STATUS_CODE_UNSET: "unset",
	tracepb.Status_STATUS_CODE_OK:    "ok",
	tracepb.Status_STATUS_CODE_ERROR: "error",
}

var SpanKinds = []string{"unspecified", "internal", "server", "client", "producer", "consumer"}

var StatusCodes = []string{"unset", "ok", "error"}

var Severities = []string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL"}

func severityName(n int32) string {
	if n < 1 || n > 24 {
		return ""
	}
	return Severities[(n-1)/4]
}

func severityLevel(name string) int32 {
	for i, s := range Severities {
		if s == name {
			return int32(i*4 + 1)
		}
	}
	return 0
}

func checkID(raw []byte, size int, at string, optional bool) (string, *fault) {
	if len(raw) == 0 && optional {
		return "", nil
	}
	if len(raw) != size {
		return "", badData("%s must be %d bytes (%d hex characters), got %d bytes", at, size, size*2, len(raw))
	}
	if bytes.Equal(raw, make([]byte, size)) {
		return "", badData("%s is all zeros, which OTLP treats as invalid", at)
	}
	return hex.EncodeToString(raw), nil
}

func stamp(ns uint64) string {
	if ns == 0 {
		return ""
	}
	return time.Unix(0, int64(ns)).UTC().Format(time.RFC3339Nano)
}

func value(v *commonpb.AnyValue) any {
	if v == nil {
		return nil
	}
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_BoolValue:
		return x.BoolValue
	case *commonpb.AnyValue_IntValue:
		if x.IntValue > maxSafeInt || x.IntValue < -maxSafeInt {
			return fmt.Sprint(x.IntValue)
		}
		return x.IntValue
	case *commonpb.AnyValue_DoubleValue:
		switch {
		case math.IsNaN(x.DoubleValue):
			return "NaN"
		case math.IsInf(x.DoubleValue, 1):
			return "Infinity"
		case math.IsInf(x.DoubleValue, -1):
			return "-Infinity"
		}
		return x.DoubleValue
	case *commonpb.AnyValue_BytesValue:
		return base64.StdEncoding.EncodeToString(x.BytesValue)
	case *commonpb.AnyValue_ArrayValue:
		out := []any{}
		for _, e := range x.ArrayValue.GetValues() {
			out = append(out, value(e))
		}
		return out
	case *commonpb.AnyValue_KvlistValue:
		out := map[string]any{}
		for _, kv := range x.KvlistValue.GetValues() {
			out[kv.GetKey()] = value(kv.GetValue())
		}
		return out
	}
	return nil
}

func flatten(kvs []*commonpb.KeyValue) map[string]any {
	out := map[string]any{}
	var walk func(prefix string, kvs []*commonpb.KeyValue)
	walk = func(prefix string, kvs []*commonpb.KeyValue) {
		for _, kv := range kvs {
			key := prefix + kv.GetKey()
			if l, ok := kv.GetValue().GetValue().(*commonpb.AnyValue_KvlistValue); ok {
				walk(key+".", l.KvlistValue.GetValues())
				continue
			}
			out[key] = value(kv.GetValue())
		}
	}
	walk("", kvs)
	return out
}

func serviceOf(res map[string]any) string {
	if s, ok := res["service.name"].(string); ok && s != "" {
		return s
	}
	return "unknown_service"
}

func resourceOf(r *resourcepb.Resource) map[string]any {
	return flatten(r.GetAttributes())
}

func normalizeSpans(td *tracepb.TracesData, now string) ([]*Span, *fault) {
	var out []*Span
	for i, rs := range td.GetResourceSpans() {
		res := resourceOf(rs.GetResource())
		service := serviceOf(res)
		for j, ss := range rs.GetScopeSpans() {
			scope := ss.GetScope().GetName()
			for k, sp := range ss.GetSpans() {
				at := fmt.Sprintf("resourceSpans[%d].scopeSpans[%d].spans[%d]", i, j, k)
				tid, f := checkID(sp.GetTraceId(), 16, at+".traceId", false)
				if f != nil {
					return nil, f
				}
				sid, f := checkID(sp.GetSpanId(), 8, at+".spanId", false)
				if f != nil {
					return nil, f
				}
				pid, f := checkID(sp.GetParentSpanId(), 8, at+".parentSpanId", true)
				if f != nil {
					return nil, f
				}
				s := &Span{
					ReceivedAt:   now,
					Service:      service,
					Scope:        scope,
					Resource:     res,
					Attributes:   flatten(sp.GetAttributes()),
					TraceID:      tid,
					SpanID:       sid,
					ParentSpanID: pid,
					Name:         sp.GetName(),
					Kind:         spanKinds[sp.GetKind()],
					Status:       Status{Code: statusCodes[sp.GetStatus().GetCode()], Message: sp.GetStatus().GetMessage()},
					StartTime:    stamp(sp.GetStartTimeUnixNano()),
					EndTime:      stamp(sp.GetEndTimeUnixNano()),
					Events:       []Event{},
					Links:        []Link{},
					Dropped:      Dropped{Attributes: sp.GetDroppedAttributesCount(), Events: sp.GetDroppedEventsCount(), Links: sp.GetDroppedLinksCount()},
				}
				if s.Kind == "" {
					s.Kind = "unspecified"
				}
				if s.Status.Code == "" {
					s.Status.Code = "unset"
				}
				if st, en := sp.GetStartTimeUnixNano(), sp.GetEndTimeUnixNano(); st > 0 && en >= st {
					s.DurationMS = float64(en-st) / 1e6
				}
				for _, ev := range sp.GetEvents() {
					s.Events = append(s.Events, Event{Name: ev.GetName(), Time: stamp(ev.GetTimeUnixNano()), Attributes: flatten(ev.GetAttributes())})
				}
				for l, ln := range sp.GetLinks() {
					lat := fmt.Sprintf("%s.links[%d]", at, l)
					ltid, f := checkID(ln.GetTraceId(), 16, lat+".traceId", false)
					if f != nil {
						return nil, f
					}
					lsid, f := checkID(ln.GetSpanId(), 8, lat+".spanId", false)
					if f != nil {
						return nil, f
					}
					s.Links = append(s.Links, Link{TraceID: ltid, SpanID: lsid, Attributes: flatten(ln.GetAttributes())})
				}
				out = append(out, s)
			}
		}
	}
	return out, nil
}

func normalizeLogs(ld *logspb.LogsData, now string) ([]*Log, *fault) {
	var out []*Log
	for i, rl := range ld.GetResourceLogs() {
		res := resourceOf(rl.GetResource())
		service := serviceOf(res)
		for j, sl := range rl.GetScopeLogs() {
			scope := sl.GetScope().GetName()
			for k, lr := range sl.GetLogRecords() {
				at := fmt.Sprintf("resourceLogs[%d].scopeLogs[%d].logRecords[%d]", i, j, k)
				tid, f := checkID(lr.GetTraceId(), 16, at+".traceId", true)
				if f != nil {
					return nil, f
				}
				sid, f := checkID(lr.GetSpanId(), 8, at+".spanId", true)
				if f != nil {
					return nil, f
				}
				n := int32(lr.GetSeverityNumber())
				sev := severityName(n)
				if sev == "" {
					sev = strings.ToUpper(strings.TrimSpace(lr.GetSeverityText()))
				}
				level := n
				if n < 1 || n > 24 {
					level = severityLevel(sev)
				}
				out = append(out, &Log{
					ReceivedAt:     now,
					Service:        service,
					Scope:          scope,
					Resource:       res,
					Attributes:     flatten(lr.GetAttributes()),
					Time:           stamp(lr.GetTimeUnixNano()),
					ObservedTime:   stamp(lr.GetObservedTimeUnixNano()),
					SeverityNumber: n,
					Severity:       sev,
					SeverityText:   lr.GetSeverityText(),
					Body:           value(lr.GetBody()),
					TraceID:        tid,
					SpanID:         sid,
					EventName:      lr.GetEventName(),
					level:          level,
				})
			}
		}
	}
	return out, nil
}

func sizeOf(v any) int64 {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return int64(len(b))
}

func bodyText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func sameValue(stored, want any) bool {
	switch w := want.(type) {
	case string:
		s, ok := stored.(string)
		return ok && s == w
	case bool:
		b, ok := stored.(bool)
		return ok && b == w
	case json.Number:
		r, ok := new(big.Rat).SetString(w.String())
		if !ok {
			return false
		}
		var got *big.Rat
		switch x := stored.(type) {
		case int64:
			got = new(big.Rat).SetInt64(x)
		case float64:
			got = new(big.Rat)
			if got.SetFloat64(x) == nil {
				return false
			}
		default:
			return false
		}
		return got.Cmp(r) == 0
	}
	return false
}

func matchAttributes(attrs map[string]any, equal map[string]any, contains map[string]string) bool {
	for k, want := range equal {
		got, ok := attrs[k]
		if !ok || !sameValue(got, want) {
			return false
		}
	}
	for k, part := range contains {
		got, ok := attrs[k].(string)
		if !ok || !strings.Contains(got, part) {
			return false
		}
	}
	return true
}

func addKeys(seen map[string]bool, maps ...map[string]any) {
	for _, m := range maps {
		for k := range m {
			seen[k] = true
		}
	}
}

func sortedSet(seen map[string]bool) []string {
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
