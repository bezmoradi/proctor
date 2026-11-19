package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"proctor/protocol"
)

const (
	APIVersion     = "1.44"
	apiPrefix      = "/v" + APIVersion
	requestHost    = "docker"
	MaxResponse    = 8 << 20
	maxMessage     = 4 << 10
	maxFilterTerms = 40
	DialTimeout    = 5 * time.Second
	RequestTimeout = 10 * time.Second
)

const (
	pathPing       = "/_ping"
	pathVersion    = "/version"
	pathContainers = "/containers/json"
	pathNetworks   = "/networks"
	pathVolumes    = "/volumes"
)

var (
	objectID      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	containerPath = regexp.MustCompile(`^/containers/([^/]+)/json$`)
	networkPath   = regexp.MustCompile(`^/networks/([^/]+)$`)
	errNotFound   = errors.New("not found")
)

func Allowed(method string, u *url.URL) error {
	if method != http.MethodGet {
		return fmt.Errorf("method %q refused: the docker driver sends only GET", method)
	}
	if u == nil {
		return errors.New("no URL")
	}
	switch {
	case u.Scheme != "http" || u.Host != requestHost:
		return fmt.Errorf("%s://%s refused: requests go only to http://%s over the socket", u.Scheme, u.Host, requestHost)
	case u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawFragment != "" || u.ForceQuery:
		return errors.New("a URL with user info, an opaque part, a fragment or an empty query is refused")
	case u.RawPath != "":
		return fmt.Errorf("path %q refused: percent-encoded characters aren't allowed", u.RawPath)
	case path.Clean(u.Path) != u.Path:
		return fmt.Errorf("path %q refused: it isn't clean", u.Path)
	case !strings.HasPrefix(u.Path, apiPrefix+"/"):
		return fmt.Errorf("path %q refused: every request names API %s", u.Path, APIVersion)
	}
	rest := strings.TrimPrefix(u.Path, apiPrefix)
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fmt.Errorf("query refused: %v", err)
	}
	if query.Encode() != u.RawQuery {
		return errors.New("query refused: it isn't in canonical form")
	}
	var keys []string
	switch {
	case rest == pathPing, rest == pathVersion:
	case rest == pathContainers:
		keys = []string{"all", "filters"}
	case rest == pathNetworks, rest == pathVolumes:
		keys = []string{"filters"}
	case containerPath.MatchString(rest):
		if id := containerPath.FindStringSubmatch(rest)[1]; !objectID.MatchString(id) {
			return fmt.Errorf("path %q refused: a container is inspected only by the 64-digit ID Docker gave", u.Path)
		}
	case networkPath.MatchString(rest):
		if id := networkPath.FindStringSubmatch(rest)[1]; !objectID.MatchString(id) {
			return fmt.Errorf("path %q refused: a network is inspected only by the 64-digit ID Docker gave", u.Path)
		}
	default:
		return fmt.Errorf("path %q refused: it isn't on the docker driver's list (DKR-05)", u.Path)
	}
	for k, vs := range query {
		if !slices.Contains(keys, k) {
			return fmt.Errorf("query key %q refused on %s", k, rest)
		}
		if len(vs) != 1 {
			return fmt.Errorf("query key %q refused: it appears %d times", k, len(vs))
		}
		switch k {
		case "all":
			if vs[0] != "1" {
				return fmt.Errorf("all=%q refused: only all=1 is sent", vs[0])
			}
		case "filters":
			if err := allowedFilters(vs[0]); err != nil {
				return fmt.Errorf("filters refused: %v", err)
			}
		}
	}
	return nil
}

func allowedFilters(raw string) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	var f map[string][]string
	if err := dec.Decode(&f); err != nil {
		return fmt.Errorf("not a JSON object of label lists: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the JSON object")
	}
	if len(f) != 1 {
		return errors.New("the only filter is label")
	}
	terms, ok := f["label"]
	switch {
	case !ok:
		return errors.New("the only filter is label")
	case len(terms) == 0 || len(terms) > maxFilterTerms:
		return fmt.Errorf("label holds %d terms; 1 to %d are allowed", len(terms), maxFilterTerms)
	}
	for _, t := range terms {
		key, _, _ := strings.Cut(t, "=")
		if !labelKey.MatchString(key) {
			return fmt.Errorf("label term %q doesn't start with a label key", t)
		}
		if hasControl(t) {
			return errors.New("a label term holds a control character")
		}
	}
	return nil
}

const bindingSocket = "the binding's socket"

type client struct {
	socket string
	hc     *http.Client
}

func newClient(socket string) *client {
	dialer := &net.Dialer{Timeout: DialTimeout}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		},
		ForceAttemptHTTP2:      false,
		MaxIdleConns:           2,
		IdleConnTimeout:        30 * time.Second,
		ResponseHeaderTimeout:  RequestTimeout,
		MaxResponseHeaderBytes: 64 << 10,
		DisableCompression:     true,
	}
	return &client{socket: socket, hc: &http.Client{
		Transport:     transport,
		Timeout:       RequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *client) get(ctx context.Context, p string, query url.Values, out any) error {
	u := &url.URL{Scheme: "http", Host: requestHost, Path: apiPrefix + p, RawQuery: query.Encode()}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "GET %s: %v", p, err)
	}
	if err := Allowed(req.Method, req.URL); err != nil {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "the docker driver refused its own request: %v", err)
	}
	if req.Body != nil || req.ContentLength != 0 || len(req.Header) != 0 {
		return protocol.Lastingf(protocol.CauseInvalidRequest, "the docker driver refused its own request: GET %s has a body or headers", p)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return c.transportError(ctx, p, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponse+1))
	if err != nil {
		return c.transportError(ctx, p, err)
	}
	if len(body) > MaxResponse {
		return protocol.Errorf(protocol.CauseResponseTooLarge, "GET %s answered more than %d bytes", p, MaxResponse)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return protocol.Lastingf(protocol.CauseQueryError, "GET %s answered %d; Docker doesn't redirect, so the socket isn't Docker's", p, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		e := protocol.Errorf(protocol.CauseQueryError, "GET %s answered %d: %s", p, resp.StatusCode, dockerMessage(body))
		e.Data.Lasting = resp.StatusCode == http.StatusBadRequest
		return e
	}
	want := "application/json"
	if p == pathPing {
		want = "text/plain"
	}
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || mt != want {
		return protocol.Errorf(protocol.CauseUnreadableResponse, "GET %s answered Content-Type %q, not %s", p, oneLine(resp.Header.Get("Content-Type")), want)
	}
	if s, isText := out.(*string); isText {
		*s = string(body)
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return protocol.Errorf(protocol.CauseUnreadableResponse, "GET %s: the answer isn't what Docker's API %s sends: %v", p, APIVersion, decodeProblem(err))
	}
	return nil
}

func decodeProblem(err error) string {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		return fmt.Sprintf("field %s holds a JSON %s", te.Field, te.Value)
	}
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return fmt.Sprintf("invalid JSON at byte %d", se.Offset)
	}
	return "it can't be decoded"
}

func dockerMessage(body []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &m); err != nil || m.Message == "" {
		return "(no message)"
	}
	return cut(oneLine(m.Message), maxMessage)
}

func (c *client) transportError(ctx context.Context, p string, err error) *protocol.Error {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || (errors.As(err, &ne) && ne.Timeout()):
		return protocol.Errorf(protocol.CauseTimeout, "GET %s timed out", p)
	case errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM):
		return protocol.Lastingf(protocol.CauseUnreachable, "this user can't open %s; on Linux add it to the docker group", bindingSocket)
	case errors.Is(err, syscall.ENOTSOCK):
		return protocol.Lastingf(protocol.CauseUnreachable, "%s isn't a Unix socket", bindingSocket)
	case errors.Is(err, syscall.ENOENT):
		return protocol.Errorf(protocol.CauseUnreachable, "%s doesn't exist; is Docker running?", bindingSocket)
	case errors.Is(err, syscall.ECONNREFUSED):
		return protocol.Errorf(protocol.CauseUnreachable, "%s refused the connection; is Docker running?", bindingSocket)
	}
	return protocol.Errorf(protocol.CauseUnreachable, "GET %s on %s failed: %s", p, bindingSocket, cut(oneLine(c.withoutSocket(transportProblem(err))), maxMessage))
}

func transportProblem(err error) string {
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return oe.Op + ": " + oe.Err.Error()
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err.Error()
	}
	return err.Error()
}

func (c *client) withoutSocket(s string) string {
	if c.socket == "" {
		return s
	}
	return strings.ReplaceAll(s, c.socket, bindingSocket)
}

func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == '\t' }), " ")
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "..."
}
