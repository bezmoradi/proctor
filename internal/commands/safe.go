package commands

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

func unsafeRune(r rune) bool {
	switch {
	case unicode.IsControl(r):
		return true
	case r == ' ', r == ' ':
		return true
	case r == '؜', r == '‎', r == '‏':
		return true
	case r >= '‪' && r <= '‮':
		return true
	case r >= '⁦' && r <= '⁩':
		return true
	}
	return false
}

func safe(s string) string {
	clean := true
	for i, r := range s {
		if unsafeRune(r) || (r == utf8.RuneError && !strings.HasPrefix(s[i:], "�")) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case unsafeRune(r) && r < 0x80:
			fmt.Fprintf(&b, `\x%02x`, r)
		case unsafeRune(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}

func safeAll(vals []string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = safe(v)
	}
	return out
}

func code(s string) string {
	s = safe(s)
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	if longest == 0 {
		return "`" + s + "`"
	}
	fence := strings.Repeat("`", longest+1)
	return fence + " " + s + " " + fence
}

func md(s string) string {
	s = safe(s)
	var b strings.Builder
	for _, r := range s {
		if r < 0x80 && (unicode.IsPunct(r) || unicode.IsSymbol(r)) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
