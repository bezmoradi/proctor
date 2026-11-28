package main

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/proctor/protocol"
)

const (
	opOpen   = "open"
	opFrames = "frames"
)

type openArgs struct {
	Path    string         `json:"path"`
	Host    string         `json:"host"`
	Query   map[string]any `json:"query"`
	Headers map[string]any `json:"headers"`
	Format  string         `json:"format"`
}

type streams struct {
	mu   sync.Mutex
	next int
	open map[string]*stream
}

var ndjsonTypes = []string{"application/x-ndjson", "application/ndjson", "application/jsonl", "application/x-jsonlines", "application/jsonlines"}

func formatOf(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	if mt == "text/event-stream" {
		return formatSSE
	}
	for _, t := range ndjsonTypes {
		if mt == t {
			return formatNDJSON
		}
	}
	return ""
}

func (d *driver) open(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	if p.Handle != "" {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s opens a handle; it can't run on one", opOpen)
	}
	var a openArgs
	if err := protocol.Decode(p.Args, &a); err != nil {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s: %v", opOpen, err)
	}
	if a.Format != "" && a.Format != formatNDJSON && a.Format != formatSSE {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s: format %q must be %s or %s", opOpen, a.Format, formatNDJSON, formatSSE)
	}
	sctx, cancel := context.WithCancel(context.Background())
	out, err := d.build(sctx, requestArgs{Method: http.MethodGet, Path: a.Path, Host: a.Host, Query: a.Query, Headers: a.Headers}, p.Credential, nil)
	if err != nil {
		cancel()
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s: %v", opOpen, err)
	}
	stop := context.AfterFunc(ctx, cancel)
	start := time.Now()
	resp, err := d.client.Do(out.req)
	if err != nil {
		stop()
		cancel()
		cause := requestCause(err)
		if ctx.Err() != nil {
			cause = protocol.CauseTimeout
			return protocol.CallResult{}, protocol.Errorf(cause, "open GET %s: no response headers arrived within the step's timeout", out.target)
		}
		return protocol.CallResult{}, protocol.Errorf(cause, "open GET %s: request failed: %v", out.target, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
		resp.Body.Close()
		stop()
		cancel()
		elapsed := time.Since(start).Milliseconds()
		switch {
		case rerr != nil && ctx.Err() != nil:
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseTimeout, "open GET %s -> %d: the refusal's body didn't end within the step's timeout", out.target, resp.StatusCode)
		case rerr != nil:
			return protocol.CallResult{}, protocol.Errorf(readCause(rerr), "open GET %s -> %d: reading the response: %v", out.target, resp.StatusCode, rerr)
		case len(body) > maxBodyBytes:
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseResponseTooLarge, "open GET %s -> %d: the response body is larger than %d bytes", out.target, resp.StatusCode, maxBodyBytes)
		}
		f := fact(resp, body)
		f["open"] = false
		return protocol.CallResult{
			Fact: f,
			Evidence: protocol.Evidence{
				Summary:         fmt.Sprintf("open GET %s -> %d, no stream", out.target, resp.StatusCode),
				RequestExcerpt:  requestExcerpt(out),
				ResponseExcerpt: responseExcerpt(resp, body),
				DurationMS:      elapsed,
			},
		}, nil
	}
	if !stop() {
		resp.Body.Close()
		cancel()
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseTimeout, "open GET %s -> %d: the step's timeout ended as the stream opened", out.target, resp.StatusCode)
	}
	format := a.Format
	if format == "" {
		format = formatOf(resp.Header.Get("Content-Type"))
	}
	if format == "" {
		resp.Body.Close()
		cancel()
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnreadableResponse, "open GET %s -> %d: Content-Type %q is neither NDJSON (%s) nor server-sent events (text/event-stream); set format to read it as one", out.target, resp.StatusCode, resp.Header.Get("Content-Type"), strings.Join(ndjsonTypes, ", "))
	}
	s := startStream(format, resp.Body, cancel)
	id := d.streams.add(s)
	f := fact(resp, nil)
	f["open"] = true
	f["format"] = format
	return protocol.CallResult{
		Fact: f,
		Evidence: protocol.Evidence{
			Summary:         fmt.Sprintf("open GET %s -> %d, reading %s as %s", out.target, resp.StatusCode, format, id),
			RequestExcerpt:  requestExcerpt(out),
			ResponseExcerpt: responseExcerpt(resp, nil),
			DurationMS:      time.Since(start).Milliseconds(),
		},
		Handle: id,
	}, nil
}

func (d *driver) frames(p protocol.CallParams) (protocol.CallResult, error) {
	s, err := d.streams.lookup(p.Handle)
	if err != nil {
		return protocol.CallResult{}, err
	}
	if p.Args != nil {
		m, ok := p.Args.(map[string]any)
		if !ok || len(m) > 0 {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s takes no arguments", opFrames)
		}
	}
	start := time.Now()
	snap := s.snapshot()
	list := make([]any, 0, len(snap.frames))
	var sb strings.Builder
	for _, f := range snap.frames {
		list = append(list, f)
		if sb.Len() < excerptBytes {
			sb.WriteString(frameExcerpt(s.format, f))
			sb.WriteString("\n")
		}
	}
	fact := map[string]any{
		"format":         s.format,
		"count":          len(list),
		"frames":         list,
		"closed":         snap.closed,
		"overflow":       snap.overflow,
		"body_text":      snap.text,
		"body_truncated": snap.textCut,
	}
	state := "open"
	if snap.closed {
		fact["close_reason"] = snap.closeReason
		state = "closed: " + snap.closeReason
	}
	if snap.overflow {
		fact["overflow_reason"] = snap.overflowWhy
		state += "; overflowed: " + snap.overflowWhy
	}
	if snap.hasRetry {
		fact["retry"] = snap.retry
	}
	return protocol.CallResult{
		Fact: fact,
		Evidence: protocol.Evidence{
			Summary:         fmt.Sprintf("frames on %s: %d %s frames, %s", p.Handle, len(list), s.format, state),
			ResponseExcerpt: excerpt([]byte(sb.String())),
			DurationMS:      time.Since(start).Milliseconds(),
		},
	}, nil
}

func frameExcerpt(format string, f map[string]any) string {
	if format == formatSSE {
		data, _ := f["data"].(string)
		event, _ := f["event"].(string)
		id, _ := f["id"].(string)
		head := "event " + event
		if id != "" {
			head += " id " + id
		}
		return head + ": " + string(redactedBody([]byte(data)))
	}
	text, _ := f["text"].(string)
	return string(redactedBody([]byte(text)))
}

func (st *streams) add(s *stream) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.open == nil {
		st.open = map[string]*stream{}
	}
	st.next++
	id := fmt.Sprintf("http-%d", st.next)
	st.open[id] = s
	return id
}

func (st *streams) lookup(handle string) (*stream, error) {
	if handle == "" {
		return nil, protocol.Errorf(protocol.CauseInvalidRequest, "%s needs a handle from %s.%s", opFrames, driverName, opOpen)
	}
	st.mu.Lock()
	s, ok := st.open[handle]
	st.mu.Unlock()
	if !ok {
		return nil, protocol.Errorf(protocol.CauseInvalidRequest, "the http driver has no open handle %q", handle)
	}
	return s, nil
}

func (st *streams) close(handle string) error {
	st.mu.Lock()
	s, ok := st.open[handle]
	delete(st.open, handle)
	st.mu.Unlock()
	if !ok {
		return protocol.Errorf(protocol.CauseInvalidRequest, "the http driver has no open handle %q", handle)
	}
	s.shut()
	return nil
}

func (st *streams) shutdown() {
	st.mu.Lock()
	all := make([]*stream, 0, len(st.open))
	for id, s := range st.open {
		all = append(all, s)
		delete(st.open, id)
	}
	st.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range all {
		wg.Go(s.shut)
	}
	wg.Wait()
}
