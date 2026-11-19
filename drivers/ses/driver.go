package ses

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"proctor/protocol"
	"proctor/protocol/schema"
)

var Version = "0.0.0-dev"

const (
	Name         = "ses"
	OpSent       = "sent"
	SentPath     = "/__proctor/sent"
	Mailbox      = "sent"
	MaxResponse  = 15 << 20
	CallTimeout  = 10 * time.Second
	ReadyAttempt = 3 * time.Second
)

var (
	refRE    = regexp.MustCompile(`\$\{([^}]*)\}`)
	varName  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)
	fillRE   = regexp.MustCompile(`^fill\([0-9]+\)$`)
	secretRE = regexp.MustCompile(`^secret\.`)
)

var stringList = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}

var ArgsSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"to":   map[string]any{"type": "string"},
		"from": map[string]any{"type": "string"},
	},
	"additionalProperties": false,
}

var emailSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"message_id": map[string]any{"type": "string"},
		"from":       map[string]any{"type": "string"},
		"to":         stringList,
		"cc":         stringList,
		"bcc":        stringList,
		"reply_to":   stringList,
		"subject":    map[string]any{"type": "string"},
		"text":       map[string]any{"type": "string"},
		"html":       map[string]any{"type": "string"},
		"sent_at":    map[string]any{"type": "string"},
	},
	"required":             []string{"message_id", "from", "to", "cc", "bcc", "reply_to", "subject", "text", "html", "sent_at"},
	"additionalProperties": false,
}

var FactSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"count":  map[string]any{"type": "integer", "minimum": 0},
		"emails": map[string]any{"type": "array", "items": emailSchema},
	},
	"required":             []string{"count", "emails"},
	"additionalProperties": false,
}

var ConnectionSchema = map[string]any{
	"type": []string{"object", "null"},
	"properties": map[string]any{
		"endpoint":  map[string]any{"type": "string", "pattern": `^https?://`},
		"ca_bundle": map[string]any{"type": "string"},
	},
	"required":             []string{"endpoint"},
	"additionalProperties": false,
}

type connection struct {
	Endpoint string `json:"endpoint"`
	CABundle string `json:"ca_bundle"`
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
			Name:       OpSent,
			Kind:       protocol.KindObserve,
			ArgsSchema: ArgsSchema,
			FactSchema: FactSchema,
			Credential: protocol.CredentialNone,
		}},
		PolicySchema:     nil,
		ConnectionSchema: ConnectionSchema,
	}, nil
}

func (d *Driver) Configure(ctx context.Context, p protocol.ConfigureParams) error {
	if p.Policy != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "the ses driver takes no policy")
	}
	if p.Connection == nil {
		return nil
	}
	if len(p.Secrets) > 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: the ses driver takes no secrets, so no key may end in _env")
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
	if c.CABundle != "" {
		if base.Scheme != "https" {
			return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: ca_bundle is set but the endpoint is not https")
		}
		pem, err := os.ReadFile(c.CABundle)
		if err != nil {
			return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: ca_bundle can't be read: %v", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: ca_bundle %s holds no PEM certificate", c.CABundle)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	d.base = base
	d.client = &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return nil
}

func (d *Driver) Validate(ctx context.Context, p protocol.ValidateParams) (protocol.ValidateResult, error) {
	r := protocol.ValidateResult{Kind: protocol.KindObserve}
	add := func(rule, path, format string, args ...any) {
		r.Problems = append(r.Problems, protocol.Problem{Rule: rule, Message: fmt.Sprintf(format, args...), Path: path})
	}
	if p.Op != OpSent {
		add("EXE-01", "$.do", "unknown operation %q; the ses driver has only %q", p.Op, OpSent)
		return r, nil
	}
	r.Targets = []string{Mailbox}
	if p.Setup {
		add("FIX-03", "$.do", "reading sent emails is a verification step; setup goes through the application")
	}
	if p.Args != nil {
		if _, ok := p.Args.(map[string]any); !ok {
			add("EXE-01", "$.with", "with must be an object with optional to and from")
			return r, nil
		}
	}
	for _, msg := range schema.Check(ArgsSchema, argsOrEmpty(p.Args), schema.AllowRefs) {
		add("EXE-01", "$.with", "%s", msg)
	}
	m, _ := p.Args.(map[string]any)
	for _, key := range []string{"to", "from"} {
		v, ok := m[key].(string)
		if !ok {
			continue
		}
		path := "$.with." + key
		if strings.TrimSpace(v) == "" {
			add("EXE-01", path, "%s must not be empty; leave it out to match every email", key)
			continue
		}
		names, err := references(v)
		if err != nil {
			add("HTTP-02", path, "%v", err)
			continue
		}
		for _, n := range names {
			if fillRE.MatchString(n) || secretRE.MatchString(n) {
				add("HTTP-02", path, "${%s} may be used only in a request body or request headers, not in an email filter", n)
			}
		}
		if len(names) == 0 {
			if _, err := mail.ParseAddress(v); err != nil {
				add("EXE-01", path, "%s %q is not an email address: %v", key, v, err)
			}
		}
	}
	r.Refs = refs(p.Args)
	return r, nil
}

func argsOrEmpty(v any) any {
	if v == nil {
		return map[string]any{}
	}
	return v
}

func references(s string) ([]string, error) {
	var names []string
	for _, m := range refRE.FindAllStringSubmatch(s, -1) {
		if m[1] != "run.id" && !varName.MatchString(m[1]) && !fillRE.MatchString(m[1]) && !secretRE.MatchString(m[1]) {
			return nil, fmt.Errorf("unsupported reference ${%s}: supported are ${run.id}, ${<identity>.<value>} and ${name} for values saved by earlier steps", m[1])
		}
		names = append(names, m[1])
	}
	if strings.Count(s, "${") != len(names) {
		return nil, fmt.Errorf("unclosed ${ in %q", s)
	}
	return names, nil
}

func refs(v any) []string {
	seen := map[string]bool{}
	m, _ := v.(map[string]any)
	for _, val := range m {
		if s, ok := val.(string); ok {
			for _, x := range refRE.FindAllStringSubmatch(s, -1) {
				seen[x[1]] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
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
		return protocol.ReadyResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the ses driver has no connection")
	}
	ctx, cancel := context.WithTimeout(ctx, ReadyAttempt)
	defer cancel()
	if _, err := d.fetch(ctx); err != nil {
		var e *protocol.Error
		if errors.As(err, &e) {
			return protocol.ReadyResult{Detail: e.Message, Lasting: e.Data.Lasting}, nil
		}
		return protocol.ReadyResult{Detail: err.Error()}, nil
	}
	return protocol.ReadyResult{Ready: true, Detail: fmt.Sprintf("GET %s answered with the sent-email log", SentPath)}, nil
}

type sentArgs struct {
	To   *string `json:"to"`
	From *string `json:"from"`
}

func (d *Driver) Call(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	start := time.Now()
	if p.Op != OpSent {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "unknown operation %q", p.Op)
	}
	if d.client == nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the ses driver has no connection")
	}
	var a sentArgs
	if p.Args != nil {
		if err := protocol.Decode(p.Args, &a); err != nil {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "sent: %v", err)
		}
	}
	var to, from string
	var filters []string
	if a.To != nil {
		if to = normalize(*a.To); to == "" {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "sent: to is empty")
		}
		filters = append(filters, "to "+to)
	}
	if a.From != nil {
		if from = normalize(*a.From); from == "" {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "sent: from is empty")
		}
		filters = append(filters, "from "+from)
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	records, err := d.fetch(ctx)
	if err != nil {
		return protocol.CallResult{}, err
	}
	emails := make([]any, 0, len(records))
	for _, rec := range records {
		e := toEmail(rec)
		if to != "" && !anyMatches(to, e.To, e.Cc, e.Bcc) {
			continue
		}
		if from != "" && normalize(e.From) != from {
			continue
		}
		emails = append(emails, e.fact())
	}
	scope := "every email"
	if len(filters) > 0 {
		scope = strings.Join(filters, ", ")
	}
	noun := "emails"
	if len(emails) == 1 {
		noun = "email"
	}
	return protocol.CallResult{
		Fact: map[string]any{"count": len(emails), "emails": emails},
		Evidence: protocol.Evidence{
			Summary:        fmt.Sprintf("sent (%s) -> %d %s of %d recorded", scope, len(emails), noun, len(records)),
			RequestExcerpt: "GET " + SentPath,
			DurationMS:     time.Since(start).Milliseconds(),
		},
	}, nil
}

func anyMatches(want string, lists ...[]string) bool {
	for _, l := range lists {
		for _, a := range l {
			if normalize(a) == want {
				return true
			}
		}
	}
	return false
}

func normalize(a string) string {
	a = strings.TrimSpace(a)
	if p, err := mail.ParseAddress(a); err == nil {
		a = p.Address
	}
	return strings.ToLower(a)
}

func (d *Driver) fetch(ctx context.Context) ([]record, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.base.Scheme+"://"+d.base.Host+strings.TrimSuffix(d.base.EscapedPath(), "/")+SentPath, nil)
	if err != nil {
		return nil, protocol.Lastingf(protocol.CauseInvalidRequest, "GET %s: %v", SentPath, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, requestError(ctx, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponse+1))
	if err != nil {
		return nil, requestError(ctx, err)
	}
	if len(body) > MaxResponse {
		return nil, protocol.Errorf(protocol.CauseResponseTooLarge, "GET %s returned more than %d bytes", SentPath, MaxResponse)
	}
	if resp.StatusCode != http.StatusOK {
		e := protocol.Errorf(protocol.CauseUnreadableResponse, "GET %s answered %d; the endpoint must be proctor-sim-ses", SentPath, resp.StatusCode)
		e.Data.Lasting = resp.StatusCode == http.StatusNotFound
		return nil, e
	}
	var out struct {
		Emails *[]record `json:"emails"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, protocol.Errorf(protocol.CauseUnreadableResponse, "GET %s: the answer is not the sent-email log: %v", SentPath, err)
	}
	if out.Emails == nil {
		return nil, protocol.Errorf(protocol.CauseUnreadableResponse, "GET %s: the answer has no emails list", SentPath)
	}
	return *out.Emails, nil
}

func requestError(ctx context.Context, err error) *protocol.Error {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || (errors.As(err, &ne) && ne.Timeout()) {
		return protocol.Errorf(protocol.CauseTimeout, "GET %s timed out", SentPath)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return protocol.Errorf(protocol.CauseUnreachable, "GET %s failed: %v", SentPath, err)
}

func (d *Driver) Close(ctx context.Context, p protocol.CloseParams) error {
	return protocol.Errorf(protocol.CauseInvalidRequest, "the ses driver opens no handles")
}
