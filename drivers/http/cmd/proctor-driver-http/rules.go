package main

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

var (
	refRE    = regexp.MustCompile(`\$\{([^}]*)\}`)
	wholeRef = regexp.MustCompile(`^\$\{[^{}]+\}$`)
	fillRE   = regexp.MustCompile(`^fill\(([1-9][0-9]{0,7})\)$`)
	secretRE = regexp.MustCompile(`^secret\.[A-Za-z_][A-Za-z0-9_]*$`)
)

const (
	maxFill     = 16 << 20
	doubleSlash = "/" + "/"
)

func knownMethod(m string) bool {
	return slices.Contains(methods, m)
}

func observes(m string) bool {
	return m == "GET" || m == "HEAD" || m == "OPTIONS"
}

func destructive(m string) bool {
	return m == "PUT" || m == "PATCH" || m == "DELETE"
}

func checkRequestPath(p string) error {
	switch {
	case !strings.HasPrefix(p, "/"):
		return fmt.Errorf("path %q must start with /", p)
	case strings.HasPrefix(p, doubleSlash):
		return fmt.Errorf("path %q starts with %s, which names a host", p, doubleSlash)
	case strings.ContainsAny(p, "?#\\"):
		return fmt.Errorf("path %q contains ?, # or \\; put query parameters in query", p)
	}
	for _, r := range p {
		if r <= ' ' || r == 0x7f {
			return fmt.Errorf("path %q contains a space or control character", p)
		}
	}
	lower := strings.ToLower(p)
	if strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") {
		return fmt.Errorf("path %q contains a percent-encoded / or \\", p)
	}
	for _, seg := range strings.Split(p[1:], "/") {
		dec, err := url.PathUnescape(seg)
		if err != nil {
			return fmt.Errorf("path %q: %v", p, err)
		}
		if dec == "." || dec == ".." {
			return fmt.Errorf("path %q contains a . or .. segment", p)
		}
	}
	return nil
}

func checkResolvedPath(p string) error {
	switch {
	case !strings.HasPrefix(p, "/"):
		return fmt.Errorf("path %q must start with / after inserting saved values (HTTP-05)", p)
	case strings.HasPrefix(p, doubleSlash):
		return fmt.Errorf("path %q starts with %s after inserting saved values (HTTP-05)", p, doubleSlash)
	case strings.ContainsAny(p, "?#\\"):
		return fmt.Errorf("path %q contains ?, # or \\ after inserting saved values (HTTP-05)", p)
	}
	for _, r := range p {
		if r <= ' ' || r == 0x7f {
			return fmt.Errorf("path %q contains a space or control character after inserting saved values (HTTP-05)", p)
		}
	}
	for _, seg := range strings.Split(p[1:], "/") {
		dec, err := url.PathUnescape(seg)
		if err != nil {
			return fmt.Errorf("path %q: %v (HTTP-05)", p, err)
		}
		if seg == "." || seg == ".." || dec == "." || dec == ".." {
			return fmt.Errorf("path %q has a . or .. segment after inserting saved values (HTTP-05)", p)
		}
	}
	return nil
}

func forbiddenHeader(name string) (rule, why string) {
	switch strings.ToLower(name) {
	case "host":
		return "HTTP-05", "the target comes from the binding"
	case "user-agent":
		return "HTTP-04", "the User-Agent comes from the binding"
	case "authorization", "proxy-authorization", "cookie":
		return "AUTH-02", "credentials come from identities, never from specifications"
	case "content-length", "transfer-encoding", "connection":
		return "EXE-01", "the HTTP client sets it"
	}
	return "", ""
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

func isFill(name string) bool {
	m := fillRE.FindStringSubmatch(name)
	if m == nil {
		return false
	}
	n, _ := strconv.Atoi(m[1])
	return n <= maxFill
}

func isSecret(name string) bool {
	return secretRE.MatchString(name)
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

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
