package simhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	MaxKept        = 10000
	MaxKeptBytes   = 128 << 20
	MaxBodyKept    = 64 << 10
	MaxBodyRead    = 16 << 20
	MaxAnswer      = 100
	MaxAnswerBytes = 12 << 20
	Redacted       = "[redacted]"
)

type Entry struct {
	Seq           uint64              `json:"seq"`
	At            string              `json:"at"`
	Route         string              `json:"route"`
	Method        string              `json:"method"`
	Host          string              `json:"host"`
	Path          string              `json:"path"`
	Query         map[string][]string `json:"query"`
	Headers       map[string][]string `json:"headers"`
	Body          json.RawMessage     `json:"body,omitempty"`
	BodyText      *string             `json:"body_text,omitempty"`
	BodyTruncated bool                `json:"body_truncated"`
	BodyInvalid   string              `json:"body_invalid,omitempty"`
	BodyBytes     int64               `json:"body_bytes"`
	Status        int                 `json:"status"`
	Fault         bool                `json:"fault"`

	size int64
}

type Journal struct {
	mu      sync.Mutex
	entries []*Entry
	bytes   int64
	seq     uint64
	dropped uint64
}

func (j *Journal) add(e *Entry) uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	e.Seq = j.seq
	j.entries = append(j.entries, e)
	j.bytes += e.size
	drop := 0
	for len(j.entries)-drop > MaxKept || (j.bytes > MaxKeptBytes && len(j.entries)-drop > 1) {
		j.bytes -= j.entries[drop].size
		j.dropped = j.entries[drop].Seq
		j.entries[drop] = nil
		drop++
	}
	if drop > 0 {
		j.entries = append([]*Entry(nil), j.entries[drop:]...)
	}
	return e.Seq
}

func (j *Journal) answered(e *Entry, status int, fault bool) {
	j.mu.Lock()
	e.Status, e.Fault = status, fault
	j.mu.Unlock()
}

func (j *Journal) last() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.seq
}

func (j *Journal) reset() {
	j.mu.Lock()
	j.entries, j.bytes = nil, 0
	j.mu.Unlock()
}

type ReceivedQuery struct {
	After  uint64
	Route  string
	Method string
	Path   string
}

type ReceivedAnswer struct {
	LastSeq      uint64            `json:"last_seq"`
	FirstKeptSeq uint64            `json:"first_kept_seq"`
	More         bool              `json:"more"`
	Total        int               `json:"total"`
	Unmatched    int               `json:"unmatched"`
	Recorded     int               `json:"recorded"`
	Requests     []json.RawMessage `json:"requests"`
}

func (j *Journal) query(q ReceivedQuery) (ReceivedAnswer, error) {
	j.mu.Lock()
	snapshot := make([]Entry, 0, len(j.entries))
	for _, e := range j.entries {
		if e.Seq > q.After {
			snapshot = append(snapshot, *e)
		}
	}
	a := ReceivedAnswer{LastSeq: j.seq, FirstKeptSeq: j.dropped + 1, Recorded: len(j.entries), Requests: []json.RawMessage{}}
	j.mu.Unlock()
	size := 0
	for i := range snapshot {
		e := &snapshot[i]
		if e.Route == "" {
			a.Unmatched++
		}
		if q.Route != "" && e.Route != q.Route || q.Method != "" && e.Method != q.Method || q.Path != "" && e.Path != q.Path {
			continue
		}
		a.Total++
		if a.More {
			continue
		}
		b, err := json.Marshal(e)
		if err != nil {
			return ReceivedAnswer{}, err
		}
		if len(a.Requests) >= MaxAnswer || size+len(b) > MaxAnswerBytes {
			a.More = true
			continue
		}
		size += len(b)
		a.Requests = append(a.Requests, b)
	}
	return a, nil
}

type capture struct {
	raw   []byte
	total int64
	value any
	json  bool
}

func readBody(r io.Reader) capture {
	var c capture
	buf := &bytes.Buffer{}
	n, _ := io.Copy(buf, io.LimitReader(r, MaxBodyRead))
	rest, _ := io.Copy(io.Discard, r)
	c.raw, c.total = buf.Bytes(), n+rest
	if len(c.raw) > 0 && c.total <= MaxBodyRead {
		if v, reason, _ := parseJSON(c.raw); reason == "" {
			c.value, c.json = v, true
		}
	}
	return c
}

func (rs *Routes) entry(r *http.Request, route string, body capture) *Entry {
	e := &Entry{
		At:        time.Now().UTC().Format(time.RFC3339Nano),
		Route:     route,
		Method:    r.Method,
		Host:      r.Host,
		Path:      r.URL.EscapedPath(),
		Query:     map[string][]string{},
		Headers:   map[string][]string{},
		BodyBytes: body.total,
	}
	for name, values := range r.URL.Query() {
		kept := append([]string(nil), values...)
		if rs.redactQuery[name] {
			for i := range kept {
				kept[i] = Redacted
			}
		}
		e.Query[name] = kept
		e.size += int64(len(name))
		for _, v := range kept {
			e.size += int64(len(v))
		}
	}
	for name, values := range r.Header {
		lower := strings.ToLower(name)
		for _, v := range values {
			if rs.redactHeaders[lower] {
				v = Redacted
			}
			e.Headers[lower] = append(e.Headers[lower], v)
			e.size += int64(len(lower) + len(v))
		}
	}
	rs.keepBody(e, r.Header.Get("Content-Type"), body)
	e.size += int64(len(e.Body))
	if e.BodyText != nil {
		e.size += int64(len(*e.BodyText))
	}
	return e
}

func (rs *Routes) keepBody(e *Entry, contentType string, body capture) {
	if len(body.raw) == 0 {
		return
	}
	text := func(b []byte, cut bool) {
		s := cutText(b, MaxBodyKept)
		e.BodyText = &s
		e.BodyTruncated = cut || len(b) > MaxBodyKept
	}
	if body.json {
		v := body.value
		for _, p := range rs.redactPaths {
			v = redactPath(v, p)
		}
		b, err := json.Marshal(v)
		if err == nil && len(b) <= MaxBodyKept {
			e.Body = b
			return
		}
		if err == nil {
			text(b, true)
			return
		}
	}
	if body.total > MaxBodyRead {
		text(body.raw, true)
		return
	}
	_, reason, syntaxOK := parseJSON(body.raw)
	text(body.raw, false)
	if syntaxOK || claimsJSON(contentType) {
		e.BodyInvalid = reason
	}
}

func redactPath(v any, path []string) any {
	switch x := v.(type) {
	case map[string]any:
		val, ok := x[path[0]]
		if !ok {
			return v
		}
		if len(path) == 1 {
			x[path[0]] = Redacted
		} else {
			x[path[0]] = redactPath(val, path[1:])
		}
	case []any:
		for i := range x {
			x[i] = redactPath(x[i], path)
		}
	}
	return v
}

func claimsJSON(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

func cutText(b []byte, limit int) string {
	if len(b) <= limit {
		return string(b)
	}
	end := limit
	for end > 0 && end > limit-utf8.UTFMax && !utf8.RuneStart(b[end]) {
		end--
	}
	return string(b[:end])
}

func parseJSON(b []byte) (value any, reason string, syntaxOK bool) {
	v, err := decodeJSON(b)
	if err != nil {
		return nil, err.Error(), false
	}
	if k, dup := duplicateKey(b); dup {
		return nil, fmt.Sprintf("duplicate object key %q", k), true
	}
	return v, "", true
}

func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after the JSON value")
	}
	return v, nil
}

func duplicateKey(b []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	type frame struct {
		obj    bool
		seen   map[string]bool
		expKey bool
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", false
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if top != nil && top.obj && top.expKey {
			if d, ok := tok.(json.Delim); ok && d == '}' {
				stack = stack[:len(stack)-1]
				if len(stack) > 0 && stack[len(stack)-1].obj {
					stack[len(stack)-1].expKey = true
				}
				continue
			}
			k := tok.(string)
			if top.seen[k] {
				return k, true
			}
			top.seen[k] = true
			top.expKey = false
			continue
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{':
				stack = append(stack, &frame{obj: true, seen: map[string]bool{}, expKey: true})
				continue
			case '[':
				stack = append(stack, &frame{})
				continue
			case ']':
				stack = stack[:len(stack)-1]
			}
		}
		if len(stack) > 0 && stack[len(stack)-1].obj {
			stack[len(stack)-1].expKey = true
		}
		if len(stack) == 0 && !dec.More() {
			return "", false
		}
	}
}
