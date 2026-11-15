package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"proctor/protocol"
)

const (
	driverName     = "http"
	driverVersion  = "0.1.0"
	opRequest      = "request"
	maxBodyBytes   = 10 << 20
	bodyTextBytes  = 64 << 10
	excerptBytes   = 4 << 10
	defaultTimeout = 10 * time.Second
	readyAttempt   = 3 * time.Second
)

var methods = []string{"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"}

var scalar = map[string]any{"type": []string{"string", "number", "boolean"}}

var argsSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"method":  map[string]any{"type": "string", "enum": methods},
		"path":    map[string]any{"type": "string"},
		"query":   map[string]any{"type": "object", "additionalProperties": scalar},
		"headers": map[string]any{"type": "object", "additionalProperties": scalar},
		"body":    true,
	},
	"required":             []string{"method", "path"},
	"additionalProperties": false,
}

var stringLists = map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}

var factSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"status":       map[string]any{"type": "integer", "minimum": 100, "maximum": 999},
		"headers":      stringLists,
		"header_lists": stringLists,
		"body":         true,
		"body_text":    map[string]any{"type": "string"},
		"body_invalid": map[string]any{"type": "string"},
	},
	"required":             []string{"status", "headers", "header_lists"},
	"additionalProperties": false,
}

var connectionSchema = map[string]any{
	"type": []string{"object", "null"},
	"properties": map[string]any{
		"base_url":   map[string]any{"type": "string", "pattern": `^https?://`},
		"user_agent": map[string]any{"type": "string", "pattern": `^[^\r\n]*[^\s][^\r\n]*$`},
		"ready_path": map[string]any{"type": "string", "pattern": `^/([^/?#][^?#]*)?$`},
	},
	"required":             []string{"base_url", "user_agent"},
	"additionalProperties": false,
}

type connection struct {
	BaseURL   string `json:"base_url"`
	UserAgent string `json:"user_agent"`
	ReadyPath string `json:"ready_path"`
}

type driver struct {
	base      *url.URL
	userAgent string
	readyPath string
	client    *http.Client
}

func (d *driver) Hello(ctx context.Context, p protocol.HelloParams) (protocol.HelloResult, error) {
	return protocol.HelloResult{
		Name:     driverName,
		Version:  driverVersion,
		Protocol: protocol.Version,
		Operations: []protocol.Operation{{
			Name:       opRequest,
			Kind:       protocol.KindByArgs,
			ArgsSchema: argsSchema,
			FactSchema: factSchema,
			Credential: protocol.CredentialOptional,
			Escape:     map[string]string{"$.path": protocol.EscapePathSegment},
			Redact:     []string{"$.headers.set-cookie", "$.header_lists.set-cookie"},
			Data:       []string{"$.body"},
		}},
		PolicySchema:     nil,
		ConnectionSchema: connectionSchema,
	}, nil
}

func (d *driver) Configure(ctx context.Context, p protocol.ConfigureParams) error {
	if p.Policy != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "the http driver takes no policy")
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
	case base.Scheme != "http" && base.Scheme != "https":
		bad("connection: base_url must be an http or https URL")
	case base.Host == "":
		bad("connection: base_url has no host")
	case base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.ForceQuery:
		bad("connection: base_url can't contain credentials, a query or a fragment")
	}
	if strings.TrimSpace(c.UserAgent) == "" {
		bad("connection: user_agent is required (HTTP-04)")
	} else if strings.ContainsAny(c.UserAgent, "\r\n") {
		bad("connection: user_agent contains a line break")
	}
	if c.ReadyPath != "" && (!strings.HasPrefix(c.ReadyPath, "/") || strings.HasPrefix(c.ReadyPath, doubleSlash) || strings.ContainsAny(c.ReadyPath, "?#")) {
		bad("connection: ready_path %q must be a path starting with a single /", c.ReadyPath)
	}
	if len(msgs) > 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "%s", strings.Join(msgs, "; "))
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	d.base = base
	d.userAgent = c.UserAgent
	d.readyPath = c.ReadyPath
	d.client = &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return nil
}

func (d *driver) Prove(ctx context.Context) (protocol.ProveResult, error) {
	return protocol.ProveResult{Proofs: []protocol.Proof{}}, nil
}

func (d *driver) Ready(ctx context.Context) (protocol.ReadyResult, error) {
	if d.client == nil {
		return protocol.ReadyResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the http driver has no connection")
	}
	if d.readyPath == "" {
		return protocol.ReadyResult{Ready: true, Detail: "no ready_path is set, so there is nothing to wait for"}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, readyAttempt)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.join(d.readyPath), nil)
	if err != nil {
		return protocol.ReadyResult{Detail: err.Error(), Lasting: true}, nil
	}
	req.Header.Set("User-Agent", d.userAgent)
	resp, err := d.client.Do(req)
	if err != nil {
		return protocol.ReadyResult{Detail: fmt.Sprintf("GET %s failed (%s): %v", d.readyPath, requestCause(err), err)}, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	detail := fmt.Sprintf("GET %s answered %d", d.readyPath, resp.StatusCode)
	return protocol.ReadyResult{Ready: resp.StatusCode >= 200 && resp.StatusCode < 300, Detail: detail}, nil
}

func (d *driver) Close(ctx context.Context, p protocol.CloseParams) error {
	return protocol.Errorf(protocol.CauseInvalidRequest, "the http driver opens no handles")
}

func (d *driver) join(path string) string {
	return d.base.Scheme + "://" + d.base.Host + d.basePath() + path
}

func (d *driver) basePath() string {
	return strings.TrimSuffix(d.base.EscapedPath(), "/")
}

func main() {
	if err := protocol.Serve(&driver{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
