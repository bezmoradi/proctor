package ws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"proctor/protocol"
)

var (
	refRE    = regexp.MustCompile(`\$\{([^}]*)\}`)
	fillRE   = regexp.MustCompile(`^fill\(([1-9][0-9]{0,7})\)$`)
	secretRE = regexp.MustCompile(`^secret\.[A-Za-z_][A-Za-z0-9_]*$`)
)

func parseJSON(b []byte) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	if duplicateKey(b) {
		return nil, false
	}
	return v, true
}

func duplicateKey(b []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	dup, err := scanValue(dec)
	return err != nil || dup
}

func scanValue(dec *json.Decoder) (bool, error) {
	tok, err := dec.Token()
	if err != nil {
		return false, err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return false, nil
	}
	switch d {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return false, err
			}
			k, _ := kt.(string)
			if seen[k] {
				return true, nil
			}
			seen[k] = true
			if dup, err := scanValue(dec); dup || err != nil {
				return dup, err
			}
		}
	case '[':
		for dec.More() {
			if dup, err := scanValue(dec); dup || err != nil {
				return dup, err
			}
		}
	}
	_, err = dec.Token()
	return false, err
}

func compactJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func dialCause(err error) string {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
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

func forbiddenHeader(name string) (rule, why string) {
	lower := strings.ToLower(name)
	switch {
	case lower == "host":
		return "EXE-01", "the target comes from the binding"
	case lower == "authorization" || lower == "proxy-authorization" || lower == "cookie":
		return "AUTH-02", "credentials come from identities, never from specifications"
	case lower == "connection" || lower == "upgrade" || lower == "content-length" || lower == "transfer-encoding":
		return "EXE-01", "the WebSocket client sets it"
	case lower == "sec-websocket-protocol":
		return "EXE-01", "list subprotocols in subprotocols"
	case strings.HasPrefix(lower, "sec-websocket-"):
		return "EXE-01", "the WebSocket client sets it"
	}
	return "", ""
}

func protocolHeader(name string) bool {
	rule, _ := forbiddenHeader(name)
	return rule == "EXE-01"
}

type authPlace struct {
	kind string
	name string
}

func parseAuth(s string) (authPlace, error) {
	switch {
	case s == "" || s == "subprotocol":
		return authPlace{kind: "subprotocol"}, nil
	case strings.HasPrefix(s, "query:"):
		name := strings.TrimPrefix(s, "query:")
		if name == "" || hasControl(name) {
			return authPlace{}, fmt.Errorf("auth %q needs a query parameter name after query:", s)
		}
		return authPlace{kind: "query", name: name}, nil
	case strings.HasPrefix(s, "header:"):
		name := strings.TrimPrefix(s, "header:")
		if !isToken(name) {
			return authPlace{}, fmt.Errorf("auth %q needs a valid HTTP header name after header:", s)
		}
		if protocolHeader(name) {
			return authPlace{}, fmt.Errorf("auth %q names a header the WebSocket handshake owns", s)
		}
		return authPlace{kind: "header", name: name}, nil
	}
	return authPlace{}, fmt.Errorf("auth %q must be subprotocol, query:<name> or header:<name>", s)
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

func isFill(name string) bool {
	m := fillRE.FindStringSubmatch(name)
	if m == nil {
		return false
	}
	n, _ := strconv.Atoi(m[1])
	return n <= maxBytes
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
