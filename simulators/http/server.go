package simhttp

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	SimName       = "proctor-sim-http"
	ControlPrefix = "/" + ControlRoot + "/"
	RoutesPath    = ControlPrefix + "routes"
	ReceivedPath  = ControlPrefix + "received"
	FaultsPath    = ControlPrefix + "faults"
	ResetPath     = ControlPrefix + "reset"
	maxControlIn  = 1 << 20
)

type Server struct {
	routes  *Routes
	journal *Journal
	faults  *Faults
	logger  *log.Logger
	version string
}

func New(routes *Routes, version string, logger *log.Logger) *Server {
	return &Server{routes: routes, journal: &Journal{}, faults: &Faults{}, logger: logger, version: version}
}

func (s *Server) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/"+ControlRoot || strings.HasPrefix(r.URL.Path, ControlPrefix) {
		s.control(w, r)
		return
	}
	s.serveApp(w, r)
}

func (s *Server) serveApp(w http.ResponseWriter, r *http.Request) {
	body := readBody(r.Body)
	escaped := r.URL.EscapedPath()
	in := &incoming{method: r.Method, segs: requestSegments(escaped), query: r.URL.Query(), header: r.Header, host: r.Host, body: body.value, hasJSON: body.json}
	route, params := s.routes.match(in)
	routeID, name := "", "(unmatched)"
	if route != nil {
		routeID, name = route.ID, route.ID
	}
	fault := s.faults.take(routeID)
	e := s.routes.entry(r, routeID, body)
	seq := s.journal.add(e)
	line := fmt.Sprintf("%s %s -> %s", r.Method, escaped, name)

	delay := time.Duration(0)
	if route != nil {
		delay = route.delay
	}
	if fault != nil && fault.Spec.DelayMS > 0 {
		delay = time.Duration(fault.Spec.DelayMS) * time.Millisecond
	}
	if delay > 0 {
		t := time.NewTimer(delay)
		select {
		case <-t.C:
		case <-r.Context().Done():
			t.Stop()
			s.journal.answered(e, 0, fault != nil)
			s.logf("%s (%s, no answer: the client went away or the sim is stopping)", line, faultNote(fault))
			panic(http.ErrAbortHandler)
		}
	}
	switch {
	case fault != nil && fault.Spec.Reset:
		s.journal.answered(e, 0, true)
		s.logf("%s (fault reset)", line)
		resetConn(w)
		return
	case fault != nil && fault.Spec.Status > 0:
		hdr := w.Header()
		for k, v := range fault.Spec.Headers {
			hdr.Set(k, v)
		}
		var payload []byte
		switch {
		case fault.Spec.JSON != nil:
			payload = fault.Spec.JSON
			if hdr.Get("Content-Type") == "" {
				hdr.Set("Content-Type", "application/json")
			}
		case fault.Spec.Text != nil:
			payload = []byte(*fault.Spec.Text)
			if hdr.Get("Content-Type") == "" {
				hdr.Set("Content-Type", "text/plain; charset=utf-8")
			}
		}
		s.journal.answered(e, fault.Spec.Status, true)
		s.logf("%s (fault %d)", line, fault.Spec.Status)
		w.WriteHeader(fault.Spec.Status)
		_, _ = w.Write(payload)
		return
	case route == nil:
		s.journal.answered(e, http.StatusNotFound, fault != nil)
		s.logf("%s 404%s", line, faultSuffix(fault))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Proctor-Sim", "unmatched")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("%s: no route matches %s %s", SimName, r.Method, escaped)})
		return
	}
	payload, err := route.render(w.Header(), params, in.query, seq)
	if err != nil {
		s.journal.answered(e, http.StatusInternalServerError, fault != nil)
		s.logf("%s 500 (the answer could not be rendered: %v)", line, err)
		http.Error(w, SimName+": the route's answer could not be rendered", http.StatusInternalServerError)
		return
	}
	s.journal.answered(e, route.status, fault != nil)
	s.logf("%s %d%s", line, route.status, faultSuffix(fault))
	w.WriteHeader(route.status)
	_, _ = w.Write(payload)
}

func faultNote(f *Fault) string {
	if f == nil {
		return "delay"
	}
	return f.describe()
}

func faultSuffix(f *Fault) string {
	if f == nil {
		return ""
	}
	return " (" + f.describe() + ")"
}

func resetConn(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		panic(http.ErrAbortHandler)
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	raw := conn
	if tc, ok := conn.(*tls.Conn); ok {
		raw = tc.NetConn()
	}
	if tcp, ok := raw.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	_ = raw.Close()
}

func (r *Route) render(hdr http.Header, params map[string]string, query map[string][]string, seq uint64) ([]byte, error) {
	expand := func(s string) string {
		return templateRE.ReplaceAllStringFunc(s, func(m string) string {
			name := m[2 : len(m)-2]
			switch {
			case name == "seq":
				return strconv.FormatUint(seq, 10)
			case strings.HasPrefix(name, "path."):
				return params[strings.TrimPrefix(name, "path.")]
			case strings.HasPrefix(name, "query."):
				if v := query[strings.TrimPrefix(name, "query.")]; len(v) > 0 {
					return v[0]
				}
			}
			return ""
		})
	}
	for _, h := range r.header {
		hdr.Set(h[0], expand(h[1]))
	}
	switch {
	case r.hasJSON:
		if hdr.Get("Content-Type") == "" {
			hdr.Set("Content-Type", "application/json")
		}
		var walk func(v any) any
		walk = func(v any) any {
			switch x := v.(type) {
			case string:
				if x == "{{seq}}" {
					return json.Number(strconv.FormatUint(seq, 10))
				}
				return expand(x)
			case map[string]any:
				out := make(map[string]any, len(x))
				for k, e := range x {
					out[k] = walk(e)
				}
				return out
			case []any:
				out := make([]any, len(x))
				for i, e := range x {
					out[i] = walk(e)
				}
				return out
			}
			return v
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(walk(r.json)); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case r.hasText:
		if hdr.Get("Content-Type") == "" {
			hdr.Set("Content-Type", "text/plain; charset=utf-8")
		}
		return []byte(expand(r.text)), nil
	case r.hasFile:
		return r.file, nil
	}
	return nil, nil
}

type controlError struct {
	Error string     `json:"error"`
	Code  string     `json:"code,omitempty"`
	Fault *FaultInfo `json:"fault,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func notAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeJSON(w, http.StatusMethodNotAllowed, controlError{Error: "method not allowed; use " + allow, Code: "method_not_allowed"})
}

func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	p := r.URL.Path
	switch {
	case p == RoutesPath:
		if !read {
			notAllowed(w, "GET, HEAD")
			return
		}
		s.listRoutes(w)
	case p == ReceivedPath:
		if !read {
			notAllowed(w, "GET, HEAD")
			return
		}
		s.received(w, r)
	case p == FaultsPath:
		switch {
		case read:
			writeJSON(w, http.StatusOK, map[string]any{"faults": s.faults.list()})
		case r.Method == http.MethodPost:
			s.addFault(w, r)
		default:
			notAllowed(w, "GET, HEAD, POST")
		}
	case strings.HasPrefix(p, FaultsPath+"/") && !strings.Contains(strings.TrimPrefix(p, FaultsPath+"/"), "/"):
		if r.Method != http.MethodDelete {
			notAllowed(w, "DELETE")
			return
		}
		id := strings.TrimPrefix(p, FaultsPath+"/")
		if !s.faults.remove(id) {
			writeJSON(w, http.StatusNotFound, controlError{Error: fmt.Sprintf("no active fault %q", id), Code: "unknown_fault"})
			return
		}
		s.logf("fault %s deleted", id)
		w.WriteHeader(http.StatusNoContent)
	case p == ResetPath:
		if r.Method != http.MethodPost {
			notAllowed(w, "POST")
			return
		}
		s.journal.reset()
		s.faults.reset()
		s.logf("reset: the request log and the faults were cleared (seq continues after %d)", s.journal.last())
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusNotFound, controlError{Error: fmt.Sprintf("%s has no control endpoint %s; it has %s, %s, %s and %s", SimName, p, RoutesPath, ReceivedPath, FaultsPath, ResetPath), Code: "unknown_endpoint"})
	}
}

type routeInfo struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Path   string `json:"path"`
}

func (s *Server) listRoutes(w http.ResponseWriter) {
	list := make([]routeInfo, 0, len(s.routes.List))
	for _, r := range s.routes.List {
		list = append(list, routeInfo{ID: r.ID, Method: r.Method, Path: r.Path})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sim": SimName, "version": s.version, "sha256": s.routes.SHA256, "routes": list})
}

func (s *Server) received(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var rq ReceivedQuery
	if v := q.Get("after"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, controlError{Error: fmt.Sprintf("after %q must be a whole number", v), Code: "bad_request"})
			return
		}
		rq.After = n
	}
	rq.Route, rq.Method, rq.Path = q.Get("route"), q.Get("method"), q.Get("path")
	if rq.Route != "" && !s.routes.Has(rq.Route) {
		writeJSON(w, http.StatusBadRequest, controlError{Error: fmt.Sprintf("route %q is not in the route file", rq.Route), Code: "unknown_route"})
		return
	}
	a, err := s.journal.query(rq)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, controlError{Error: err.Error(), Code: "internal"})
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) addFault(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxControlIn+1))
	if err != nil || len(b) > maxControlIn {
		writeJSON(w, http.StatusBadRequest, controlError{Error: "the fault must be a JSON object of at most 1 MiB", Code: "bad_request"})
		return
	}
	var spec FaultSpec
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		writeJSON(w, http.StatusBadRequest, controlError{Error: "the fault is not valid: " + err.Error(), Code: "bad_request"})
		return
	}
	if dec.More() {
		writeJSON(w, http.StatusBadRequest, controlError{Error: "trailing data after the fault", Code: "bad_request"})
		return
	}
	if err := spec.check(); err != nil {
		writeJSON(w, http.StatusBadRequest, controlError{Error: err.Error(), Code: "bad_request"})
		return
	}
	if spec.Route != AllRoutes && !s.routes.Has(spec.Route) {
		writeJSON(w, http.StatusNotFound, controlError{Error: fmt.Sprintf("route %q is not in the route file", spec.Route), Code: "unknown_route"})
		return
	}
	f, conflict := s.faults.add(spec)
	if conflict != nil {
		info := conflict.info()
		writeJSON(w, http.StatusConflict, controlError{Error: fmt.Sprintf("a fault on route %s would overlap the active fault %s on route %s, which expires at %s; a route takes one fault at a time, and * overlaps every route", spec.Route, conflict.ID, conflict.Spec.Route, info.ExpiresAt), Code: "conflict", Fault: &info})
		return
	}
	info := f.info()
	s.logf("fault %s set on %s (%s, until %s)", f.ID, spec.Route, f.describe(), info.ExpiresAt)
	writeJSON(w, http.StatusCreated, map[string]any{"id": f.ID, "route": spec.Route, "expires_at": info.ExpiresAt, "last_seq": s.journal.last()})
}
