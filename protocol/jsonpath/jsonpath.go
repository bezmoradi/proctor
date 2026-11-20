package jsonpath

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxNameBytes = 256

type Segment struct {
	Name    string
	Index   int
	IsIndex bool
}

func Parse(s string) ([]Segment, error) {
	if !strings.HasPrefix(s, "$") {
		return nil, fmt.Errorf("JSONPath %q must start with $", s)
	}
	var segs []Segment
	rest := s[1:]
	for rest != "" {
		switch rest[0] {
		case '.':
			n := plainLen(rest[1:])
			if n == 0 {
				return nil, fmt.Errorf("JSONPath %q: expected a field name after '.' (a letter or _, then letters, digits, _ or -); write a name with other characters in brackets, as ['8080/tcp']", s)
			}
			segs = append(segs, Segment{Name: rest[1 : 1+n]})
			rest = rest[1+n:]
		case '[':
			if len(rest) > 1 && (rest[1] == '\'' || rest[1] == '"') {
				name, n, err := quoted(rest[1:])
				if err != nil {
					return nil, fmt.Errorf("JSONPath %q: %v", s, err)
				}
				rest = rest[1+n:]
				if !strings.HasPrefix(rest, "]") {
					return nil, fmt.Errorf("JSONPath %q: expected ] after the quoted name %q", s, name)
				}
				segs = append(segs, Segment{Name: name})
				rest = rest[1:]
				continue
			}
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return nil, fmt.Errorf("JSONPath %q: unclosed '['", s)
			}
			digits := rest[1:end]
			n, err := strconv.Atoi(digits)
			if err != nil || !wholeNumber(digits) {
				return nil, fmt.Errorf("JSONPath %q: [%s] is neither an index (a non-negative whole number without a sign or leading zero) nor a quoted name (['name'] or [\"name\"])", s, digits)
			}
			segs = append(segs, Segment{Index: n, IsIndex: true})
			rest = rest[end+1:]
		default:
			return nil, fmt.Errorf("JSONPath %q: unsupported syntax at %q (supported: $, .name, [index] and ['name'])", s, rest)
		}
	}
	return segs, nil
}

func ParseRelative(s string) ([]Segment, error) {
	switch {
	case strings.HasPrefix(s, "$"):
		return Parse(s)
	case strings.HasPrefix(s, "["):
		return Parse("$" + s)
	}
	return Parse("$." + s)
}

func String(segs []Segment) string {
	var b strings.Builder
	b.WriteByte('$')
	for _, s := range segs {
		b.WriteString(s.text())
	}
	return b.String()
}

func Relative(segs []Segment) string {
	if len(segs) == 0 {
		return "$"
	}
	return strings.TrimPrefix(String(segs)[1:], ".")
}

func Canonical(s string) (string, error) {
	segs, err := Parse(s)
	if err != nil {
		return "", err
	}
	return String(segs), nil
}

func Child(path, name string) string {
	return path + Segment{Name: name}.text()
}

func Plain(name string) bool {
	return name != "" && plainLen(name) == len(name)
}

func (s Segment) text() string {
	switch {
	case s.IsIndex:
		return "[" + strconv.Itoa(s.Index) + "]"
	case Plain(s.Name):
		return "." + s.Name
	}
	return "['" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s.Name) + "']"
}

func plainLen(s string) int {
	for i := 0; i < len(s); i++ {
		c := s[i]
		letter := c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
		if !letter && (i == 0 || c != '-' && (c < '0' || c > '9')) {
			return i
		}
	}
	return len(s)
}

func wholeNumber(s string) bool {
	if s == "" || len(s) > 1 && s[0] == '0' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func quoted(s string) (string, int, error) {
	q := s[0]
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c == q:
			name := b.String()
			if err := checkName(name); err != nil {
				return "", 0, err
			}
			return name, i + 1, nil
		case c == '\\':
			if i+1 == len(s) || !strings.ContainsRune(`\'"`, rune(s[i+1])) {
				return "", 0, fmt.Errorf("a backslash in a quoted name must be followed by \\, ' or \"")
			}
			i++
			b.WriteByte(s[i])
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, fmt.Errorf("the quoted name has no closing %c", q)
}

func checkName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("a quoted name can't be empty")
	case len(name) > MaxNameBytes:
		return fmt.Errorf("a quoted name can be at most %d bytes, not %d", MaxNameBytes, len(name))
	case !utf8.ValidString(name):
		return fmt.Errorf("a quoted name must be valid UTF-8")
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		return fmt.Errorf("a quoted name can't hold control characters")
	}
	return nil
}
