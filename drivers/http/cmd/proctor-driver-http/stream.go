package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	formatNDJSON    = "ndjson"
	formatSSE       = "sse"
	maxStreamFrames = 10000
	maxStreamBytes  = 4 << 20
	maxFrameBytes   = 1 << 20
	streamTextBytes = 1 << 20
	streamChunk     = 32 << 10
	streamCloseWait = 5 * time.Second
	defaultEvent    = "message"
	serverEnded     = "the server ended the response"
)

var streamFormats = []string{formatNDJSON, formatSSE}

type stream struct {
	format string
	body   io.ReadCloser
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once

	mu          sync.Mutex
	frames      []map[string]any
	frameBytes  int
	text        []byte
	textCut     bool
	overflow    bool
	overflowWhy string
	closed      bool
	closeReason string
	retry       int64
	hasRetry    bool
	shutting    bool
}

type streamSnapshot struct {
	frames      []map[string]any
	text        string
	textCut     bool
	overflow    bool
	overflowWhy string
	closed      bool
	closeReason string
	retry       int64
	hasRetry    bool
}

func startStream(format string, body io.ReadCloser, cancel context.CancelFunc) *stream {
	s := &stream{format: format, body: body, cancel: cancel, done: make(chan struct{})}
	go s.read()
	return s
}

func (s *stream) read() {
	defer close(s.done)
	var p parser
	if s.format == formatSSE {
		p = &sseParser{s: s, event: defaultEvent}
	} else {
		p = &ndjsonParser{s: s}
	}
	sp := &splitter{sse: s.format == formatSSE, line: p.line}
	buf := make([]byte, streamChunk)
	for {
		n, err := s.body.Read(buf)
		if n > 0 {
			s.keepText(buf[:n])
			sp.feed(buf[:n])
		}
		if err == nil {
			continue
		}
		s.mu.Lock()
		stopping := s.shutting
		s.mu.Unlock()
		if stopping {
			return
		}
		clean := errors.Is(err, io.EOF)
		if pending := sp.end(clean); pending != "" {
			s.markOverflow(pending)
		}
		if why := p.end(clean); why != "" {
			s.markOverflow(why)
		}
		s.mu.Lock()
		s.closed = true
		s.closeReason = serverEnded
		if !clean {
			s.closeReason = "reading the stream failed: " + err.Error()
		}
		s.mu.Unlock()
		return
	}
}

func (s *stream) keepText(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.textCut {
		return
	}
	room := streamTextBytes - len(s.text)
	if len(b) > room {
		s.text = append(s.text, b[:room]...)
		s.textCut = true
		return
	}
	s.text = append(s.text, b...)
}

func (s *stream) markOverflow(why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.overflow {
		s.overflow, s.overflowWhy = true, why
	}
}

func (s *stream) full() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.overflow
}

func (s *stream) add(f map[string]any, size int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.overflow:
		return
	case len(s.frames) >= maxStreamFrames:
		s.overflow, s.overflowWhy = true, fmt.Sprintf("more than %d frames arrived, so every frame after the first %d is left out", maxStreamFrames, maxStreamFrames)
		return
	case s.frameBytes+size > maxStreamBytes:
		s.overflow, s.overflowWhy = true, fmt.Sprintf("the frame after the first %d would take the frames past %d bytes, so it and every later frame are left out", len(s.frames), maxStreamBytes)
		return
	}
	s.frames = append(s.frames, f)
	s.frameBytes += size
}

func (s *stream) setRetry(ms int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retry, s.hasRetry = ms, true
}

func (s *stream) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

func (s *stream) snapshot() streamSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return streamSnapshot{
		frames:      append([]map[string]any(nil), s.frames...),
		text:        strings.ToValidUTF8(string(s.text), "�"),
		textCut:     s.textCut,
		overflow:    s.overflow,
		overflowWhy: s.overflowWhy,
		closed:      s.closed,
		closeReason: s.closeReason,
		retry:       s.retry,
		hasRetry:    s.hasRetry,
	}
}

func (s *stream) shut() {
	s.once.Do(func() {
		s.mu.Lock()
		s.shutting = true
		s.mu.Unlock()
		s.cancel()
		_ = s.body.Close()
		select {
		case <-s.done:
		case <-time.After(streamCloseWait):
		}
	})
}

type parser interface {
	line(b []byte, tooLong bool)
	end(clean bool) string
}

type splitter struct {
	sse     bool
	line    func(b []byte, tooLong bool)
	buf     []byte
	tooLong bool
	afterCR bool
	started bool
}

func (sp *splitter) feed(b []byte) {
	if !sp.started {
		sp.started = true
		if sp.sse {
			b = bytes.TrimPrefix(b, []byte("\xEF\xBB\xBF"))
		}
	}
	for len(b) > 0 {
		if sp.afterCR {
			sp.afterCR = false
			if b[0] == '\n' {
				b = b[1:]
				continue
			}
		}
		i := bytes.IndexAny(b, "\r\n")
		if !sp.sse {
			i = bytes.IndexByte(b, '\n')
		}
		if i < 0 {
			sp.keep(b)
			return
		}
		sp.keep(b[:i])
		if sp.sse && b[i] == '\r' {
			sp.afterCR = true
		}
		sp.emit()
		b = b[i+1:]
	}
}

func (sp *splitter) keep(b []byte) {
	if sp.tooLong {
		return
	}
	if len(sp.buf)+len(b) > maxFrameBytes {
		sp.tooLong = true
		sp.buf = nil
		return
	}
	sp.buf = append(sp.buf, b...)
}

func (sp *splitter) emit() {
	line := sp.buf
	if !sp.sse {
		line = bytes.TrimSuffix(line, []byte("\r"))
	}
	sp.line(line, sp.tooLong)
	sp.buf, sp.tooLong = sp.buf[:0], false
	if cap(sp.buf) > streamChunk {
		sp.buf = nil
	}
}

func (sp *splitter) end(clean bool) string {
	partial := len(sp.buf) > 0 || sp.tooLong
	switch {
	case !partial:
		return ""
	case sp.sse && !sp.tooLong && sp.buf[0] == ':':
		return ""
	case !clean:
		return "the stream broke in the middle of a line, so that frame is incomplete and left out"
	case sp.sse:
		return "the stream ended in the middle of a line, before the blank line that ends an event, so the event was not dispatched (as the server-sent events specification says) and is left out"
	}
	sp.emit()
	return ""
}

type ndjsonParser struct {
	s *stream
}

func (p *ndjsonParser) line(b []byte, tooLong bool) {
	if p.s.full() {
		return
	}
	n := p.s.count()
	if tooLong {
		p.s.markOverflow(fmt.Sprintf("the line after the first %d frames is longer than %d bytes, so it and every later frame are left out", n, maxFrameBytes))
		return
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return
	}
	f := map[string]any{"text": validText(b)}
	v, reason, _ := parseJSON(b)
	if reason == "" {
		f["json"] = v
	} else {
		f["json_invalid"] = reason
	}
	p.s.add(f, len(b))
}

func (p *ndjsonParser) end(clean bool) string {
	return ""
}

type sseParser struct {
	s       *stream
	event   string
	data    []byte
	hasData bool
	lastID  string
	pending bool
	tooLong bool
	size    int
}

func (p *sseParser) line(b []byte, tooLong bool) {
	if p.s.full() {
		return
	}
	if tooLong {
		p.tooLong, p.pending = true, true
		return
	}
	if len(b) == 0 {
		p.dispatch()
		return
	}
	if b[0] == ':' {
		return
	}
	p.pending = true
	field, value := b, []byte(nil)
	if i := bytes.IndexByte(b, ':'); i >= 0 {
		field, value = b[:i], b[i+1:]
		value = bytes.TrimPrefix(value, []byte(" "))
	}
	p.size += len(b)
	switch string(field) {
	case "event":
		p.event = string(value)
	case "data":
		p.data = append(p.data, value...)
		p.data = append(p.data, '\n')
		p.hasData = true
	case "id":
		if bytes.IndexByte(value, 0) < 0 {
			p.lastID = string(value)
		}
	case "retry":
		if ms, err := strconv.ParseInt(string(value), 10, 64); err == nil && ms >= 0 && allDigits(value) {
			p.s.setRetry(ms)
		}
	}
	if p.size > maxFrameBytes {
		p.tooLong = true
	}
}

func allDigits(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (p *sseParser) dispatch() {
	defer func() {
		p.event, p.data, p.hasData, p.pending, p.tooLong, p.size = defaultEvent, nil, false, false, false, 0
	}()
	if p.tooLong {
		n := p.s.count()
		p.s.markOverflow(fmt.Sprintf("the event after the first %d frames is longer than %d bytes, so it and every later frame are left out", n, maxFrameBytes))
		return
	}
	if !p.hasData {
		return
	}
	data := bytes.TrimSuffix(p.data, []byte("\n"))
	event := p.event
	if event == "" {
		event = defaultEvent
	}
	f := map[string]any{"event": validText([]byte(event)), "data": validText(data), "id": validText([]byte(p.lastID))}
	if v, reason, _ := parseJSON(data); reason == "" {
		f["json"] = v
	}
	p.s.add(f, p.size)
}

func (p *sseParser) end(clean bool) string {
	if p.pending && !p.s.full() {
		if clean {
			return "the stream ended in the middle of an event, before the blank line that ends it, so the event was not dispatched (as the server-sent events specification says) and is left out"
		}
		return "the stream broke in the middle of an event, so it is incomplete and left out"
	}
	return ""
}

func validText(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return strings.ToValidUTF8(string(b), "�")
}
