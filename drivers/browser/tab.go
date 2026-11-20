package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

type credential struct {
	header string
	value  string
	cookie string
	secret string
}

type request struct {
	url, method string
}

type tab struct {
	d      *Driver
	id     string
	ctx    context.Context
	cancel context.CancelFunc
	target target.ID
	cred   credential
	once   sync.Once

	mu        sync.Mutex
	console   []any
	failed    []any
	overflow  bool
	reasons   []string
	requests  map[network.RequestID]request
	blocked   map[network.RequestID]bool
	popups    map[target.ID]bool
	navs      int
	loads     int
	status    int64
	navStatus int
}

func (d *Driver) openTab(ctx context.Context, cred credential) (*tab, error) {
	bctx, err := d.engine.browserCtx(ctx)
	if err != nil {
		return nil, err
	}
	tctx, tcancel := chromedp.NewContext(bctx, chromedp.WithNewBrowserContext())
	t := &tab{d: d, ctx: tctx, cancel: tcancel, cred: cred,
		requests: map[network.RequestID]request{}, blocked: map[network.RequestID]bool{}, popups: map[target.ID]bool{}}
	fail := func(err error) (*tab, error) {
		t.close()
		return nil, err
	}
	listening := make(chan error, 1)
	go func() { listening <- chromedp.Listen(tctx, t.onEvent) }()
	select {
	case err := <-listening:
		if err != nil {
			return fail(err)
		}
	case <-ctx.Done():
		return fail(fmt.Errorf("opening a page: %w", ctx.Err()))
	}
	cctx, ccancel := within(ctx, tctx)
	defer ccancel()
	c := chromedp.FromContext(tctx)
	t.target = c.Target.TargetID
	d.engine.register(t.target, t)
	if _, err := chromedp.CallBrowser(cctx, browser.SetDownloadBehavior, browser.SetDownloadBehaviorParams{Behavior: browser.SetDownloadBehaviorBehaviorDeny, BrowserContextID: c.BrowserContextID}); err != nil {
		return fail(err)
	}
	if err := t.guard(cctx); err != nil {
		return fail(err)
	}
	if cred.cookie != "" {
		secure := d.base.Scheme == "https"
		httpOnly := true
		path := d.basePath() + "/"
		_, err := chromedp.Call(cctx, network.SetCookie, network.SetCookieParams{Name: cred.cookie, Value: cred.value,
			URL: d.baseOrigin + path, Path: path, Secure: &secure, HTTPOnly: &httpOnly, SameSite: network.CookieSameSiteLax})
		if err != nil {
			return fail(fmt.Errorf("setting cookie %s: %v", cred.cookie, err))
		}
	}
	return t, nil
}

func within(ctx, tctx context.Context) (context.Context, context.CancelFunc) {
	if dl, ok := ctx.Deadline(); ok {
		return context.WithDeadline(tctx, dl)
	}
	return context.WithTimeout(tctx, defaultTimeout)
}

func (t *tab) close() {
	t.once.Do(func() {
		if t.target != "" {
			t.d.engine.unregister(t.target)
		}
		stopWithin(t.cancel, closeWait+time.Second)
	})
}

func (t *tab) scrub(s string) string {
	if t.cred.secret == "" {
		return s
	}
	return strings.ReplaceAll(s, t.cred.secret, hidden)
}

func (t *tab) popupSeen(id target.ID) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.popups[id] {
		return false
	}
	t.popups[id] = true
	return true
}

func (t *tab) note(reason string) {
	t.overflow = true
	for _, r := range t.reasons {
		if r == reason {
			return
		}
	}
	t.reasons = append(t.reasons, reason)
}

func (t *tab) addFailed(e map[string]any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.failed) >= maxEntries {
		t.note(fmt.Sprintf("failed_requests keeps the first %d", maxEntries))
		return
	}
	t.failed = append(t.failed, e)
}

func (t *tab) addConsole(source, text, at string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.console) >= maxEntries {
		t.note(fmt.Sprintf("console_errors keeps the first %d", maxEntries))
		return
	}
	e := map[string]any{"source": source, "text": t.scrub(cut(text, entryBytes))}
	if at != "" {
		e["url"] = t.scrub(cut(at, urlBytes))
	}
	t.console = append(t.console, e)
}

func (t *tab) onEvent(ev any) {
	switch ev := ev.(type) {
	case *fetch.EventRequestPaused:
		go t.decide(ev)
	case *network.EventRequestWillBeSent:
		t.mu.Lock()
		if len(t.requests) < 10*maxEntries {
			t.requests[ev.RequestID] = request{url: ev.Request.URL, method: ev.Request.Method}
		}
		if ev.Type == network.ResourceTypeDocument && string(ev.FrameID) == string(t.target) && ev.RedirectResponse == nil {
			t.navs++
		}
		t.mu.Unlock()
	case *network.EventResponseReceived:
		r := ev.Response
		if r == nil {
			return
		}
		if ev.Type == network.ResourceTypeDocument && string(ev.FrameID) == string(t.target) {
			t.mu.Lock()
			t.status = r.Status
			t.navStatus = t.navs
			t.mu.Unlock()
		}
		if r.Status >= 400 && !(ev.Type == network.ResourceTypeOther && iconRequest(r.URL)) {
			t.mu.Lock()
			req := t.requests[ev.RequestID]
			t.mu.Unlock()
			method := req.method
			if method == "" {
				method = "GET"
			}
			t.addFailed(map[string]any{"url": t.scrub(cut(r.URL, urlBytes)), "method": method, "type": string(ev.Type),
				"status": r.Status, "error": "", "blocked": false})
		}
	case *network.EventLoadingFailed:
		t.mu.Lock()
		req, known := t.requests[ev.RequestID]
		ours := t.blocked[ev.RequestID]
		delete(t.blocked, ev.RequestID)
		delete(t.requests, ev.RequestID)
		t.mu.Unlock()
		if ours || !known {
			return
		}
		if ok, origin := t.d.allowedURL(req.url); !ok {
			t.addFailed(t.blockedEntry(req.url, req.method, string(ev.Type), origin))
			return
		}
		if ev.Canceled {
			return
		}
		t.addFailed(map[string]any{"url": t.scrub(cut(req.url, urlBytes)), "method": req.method, "type": string(ev.Type),
			"status": 0, "error": ev.ErrorText, "blocked": false})
	case *network.EventWebSocketCreated:
		if ok, origin := t.d.allowedURL(ev.URL); !ok {
			e := t.blockedEntry(ev.URL, "GET", "WebSocket", origin)
			if t.d.remoteURL != "" {
				e["blocked"] = false
				e["error"] = fmt.Sprintf("not blocked: %s isn't an allowed origin, but with remote_url the browser driver can't stop a WebSocket", origin)
			}
			t.addFailed(e)
		}
	case *runtime.EventBindingCalled:
		if ev.Name == windowBinding {
			t.addFailed(t.windowEntry(ev.Payload))
		}
	case *network.EventLoadingFinished:
		t.mu.Lock()
		delete(t.requests, ev.RequestID)
		t.mu.Unlock()
	case *page.EventLoadEventFired:
		t.mu.Lock()
		t.loads++
		t.mu.Unlock()
	case *runtime.EventConsoleAPICalled:
		if ev.Type != runtime.ConsoleAPICalledTypeError && ev.Type != runtime.ConsoleAPICalledTypeAssert {
			return
		}
		parts := make([]string, 0, len(ev.Args))
		for _, a := range ev.Args {
			parts = append(parts, remoteText(a))
		}
		t.addConsole("console", strings.Join(parts, " "), "")
	case *runtime.EventExceptionThrown:
		x := ev.ExceptionDetails
		if x == nil {
			return
		}
		text := x.Text
		if x.Exception != nil && x.Exception.Description != "" {
			text = x.Exception.Description
		}
		t.addConsole("exception", text, x.URL)
	case *log.EventEntryAdded:
		if ev.Entry == nil || ev.Entry.Level != log.EntryLevelError || ev.Entry.Source == log.EntrySourceNetwork {
			return
		}
		t.addConsole("log", ev.Entry.Text, ev.Entry.URL)
	case *page.EventJavascriptDialogOpening:
		accept := ev.Type == page.DialogTypeBeforeunload || ev.Type == page.DialogTypeAlert
		go func() {
			cctx, cancel := context.WithTimeout(t.ctx, closeWait)
			defer cancel()
			_, _ = chromedp.Call(cctx, page.HandleJavaScriptDialog, page.HandleJavaScriptDialogParams{Accept: accept})
		}()
	}
}

func remoteText(a *runtime.RemoteObject) string {
	if a == nil {
		return ""
	}
	if len(a.Value) > 0 {
		s := string(a.Value)
		if strings.HasPrefix(s, `"`) {
			var out string
			if err := json.Unmarshal(a.Value, &out); err == nil {
				return out
			}
		}
		return s
	}
	if a.UnserializableValue != "" {
		return string(a.UnserializableValue)
	}
	return a.Description
}

func (t *tab) decide(ev *fetch.EventRequestPaused) {
	ctx, cancel := context.WithTimeout(t.ctx, closeWait)
	defer cancel()
	raw := ""
	method := "GET"
	if ev.Request != nil {
		raw, method = ev.Request.URL, ev.Request.Method
	}
	ok, origin := t.d.allowedURL(raw)
	if !ok {
		t.mu.Lock()
		seen := ev.NetworkID != "" && t.blocked[ev.NetworkID]
		if ev.NetworkID != "" {
			t.blocked[ev.NetworkID] = true
		}
		t.mu.Unlock()
		if !seen {
			t.addFailed(t.blockedEntry(raw, method, string(ev.ResourceType), origin))
		}
		_, _ = chromedp.Call(ctx, fetch.FailRequest, fetch.FailRequestParams{RequestID: ev.RequestID, ErrorReason: network.ErrorReasonBlockedByClient})
		return
	}
	params := fetch.ContinueRequestParams{RequestID: ev.RequestID}
	if t.cred.header != "" && origin == t.d.baseOrigin && ev.Request != nil {
		var headers []*fetch.HeaderEntry
		for _, name := range sortedKeys(ev.Request.Headers) {
			if strings.EqualFold(name, t.cred.header) {
				continue
			}
			headers = append(headers, &fetch.HeaderEntry{Name: name, Value: fmt.Sprint(ev.Request.Headers[name])})
		}
		params.Headers = append(headers, &fetch.HeaderEntry{Name: t.cred.header, Value: t.cred.value})
	}
	_, _ = chromedp.Call(ctx, fetch.ContinueRequest, params)
}

func (t *tab) snapshot() (console, failed []any, overflow bool, reasons []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]any{}, t.console...), append([]any{}, t.failed...), t.overflow, append([]string(nil), t.reasons...)
}

func (t *tab) counters() (navs, loads int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.navs, t.loads
}

func (t *tab) lastStatus(since int) (int64, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.navStatus > since && t.status > 0 {
		return t.status, true
	}
	return 0, false
}
