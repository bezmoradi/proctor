package github

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

var Issuer = "https://token.actions.githubusercontent.com"

const Audience = "proctor"

const maxBody = 1 << 20

func issuer() string {
	if Issuer == "env" {
		return os.Getenv("PROCTOR_TEST_OIDC_ISSUER")
	}
	return Issuer
}

func Client(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type Identity struct {
	Repository     string `json:"repository"`
	WorkflowRef    string `json:"workflow_ref"`
	WorkflowSHA    string `json:"workflow_sha"`
	JobWorkflowRef string `json:"job_workflow_ref"`
	JobWorkflowSHA string `json:"job_workflow_sha"`
	SHA            string `json:"sha"`
	EventName      string `json:"event_name"`
	RunID          string `json:"run_id"`
	RunAttempt     string `json:"run_attempt"`
}

var ErrNoToken = errors.New("no OIDC token request variables")

func FetchIdentity(ctx context.Context, c *http.Client) (*Identity, error) {
	reqURL, reqTok := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"), os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if reqURL == "" || reqTok == "" {
		return nil, ErrNoToken
	}
	u, err := url.Parse(reqURL)
	if err != nil {
		return nil, fmt.Errorf("token request URL: %v", err)
	}
	q := u.Query()
	q.Set("audience", Audience)
	u.RawQuery = q.Encode()
	var resp struct {
		Value string `json:"value"`
	}
	if err := getJSON(ctx, c, u.String(), "Bearer "+reqTok, &resp); err != nil {
		return nil, fmt.Errorf("requesting the OIDC token: %v", err)
	}
	return verify(ctx, c, resp.Value, time.Now())
}

func verify(ctx context.Context, c *http.Client, token string, now time.Time) (*Identity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("OIDC token is not a JWT")
	}
	var head struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodePart(parts[0], &head); err != nil {
		return nil, fmt.Errorf("OIDC token header: %v", err)
	}
	if head.Alg != "RS256" || head.Kid == "" {
		return nil, fmt.Errorf("OIDC token uses alg %q; only RS256 with a key ID is accepted", head.Alg)
	}
	iss := issuer()
	if iss == "" {
		return nil, errors.New("no OIDC issuer configured")
	}
	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := getJSON(ctx, c, iss+"/.well-known/jwks", "", &jwks); err != nil {
		return nil, fmt.Errorf("fetching the issuer's keys: %v", err)
	}
	var pub *rsa.PublicKey
	for _, k := range jwks.Keys {
		if k.Kid != head.Kid || k.Kty != "RSA" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
			return nil, errors.New("issuer key is malformed")
		}
		ei := 0
		for _, b := range e {
			ei = ei<<8 | int(b)
		}
		pub = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: ei}
	}
	if pub == nil {
		return nil, fmt.Errorf("issuer publishes no RSA key %q", head.Kid)
	}
	if pub.N.BitLen() < 2048 {
		return nil, errors.New("issuer key is shorter than 2048 bits")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("OIDC token signature is not base64url")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return nil, errors.New("OIDC token signature doesn't verify")
	}
	var claims map[string]any
	if err := decodePart(parts[1], &claims); err != nil {
		return nil, fmt.Errorf("OIDC token claims: %v", err)
	}
	if s, _ := claims["iss"].(string); s != iss {
		return nil, fmt.Errorf("OIDC token issuer is %q, not %q", s, iss)
	}
	if !hasAudience(claims["aud"]) {
		return nil, fmt.Errorf("OIDC token audience isn't %q", Audience)
	}
	const skew = 60
	exp, okExp := claims["exp"].(float64)
	if !okExp || int64(exp) < now.Unix()-skew {
		return nil, errors.New("OIDC token is expired or has no expiry")
	}
	if nbf, ok := claims["nbf"].(float64); ok && int64(nbf) > now.Unix()+skew {
		return nil, errors.New("OIDC token isn't valid yet")
	}
	id := &Identity{}
	for _, f := range []struct {
		name string
		dst  *string
	}{
		{"repository", &id.Repository}, {"workflow_ref", &id.WorkflowRef}, {"workflow_sha", &id.WorkflowSHA},
		{"job_workflow_ref", &id.JobWorkflowRef}, {"job_workflow_sha", &id.JobWorkflowSHA}, {"sha", &id.SHA},
		{"event_name", &id.EventName}, {"run_id", &id.RunID}, {"run_attempt", &id.RunAttempt},
	} {
		v, _ := claims[f.name].(string)
		if v == "" {
			return nil, fmt.Errorf("OIDC token has no %s claim", f.name)
		}
		*f.dst = v
	}
	return id, nil
}

func hasAudience(v any) bool {
	switch a := v.(type) {
	case string:
		return a == Audience
	case []any:
		for _, x := range a {
			if s, _ := x.(string); s == Audience {
				return true
			}
		}
	}
	return false
}

func decodePart(s string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return errors.New("not base64url")
	}
	return json.Unmarshal(b, v)
}

func getJSON(ctx context.Context, c *http.Client, u, auth string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "proctor")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := c.Do(req)
	if err != nil {
		return errors.New(scrubURL(err.Error()))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxBody {
		return errors.New("response larger than 1 MiB")
	}
	if resp.StatusCode != http.StatusOK {
		return &StatusError{Code: resp.StatusCode}
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("response isn't JSON: %v", err)
	}
	return nil
}

type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("HTTP status %d", e.Code) }

var queryPart = regexp.MustCompile(`\?[^\s"]*`)

func scrubURL(s string) string { return queryPart.ReplaceAllString(s, "?…") }

type CodeownersError struct {
	Line    int    `json:"line"`
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

func CodeownersErrors(ctx context.Context, c *http.Client, apiURL, repo, token, ref string) ([]CodeownersError, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`).MatchString(repo) {
		return nil, fmt.Errorf("repository %q isn't owner/name", repo)
	}
	u := strings.TrimRight(apiURL, "/") + "/repos/" + repo + "/codeowners/errors?ref=" + url.QueryEscape(ref)
	var resp struct {
		Errors *[]CodeownersError `json:"errors"`
	}
	if err := getJSON(ctx, c, u, "Bearer "+token, &resp); err != nil {
		var se *StatusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return nil, errors.New("GitHub found no CODEOWNERS file at the base commit (HTTP 404)")
		}
		return nil, err
	}
	if resp.Errors == nil {
		return nil, errors.New("GitHub's answer has no errors list")
	}
	return *resp.Errors, nil
}

func Unowned(codeowners []byte, paths, owners []string) []string {
	type rule struct {
		re     *regexp.Regexp
		owners []string
	}
	var rules []rule
	for _, line := range strings.Split(string(codeowners), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		rules = append(rules, rule{patternRegexp(f[0]), f[1:]})
	}
	want := map[string]bool{}
	for _, o := range owners {
		want[strings.ToLower(o)] = true
	}
	var out []string
	for _, p := range paths {
		owned := false
		for i := len(rules) - 1; i >= 0; i-- {
			if rules[i].re.MatchString(p) {
				for _, o := range rules[i].owners {
					if want[strings.ToLower(o)] {
						owned = true
					}
				}
				break
			}
		}
		if !owned {
			out = append(out, p)
		}
	}
	return out
}

func patternRegexp(p string) *regexp.Regexp {
	anchored := strings.HasPrefix(p, "/") || strings.Contains(strings.TrimSuffix(p, "/"), "/")
	p = strings.TrimPrefix(p, "/")
	dirOnly := strings.HasSuffix(p, "/")
	p = strings.TrimSuffix(p, "/")
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		switch {
		case strings.HasPrefix(p[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i++
		case p[i] == '*':
			b.WriteString("[^/]*")
		case p[i] == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(p[i : i+1]))
		}
	}
	prefix := "^(?:.*/)?"
	if anchored {
		prefix = "^"
	}
	suffix := "(?:/.*)?$"
	if dirOnly {
		suffix = "/.*$"
	}
	if p == "*" || p == "" {
		return regexp.MustCompile("^.*$")
	}
	return regexp.MustCompile(prefix + b.String() + suffix)
}
