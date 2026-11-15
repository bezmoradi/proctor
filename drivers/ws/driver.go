package ws

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"proctor/protocol"
)

var Version = "0.0.0-dev"

const (
	Name       = "ws"
	OpConnect  = "connect"
	OpSend     = "send"
	OpMessages = "messages"

	defaultTimeout = 10 * time.Second
	readyAttempt   = 3 * time.Second
	closeWait      = 5 * time.Second
	maxFrames      = 10000
	maxBytes       = 16 << 20
	bodyTextBytes  = 1024
	excerptBytes   = 4 << 10
	doubleSlash    = "/" + "/"
)

var scalar = map[string]any{"type": []string{"string", "number", "boolean"}}

var stringLists = map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}

var ConnectArgsSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"path":         map[string]any{"type": "string"},
		"query":        map[string]any{"type": "object", "additionalProperties": scalar},
		"headers":      map[string]any{"type": "object", "additionalProperties": scalar},
		"subprotocols": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"auth":         map[string]any{"type": "string"},
		"no_origin":    map[string]any{"type": "boolean"},
	},
	"required":             []string{"path"},
	"additionalProperties": false,
}

var ConnectFactSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"connected":   map[string]any{"type": "boolean"},
		"status":      map[string]any{"type": "integer", "minimum": 100, "maximum": 999},
		"subprotocol": map[string]any{"type": "string"},
		"headers":     stringLists,
		"body_text":   map[string]any{"type": "string"},
	},
	"required":             []string{"connected", "status", "subprotocol", "headers"},
	"additionalProperties": false,
}

var SendArgsSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"json": true,
		"text": map[string]any{"type": "string"},
	},
	"additionalProperties": false,
}

var SendFactSchema = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"sent": map[string]any{"type": "boolean"}},
	"required":             []string{"sent"},
	"additionalProperties": false,
}

var MessagesArgsSchema = map[string]any{
	"type":                 []string{"object", "null"},
	"additionalProperties": false,
}

var MessagesFactSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"count": map[string]any{"type": "integer", "minimum": 0},
		"messages": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type": map[string]any{"type": "string", "enum": []string{"text", "binary"}},
					"json": true,
					"text": map[string]any{"type": "string"},
				},
				"required":             []string{"type"},
				"additionalProperties": false,
			},
		},
		"closed":       map[string]any{"type": "boolean"},
		"close_code":   map[string]any{"type": "integer"},
		"close_reason": map[string]any{"type": "string"},
		"overflow":     map[string]any{"type": "boolean"},
	},
	"required":             []string{"count", "messages", "closed", "overflow"},
	"additionalProperties": false,
}

var ConnectionSchema = map[string]any{
	"type": []string{"object", "null"},
	"properties": map[string]any{
		"base_url":   map[string]any{"type": "string", "pattern": `^(wss?|https?)://`},
		"origin":     map[string]any{"type": "string", "pattern": `^https?://[^/?#\s]+$`},
		"ready_path": map[string]any{"type": "string", "pattern": `^/([^/?#][^?#]*)?$`},
	},
	"required":             []string{"base_url"},
	"additionalProperties": false,
}

type connection struct {
	BaseURL   string `json:"base_url"`
	Origin    string `json:"origin"`
	ReadyPath string `json:"ready_path"`
}

type Driver struct {
	configured bool
	base       *url.URL
	origin     string
	readyPath  string
	client     *http.Client

	mu    sync.Mutex
	next  int
	conns map[string]*socket
}

func New() *Driver {
	return &Driver{conns: map[string]*socket{}}
}

func (d *Driver) Hello(ctx context.Context, p protocol.HelloParams) (protocol.HelloResult, error) {
	return protocol.HelloResult{
		Name:     Name,
		Version:  Version,
		Protocol: protocol.Version,
		Operations: []protocol.Operation{
			{
				Name:        OpConnect,
				Kind:        protocol.KindAct,
				ArgsSchema:  ConnectArgsSchema,
				FactSchema:  ConnectFactSchema,
				OpensHandle: true,
				Credential:  protocol.CredentialOptional,
				Escape:      map[string]string{"$.path": protocol.EscapePathSegment},
				Redact:      []string{"$.headers.set-cookie"},
			},
			{
				Name:       OpSend,
				Kind:       protocol.KindAct,
				ArgsSchema: SendArgsSchema,
				FactSchema: SendFactSchema,
				UsesHandle: true,
				Credential: protocol.CredentialNone,
				Data:       []string{"$.json", "$.text"},
			},
			{
				Name:       OpMessages,
				Kind:       protocol.KindObserve,
				ArgsSchema: MessagesArgsSchema,
				FactSchema: MessagesFactSchema,
				UsesHandle: true,
				Credential: protocol.CredentialNone,
			},
		},
		PolicySchema:     nil,
		ConnectionSchema: ConnectionSchema,
	}, nil
}

func (d *Driver) Configure(ctx context.Context, p protocol.ConfigureParams) error {
	if p.Policy != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "the ws driver takes no policy")
	}
	if p.Connection == nil {
		return nil
	}
	var c connection
	if err := protocol.Decode(p.Connection, &c); err != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: %v", err)
	}
	var msgs []string
	bad := func(format string, args ...any) { msgs = append(msgs, fmt.Sprintf(format, args...)) }
	base, err := url.Parse(c.BaseURL)
	switch {
	case c.BaseURL == "":
		bad("connection: base_url is required")
	case err != nil:
		bad("connection: base_url: %v", err)
	case base.Scheme != "ws" && base.Scheme != "wss" && base.Scheme != "http" && base.Scheme != "https":
		bad("connection: base_url must be a ws, wss, http or https URL")
	case base.Host == "":
		bad("connection: base_url has no host")
	case base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.ForceQuery:
		bad("connection: base_url can't contain credentials, a query or a fragment")
	}
	if c.Origin != "" {
		o, err := url.Parse(c.Origin)
		if err != nil || (o.Scheme != "http" && o.Scheme != "https") || o.Host == "" || o.User != nil || o.Path != "" || o.RawQuery != "" || o.Fragment != "" || o.ForceQuery || hasControl(c.Origin) {
			bad("connection: origin %q must be a scheme and host such as https://app.example.com, with no path", c.Origin)
		}
	}
	if c.ReadyPath != "" && (!strings.HasPrefix(c.ReadyPath, "/") || strings.HasPrefix(c.ReadyPath, doubleSlash) || strings.ContainsAny(c.ReadyPath, "?#")) {
		bad("connection: ready_path %q must be a path starting with a single /", c.ReadyPath)
	}
	if len(msgs) > 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "%s", strings.Join(msgs, "; "))
	}
	switch base.Scheme {
	case "http":
		base.Scheme = "ws"
	case "https":
		base.Scheme = "wss"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	d.base = base
	d.origin = c.Origin
	d.readyPath = c.ReadyPath
	d.client = &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	d.configured = true
	return nil
}

func (d *Driver) Prove(ctx context.Context) (protocol.ProveResult, error) {
	return protocol.ProveResult{Proofs: []protocol.Proof{}}, nil
}

func (d *Driver) Ready(ctx context.Context) (protocol.ReadyResult, error) {
	if !d.configured {
		return protocol.ReadyResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the ws driver has no connection")
	}
	if d.readyPath == "" {
		return protocol.ReadyResult{Ready: true, Detail: "no ready_path is set, so there is nothing to wait for"}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, readyAttempt)
	defer cancel()
	target := d.httpScheme() + "://" + d.base.Host + d.basePath() + d.readyPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return protocol.ReadyResult{Detail: err.Error(), Lasting: true}, nil
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return protocol.ReadyResult{Detail: fmt.Sprintf("GET %s failed (%s): %v", d.readyPath, dialCause(err), err)}, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	detail := fmt.Sprintf("GET %s answered %d", d.readyPath, resp.StatusCode)
	return protocol.ReadyResult{Ready: resp.StatusCode >= 200 && resp.StatusCode < 300, Detail: detail}, nil
}

func (d *Driver) Close(ctx context.Context, p protocol.CloseParams) error {
	d.mu.Lock()
	s, ok := d.conns[p.Handle]
	delete(d.conns, p.Handle)
	d.mu.Unlock()
	if !ok {
		return protocol.Errorf(protocol.CauseInvalidRequest, "the ws driver has no open handle %q", p.Handle)
	}
	s.shut()
	return nil
}

func (d *Driver) Shutdown() {
	d.mu.Lock()
	all := make([]*socket, 0, len(d.conns))
	for id, s := range d.conns {
		all = append(all, s)
		delete(d.conns, id)
	}
	d.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.shut()
		}()
	}
	wg.Wait()
}

func (d *Driver) httpScheme() string {
	if d.base.Scheme == "wss" {
		return "https"
	}
	return "http"
}

func (d *Driver) basePath() string {
	return strings.TrimSuffix(d.base.EscapedPath(), "/")
}

func (d *Driver) lookup(handle string) (*socket, error) {
	if handle == "" {
		return nil, protocol.Errorf(protocol.CauseInvalidRequest, "this operation needs a handle from %s.%s", Name, OpConnect)
	}
	d.mu.Lock()
	s, ok := d.conns[handle]
	d.mu.Unlock()
	if !ok {
		return nil, protocol.Errorf(protocol.CauseInvalidRequest, "the ws driver has no open handle %q", handle)
	}
	return s, nil
}

func (d *Driver) add(s *socket) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.next++
	id := fmt.Sprintf("ws-%d", d.next)
	d.conns[id] = s
	return id
}
