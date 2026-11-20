package simhttp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	AllRoutes  = "*"
	MaxTimes   = 10000
	MinTTL     = time.Second
	MaxTTL     = time.Hour
	DefaultTTL = 10 * time.Minute
)

type FaultSpec struct {
	Route   string            `json:"route"`
	Status  int               `json:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	JSON    json.RawMessage   `json:"json,omitempty"`
	Text    *string           `json:"text,omitempty"`
	DelayMS int64             `json:"delay_ms,omitempty"`
	Reset   bool              `json:"reset,omitempty"`
	Times   int               `json:"times,omitempty"`
	TTLMS   int64             `json:"ttl_ms,omitempty"`
}

type Fault struct {
	ID        string
	Spec      FaultSpec
	Remaining int
	Expires   time.Time
}

type FaultInfo struct {
	ID        string `json:"id"`
	Route     string `json:"route"`
	ExpiresAt string `json:"expires_at"`
	Status    int    `json:"status,omitempty"`
	Reset     bool   `json:"reset,omitempty"`
	DelayMS   int64  `json:"delay_ms,omitempty"`
	Times     int    `json:"times,omitempty"`
	Remaining *int   `json:"remaining,omitempty"`
}

func (f *Fault) info() FaultInfo {
	i := FaultInfo{ID: f.ID, Route: f.Spec.Route, ExpiresAt: f.Expires.UTC().Format(time.RFC3339Nano), Status: f.Spec.Status, Reset: f.Spec.Reset, DelayMS: f.Spec.DelayMS, Times: f.Spec.Times}
	if f.Spec.Times > 0 {
		left := f.Remaining
		i.Remaining = &left
	}
	return i
}

func (f *Fault) describe() string {
	switch {
	case f.Spec.Reset:
		return "fault reset"
	case f.Spec.Status > 0:
		return fmt.Sprintf("fault %d", f.Spec.Status)
	}
	return "fault delay " + (time.Duration(f.Spec.DelayMS) * time.Millisecond).String()
}

type Faults struct {
	mu     sync.Mutex
	active map[string]*Fault
}

func (s FaultSpec) check() error {
	switch {
	case s.Route == "":
		return fmt.Errorf("route is required: a route id or %q", AllRoutes)
	case s.Reset && (s.Status != 0 || s.Headers != nil || s.JSON != nil || s.Text != nil):
		return fmt.Errorf("reset sends no answer, so it can't come with status, headers, json or text")
	case s.Status == 0 && (s.Headers != nil || s.JSON != nil || s.Text != nil):
		return fmt.Errorf("headers, json and text need a status")
	case s.Status != 0 && (s.Status < 200 || s.Status > 599):
		return fmt.Errorf("status must be from 200 to 599")
	case s.JSON != nil && s.Text != nil:
		return fmt.Errorf("give json or text, not both")
	case (s.Status == 204 || s.Status == 304) && (s.JSON != nil || s.Text != nil):
		return fmt.Errorf("a %d answer has no body", s.Status)
	case s.DelayMS < 0 || s.DelayMS > 0 && (time.Duration(s.DelayMS)*time.Millisecond < MinDelay || time.Duration(s.DelayMS)*time.Millisecond > MaxDelay):
		return fmt.Errorf("delay_ms must be from 1 to %d", MaxDelay.Milliseconds())
	case !s.Reset && s.Status == 0 && s.DelayMS == 0:
		return fmt.Errorf("give status, reset or delay_ms")
	case s.Times < 0 || s.Times > MaxTimes:
		return fmt.Errorf("times must be from 1 to %d, or left out for no limit", MaxTimes)
	case s.TTLMS < 0 || s.TTLMS > 0 && (time.Duration(s.TTLMS)*time.Millisecond < MinTTL || time.Duration(s.TTLMS)*time.Millisecond > MaxTTL):
		return fmt.Errorf("ttl_ms must be from %d to %d", MinTTL.Milliseconds(), MaxTTL.Milliseconds())
	}
	if s.JSON != nil && !json.Valid(s.JSON) {
		return fmt.Errorf("json is not valid JSON")
	}
	for name, v := range s.Headers {
		lower := strings.ToLower(name)
		switch {
		case !headerNameRE.MatchString(name):
			return fmt.Errorf("headers: %q is not a header name", name)
		case forbiddenResponseHeaders[lower]:
			return fmt.Errorf("headers can't set %s; the sim sets it", name)
		case strings.ContainsAny(v, "\r\n"):
			return fmt.Errorf("headers.%s holds a line break", name)
		}
	}
	return nil
}

func (fs *Faults) purge(now time.Time) {
	for id, f := range fs.active {
		if !now.Before(f.Expires) {
			delete(fs.active, id)
		}
	}
}

func (fs *Faults) add(s FaultSpec) (*Fault, *Fault) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	now := time.Now()
	fs.purge(now)
	for _, f := range fs.sorted() {
		if f.Spec.Route == s.Route || f.Spec.Route == AllRoutes || s.Route == AllRoutes {
			c := *f
			return nil, &c
		}
	}
	ttl := DefaultTTL
	if s.TTLMS > 0 {
		ttl = time.Duration(s.TTLMS) * time.Millisecond
	}
	f := &Fault{ID: newID(), Spec: s, Remaining: s.Times, Expires: now.Add(ttl)}
	if fs.active == nil {
		fs.active = map[string]*Fault{}
	}
	fs.active[f.ID] = f
	c := *f
	return &c, nil
}

func (fs *Faults) take(route string) *Fault {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.purge(time.Now())
	for _, f := range fs.sorted() {
		if f.Spec.Route != AllRoutes && (route == "" || f.Spec.Route != route) {
			continue
		}
		if f.Spec.Times > 0 {
			f.Remaining--
			if f.Remaining <= 0 {
				delete(fs.active, f.ID)
			}
		}
		c := *f
		return &c
	}
	return nil
}

func (fs *Faults) remove(id string) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.purge(time.Now())
	_, ok := fs.active[id]
	delete(fs.active, id)
	return ok
}

func (fs *Faults) list() []FaultInfo {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.purge(time.Now())
	out := []FaultInfo{}
	for _, f := range fs.sorted() {
		out = append(out, f.info())
	}
	return out
}

func (fs *Faults) reset() {
	fs.mu.Lock()
	fs.active = nil
	fs.mu.Unlock()
}

func (fs *Faults) sorted() []*Fault {
	out := make([]*Fault, 0, len(fs.active))
	for _, f := range fs.active {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "f" + hex.EncodeToString(b)
}
