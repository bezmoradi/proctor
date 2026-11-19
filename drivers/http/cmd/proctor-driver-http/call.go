package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"proctor/protocol"
)

const (
	hidden   = "[hidden]"
	redacted = "[redacted]"
)

type requestArgs struct {
	Method     string         `json:"method"`
	Path       string         `json:"path"`
	Query      map[string]any `json:"query"`
	Headers    map[string]any `json:"headers"`
	Body       any            `json:"body"`
	BodyText   any            `json:"body_text"`
	BodyBase64 any            `json:"body_base64"`
	BodyFill   any            `json:"body_fill"`
	BodyFile   any            `json:"body_file"`
}

type outgoing struct {
	req    *http.Request
	target string
	body   []byte
	raw    *rawBody
}

func (d *driver) Call(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	if d.client == nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the http driver has no connection")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}
	switch p.Op {
	case opRequest:
	case opOpen:
		return d.open(ctx, p)
	case opFrames:
		return d.frames(p)
	default:
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "the http driver has no operation %q", p.Op)
	}
	if p.Handle != "" {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s runs on no handle", opRequest)
	}
	var a requestArgs
	if err := protocol.Decode(p.Args, &a); err != nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "request: %v", err)
	}
	out, err := d.build(ctx, a, p.Credential, p.Files)
	if err != nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%v", err)
	}
	start := time.Now()
	resp, err := d.client.Do(out.req)
	if err != nil {
		return protocol.CallResult{}, protocol.Errorf(requestCause(err), "%s %s: request failed: %v", a.Method, out.target, err)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	resp.Body.Close()
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		return protocol.CallResult{}, protocol.Errorf(readCause(err), "%s %s -> %d: reading the response: %v", a.Method, out.target, resp.StatusCode, err)
	}
	if len(body) > maxBodyBytes {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseResponseTooLarge, "%s %s -> %d: the response body is larger than %d bytes", a.Method, out.target, resp.StatusCode, maxBodyBytes)
	}
	return protocol.CallResult{
		Fact: fact(resp, body),
		Evidence: protocol.Evidence{
			Summary:         fmt.Sprintf("%s %s -> %d", a.Method, out.target, resp.StatusCode),
			RequestExcerpt:  requestExcerpt(out),
			ResponseExcerpt: responseExcerpt(resp, body),
			DurationMS:      elapsed,
		},
	}, nil
}

func (d *driver) build(ctx context.Context, a requestArgs, cred *protocol.Credential, files map[string]protocol.File) (outgoing, error) {
	var out outgoing
	if !knownMethod(a.Method) {
		return out, fmt.Errorf("unsupported method %q", a.Method)
	}
	if err := checkResolvedPath(a.Path); err != nil {
		return out, err
	}
	q := url.Values{}
	for _, k := range sortedKeys(a.Query) {
		val, err := text(a.Query[k])
		if err != nil {
			return out, fmt.Errorf("query %s: %v", k, err)
		}
		q.Set(k, val)
	}
	out.target = a.Path
	u := d.join(a.Path)
	if len(q) > 0 {
		out.target += "?" + q.Encode()
		u += "?" + q.Encode()
	}
	var rd io.Reader
	forms := 0
	for _, f := range []any{a.Body, a.BodyText, a.BodyBase64, a.BodyFill, a.BodyFile} {
		if f != nil {
			forms++
		}
	}
	if forms > 1 {
		return out, errors.New("a request has one body, but more than one of body, body_text, body_base64, body_fill and body_file is set")
	}
	if forms > 0 && a.Method == http.MethodHead {
		return out, errors.New("a HEAD request can't send a body")
	}
	raw, err := buildRawBody(a, files)
	if err != nil {
		return out, err
	}
	if raw != nil {
		if limit := d.policy.bodyLimit(); raw.size > limit {
			return out, fmt.Errorf("the body holds %d bytes, more than the policy's %s (%d)", raw.size, maxBodyKey, limit)
		}
		out.raw = raw
		rd = raw.reader
	}
	if a.Body != nil {
		b, err := json.Marshal(a.Body)
		if err != nil {
			return out, fmt.Errorf("body can't be sent as JSON: %v", err)
		}
		out.body = b
		rd = bytes.NewReader(b)
	}
	if a.BodyText != nil {
		t, err := text(a.BodyText)
		if err != nil {
			return out, fmt.Errorf("body_text %v", err)
		}
		out.body = []byte(t)
		rd = bytes.NewReader(out.body)
	}
	if limit := d.policy.bodyLimit(); int64(len(out.body)) > limit {
		return out, fmt.Errorf("the body holds %d bytes, more than the policy's %s (%d)", len(out.body), maxBodyKey, limit)
	}
	req, err := http.NewRequestWithContext(ctx, a.Method, u, rd)
	if err != nil {
		return out, err
	}
	if raw != nil {
		req.ContentLength = raw.size
		if raw.fill != nil {
			f := *raw.fill
			req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(f.reader()), nil }
		}
	}
	if req.URL.Scheme != d.base.Scheme || req.URL.Host != d.base.Host || !strings.HasPrefix(req.URL.EscapedPath(), d.basePath()+"/") {
		return out, fmt.Errorf("request URL %s leaves the target (HTTP-05)", u)
	}
	if a.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, name := range sortedKeys(a.Headers) {
		if rule, why := forbiddenHeader(name); rule != "" {
			return out, fmt.Errorf("header %q can't be set by a specification: %s (%s)", name, why, rule)
		}
		if !validHeaderName(name) {
			return out, fmt.Errorf("header name %q is not a valid HTTP header name", name)
		}
		val, err := text(a.Headers[name])
		if err != nil {
			return out, fmt.Errorf("header %s: %v", name, err)
		}
		if strings.ContainsAny(val, "\r\n") {
			return out, fmt.Errorf("header %s contains a line break after inserting saved values", name)
		}
		if hasControl(val) {
			return out, fmt.Errorf("header %s contains a control character", name)
		}
		req.Header.Set(name, val)
	}
	req.Header.Set("User-Agent", d.userAgent)
	if cred != nil {
		var auth string
		switch cred.Scheme {
		case protocol.SchemeBearer:
			auth = "Bearer " + cred.Value
		case protocol.SchemeHeader:
			auth = cred.Value
		default:
			return out, fmt.Errorf("identity %q: unknown credential scheme %q", cred.Identity, cred.Scheme)
		}
		switch {
		case cred.Value == "":
			return out, fmt.Errorf("identity %q: the credential is empty", cred.Identity)
		case strings.ContainsAny(cred.Value, "\r\n") || hasControl(cred.Value):
			return out, fmt.Errorf("identity %q: the credential contains a line break or control character", cred.Identity)
		}
		req.Header.Set("Authorization", auth)
	}
	out.req = req
	return out, nil
}

func text(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case json.Number:
		return x.String(), nil
	case bool:
		return strconv.FormatBool(x), nil
	}
	return "", fmt.Errorf("must be text, a number or true or false, not %s", show(v))
}

func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if (s[i] < ' ' && s[i] != '\t') || s[i] == 0x7f {
			return true
		}
	}
	return false
}

func requestCause(err error) string {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return protocol.CauseTimeout
	}
	return protocol.CauseUnreachable
}

func readCause(err error) string {
	if requestCause(err) == protocol.CauseTimeout {
		return protocol.CauseTimeout
	}
	return protocol.CauseUnreadableResponse
}

func fact(resp *http.Response, body []byte) map[string]any {
	headers := map[string][]string{}
	for _, name := range sortedKeys(resp.Header) {
		lower := strings.ToLower(name)
		headers[lower] = append(headers[lower], resp.Header[name]...)
	}
	lists := map[string][]string{}
	for _, name := range sortedKeys(headers) {
		parts := []string{}
		for _, line := range headers[name] {
			for _, part := range strings.Split(line, ",") {
				if part = strings.TrimSpace(part); part != "" {
					parts = append(parts, part)
				}
			}
		}
		lists[name] = parts
	}
	f := map[string]any{"status": resp.StatusCode, "headers": headers, "header_lists": lists}
	if len(body) == 0 {
		return f
	}
	v, reason, syntaxOK := parseJSON(body)
	if reason == "" {
		f["body"] = v
		return f
	}
	f["body_text"] = cut(body, bodyTextBytes)
	f["body_truncated"] = len(body) > bodyTextBytes
	if syntaxOK || claimsJSON(resp.Header.Get("Content-Type")) {
		f["body_invalid"] = reason
	}
	return f
}

func claimsJSON(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

func cut(b []byte, limit int) string {
	if len(b) <= limit {
		return string(b)
	}
	end := limit
	for end > 0 && end > limit-utf8.UTFMax && !utf8.RuneStart(b[end]) {
		end--
	}
	return string(b[:end])
}

func requestExcerpt(out outgoing) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s\n", out.req.Method, out.req.URL.String())
	for _, name := range sortedKeys(out.req.Header) {
		for _, val := range out.req.Header[name] {
			if sensitiveHeader(name) {
				val = hidden
			}
			fmt.Fprintf(&sb, "%s: %s\n", name, val)
		}
	}
	switch {
	case out.raw != nil:
		sb.WriteString("\n")
		sb.WriteString(out.raw.describe())
	case len(out.body) > 0:
		sb.WriteString("\n")
		sb.WriteString(excerpt(out.body))
	}
	return sb.String()
}

func responseExcerpt(resp *http.Response, body []byte) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "HTTP %d\n", resp.StatusCode)
	for _, name := range sortedKeys(resp.Header) {
		for _, val := range resp.Header[name] {
			if sensitiveHeader(name) {
				val = hidden
			}
			fmt.Fprintf(&sb, "%s: %s\n", name, val)
		}
	}
	if len(body) > 0 {
		sb.WriteString("\n")
		sb.WriteString(excerpt(redactedBody(body)))
	}
	return sb.String()
}

func redactedBody(body []byte) []byte {
	v, reason, _ := parseJSON(body)
	if reason != "" {
		return body
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(redactSecrets(v, false)); err != nil {
		return body
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func redactSecrets(v any, secret bool) any {
	switch x := v.(type) {
	case string:
		if secret {
			return redacted
		}
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = redactSecrets(e, secret || secretName(k))
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = redactSecrets(e, secret)
		}
		return out
	}
	return v
}

func secretName(name string) bool {
	lower := strings.ToLower(name)
	for _, w := range []string{"password", "secret", "token", "api_key"} {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

func excerpt(b []byte) string {
	s := cut(b, excerptBytes)
	if len(s) < len(b) {
		s += fmt.Sprintf("\n[%d more bytes]", len(b)-len(s))
	}
	return s
}

func sensitiveHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie":
		return true
	}
	return false
}
