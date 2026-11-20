package browser

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/proctor/protocol"
)

var Version = "0.0.0-dev"

const (
	Name    = "browser"
	OpOpen  = "open"
	OpFill  = "fill"
	OpClick = "click"
	OpPage  = "page"

	defaultTimeout = 10 * time.Second
	readyAttempt   = 3 * time.Second
	closeWait      = 5 * time.Second
	settleWait     = 300 * time.Millisecond
	pollEvery      = 50 * time.Millisecond
	maxSelectors   = 100
	textBytes      = 64 << 10
	attrBytes      = 4 << 10
	entryBytes     = 4 << 10
	urlBytes       = 2 << 10
	maxEntries     = 1000
	maxValue       = 1 << 20
	maxFill        = 16 << 20
	excerptBytes   = 4 << 10
	doubleSlash    = "/" + "/"
)

var scalar = map[string]any{"type": []string{"string", "number", "boolean"}}

var OpenArgsSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"path":  map[string]any{"type": "string"},
		"query": map[string]any{"type": "object", "additionalProperties": scalar},
		"auth":  map[string]any{"type": "string"},
	},
	"required":             []string{"path"},
	"additionalProperties": false,
}

var OpenFactSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"url":    map[string]any{"type": "string"},
		"status": map[string]any{"type": "integer", "minimum": 100, "maximum": 999},
		"title":  map[string]any{"type": "string"},
	},
	"required":             []string{"url", "status", "title"},
	"additionalProperties": false,
}

var FillArgsSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"selector": map[string]any{"type": "string"},
		"value":    map[string]any{"type": "string"},
	},
	"required":             []string{"selector", "value"},
	"additionalProperties": false,
}

var FillFactSchema = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"filled": map[string]any{"type": "boolean"}},
	"required":             []string{"filled"},
	"additionalProperties": false,
}

var ClickArgsSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"selector": map[string]any{"type": "string"},
		"wait_for": map[string]any{"type": "string"},
	},
	"required":             []string{"selector"},
	"additionalProperties": false,
}

var ClickFactSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"url":       map[string]any{"type": "string"},
		"title":     map[string]any{"type": "string"},
		"navigated": map[string]any{"type": "boolean"},
		"status":    map[string]any{"type": "integer", "minimum": 100, "maximum": 999},
	},
	"required":             []string{"url", "title", "navigated"},
	"additionalProperties": false,
}

var PageArgsSchema = map[string]any{
	"type": []string{"object", "null"},
	"properties": map[string]any{
		"selectors": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
	},
	"additionalProperties": false,
}

var elementSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"count":          map[string]any{"type": "integer", "minimum": 0},
		"text":           map[string]any{"type": "string"},
		"text_truncated": map[string]any{"type": "boolean"},
		"visible":        map[string]any{"type": "boolean"},
		"attributes":     map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
	},
	"required":             []string{"count", "text", "text_truncated", "visible", "attributes"},
	"additionalProperties": false,
}

var consoleSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"source": map[string]any{"type": "string", "enum": []string{"console", "exception", "log"}},
		"text":   map[string]any{"type": "string"},
		"url":    map[string]any{"type": "string"},
	},
	"required":             []string{"source", "text"},
	"additionalProperties": false,
}

var failedSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"url":     map[string]any{"type": "string"},
		"method":  map[string]any{"type": "string"},
		"type":    map[string]any{"type": "string"},
		"status":  map[string]any{"type": "integer", "minimum": 0, "maximum": 999},
		"error":   map[string]any{"type": "string"},
		"blocked": map[string]any{"type": "boolean"},
	},
	"required":             []string{"url", "method", "status", "error", "blocked"},
	"additionalProperties": false,
}

var PageFactSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"url":             map[string]any{"type": "string"},
		"title":           map[string]any{"type": "string"},
		"selectors":       map[string]any{"type": "object", "additionalProperties": elementSchema},
		"console_errors":  map[string]any{"type": "array", "items": consoleSchema},
		"failed_requests": map[string]any{"type": "array", "items": failedSchema},
		"overflow":        map[string]any{"type": "boolean"},
		"overflow_reason": map[string]any{"type": "string"},
	},
	"required":             []string{"url", "title", "selectors", "console_errors", "failed_requests", "overflow"},
	"additionalProperties": false,
}

var PolicySchema = map[string]any{
	"type": []string{"object", "null"},
	"properties": map[string]any{
		"origins": map[string]any{"type": "array", "items": map[string]any{"type": "string", "pattern": `^https?://[^/?#\s]+/?$`}},
	},
	"additionalProperties": false,
}

var ConnectionSchema = map[string]any{
	"type": []string{"object", "null"},
	"properties": map[string]any{
		"base_url":    map[string]any{"type": "string", "pattern": `^https?://`},
		"chrome_path": map[string]any{"type": "string"},
		"remote_url":  map[string]any{"type": "string", "pattern": `^(wss?|https?)://`},
		"ready_path":  map[string]any{"type": "string", "pattern": `^/([^/?#][^?#]*)?$`},
	},
	"required":             []string{"base_url"},
	"additionalProperties": false,
}

type policy struct {
	Origins []string `json:"origins"`
}

type connection struct {
	BaseURL    string `json:"base_url"`
	ChromePath string `json:"chrome_path"`
	RemoteURL  string `json:"remote_url"`
	ReadyPath  string `json:"ready_path"`
}

type Driver struct {
	configured bool
	base       *url.URL
	baseOrigin string
	allowed    map[string]bool
	hosts      []string
	chromePath string
	remoteURL  string
	readyPath  string
	client     *http.Client

	engine *engine

	mu   sync.Mutex
	next int
	tabs map[string]*tab
}

func New() *Driver {
	return &Driver{tabs: map[string]*tab{}, allowed: map[string]bool{}}
}

func (d *Driver) Hello(ctx context.Context, p protocol.HelloParams) (protocol.HelloResult, error) {
	return protocol.HelloResult{
		Name:     Name,
		Version:  Version,
		Protocol: protocol.Version,
		Operations: []protocol.Operation{
			{
				Name:        OpOpen,
				Kind:        protocol.KindAct,
				ArgsSchema:  OpenArgsSchema,
				FactSchema:  OpenFactSchema,
				OpensHandle: true,
				Credential:  protocol.CredentialOptional,
				Escape:      map[string]string{"$.path": protocol.EscapePathSegment},
			},
			{
				Name:       OpFill,
				Kind:       protocol.KindAct,
				ArgsSchema: FillArgsSchema,
				FactSchema: FillFactSchema,
				UsesHandle: true,
				Credential: protocol.CredentialNone,
				Data:       []string{"$.value"},
			},
			{
				Name:       OpClick,
				Kind:       protocol.KindAct,
				ArgsSchema: ClickArgsSchema,
				FactSchema: ClickFactSchema,
				UsesHandle: true,
				Credential: protocol.CredentialNone,
			},
			{
				Name:        OpPage,
				Kind:        protocol.KindObserve,
				ArgsSchema:  PageArgsSchema,
				FactSchema:  PageFactSchema,
				UsesHandle:  true,
				Accumulates: true,
				Credential:  protocol.CredentialNone,
			},
		},
		PolicySchema:     PolicySchema,
		ConnectionSchema: ConnectionSchema,
	}, nil
}

func (d *Driver) Configure(ctx context.Context, p protocol.ConfigureParams) error {
	var pol policy
	if p.Policy != nil {
		if err := protocol.Decode(p.Policy, &pol); err != nil {
			return protocol.Lastingf(protocol.CauseInvalidRequest, "policy: %v", err)
		}
	}
	var msgs []string
	bad := func(format string, args ...any) { msgs = append(msgs, fmt.Sprintf(format, args...)) }
	allowed := map[string]bool{}
	var hosts []string
	for i, o := range pol.Origins {
		norm, err := parseOrigin(o)
		switch {
		case err != nil:
			bad("policy: origins[%d] %q %v (BROWSER-06)", i, o, err)
		case allowed[norm]:
			bad("policy: origins[%d] %q is listed twice (BROWSER-06)", i, o)
		default:
			allowed[norm] = true
			u, _ := url.Parse(norm)
			hosts = append(hosts, u.Hostname())
		}
	}
	if len(msgs) > 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "%s", strings.Join(msgs, "; "))
	}
	if p.Connection == nil {
		return nil
	}
	var c connection
	if err := protocol.Decode(p.Connection, &c); err != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: %v", err)
	}
	base, err := url.Parse(c.BaseURL)
	switch {
	case c.BaseURL == "":
		bad("connection: base_url is required")
	case err != nil:
		bad("connection: base_url: %v", err)
	case base.Scheme != "http" && base.Scheme != "https":
		bad("connection: base_url must be an http or https URL")
	case base.Host == "":
		bad("connection: base_url has no host")
	case base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.ForceQuery:
		bad("connection: base_url can't contain credentials, a query or a fragment")
	}
	if c.ChromePath != "" && c.RemoteURL != "" {
		bad("connection: give chrome_path or remote_url, not both")
	}
	if c.ChromePath != "" && (!filepath.IsAbs(c.ChromePath) || hasControl(c.ChromePath)) {
		bad("connection: chrome_path %q must be an absolute path", c.ChromePath)
	}
	if c.RemoteURL != "" {
		r, err := url.Parse(c.RemoteURL)
		if err != nil || r.Host == "" || r.User != nil || r.Fragment != "" || hasControl(c.RemoteURL) {
			bad("connection: remote_url %q must be a ws, wss, http or https address of a DevTools endpoint, without credentials", c.RemoteURL)
		}
	}
	if c.ReadyPath != "" && (!strings.HasPrefix(c.ReadyPath, "/") || strings.HasPrefix(c.ReadyPath, doubleSlash) || strings.ContainsAny(c.ReadyPath, "?#")) {
		bad("connection: ready_path %q must be a path starting with a single /", c.ReadyPath)
	}
	if len(msgs) > 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "%s", strings.Join(msgs, "; "))
	}
	origin, _ := originOf(base)
	allowed[origin] = true
	hosts = append(hosts, base.Hostname())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	d.base = base
	d.baseOrigin = origin
	d.allowed = allowed
	d.hosts = hosts
	d.chromePath = c.ChromePath
	d.remoteURL = c.RemoteURL
	d.readyPath = c.ReadyPath
	d.client = &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	d.engine = &engine{d: d}
	d.configured = true
	return nil
}

func (d *Driver) Prove(ctx context.Context) (protocol.ProveResult, error) {
	return protocol.ProveResult{Proofs: []protocol.Proof{}}, nil
}

func (d *Driver) Ready(ctx context.Context) (protocol.ReadyResult, error) {
	if !d.configured {
		return protocol.ReadyResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the browser driver has no connection")
	}
	ctx, cancel := context.WithTimeout(ctx, readyAttempt)
	defer cancel()
	detail, err := d.engine.ensure(ctx)
	if err != nil {
		return protocol.ReadyResult{Detail: err.Error(), Lasting: lasting(err)}, nil
	}
	if d.readyPath == "" {
		return protocol.ReadyResult{Ready: true, Detail: detail + "; no ready_path is set, so there is nothing else to wait for"}, nil
	}
	target := d.base.Scheme + "://" + d.base.Host + d.basePath() + d.readyPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return protocol.ReadyResult{Detail: err.Error(), Lasting: true}, nil
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return protocol.ReadyResult{Detail: fmt.Sprintf("%s; GET %s failed (%s): %v", detail, d.readyPath, errCause(ctx, err), err)}, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	return protocol.ReadyResult{Ready: resp.StatusCode >= 200 && resp.StatusCode < 300, Detail: fmt.Sprintf("%s; GET %s answered %d", detail, d.readyPath, resp.StatusCode)}, nil
}

func (d *Driver) Close(ctx context.Context, p protocol.CloseParams) error {
	d.mu.Lock()
	t, ok := d.tabs[p.Handle]
	delete(d.tabs, p.Handle)
	d.mu.Unlock()
	if !ok {
		return protocol.Errorf(protocol.CauseInvalidRequest, "the browser driver has no open handle %q", p.Handle)
	}
	t.close()
	return nil
}

func (d *Driver) Shutdown() {
	d.mu.Lock()
	all := make([]*tab, 0, len(d.tabs))
	for id, t := range d.tabs {
		all = append(all, t)
		delete(d.tabs, id)
	}
	d.mu.Unlock()
	var wg sync.WaitGroup
	for _, t := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t.close()
		}()
	}
	wg.Wait()
	if d.engine != nil {
		d.engine.stop()
	}
}

func (d *Driver) basePath() string {
	return strings.TrimSuffix(d.base.EscapedPath(), "/")
}

func (d *Driver) lookup(handle string) (*tab, error) {
	if handle == "" {
		return nil, protocol.Errorf(protocol.CauseInvalidRequest, "this operation needs a handle from %s.%s", Name, OpOpen)
	}
	d.mu.Lock()
	t, ok := d.tabs[handle]
	d.mu.Unlock()
	if !ok {
		return nil, protocol.Errorf(protocol.CauseInvalidRequest, "the browser driver has no open handle %q", handle)
	}
	return t, nil
}

func (d *Driver) add(t *tab) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.next++
	id := fmt.Sprintf("browser-%d", d.next)
	d.tabs[id] = t
	t.id = id
	return id
}

func (d *Driver) allowedURL(raw string) (bool, string) {
	u, err := url.Parse(raw)
	if err != nil {
		return false, ""
	}
	switch strings.ToLower(u.Scheme) {
	case "data", "blob", "about":
		return true, ""
	}
	o, ok := originOf(u)
	if !ok {
		return false, u.Scheme + ":"
	}
	return d.allowed[o], o
}
