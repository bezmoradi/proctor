package applogs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"proctor/internal/expr"
)

type Mark struct {
	Offset  int64
	info    os.FileInfo
	seq     uint64
	refused uint64
}

type Window struct {
	Lines []string
	Note  string
}

type Source interface {
	Mark() (Mark, error)
	Since(m Mark) (Window, error)
}

type File struct {
	Path string
}

const backChunk = 64 << 10

func (f File) open() (*os.File, os.FileInfo, error) {
	fh, err := os.Open(f.Path)
	if err != nil {
		return nil, nil, err
	}
	st, err := fh.Stat()
	if err != nil {
		fh.Close()
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		fh.Close()
		return nil, nil, fmt.Errorf("%s is not a regular file", f.Path)
	}
	return fh, st, nil
}

func (f File) Mark() (Mark, error) {
	fh, st, err := f.open()
	if err != nil {
		return Mark{}, err
	}
	defer fh.Close()
	off, err := lineStart(fh, st.Size())
	if err != nil {
		return Mark{}, err
	}
	return Mark{Offset: off, info: st}, nil
}

func lineStart(r io.ReaderAt, size int64) (int64, error) {
	end := size
	buf := make([]byte, backChunk)
	for end > 0 {
		start := end - backChunk
		if start < 0 {
			start = 0
		}
		n, err := r.ReadAt(buf[:end-start], start)
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if i := bytes.LastIndexByte(buf[:n], '\n'); i >= 0 {
			return start + int64(i) + 1, nil
		}
		end = start
	}
	return 0, nil
}

func (f File) Since(m Mark) (Window, error) {
	fh, st, err := f.open()
	if err != nil {
		return Window{}, err
	}
	defer fh.Close()
	var w Window
	start := m.Offset
	switch {
	case m.info != nil && !os.SameFile(m.info, st):
		start = 0
		w.Note = "the log file was replaced while the scenario ran (rotated?); its new file was read from the start"
	case st.Size() < m.Offset:
		start = 0
		w.Note = fmt.Sprintf("the log file shrank below the scenario's start position (%d bytes, now %d; truncated or rotated); it was read from the start", m.Offset, st.Size())
	}
	if _, err := fh.Seek(start, io.SeekStart); err != nil {
		return Window{}, err
	}
	rd := bufio.NewReader(fh)
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return w, nil
			}
			return Window{}, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		w.Lines = append(w.Lines, line)
	}
}

type Rules struct {
	Format       string
	Levels       []string
	LevelField   string
	MessageField string
	Numbers      map[int64]string
}

type Entry struct {
	Level   string
	Number  string
	Message string
	Time    string
	Line    string

	object map[string]any
	pairs  map[string]logfmtValue
}

type Reading struct {
	Entry    Entry
	Parsed   bool
	Failing  bool
	Runtime  bool
	Unmapped string
}

const formatLogfmt = "logfmt"

var runtimePrefixes = []struct{ prefix, level string }{
	{"panic:", "PANIC"},
	{"fatal error:", "FATAL"},
}

func (r Rules) failing(level string) bool {
	u := strings.ToUpper(level)
	for _, l := range r.Levels {
		if strings.ToUpper(l) == u {
			return true
		}
	}
	return false
}

func (r Rules) Read(line string) Reading {
	var e Entry
	var ok bool
	if r.Format == formatLogfmt {
		e, ok = r.logfmtEntry(line)
	} else {
		e, ok = r.jsonEntry(line)
	}
	if ok {
		e.Line = line
		if e.Number != "" {
			name, mapped := r.mapNumber(e.Number)
			if !mapped {
				return Reading{Entry: e, Parsed: true, Unmapped: e.Number}
			}
			e.Level = name
		}
		return Reading{Entry: e, Parsed: true, Failing: r.failing(e.Level)}
	}
	for _, p := range runtimePrefixes {
		if strings.HasPrefix(line, p.prefix) {
			e := Entry{Level: p.level, Message: strings.TrimSpace(strings.TrimPrefix(line, p.prefix)), Line: line}
			return Reading{Entry: e, Parsed: true, Failing: r.failing(p.level), Runtime: true}
		}
	}
	return Reading{}
}

func (r Rules) jsonEntry(line string) (Entry, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{") {
		return Entry{}, false
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || dec.More() {
		return Entry{}, false
	}
	e := Entry{Message: text(obj[r.MessageField]), Time: text(obj["time"]), object: obj}
	switch v := obj[r.LevelField].(type) {
	case string:
		e.Level = v
	case json.Number:
		e.Number = v.String()
	default:
		return Entry{}, false
	}
	return e, true
}

var numberText = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

func (r Rules) mapNumber(s string) (string, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return "", false
	}
	name, ok := r.Numbers[n]
	return name, ok
}

func text(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

type Hit func(path, name string) bool

func RedactLine(line string, hit Hit) (string, []string) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "{") {
		dec := json.NewDecoder(strings.NewReader(trimmed))
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == nil && !dec.More() {
			var found []string
			if out, changed := redactRaw(raw, "$", hit, &found); changed {
				return string(out), found
			}
			return line, nil
		}
	}
	pairs, ok := scanLogfmt(line)
	if !ok {
		return line, nil
	}
	var b strings.Builder
	var found []string
	at := 0
	for _, p := range pairs {
		if !hit(expr.Child("$", p.key), p.key) {
			continue
		}
		if !numberText.MatchString(p.value.text) {
			found = append(found, p.value.text)
		}
		b.WriteString(line[at:p.start])
		b.WriteString(Redacted)
		at = p.end
	}
	if at == 0 {
		return line, nil
	}
	b.WriteString(line[at:])
	return b.String(), found
}

func redactRaw(raw json.RawMessage, path string, hit Hit, found *[]string) (json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return raw, false
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if _, err := dec.Token(); err != nil {
		return raw, false
	}
	var parts [][]byte
	changed := false
	for i := 0; dec.More(); i++ {
		var key []byte
		child := fmt.Sprintf("%s[%d]", path, i)
		name := ""
		if trimmed[0] == '{' {
			tok, err := dec.Token()
			k, isKey := tok.(string)
			if err != nil || !isKey {
				return raw, false
			}
			key, name, child = encodeKey(k), k, expr.Child(path, k)
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return raw, false
		}
		if hit(child, name) {
			*found = append(*found, rawLeaves(v)...)
			v, changed = json.RawMessage(`"`+Redacted+`"`), true
		} else if out, c := redactRaw(v, child, hit, found); c {
			v, changed = out, true
		}
		if key != nil {
			parts = append(parts, append(append(key, ':'), v...))
		} else {
			parts = append(parts, v)
		}
	}
	if !changed {
		return raw, false
	}
	open, end := byte('{'), byte('}')
	if trimmed[0] == '[' {
		open, end = '[', ']'
	}
	out := append([]byte{open}, bytes.Join(parts, []byte(","))...)
	return append(out, end), true
}

func encodeKey(k string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(k); err != nil {
		return []byte(strconv.Quote(k))
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

func rawLeaves(raw json.RawMessage) []string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return leaves(v)
}

func leaves(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case map[string]any:
		var out []string
		for _, e := range x {
			out = append(out, leaves(e)...)
		}
		return out
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, leaves(e)...)
		}
		return out
	}
	return nil
}
