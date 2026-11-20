package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bezmoradi/proctor/protocol"
)

var (
	refRE    = regexp.MustCompile(`\$\{([^}]*)\}`)
	fillRE   = regexp.MustCompile(`^fill\(([1-9][0-9]{0,7})\)$`)
	secretRE = regexp.MustCompile(`^secret\.[A-Za-z_][A-Za-z0-9_]*$`)
	nameRE   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
)

func errCause(ctx context.Context, err error) string {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || (errors.As(err, &ne) && ne.Timeout()) {
		return protocol.CauseTimeout
	}
	return protocol.CauseUnreachable
}

func scalarText(v any) (string, error) {
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

func show(x any) string {
	if s, ok := x.(string); ok {
		return strconv.Quote(s)
	}
	if x == nil {
		return "nothing"
	}
	b, err := json.Marshal(x)
	if err != nil {
		return fmt.Sprintf("%v", x)
	}
	return string(b)
}

func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if (s[i] < ' ' && s[i] != '\t') || s[i] == 0x7f {
			return true
		}
	}
	return false
}

func typedControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if (s[i] < ' ' && s[i] != '\t' && s[i] != '\n') || s[i] == 0x7f || s[i] == '\r' {
			return true
		}
	}
	return false
}

func isTokenChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isTokenChar(s[i]) {
			return false
		}
	}
	return true
}

func isCookieValue(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c >= 0x7f || c == '"' || c == ',' || c == ';' || c == '\\' {
			return false
		}
	}
	return s != ""
}

func checkPath(p string, resolved bool) error {
	after := ""
	if resolved {
		after = " after inserting saved values"
	}
	switch {
	case !strings.HasPrefix(p, "/"):
		return fmt.Errorf("path %q must start with /%s", p, after)
	case strings.HasPrefix(p, doubleSlash):
		return fmt.Errorf("path %q starts with %s%s, which names a host", p, doubleSlash, after)
	case strings.ContainsAny(p, "?#\\"):
		return fmt.Errorf("path %q contains ?, # or \\%s; put query parameters in query", p, after)
	}
	for _, r := range p {
		if r <= ' ' || r == 0x7f {
			return fmt.Errorf("path %q contains a space or control character%s", p, after)
		}
	}
	if !resolved {
		lower := strings.ToLower(p)
		if strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") {
			return fmt.Errorf("path %q contains a percent-encoded / or \\", p)
		}
	}
	for _, seg := range strings.Split(p[1:], "/") {
		dec, err := url.PathUnescape(seg)
		if err != nil {
			return fmt.Errorf("path %q: %v", p, err)
		}
		if dec == "." || dec == ".." {
			return fmt.Errorf("path %q contains a . or .. segment%s", p, after)
		}
	}
	return nil
}

func originOf(u *url.URL) (string, bool) {
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	switch scheme {
	case "http", "ws":
		scheme = "http"
		if port == "80" {
			port = ""
		}
	case "https", "wss":
		scheme = "https"
		if port == "443" {
			port = ""
		}
	default:
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", false
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host, true
}

func parseOrigin(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil {
		return "", err
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || u.Opaque != "" || hasControl(s) {
		return "", fmt.Errorf("must be a scheme and host such as https://cdn.example.com, with no path, query or credentials")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("must be an http or https origin")
	}
	o, ok := originOf(u)
	if !ok {
		return "", fmt.Errorf("has no host")
	}
	return o, nil
}

func refNames(v any) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			for _, m := range refRE.FindAllStringSubmatch(x, -1) {
				out = append(out, m[1])
			}
		case map[string]any:
			for _, k := range sortedKeys(x) {
				walk(k)
				walk(x[k])
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

func uniqueSorted(names []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func fillSize(name string) (int, bool) {
	m := fillRE.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

func isFill(name string) bool {
	n, ok := fillSize(name)
	return ok && n <= maxFill
}

func isSecret(name string) bool {
	return secretRE.MatchString(name)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func cut(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	end := limit
	for end > 0 && end > limit-utf8.UTFMax && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

func excerpt(s string) string {
	c := cut(s, excerptBytes)
	if len(c) < len(s) {
		c += fmt.Sprintf("\n[%d more bytes]", len(s)-len(c))
	}
	return c
}
