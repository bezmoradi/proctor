package otel

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

const (
	encProto = "application/x-protobuf"
	encJSON  = "application/json"
)

const (
	codeInvalidArgument   = 3
	codeResourceExhausted = 8
)

type fault struct {
	status int
	code   int
	msg    string
}

func (f *fault) Error() string { return f.msg }

func badData(format string, args ...any) *fault {
	return &fault{http.StatusBadRequest, codeInvalidArgument, fmt.Sprintf(format, args...)}
}

func encodingOf(contentType string) (string, bool) {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", false
	}
	switch mt {
	case encProto, encJSON:
		return mt, true
	}
	return mt, false
}

func readBody(w http.ResponseWriter, r *http.Request, gzipped bool) ([]byte, *fault) {
	tooLarge := &fault{http.StatusRequestEntityTooLarge, codeResourceExhausted, fmt.Sprintf("the request is larger than %d bytes after decompression", MaxRequest)}
	var src io.Reader = http.MaxBytesReader(w, r.Body, MaxRequest)
	if gzipped {
		zr, err := gzip.NewReader(src)
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return nil, tooLarge
			}
			return nil, badData("the body is not valid gzip: %v", err)
		}
		defer zr.Close()
		src = zr
	}
	b, err := io.ReadAll(io.LimitReader(src, MaxRequest+1))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, tooLarge
		}
		if gzipped {
			return nil, badData("the body is not valid gzip: %v", err)
		}
		return nil, badData("the body could not be read: %v", err)
	}
	if len(b) > MaxRequest {
		return nil, tooLarge
	}
	return b, nil
}

var unmarshalJSON = protojson.UnmarshalOptions{DiscardUnknown: true}

func decodeTraces(b []byte, enc string) (*tracepb.TracesData, *fault) {
	td := &tracepb.TracesData{}
	if enc == encProto {
		if err := proto.Unmarshal(b, td); err != nil {
			return nil, badData("the body is not an ExportTraceServiceRequest in protobuf: %v", err)
		}
		return td, nil
	}
	fixed, f := hexIDsToBase64(b, sigTraces)
	if f != nil {
		return nil, f
	}
	if err := unmarshalJSON.Unmarshal(fixed, td); err != nil {
		return nil, badData("the body is not an ExportTraceServiceRequest in OTLP/JSON: %v", err)
	}
	return td, nil
}

func decodeLogs(b []byte, enc string) (*logspb.LogsData, *fault) {
	ld := &logspb.LogsData{}
	if enc == encProto {
		if err := proto.Unmarshal(b, ld); err != nil {
			return nil, badData("the body is not an ExportLogsServiceRequest in protobuf: %v", err)
		}
		return ld, nil
	}
	fixed, f := hexIDsToBase64(b, sigLogs)
	if f != nil {
		return nil, f
	}
	if err := unmarshalJSON.Unmarshal(fixed, ld); err != nil {
		return nil, badData("the body is not an ExportLogsServiceRequest in OTLP/JSON: %v", err)
	}
	return ld, nil
}

func field(m map[string]any, camel, snake string) (any, string) {
	if v, ok := m[camel]; ok {
		return v, camel
	}
	if v, ok := m[snake]; ok {
		return v, snake
	}
	return nil, ""
}

func objects(v any) []map[string]any {
	l, _ := v.([]any)
	out := make([]map[string]any, 0, len(l))
	for _, e := range l {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func hexIDsToBase64(b []byte, sig signal) ([]byte, *fault) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, badData("the body is not JSON: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, badData("the body has data after its JSON value")
	}
	top, ok := root.(map[string]any)
	if !ok {
		return nil, badData("the body must be a JSON object")
	}
	var f *fault
	fix := func(m map[string]any, at, camel, snake string) {
		if f != nil {
			return
		}
		v, key := field(m, camel, snake)
		if key == "" || v == nil {
			return
		}
		s, isText := v.(string)
		if !isText {
			f = badData("%s.%s must be a hex string", at, camel)
			return
		}
		raw, err := hex.DecodeString(s)
		if err != nil {
			f = badData("%s.%s %q is not hex; OTLP/JSON writes trace and span ids as hex, not base64", at, camel, s)
			return
		}
		m[key] = base64.StdEncoding.EncodeToString(raw)
	}
	if sig == sigTraces {
		rs, _ := field(top, "resourceSpans", "resource_spans")
		for i, r := range objects(rs) {
			ss, _ := field(r, "scopeSpans", "scope_spans")
			for j, s := range objects(ss) {
				spans, _ := field(s, "spans", "spans")
				for k, sp := range objects(spans) {
					at := fmt.Sprintf("resourceSpans[%d].scopeSpans[%d].spans[%d]", i, j, k)
					fix(sp, at, "traceId", "trace_id")
					fix(sp, at, "spanId", "span_id")
					fix(sp, at, "parentSpanId", "parent_span_id")
					links, _ := field(sp, "links", "links")
					for l, ln := range objects(links) {
						lat := fmt.Sprintf("%s.links[%d]", at, l)
						fix(ln, lat, "traceId", "trace_id")
						fix(ln, lat, "spanId", "span_id")
					}
				}
			}
		}
	} else {
		rl, _ := field(top, "resourceLogs", "resource_logs")
		for i, r := range objects(rl) {
			sl, _ := field(r, "scopeLogs", "scope_logs")
			for j, s := range objects(sl) {
				recs, _ := field(s, "logRecords", "log_records")
				for k, lr := range objects(recs) {
					at := fmt.Sprintf("resourceLogs[%d].scopeLogs[%d].logRecords[%d]", i, j, k)
					fix(lr, at, "traceId", "trace_id")
					fix(lr, at, "spanId", "span_id")
				}
			}
		}
	}
	if f != nil {
		return nil, f
	}
	out, err := json.Marshal(top)
	if err != nil {
		return nil, badData("the body can't be read: %v", err)
	}
	return out, nil
}

func writeExport(w http.ResponseWriter, enc string, sig signal, rejected int, msg string) {
	w.Header().Set("Content-Type", enc)
	if enc == encProto {
		var body []byte
		if rejected > 0 || msg != "" {
			var inner []byte
			inner = protowire.AppendTag(inner, 1, protowire.VarintType)
			inner = protowire.AppendVarint(inner, uint64(rejected))
			inner = protowire.AppendTag(inner, 2, protowire.BytesType)
			inner = protowire.AppendString(inner, msg)
			body = protowire.AppendTag(body, 1, protowire.BytesType)
			body = protowire.AppendBytes(body, inner)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}
	out := map[string]any{}
	if rejected > 0 || msg != "" {
		key := "rejectedSpans"
		switch sig {
		case sigLogs:
			key = "rejectedLogRecords"
		case sigMetrics:
			key = "rejectedDataPoints"
		}
		out["partialSuccess"] = map[string]any{key: fmt.Sprint(rejected), "errorMessage": msg}
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
}

func writeStatus(w http.ResponseWriter, enc string, f *fault) {
	if enc != encProto {
		enc = encJSON
	}
	w.Header().Set("Content-Type", enc)
	w.WriteHeader(f.status)
	if enc == encProto {
		var body []byte
		body = protowire.AppendTag(body, 1, protowire.VarintType)
		body = protowire.AppendVarint(body, uint64(f.code))
		body = protowire.AppendTag(body, 2, protowire.BytesType)
		body = protowire.AppendString(body, f.msg)
		_, _ = w.Write(body)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"code": f.code, "message": f.msg})
}

func isGRPC(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	return ct == "application/grpc" || strings.HasPrefix(ct, "application/grpc+") || strings.HasPrefix(ct, "application/grpc;")
}
