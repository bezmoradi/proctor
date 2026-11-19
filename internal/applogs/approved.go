package applogs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"proctor/internal/model"
	"proctor/internal/transcript"
)

const Redacted = "[redacted]"

type LineKind int

const (
	LineParsed LineKind = iota
	LineBlank
	LineTrace
	LineUnparsed
)

type Tracker struct {
	trace bool
}

func (t *Tracker) Next(line string, rd Reading) LineKind {
	switch {
	case rd.Parsed:
		t.trace = rd.Runtime
		return LineParsed
	case strings.TrimSpace(line) == "":
		return LineBlank
	case t.trace:
		return LineTrace
	}
	return LineUnparsed
}

type Value struct {
	Name    string
	Text    string
	Str     bool
	Present bool
}

type Picked struct {
	Level   string
	Message string
	Fields  []Value
	Runtime bool
}

type Collected struct {
	Lines    []Picked
	Unparsed int
	Skipped  int
}

type Filter struct {
	Field, Value string
}

func (e Entry) value(name string) (any, bool) {
	if e.object != nil {
		v, ok := e.object[name]
		return v, ok
	}
	if e.pairs != nil {
		v, ok := e.pairs[name]
		return bare(v.text), ok
	}
	return nil, false
}

type bare string

func (e Entry) text(name string) (string, bool) {
	v, ok := e.value(name)
	if !ok {
		return "", false
	}
	switch x := v.(type) {
	case string:
		return x, true
	case bare:
		return string(x), true
	}
	return text(v), true
}

func (r Rules) Collect(lines []string, fields []string, redact func(string) bool, filters []Filter) Collected {
	var c Collected
	var tr Tracker
	for _, line := range lines {
		rd := r.Read(line)
		switch tr.Next(line, rd) {
		case LineUnparsed:
			c.Unparsed++
			continue
		case LineBlank, LineTrace:
			continue
		}
		if !matches(rd, filters) {
			c.Skipped++
			continue
		}
		e := rd.Entry
		if rd.Runtime {
			c.Lines = append(c.Lines, Picked{Level: "RUNTIME", Message: line, Runtime: true})
			continue
		}
		level := e.Level
		if level == "" {
			level = e.Number
		}
		p := Picked{Level: level, Message: e.Message}
		for _, name := range fields {
			v, ok := e.value(name)
			val := Value{Name: name, Present: ok}
			if ok {
				val.Text, val.Str = render(v, redact)
				if redact(name) {
					val.Text, val.Str = Redacted, false
				}
			}
			p.Fields = append(p.Fields, val)
		}
		c.Lines = append(c.Lines, p)
	}
	return c
}

func matches(rd Reading, filters []Filter) bool {
	if len(filters) == 0 {
		return true
	}
	if rd.Runtime {
		return false
	}
	for _, f := range filters {
		got, ok := rd.Entry.text(f.Field)
		if !ok || got != f.Value {
			return false
		}
	}
	return true
}

func render(v any, redact func(string) bool) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bare:
		return string(x), false
	case nil:
		return "null", false
	case bool:
		return strconv.FormatBool(x), false
	case json.Number:
		return x.String(), false
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(redactNested(v, redact)); err != nil {
		return Redacted, false
	}
	return strings.TrimSuffix(b.String(), "\n"), true
}

func redactNested(v any, redact func(string) bool) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			if redact(k) {
				out[k] = Redacted
				continue
			}
			out[k] = redactNested(e, redact)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = redactNested(e, redact)
		}
		return out
	}
	return v
}

type Scrub struct {
	Clean   func(string) string
	Subs    []transcript.Sub
	Rules   []transcript.LogRule
	Message string
	Sorted  bool
}

func (s Scrub) prepare(c Collected) []Picked {
	out := make([]Picked, len(c.Lines))
	for i, p := range c.Lines {
		q := Picked{Level: s.Clean(p.Level), Message: transcript.ReplaceSubs(s.Clean(p.Message), s.Subs), Runtime: p.Runtime}
		for _, f := range p.Fields {
			if f.Present {
				f.Text = transcript.ReplaceSubs(s.Clean(f.Text), s.Subs)
			}
			q.Fields = append(q.Fields, f)
		}
		out[i] = q
	}
	return out
}

func (s Scrub) apply(lines []Picked, numbered bool) []Picked {
	out := make([]Picked, len(lines))
	for i, p := range lines {
		out[i] = Picked{Level: p.Level, Message: p.Message, Runtime: p.Runtime, Fields: append([]Value(nil), p.Fields...)}
	}
	for _, rule := range s.Rules {
		c := transcript.NewCounter(rule.Name, numbered && rule.Numbered)
		at := func(v string) string {
			if rule.Pattern == nil {
				return c.Whole(v)
			}
			return c.Matches(v, rule.Pattern)
		}
		for i := range out {
			p := &out[i]
			switch {
			case rule.Field == "":
				p.Message = c.Matches(p.Message, rule.Pattern)
				for j := range p.Fields {
					if p.Fields[j].Present {
						p.Fields[j].Text = c.Matches(p.Fields[j].Text, rule.Pattern)
					}
				}
			case p.Runtime:
			case rule.Field == s.Message:
				p.Message = at(p.Message)
			default:
				for j := range p.Fields {
					if p.Fields[j].Present && p.Fields[j].Name == rule.Field {
						p.Fields[j].Text = at(p.Fields[j].Text)
					}
				}
			}
		}
	}
	return out
}

func Render(c Collected, s Scrub) []byte {
	lines := s.prepare(c)
	if s.Sorted {
		shapes := s.apply(lines, false)
		keys := make([]string, len(lines))
		for i := range shapes {
			keys[i] = line(shapes[i])
		}
		idx := make([]int, len(lines))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool { return keys[idx[a]] < keys[idx[b]] })
		sorted := make([]Picked, len(lines))
		for i, j := range idx {
			sorted[i] = lines[j]
		}
		lines = sorted
	}
	lines = s.apply(lines, true)
	var b strings.Builder
	b.WriteString(model.ApprovedLogFirstLine + "\n")
	for _, p := range lines {
		b.WriteString(line(p) + "\n")
	}
	fmt.Fprintf(&b, "%s%d\n", model.ApprovedLogTrailer, c.Unparsed)
	return []byte(b.String())
}

func line(p Picked) string {
	if p.Runtime {
		return "RUNTIME " + plain(p.Message)
	}
	parts := []string{quote(p.Level, false), quote(p.Message, false)}
	for _, f := range p.Fields {
		if f.Present {
			parts = append(parts, f.Name+"="+quote(f.Text, f.Str))
		}
	}
	return strings.Join(parts, " ")
}

var literal = regexp.MustCompile(`^(-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?|true|false|null)$`)

func quote(s string, str bool) string {
	if s == "" || (str && literal.MatchString(s)) || !utf8.ValidString(s) {
		return strconv.Quote(s)
	}
	for _, r := range s {
		if r == ' ' || r == '=' || r == '"' || r == '\\' || !unicode.IsPrint(r) {
			return strconv.Quote(s)
		}
	}
	return s
}

func plain(s string) string {
	if !utf8.ValidString(s) {
		return strconv.Quote(s)
	}
	for _, r := range s {
		if r != ' ' && !unicode.IsPrint(r) {
			return strconv.Quote(s)
		}
	}
	return s
}

func ValidApprovedLog(data []byte) error {
	if !bytes.HasPrefix(data, []byte(model.ApprovedLogFirstLine+"\n")) {
		return fmt.Errorf("its first line must be %q", model.ApprovedLogFirstLine)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		return fmt.Errorf("it must end with a line break")
	}
	body := strings.TrimSuffix(string(data), "\n")
	last := body[strings.LastIndexByte(body, '\n')+1:]
	n, ok := strings.CutPrefix(last, model.ApprovedLogTrailer)
	if !ok || n == "" || strings.TrimLeft(n, "0123456789") != "" || (len(n) > 1 && n[0] == '0') {
		return fmt.Errorf("its last line must be %q followed by a count", model.ApprovedLogTrailer)
	}
	return nil
}
