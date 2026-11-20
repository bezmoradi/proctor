package otel

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bezmoradi/proctor/protocol"
	"github.com/bezmoradi/proctor/protocol/schema"
)

var Version = "0.0.0-dev"

const (
	Name         = "otel"
	OpSpans      = "spans"
	OpLogs       = "logs"
	Receiver     = "proctor-sim-otel"
	StatsPath    = "/__proctor/stats"
	SpansPath    = "/__proctor/spans"
	LogsPath     = "/__proctor/logs"
	MaxResponse  = 15 << 20
	MaxReturned  = 1000
	CallTimeout  = 10 * time.Second
	ReadyAttempt = 3 * time.Second
)

var (
	refRE    = regexp.MustCompile(`\$\{([^}]*)\}`)
	savedRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	valueRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*$`)
	fillRE   = regexp.MustCompile(`^fill\(`)
	secretRE = regexp.MustCompile(`^secret\.`)
)

var (
	SpanKinds   = []string{"unspecified", "internal", "server", "client", "producer", "consumer"}
	StatusCodes = []string{"unset", "ok", "error"}
	Severities  = []string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL"}
)

var (
	text       = map[string]any{"type": "string"}
	textList   = map[string]any{"type": "array", "items": text}
	count      = map[string]any{"type": "integer", "minimum": 0}
	freeObject = map[string]any{"type": "object"}
	scalar     = map[string]any{"type": []string{"string", "number", "boolean"}}
)

func filterProps(extra map[string]any) map[string]any {
	props := map[string]any{
		"trace_id":           text,
		"service":            text,
		"attributes":         map[string]any{"type": "object", "additionalProperties": scalar},
		"attribute_contains": map[string]any{"type": "object", "additionalProperties": text},
	}
	for k, v := range extra {
		props[k] = v
	}
	return props
}

var SpansArgsSchema = map[string]any{
	"type": []string{"object", "null"},
	"properties": filterProps(map[string]any{
		"name":   text,
		"kind":   map[string]any{"type": "string", "enum": SpanKinds},
		"status": map[string]any{"type": "string", "enum": StatusCodes},
	}),
	"additionalProperties": false,
}

var LogsArgsSchema = map[string]any{
	"type": []string{"object", "null"},
	"properties": filterProps(map[string]any{
		"severity_at_least": map[string]any{"type": "string", "enum": Severities},
		"body_contains":     text,
	}),
	"additionalProperties": false,
}

func object(props map[string]any) map[string]any {
	required := make([]string, 0, len(props))
	for k := range props {
		required = append(required, k)
	}
	sort.Strings(required)
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

var spanSchema = object(map[string]any{
	"seq":            map[string]any{"type": "integer", "minimum": 1},
	"received_at":    text,
	"service":        text,
	"scope":          text,
	"resource":       freeObject,
	"attributes":     freeObject,
	"trace_id":       text,
	"span_id":        text,
	"parent_span_id": text,
	"name":           text,
	"kind":           map[string]any{"type": "string", "enum": SpanKinds},
	"status": object(map[string]any{
		"code":    map[string]any{"type": "string", "enum": StatusCodes},
		"message": text,
	}),
	"start_time":  text,
	"end_time":    text,
	"duration_ms": map[string]any{"type": "number", "minimum": 0},
	"events": map[string]any{"type": "array", "items": object(map[string]any{
		"name":       text,
		"time":       text,
		"attributes": freeObject,
	})},
	"links": map[string]any{"type": "array", "items": object(map[string]any{
		"trace_id":   text,
		"span_id":    text,
		"attributes": freeObject,
	})},
	"dropped": object(map[string]any{
		"attributes": count,
		"events":     count,
		"links":      count,
	}),
})

var traceSchema = object(map[string]any{
	"trace_id":    text,
	"span_count":  count,
	"services":    textList,
	"root_count":  count,
	"error_count": count,
})

var logSchema = object(map[string]any{
	"seq":             map[string]any{"type": "integer", "minimum": 1},
	"received_at":     text,
	"service":         text,
	"scope":           text,
	"resource":        freeObject,
	"attributes":      freeObject,
	"time":            text,
	"observed_time":   text,
	"severity_number": map[string]any{"type": "integer"},
	"severity":        text,
	"severity_text":   text,
	"body":            true,
	"trace_id":        text,
	"span_id":         text,
	"event_name":      text,
})

func factSchema(list string, item any, extra map[string]any) map[string]any {
	props := map[string]any{
		"count":           count,
		list:              map[string]any{"type": "array", "items": item},
		"services":        textList,
		"attribute_keys":  textList,
		"overflow":        map[string]any{"type": "boolean"},
		"overflow_reason": text,
	}
	required := []string{"count", list, "services", "attribute_keys", "overflow"}
	for k, v := range extra {
		props[k] = v
		required = append(required, k)
	}
	sort.Strings(required)
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

var SpansFactSchema = factSchema("spans", spanSchema, map[string]any{"traces": map[string]any{"type": "array", "items": traceSchema}})

var LogsFactSchema = factSchema("logs", logSchema, nil)

var ConnectionSchema = map[string]any{
	"type": []string{"object", "null"},
	"properties": map[string]any{
		"endpoint": map[string]any{"type": "string", "pattern": `^https?://`},
	},
	"required":             []string{"endpoint"},
	"additionalProperties": false,
}

type connection struct {
	Endpoint string `json:"endpoint"`
}

type Driver struct {
	base   *url.URL
	client *http.Client
}

func New() *Driver {
	return &Driver{}
}

func (d *Driver) Hello(ctx context.Context, p protocol.HelloParams) (protocol.HelloResult, error) {
	return protocol.HelloResult{
		Name:     Name,
		Version:  Version,
		Protocol: protocol.Version,
		Operations: []protocol.Operation{{
			Name:        OpSpans,
			Kind:        protocol.KindObserve,
			ArgsSchema:  SpansArgsSchema,
			FactSchema:  SpansFactSchema,
			Credential:  protocol.CredentialNone,
			Accumulates: true,
			Scan:        []string{"$.spans"},
		}, {
			Name:        OpLogs,
			Kind:        protocol.KindObserve,
			ArgsSchema:  LogsArgsSchema,
			FactSchema:  LogsFactSchema,
			Credential:  protocol.CredentialNone,
			Accumulates: true,
			Scan:        []string{"$.logs"},
		}},
		PolicySchema:     nil,
		ConnectionSchema: ConnectionSchema,
	}, nil
}

func (d *Driver) Configure(ctx context.Context, p protocol.ConfigureParams) error {
	if p.Policy != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "the otel driver takes no policy")
	}
	if p.Connection == nil {
		return nil
	}
	if len(p.Secrets) > 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: the otel driver takes no secrets, so no key may end in _env")
	}
	if msgs := schema.Check(ConnectionSchema, p.Connection); len(msgs) > 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: %s", strings.Join(msgs, "; "))
	}
	var c connection
	if err := protocol.Decode(p.Connection, &c); err != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: %v", err)
	}
	base, err := url.Parse(c.Endpoint)
	switch {
	case err != nil:
		return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: endpoint: %v", err)
	case base.Scheme != "http" && base.Scheme != "https":
		return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: endpoint must be an http or https URL")
	case base.Host == "":
		return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: endpoint has no host")
	case base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.ForceQuery:
		return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: endpoint can't contain credentials, a query or a fragment")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	d.base = base
	d.client = &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return nil
}

type validation struct {
	r protocol.ValidateResult
}

func (v *validation) add(rule, path, format string, args ...any) {
	v.r.Problems = append(v.r.Problems, protocol.Problem{Rule: rule, Message: fmt.Sprintf(format, args...), Path: path})
}

func (d *Driver) Validate(ctx context.Context, p protocol.ValidateParams) (protocol.ValidateResult, error) {
	v := &validation{r: protocol.ValidateResult{Kind: protocol.KindObserve, Problems: []protocol.Problem{}, Refs: []string{}}}
	var textArgs []string
	switch p.Op {
	case OpSpans:
		textArgs = []string{"trace_id", "service", "name"}
		v.r.Targets = []string{"spans"}
	case OpLogs:
		textArgs = []string{"trace_id", "service", "body_contains"}
		v.r.Targets = []string{"logs"}
	default:
		v.add("EXE-01", "$.do", "unknown operation %q; the otel driver has %q and %q", p.Op, OpSpans, OpLogs)
		return v.r, nil
	}
	if p.Setup {
		v.add("FIX-03", "$.do", "reading telemetry is a verification step; setup goes through the application")
	}
	args := map[string]any{}
	if p.Args != nil {
		m, ok := p.Args.(map[string]any)
		if !ok {
			v.add("EXE-01", "$.with", "with must be an object of filters")
			return v.r, nil
		}
		args = m
	}
	scoped := false
	check := func(path string, s string) {
		names, err := references(s)
		if err != nil {
			v.add("HTTP-02", path, "%v", err)
			return
		}
		for _, n := range names {
			switch {
			case fillRE.MatchString(n), secretRE.MatchString(n):
				v.add("HTTP-02", path, "${%s} may be used only in a request body or request headers, not in a telemetry filter", n)
			case n == "run.id", savedRE.MatchString(n):
				scoped = true
			}
		}
	}
	for _, key := range textArgs {
		s, ok := args[key].(string)
		if !ok {
			continue
		}
		path := "$.with." + key
		if strings.TrimSpace(s) == "" {
			v.add("EXE-01", path, "%s must not be empty; leave it out to match every record", key)
			continue
		}
		check(path, s)
		if key == "trace_id" && !refRE.MatchString(s) {
			if raw, err := hex.DecodeString(s); err != nil || len(raw) != 16 || strings.Trim(s, "0") == "" {
				v.add("EXE-01", path, "trace_id %q must be 32 hex characters and not all zeros, or a saved value such as ${trace}", s)
			}
		}
	}
	for _, key := range []string{"kind", "status", "severity_at_least"} {
		if s, ok := args[key].(string); ok && refRE.MatchString(s) {
			v.add("EXE-01", "$.with."+key, "%s can't hold a reference; write the value itself", key)
		}
	}
	for _, key := range []string{"attributes", "attribute_contains"} {
		m, _ := args[key].(map[string]any)
		for _, name := range sortedKeys(m) {
			path := fmt.Sprintf("$.with.%s['%s']", key, name)
			if strings.TrimSpace(name) == "" {
				v.add("EXE-01", "$.with."+key, "%s has an empty attribute name", key)
			}
			if refRE.MatchString(name) {
				v.add("EXE-01", "$.with."+key, "attribute name %q can't hold a reference", name)
			}
			s, isText := m[name].(string)
			if !isText {
				continue
			}
			if key == "attribute_contains" && s == "" {
				v.add("EXE-01", path, "attribute_contains needs text to look for")
				continue
			}
			check(path, s)
		}
	}
	if _, has := args["trace_id"]; has {
		scoped = true
	}
	if !scoped {
		v.add("OTEL-01", "$.with", "otel.%s reads every record the receiver holds, other scenarios' and earlier runs' included; set trace_id (save it from an http.request with trace: true), or filter on a value that holds ${run.id} or a saved value", p.Op)
	}
	v.r.Refs = refs(args)
	return v.r, nil
}

func references(s string) ([]string, error) {
	var names []string
	for _, m := range refRE.FindAllStringSubmatch(s, -1) {
		n := m[1]
		if n != "run.id" && !savedRE.MatchString(n) && !valueRE.MatchString(n) && !fillRE.MatchString(n) && !secretRE.MatchString(n) {
			return nil, fmt.Errorf("unsupported reference ${%s}: supported are ${run.id}, ${<identity>.<value>} and ${name} for values saved by earlier steps", n)
		}
		names = append(names, n)
	}
	if strings.Count(s, "${") != len(names) {
		return nil, fmt.Errorf("unclosed ${ in %q", s)
	}
	return names, nil
}

func refs(v any) []string {
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			for _, m := range refRE.FindAllStringSubmatch(x, -1) {
				seen[m[1]] = true
			}
		case map[string]any:
			for k, e := range x {
				walk(k)
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (d *Driver) Prove(ctx context.Context) (protocol.ProveResult, error) {
	return protocol.ProveResult{Proofs: []protocol.Proof{}}, nil
}

func (d *Driver) Ready(ctx context.Context) (protocol.ReadyResult, error) {
	if d.client == nil {
		return protocol.ReadyResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the otel driver has no connection")
	}
	ctx, cancel := context.WithTimeout(ctx, ReadyAttempt)
	defer cancel()
	st, err := d.stats(ctx)
	if err != nil {
		var e *protocol.Error
		if errors.As(err, &e) {
			return protocol.ReadyResult{Detail: e.Message, Lasting: e.Data.Lasting}, nil
		}
		return protocol.ReadyResult{Detail: err.Error()}, nil
	}
	return protocol.ReadyResult{Ready: true, Detail: fmt.Sprintf("GET %s answered: %s %s holds %d spans and %d log records", StatsPath, st.Receiver, st.Version, st.Spans, st.Logs)}, nil
}

func (d *Driver) Close(ctx context.Context, p protocol.CloseParams) error {
	return protocol.Errorf(protocol.CauseInvalidRequest, "the otel driver opens no handles")
}
