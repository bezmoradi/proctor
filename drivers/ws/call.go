package ws

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"proctor/protocol"
)

const hidden = "[hidden]"

type connectArgs struct {
	Path         string         `json:"path"`
	Query        map[string]any `json:"query"`
	Headers      map[string]any `json:"headers"`
	Subprotocols []string       `json:"subprotocols"`
	Auth         string         `json:"auth"`
}

type handshake struct {
	target       string
	header       http.Header
	subprotocols []string
	secret       string
}

func (d *Driver) Call(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	if !d.configured {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the ws driver has no connection")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}
	switch p.Op {
	case OpConnect:
		return d.connect(ctx, p)
	case OpSend:
		return d.send(ctx, p)
	case OpMessages:
		return d.messages(p)
	}
	return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "the ws driver has no operation %q", p.Op)
}

func (d *Driver) connect(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	if p.Handle != "" {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s opens a handle; it can't run on one", OpConnect)
	}
	var a connectArgs
	if err := protocol.Decode(p.Args, &a); err != nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s: %v", OpConnect, err)
	}
	hs, err := d.build(a, p.Credential)
	if err != nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s: %v", OpConnect, err)
	}
	start := time.Now()
	conn, resp, err := websocket.Dial(ctx, hs.target, &websocket.DialOptions{
		HTTPClient:   d.client,
		HTTPHeader:   hs.header,
		Subprotocols: hs.subprotocols,
	})
	elapsed := time.Since(start).Milliseconds()
	shown := hs.shownTarget()
	if err != nil {
		if resp == nil {
			return protocol.CallResult{}, protocol.Errorf(dialCause(err), "connect %s: %s", shown, hs.scrub(err.Error()))
		}
		if resp.StatusCode == http.StatusSwitchingProtocols {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnreadableResponse, "connect %s: the server switched protocols but broke the WebSocket handshake: %s", shown, hs.scrub(err.Error()))
		}
		var body []byte
		if resp.Body != nil {
			body, _ = io.ReadAll(io.LimitReader(resp.Body, bodyTextBytes))
		}
		f := hs.fact(false, resp, "")
		if len(body) > 0 {
			f["body_text"] = hs.scrub(cut(string(body), bodyTextBytes))
		}
		return protocol.CallResult{
			Fact: f,
			Evidence: protocol.Evidence{
				Summary:         fmt.Sprintf("connect %s -> %d, refused", shown, resp.StatusCode),
				RequestExcerpt:  hs.requestExcerpt(),
				ResponseExcerpt: hs.responseExcerpt(resp, string(body)),
				DurationMS:      elapsed,
			},
		}, nil
	}
	s := open(conn)
	id := d.add(s)
	return protocol.CallResult{
		Fact: hs.fact(true, resp, conn.Subprotocol()),
		Evidence: protocol.Evidence{
			Summary:         fmt.Sprintf("connect %s -> %d, open as %s", shown, resp.StatusCode, id),
			RequestExcerpt:  hs.requestExcerpt(),
			ResponseExcerpt: hs.responseExcerpt(resp, ""),
			DurationMS:      elapsed,
		},
		Handle: id,
	}, nil
}

func (d *Driver) build(a connectArgs, cred *protocol.Credential) (handshake, error) {
	var hs handshake
	if err := checkPath(a.Path, true); err != nil {
		return hs, err
	}
	place, err := parseAuth(a.Auth)
	if err != nil {
		return hs, err
	}
	if a.Auth != "" && cred == nil {
		return hs, fmt.Errorf("auth %q is set but the step has no identity", a.Auth)
	}
	q := url.Values{}
	for _, k := range sortedKeys(a.Query) {
		v, err := scalarText(a.Query[k])
		if err != nil {
			return hs, fmt.Errorf("query %s: %v", k, err)
		}
		q.Set(k, v)
	}
	hs.header = http.Header{}
	for _, name := range sortedKeys(a.Headers) {
		if rule, why := forbiddenHeader(name); rule != "" {
			return hs, fmt.Errorf("header %q can't be set by a specification: %s (%s)", name, why, rule)
		}
		if !isToken(name) {
			return hs, fmt.Errorf("header name %q is not a valid HTTP header name", name)
		}
		if hs.header.Get(name) != "" {
			return hs, fmt.Errorf("header %q is given twice (header names ignore case)", name)
		}
		v, err := scalarText(a.Headers[name])
		if err != nil {
			return hs, fmt.Errorf("header %s: %v", name, err)
		}
		if hasControl(v) {
			return hs, fmt.Errorf("header %s contains a line break or control character after inserting saved values", name)
		}
		hs.header.Set(name, v)
	}
	if d.origin != "" && hs.header.Get("Origin") == "" {
		hs.header.Set("Origin", d.origin)
	}
	for _, sp := range a.Subprotocols {
		if !isToken(sp) {
			return hs, fmt.Errorf("subprotocol %q is not a valid token", sp)
		}
	}
	hs.subprotocols = append([]string(nil), a.Subprotocols...)
	if cred != nil {
		switch {
		case cred.Value == "":
			return hs, fmt.Errorf("identity %q: the credential is empty", cred.Identity)
		case hasControl(cred.Value):
			return hs, fmt.Errorf("identity %q: the credential contains a line break or control character", cred.Identity)
		case cred.Scheme != protocol.SchemeBearer && cred.Scheme != protocol.SchemeHeader:
			return hs, fmt.Errorf("identity %q: unknown credential scheme %q", cred.Identity, cred.Scheme)
		}
		hs.secret = cred.Value
		switch place.kind {
		case "subprotocol":
			if !isToken(cred.Value) {
				return hs, fmt.Errorf("identity %q: the credential can't be sent as a subprotocol because it isn't a token (letters, digits and !#$%%&'*+-.^_`|~)", cred.Identity)
			}
			hs.subprotocols = append(hs.subprotocols, cred.Value)
		case "query":
			if _, taken := q[place.name]; taken {
				return hs, fmt.Errorf("auth %q: query parameter %s is also set in query", a.Auth, place.name)
			}
			q.Set(place.name, cred.Value)
		case "header":
			if hs.header.Get(place.name) != "" {
				return hs, fmt.Errorf("auth %q: header %s is also set in headers", a.Auth, place.name)
			}
			v := cred.Value
			if cred.Scheme == protocol.SchemeBearer {
				v = "Bearer " + v
			}
			hs.header.Set(place.name, v)
		}
	}
	target := d.base.Scheme + "://" + d.base.Host + d.basePath() + a.Path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	u, err := url.Parse(target)
	if err != nil {
		return hs, err
	}
	if u.Scheme != d.base.Scheme || u.Host != d.base.Host || !strings.HasPrefix(u.EscapedPath(), d.basePath()+"/") {
		return hs, fmt.Errorf("URL %s leaves the target", hs.scrub(target))
	}
	hs.target = target
	return hs, nil
}

func (hs handshake) scrub(s string) string {
	if hs.secret == "" {
		return s
	}
	s = strings.ReplaceAll(s, hs.secret, hidden)
	if esc := url.QueryEscape(hs.secret); esc != hs.secret {
		s = strings.ReplaceAll(s, esc, hidden)
	}
	return s
}

func (hs handshake) shownTarget() string {
	u, err := url.Parse(hs.target)
	if err != nil {
		return hs.scrub(hs.target)
	}
	out := u.EscapedPath()
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return hs.scrub(out)
}

func (hs handshake) fact(connected bool, resp *http.Response, subprotocol string) map[string]any {
	headers := map[string][]string{}
	for _, name := range sortedKeys(resp.Header) {
		lower := strings.ToLower(name)
		for _, v := range resp.Header[name] {
			headers[lower] = append(headers[lower], hs.scrub(v))
		}
	}
	return map[string]any{
		"connected":   connected,
		"status":      resp.StatusCode,
		"subprotocol": hs.scrub(subprotocol),
		"headers":     headers,
	}
}

func sensitiveHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie":
		return true
	}
	return false
}

func (hs handshake) requestExcerpt() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "GET %s\n", hs.scrub(hs.target))
	for _, name := range sortedKeys(hs.header) {
		for _, v := range hs.header[name] {
			if sensitiveHeader(name) {
				v = hidden
			}
			fmt.Fprintf(&sb, "%s: %s\n", name, hs.scrub(v))
		}
	}
	if len(hs.subprotocols) > 0 {
		fmt.Fprintf(&sb, "Sec-WebSocket-Protocol: %s\n", hs.scrub(strings.Join(hs.subprotocols, ", ")))
	}
	return sb.String()
}

func (hs handshake) responseExcerpt(resp *http.Response, body string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "HTTP %d\n", resp.StatusCode)
	for _, name := range sortedKeys(resp.Header) {
		for _, v := range resp.Header[name] {
			if sensitiveHeader(name) {
				v = hidden
			}
			fmt.Fprintf(&sb, "%s: %s\n", name, hs.scrub(v))
		}
	}
	if body != "" {
		sb.WriteString("\n")
		sb.WriteString(excerpt(hs.scrub(body)))
	}
	return sb.String()
}

func (d *Driver) send(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	s, err := d.lookup(p.Handle)
	if err != nil {
		return protocol.CallResult{}, err
	}
	args, _ := p.Args.(map[string]any)
	if _, isObject := p.Args.(map[string]any); !isObject {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s: args must be an object with json or text", OpSend)
	}
	for _, k := range sortedKeys(args) {
		if k != "json" && k != "text" {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s: unknown argument %q", OpSend, k)
		}
	}
	jv, hasJSON := args["json"]
	tv, hasText := args["text"]
	if hasJSON == hasText {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s: give exactly one of json and text", OpSend)
	}
	var data []byte
	kind := "text"
	if hasJSON {
		kind = "json"
		data, err = compactJSON(jv)
		if err != nil {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s: json can't be written: %v", OpSend, err)
		}
	} else {
		text, ok := tv.(string)
		if !ok {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s: text must be a string, not %s", OpSend, show(tv))
		}
		data = []byte(text)
	}
	if s.isClosed() {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnreachable, "%s on %s: the connection is closed", OpSend, p.Handle)
	}
	start := time.Now()
	err = s.conn.Write(ctx, websocket.MessageText, data)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		cause := protocol.CauseUnreachable
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			cause = protocol.CauseTimeout
		}
		return protocol.CallResult{}, protocol.Errorf(cause, "%s on %s: %v", OpSend, p.Handle, err)
	}
	return protocol.CallResult{
		Fact: map[string]any{"sent": true},
		Evidence: protocol.Evidence{
			Summary:        fmt.Sprintf("send %s frame on %s (%d bytes)", kind, p.Handle, len(data)),
			RequestExcerpt: excerpt(string(data)),
			DurationMS:     elapsed,
		},
	}, nil
}

func (d *Driver) messages(p protocol.CallParams) (protocol.CallResult, error) {
	s, err := d.lookup(p.Handle)
	if err != nil {
		return protocol.CallResult{}, err
	}
	if p.Args != nil {
		m, ok := p.Args.(map[string]any)
		if !ok || len(m) > 0 {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s takes no arguments", OpMessages)
		}
	}
	start := time.Now()
	snap := s.snapshot()
	list := make([]any, 0, len(snap.frames))
	var sb strings.Builder
	for _, f := range snap.frames {
		m := map[string]any{"type": "text"}
		if f.binary {
			m["type"] = "binary"
			if sb.Len() < excerptBytes {
				sb.WriteString("[binary frame]\n")
			}
		} else {
			m["text"] = f.text
			if f.hasJSON {
				m["json"] = f.json
			}
			if sb.Len() < excerptBytes {
				sb.WriteString(f.text)
				sb.WriteString("\n")
			}
		}
		list = append(list, m)
	}
	fact := map[string]any{
		"count":    len(list),
		"messages": list,
		"closed":   snap.closed,
		"overflow": snap.overflow,
	}
	state := "open"
	if snap.closed {
		fact["close_code"] = snap.closeCode
		fact["close_reason"] = snap.closeReason
		state = fmt.Sprintf("closed with %d", snap.closeCode)
	}
	if snap.overflow {
		state += ", overflowed"
	}
	return protocol.CallResult{
		Fact: fact,
		Evidence: protocol.Evidence{
			Summary:         fmt.Sprintf("messages on %s: %d frames, %s", p.Handle, len(list), state),
			ResponseExcerpt: excerpt(sb.String()),
			DurationMS:      time.Since(start).Milliseconds(),
		},
	}, nil
}
