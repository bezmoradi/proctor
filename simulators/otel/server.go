package otel

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	Name         = "proctor-sim-otel"
	TracesPath   = "/v1/traces"
	LogsPath     = "/v1/logs"
	MetricsPath  = "/v1/metrics"
	StatsPath    = "/__proctor/stats"
	SpansPath    = "/__proctor/spans"
	LogsReadPath = "/__proctor/logs"
	MaxRequest   = 64 << 20
	MaxSpans     = 100000
	MaxLogs      = 100000
	MaxBytes     = 256 << 20
	MaxReturned  = 1000
	MaxFilter    = 1 << 20
)

type signal int

const (
	sigTraces signal = iota
	sigLogs
	sigMetrics
)

func (s signal) String() string {
	switch s {
	case sigTraces:
		return "traces"
	case sigLogs:
		return "logs"
	}
	return "metrics"
}

var grpcMethods = map[string]signal{
	"/opentelemetry.proto.collector.trace.v1.TraceService/Export":     sigTraces,
	"/opentelemetry.proto.collector.logs.v1.LogsService/Export":       sigLogs,
	"/opentelemetry.proto.collector.metrics.v1.MetricsService/Export": sigMetrics,
}

type Refused struct {
	Requests        uint64 `json:"requests"`
	Spans           uint64 `json:"spans"`
	Logs            uint64 `json:"logs"`
	MetricsRequests uint64 `json:"metrics_requests"`
}

type Stats struct {
	Receiver        string            `json:"receiver"`
	Version         string            `json:"version"`
	Spans           int               `json:"spans"`
	Logs            int               `json:"logs"`
	Bytes           int64             `json:"bytes"`
	NextSpanSeq     uint64            `json:"next_span_seq"`
	NextLogSeq      uint64            `json:"next_log_seq"`
	Refused         Refused           `json:"refused"`
	MetricsRequests uint64            `json:"metrics_requests"`
	Services        map[string]string `json:"services"`
}

type Trace struct {
	TraceID    string   `json:"trace_id"`
	SpanCount  int      `json:"span_count"`
	Services   []string `json:"services"`
	RootCount  int      `json:"root_count"`
	ErrorCount int      `json:"error_count"`
}

type Server struct {
	version  string
	logger   *log.Logger
	mu       sync.Mutex
	spans    []*Span
	logs     []*Log
	byTrace  map[string][]*Span
	bytes    int64
	nextSpan uint64
	nextLog  uint64
	refused  Refused
	metrics  uint64
	services map[string]string
}

func New(version string, logger *log.Logger) *Server {
	return &Server{version: version, logger: logger, byTrace: map[string][]*Span{}, nextSpan: 1, nextLog: 1, services: map[string]string{}}
}

func (s *Server) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case TracesPath:
		s.export(w, r, sigTraces)
	case LogsPath:
		s.export(w, r, sigLogs)
	case MetricsPath:
		s.export(w, r, sigMetrics)
	case StatsPath:
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.mu.Lock()
		st := s.statsLocked()
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, st)
	case SpansPath, LogsReadPath:
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == SpansPath {
			s.querySpans(w, r)
		} else {
			s.queryLogs(w, r)
		}
	default:
		if sig, ok := grpcMethods[r.URL.Path]; ok {
			s.refuse(sig, "%s %s: OTLP/gRPC isn't supported", r.Method, r.URL.Path)
			writeStatus(w, encJSON, grpcFault())
			return
		}
		http.Error(w, fmt.Sprintf("%s answers POST %s, %s and %s (OTLP/HTTP) and the read API under /__proctor/; %s is not one of them", Name, TracesPath, LogsPath, MetricsPath, r.URL.Path), http.StatusNotFound)
	}
}

func grpcFault() *fault {
	return &fault{http.StatusUnsupportedMediaType, codeInvalidArgument, Name + " accepts OTLP/HTTP only (application/x-protobuf or application/json), not OTLP/gRPC; export over HTTP, or forward through a collector whose otlphttp exporter points at the receiver"}
}

func (s *Server) refuse(sig signal, format string, args ...any) {
	s.mu.Lock()
	if sig == sigMetrics {
		s.refused.MetricsRequests++
	} else {
		s.refused.Requests++
	}
	s.mu.Unlock()
	s.logf("refused a %s request: %s", sig, fmt.Sprintf(format, args...))
}

func (s *Server) export(w http.ResponseWriter, r *http.Request, sig signal) {
	path := r.URL.Path
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ct := r.Header.Get("Content-Type")
	if isGRPC(ct) {
		s.refuse(sig, "POST %s with Content-Type %q: OTLP/gRPC isn't supported", path, ct)
		writeStatus(w, encJSON, grpcFault())
		return
	}
	enc, ok := encodingOf(ct)
	if !ok {
		f := &fault{http.StatusUnsupportedMediaType, codeInvalidArgument, fmt.Sprintf("Content-Type %q is not supported; send application/x-protobuf or application/json", ct)}
		s.refuse(sig, "POST %s: %s", path, f.msg)
		writeStatus(w, encJSON, f)
		return
	}
	ce := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	if ce != "" && ce != "gzip" && ce != "identity" {
		f := &fault{http.StatusUnsupportedMediaType, codeInvalidArgument, fmt.Sprintf("Content-Encoding %q is not supported; send gzip or none", ce)}
		s.refuse(sig, "POST %s: %s", path, f.msg)
		writeStatus(w, enc, f)
		return
	}
	body, f := readBody(w, r, ce == "gzip")
	if f != nil {
		s.refuse(sig, "POST %s: %s", path, f.msg)
		writeStatus(w, enc, f)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	switch sig {
	case sigMetrics:
		s.mu.Lock()
		s.metrics++
		s.mu.Unlock()
		writeExport(w, enc, sig, 0, "")
	case sigTraces:
		td, f := decodeTraces(body, enc)
		var spans []*Span
		if f == nil {
			spans, f = normalizeSpans(td, now)
		}
		if f != nil {
			s.refuse(sig, "POST %s: %s", path, f.msg)
			writeStatus(w, enc, f)
			return
		}
		rejected, why := s.addSpans(spans, now)
		if rejected > 0 {
			s.logf("refused %d of %d spans: %s", rejected, len(spans), why)
		}
		writeExport(w, enc, sig, rejected, why)
	case sigLogs:
		ld, f := decodeLogs(body, enc)
		var logs []*Log
		if f == nil {
			logs, f = normalizeLogs(ld, now)
		}
		if f != nil {
			s.refuse(sig, "POST %s: %s", path, f.msg)
			writeStatus(w, enc, f)
			return
		}
		rejected, why := s.addLogs(logs, now)
		if rejected > 0 {
			s.logf("refused %d of %d log records: %s", rejected, len(logs), why)
		}
		writeExport(w, enc, sig, rejected, why)
	}
}

func fullMessage(noun string, max int) string {
	return fmt.Sprintf("%s keeps at most %d %s and %d bytes of records, and keeps the oldest; restart it to empty it", Name, max, noun, MaxBytes)
}

func (s *Server) addSpans(spans []*Span, now string) (int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rejected := 0
	for _, sp := range spans {
		sp.size = sizeOf(sp)
		if len(s.spans) >= MaxSpans || s.bytes+sp.size > MaxBytes {
			rejected++
			continue
		}
		sp.Seq = s.nextSpan
		s.nextSpan++
		s.bytes += sp.size
		s.spans = append(s.spans, sp)
		s.byTrace[sp.TraceID] = append(s.byTrace[sp.TraceID], sp)
		s.services[sp.Service] = now
	}
	s.refused.Spans += uint64(rejected)
	if rejected == 0 {
		return 0, ""
	}
	return rejected, fullMessage("spans", MaxSpans)
}

func (s *Server) addLogs(logs []*Log, now string) (int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rejected := 0
	for _, lr := range logs {
		lr.size = sizeOf(lr)
		if len(s.logs) >= MaxLogs || s.bytes+lr.size > MaxBytes {
			rejected++
			continue
		}
		lr.Seq = s.nextLog
		s.nextLog++
		s.bytes += lr.size
		s.logs = append(s.logs, lr)
		s.services[lr.Service] = now
	}
	s.refused.Logs += uint64(rejected)
	if rejected == 0 {
		return 0, ""
	}
	return rejected, fullMessage("log records", MaxLogs)
}

func (s *Server) statsLocked() Stats {
	services := make(map[string]string, len(s.services))
	for k, v := range s.services {
		services[k] = v
	}
	return Stats{
		Receiver:        Name,
		Version:         s.version,
		Spans:           len(s.spans),
		Logs:            len(s.logs),
		Bytes:           s.bytes,
		NextSpanSeq:     s.nextSpan,
		NextLogSeq:      s.nextLog,
		Refused:         s.refused,
		MetricsRequests: s.metrics,
		Services:        services,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func filterError(w http.ResponseWriter, format string, args ...any) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf(format, args...)})
}

func readFilter(w http.ResponseWriter, r *http.Request, out any) bool {
	b, err := io.ReadAll(io.LimitReader(r.Body, MaxFilter+1))
	if err != nil {
		filterError(w, "the filter could not be read: %v", err)
		return false
	}
	if len(b) > MaxFilter {
		filterError(w, "the filter is larger than %d bytes", MaxFilter)
		return false
	}
	if len(bytes.TrimSpace(b)) == 0 {
		b = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		filterError(w, "the filter is not valid: %v", err)
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		filterError(w, "the filter has data after its JSON value")
		return false
	}
	return true
}

type common struct {
	TraceID           *string           `json:"trace_id"`
	Service           *string           `json:"service"`
	Attributes        map[string]any    `json:"attributes"`
	AttributeContains map[string]string `json:"attribute_contains"`
	After             *uint64           `json:"after"`
	All               bool              `json:"all"`
}

func (c *common) check() error {
	if c.TraceID != nil {
		t := strings.ToLower(*c.TraceID)
		if raw, err := hex.DecodeString(t); err != nil || len(raw) != 16 {
			return fmt.Errorf("trace_id %q must be 32 hex characters", *c.TraceID)
		}
		c.TraceID = &t
	}
	for k, v := range c.Attributes {
		switch v.(type) {
		case string, bool, json.Number:
		default:
			return fmt.Errorf("attributes[%q] must be text, a number or true or false", k)
		}
	}
	return nil
}

func (c *common) matches(seq uint64, traceID, service string, attrs map[string]any) bool {
	switch {
	case c.After != nil && seq <= *c.After:
		return false
	case c.TraceID != nil && traceID != *c.TraceID:
		return false
	case c.Service != nil && service != *c.Service:
		return false
	}
	return matchAttributes(attrs, c.Attributes, c.AttributeContains)
}

type spanFilter struct {
	common
	Name   *string `json:"name"`
	Kind   *string `json:"kind"`
	Status *string `json:"status"`
}

type logFilter struct {
	common
	SeverityAtLeast *string `json:"severity_at_least"`
	BodyContains    *string `json:"body_contains"`
}

type SpanAnswer struct {
	Matched       int      `json:"matched"`
	Records       []*Span  `json:"records"`
	Traces        []Trace  `json:"traces"`
	Services      []string `json:"services"`
	AttributeKeys []string `json:"attribute_keys"`
	Stats         Stats    `json:"stats"`
}

type LogAnswer struct {
	Matched       int      `json:"matched"`
	Records       []*Log   `json:"records"`
	Services      []string `json:"services"`
	AttributeKeys []string `json:"attribute_keys"`
	Stats         Stats    `json:"stats"`
}

func (s *Server) querySpans(w http.ResponseWriter, r *http.Request) {
	var f spanFilter
	if !readFilter(w, r, &f) {
		return
	}
	if err := f.check(); err != nil {
		filterError(w, "%v", err)
		return
	}
	if f.Kind != nil && !slices.Contains(SpanKinds, *f.Kind) {
		filterError(w, "kind %q must be one of %s", *f.Kind, strings.Join(SpanKinds, ", "))
		return
	}
	if f.Status != nil && !slices.Contains(StatusCodes, *f.Status) {
		filterError(w, "status %q must be one of %s", *f.Status, strings.Join(StatusCodes, ", "))
		return
	}
	s.mu.Lock()
	pool := s.spans
	if f.TraceID != nil {
		pool = s.byTrace[*f.TraceID]
	}
	ans := SpanAnswer{Records: []*Span{}, Traces: []Trace{}}
	services, keys := map[string]bool{}, map[string]bool{}
	var order []string
	inTraces := map[string]bool{}
	for _, sp := range pool {
		if !f.matches(sp.Seq, sp.TraceID, sp.Service, sp.Attributes) {
			continue
		}
		if (f.Name != nil && sp.Name != *f.Name) || (f.Kind != nil && sp.Kind != *f.Kind) || (f.Status != nil && sp.Status.Code != *f.Status) {
			continue
		}
		ans.Matched++
		if f.All || len(ans.Records) < MaxReturned {
			ans.Records = append(ans.Records, sp)
		}
		services[sp.Service] = true
		addKeys(keys, sp.Attributes, sp.Resource)
		for _, ev := range sp.Events {
			addKeys(keys, ev.Attributes)
		}
		for _, ln := range sp.Links {
			addKeys(keys, ln.Attributes)
		}
		if !inTraces[sp.TraceID] && (f.All || len(order) < MaxReturned) {
			inTraces[sp.TraceID] = true
			order = append(order, sp.TraceID)
		}
	}
	for _, id := range order {
		ans.Traces = append(ans.Traces, s.summarize(id))
	}
	ans.Services, ans.AttributeKeys = sortedSet(services), sortedSet(keys)
	ans.Stats = s.statsLocked()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, ans)
}

func (s *Server) summarize(id string) Trace {
	spans := s.byTrace[id]
	ids := make(map[string]bool, len(spans))
	for _, sp := range spans {
		ids[sp.SpanID] = true
	}
	t := Trace{TraceID: id, SpanCount: len(spans)}
	services := map[string]bool{}
	for _, sp := range spans {
		services[sp.Service] = true
		if sp.ParentSpanID == "" || !ids[sp.ParentSpanID] {
			t.RootCount++
		}
		if sp.Status.Code == "error" {
			t.ErrorCount++
		}
	}
	t.Services = sortedSet(services)
	return t
}

func (s *Server) queryLogs(w http.ResponseWriter, r *http.Request) {
	var f logFilter
	if !readFilter(w, r, &f) {
		return
	}
	if err := f.check(); err != nil {
		filterError(w, "%v", err)
		return
	}
	var least int32
	if f.SeverityAtLeast != nil {
		if least = severityLevel(*f.SeverityAtLeast); least == 0 {
			filterError(w, "severity_at_least %q must be one of %s", *f.SeverityAtLeast, strings.Join(Severities, ", "))
			return
		}
	}
	s.mu.Lock()
	ans := LogAnswer{Records: []*Log{}}
	services, keys := map[string]bool{}, map[string]bool{}
	for _, lr := range s.logs {
		if !f.matches(lr.Seq, lr.TraceID, lr.Service, lr.Attributes) {
			continue
		}
		if least > 0 && lr.level < least {
			continue
		}
		if f.BodyContains != nil && !strings.Contains(bodyText(lr.Body), *f.BodyContains) {
			continue
		}
		ans.Matched++
		if f.All || len(ans.Records) < MaxReturned {
			ans.Records = append(ans.Records, lr)
		}
		services[lr.Service] = true
		addKeys(keys, lr.Attributes, lr.Resource)
	}
	ans.Services, ans.AttributeKeys = sortedSet(services), sortedSet(keys)
	ans.Stats = s.statsLocked()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, ans)
}
