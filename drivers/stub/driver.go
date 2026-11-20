package stub

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bezmoradi/proctor/protocol"
	"github.com/bezmoradi/proctor/protocol/schema"
)

var Version = "0.0.0-dev"

const (
	Name          = "stub"
	SimName       = "proctor-sim-http"
	OpReceived    = "received"
	OpFail        = "fail"
	RoutesPath    = "/__proctor/routes"
	ReceivedPath  = "/__proctor/received"
	FaultsPath    = "/__proctor/faults"
	RoutesTarget  = "routes"
	AllRoutes     = "*"
	MaxResponse   = 15 << 20
	CallTimeout   = 10 * time.Second
	ReadyAttempt  = 3 * time.Second
	CleanupBudget = 1500 * time.Millisecond
)

var methods = []string{"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"}

var (
	refRE    = regexp.MustCompile(`\$\{([^}]*)\}`)
	varName  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)
	fillRE   = regexp.MustCompile(`^fill\([0-9]+\)$`)
	secretRE = regexp.MustCompile(`^secret\.`)
	idRE     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
)

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
	open   map[string]string
}

func New() *Driver {
	return &Driver{open: map[string]string{}}
}

func (d *Driver) Hello(ctx context.Context, p protocol.HelloParams) (protocol.HelloResult, error) {
	return protocol.HelloResult{
		Name:     Name,
		Version:  Version,
		Protocol: protocol.Version,
		Operations: []protocol.Operation{{
			Name:        OpReceived,
			Kind:        protocol.KindObserve,
			ArgsSchema:  ReceivedArgsSchema,
			FactSchema:  ReceivedFactSchema,
			Credential:  protocol.CredentialNone,
			Accumulates: true,
		}, {
			Name:        OpFail,
			Kind:        protocol.KindAct,
			ArgsSchema:  FailArgsSchema,
			FactSchema:  FailFactSchema,
			OpensHandle: true,
			Credential:  protocol.CredentialNone,
		}},
		PolicySchema:     nil,
		ConnectionSchema: ConnectionSchema,
	}, nil
}

func (d *Driver) Configure(ctx context.Context, p protocol.ConfigureParams) error {
	if p.Policy != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "the stub driver takes no policy")
	}
	if p.Connection == nil {
		return nil
	}
	if len(p.Secrets) > 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "connection: the stub driver takes no secrets, so no key may end in _env")
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
	switch p.Op {
	case OpReceived:
		return validateReceived(p), nil
	case OpFail:
		return validateFail(p), nil
	}
	r := protocol.ValidateResult{Kind: protocol.KindObserve}
	r.Problems = append(r.Problems, protocol.Problem{Rule: "EXE-01", Path: "$.do", Message: fmt.Sprintf("unknown operation %q; the stub driver has %q and %q", p.Op, OpReceived, OpFail)})
	return r, nil
}

type problems struct {
	r *protocol.ValidateResult
}

func (pr problems) add(rule, path, format string, args ...any) {
	pr.r.Problems = append(pr.r.Problems, protocol.Problem{Rule: rule, Message: fmt.Sprintf(format, args...), Path: path})
}

func routeTarget(route string) string {
	if route == AllRoutes || route == "" {
		return RoutesTarget
	}
	return RoutesTarget + "/" + route
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

func checkRefs(pr problems, rule, path, s string) []string {
	names, err := references(s)
	if err != nil {
		pr.add(rule, path, "%v", err)
		return nil
	}
	for _, n := range names {
		if fillRE.MatchString(n) || secretRE.MatchString(n) {
			pr.add(rule, path, "${%s} can't be used with the stub driver: the sim's request log and the report would hold it", n)
		}
	}
	return names
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

func (d *Driver) Prove(ctx context.Context) (protocol.ProveResult, error) {
	return protocol.ProveResult{Proofs: []protocol.Proof{}}, nil
}

type simRoutes struct {
	Sim     *string `json:"sim"`
	Version string  `json:"version"`
	SHA256  string  `json:"sha256"`
	Routes  []struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Path   string `json:"path"`
	} `json:"routes"`
}

func (s simRoutes) has(id string) bool {
	for _, r := range s.Routes {
		if r.ID == id {
			return true
		}
	}
	return false
}

func (s simRoutes) ids() string {
	ids := make([]string, len(s.Routes))
	for i, r := range s.Routes {
		ids[i] = r.ID
	}
	return strings.Join(ids, ", ")
}

func (d *Driver) Ready(ctx context.Context) (protocol.ReadyResult, error) {
	if d.client == nil {
		return protocol.ReadyResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the stub driver has no connection")
	}
	ctx, cancel := context.WithTimeout(ctx, ReadyAttempt)
	defer cancel()
	rs, err := d.routes(ctx)
	if err != nil {
		var e *protocol.Error
		if errors.As(err, &e) {
			return protocol.ReadyResult{Detail: e.Message, Lasting: e.Data.Lasting}, nil
		}
		return protocol.ReadyResult{Detail: err.Error()}, nil
	}
	return protocol.ReadyResult{Ready: true, Detail: fmt.Sprintf("GET %s: %s %s with %d routes, route file sha256 %s", RoutesPath, SimName, rs.Version, len(rs.Routes), rs.SHA256)}, nil
}

func (d *Driver) routes(ctx context.Context) (simRoutes, error) {
	status, body, err := d.do(ctx, http.MethodGet, RoutesPath, nil)
	if err != nil {
		return simRoutes{}, err
	}
	if status != http.StatusOK {
		e := protocol.Errorf(protocol.CauseUnreadableResponse, "GET %s answered %d; the endpoint must be %s", RoutesPath, status, SimName)
		e.Data.Lasting = status == http.StatusNotFound
		return simRoutes{}, e
	}
	var rs simRoutes
	if err := json.Unmarshal(body, &rs); err != nil || rs.Sim == nil || *rs.Sim != SimName {
		return simRoutes{}, protocol.Lastingf(protocol.CauseUnreadableResponse, "GET %s didn't answer with %s's route list; the endpoint must be %s", RoutesPath, SimName, SimName)
	}
	return rs, nil
}

func (d *Driver) do(ctx context.Context, method, path string, payload []byte) (int, []byte, error) {
	what := method + " " + strings.SplitN(path, "?", 2)[0]
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, d.base.Scheme+"://"+d.base.Host+strings.TrimSuffix(d.base.EscapedPath(), "/")+path, rd)
	if err != nil {
		return 0, nil, protocol.Lastingf(protocol.CauseInvalidRequest, "%s: %v", what, err)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, nil, requestError(ctx, what, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponse+1))
	if err != nil {
		return 0, nil, requestError(ctx, what, err)
	}
	if len(body) > MaxResponse {
		return 0, nil, protocol.Errorf(protocol.CauseResponseTooLarge, "%s returned more than %d bytes", what, MaxResponse)
	}
	return resp.StatusCode, body, nil
}

func requestError(ctx context.Context, what string, err error) *protocol.Error {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || (errors.As(err, &ne) && ne.Timeout()) {
		return protocol.Errorf(protocol.CauseTimeout, "%s timed out", what)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return protocol.Errorf(protocol.CauseUnreachable, "%s failed: %v", what, err)
}

type simError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
	Fault *struct {
		ID        string `json:"id"`
		Route     string `json:"route"`
		ExpiresAt string `json:"expires_at"`
	} `json:"fault"`
}

func readSimError(body []byte) simError {
	var e simError
	_ = json.Unmarshal(body, &e)
	return e
}

func (d *Driver) Call(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	if d.client == nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the stub driver has no connection")
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	switch p.Op {
	case OpReceived:
		return d.received(ctx, p)
	case OpFail:
		return d.fail(ctx, p)
	}
	return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "unknown operation %q", p.Op)
}

func (d *Driver) Close(ctx context.Context, p protocol.CloseParams) error {
	if d.client == nil {
		return protocol.Errorf(protocol.CauseNotConfigured, "the stub driver has no connection")
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	err := d.deleteFault(ctx, p.Handle)
	if err == nil {
		delete(d.open, p.Handle)
	}
	return err
}

func (d *Driver) deleteFault(ctx context.Context, id string) error {
	status, body, err := d.do(ctx, http.MethodDelete, FaultsPath+"/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		if readSimError(body).Code == "unknown_fault" {
			return nil
		}
	}
	return protocol.Errorf(protocol.CauseUnreadableResponse, "DELETE %s/%s answered %d: %s", FaultsPath, id, status, readSimError(body).Error)
}

func (d *Driver) Shutdown() {
	if d.client == nil || len(d.open) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), CleanupBudget)
	defer cancel()
	ids := make([]string, 0, len(d.open))
	for id := range d.open {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := d.deleteFault(ctx, id); err != nil {
			fmt.Fprintf(os.Stderr, "stub: couldn't delete fault %s on route %s at shutdown: %v; it ends at its expiry\n", id, d.open[id], err)
			continue
		}
		delete(d.open, id)
	}
}
