package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"

	"github.com/bezmoradi/proctor/protocol"
)

const hidden = "[hidden]"

type openArgs struct {
	Path  string         `json:"path"`
	Query map[string]any `json:"query"`
	Auth  string         `json:"auth"`
}

type fillArgs struct {
	Selector string `json:"selector"`
	Value    string `json:"value"`
}

type clickArgs struct {
	Selector string `json:"selector"`
	WaitFor  string `json:"wait_for"`
}

type pageArgs struct {
	Selectors map[string]string `json:"selectors"`
}

func (d *Driver) Call(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	if !d.configured {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseNotConfigured, "the browser driver has no connection")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}
	switch p.Op {
	case OpOpen:
		return d.open(ctx, p)
	case OpFill:
		return d.fill(ctx, p)
	case OpClick:
		return d.click(ctx, p)
	case OpPage:
		return d.page(ctx, p)
	}
	return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "the browser driver has no operation %q", p.Op)
}

func invalid(op string, err error) error {
	return protocol.Errorf(protocol.CauseInvalidRequest, "%s: %v", op, err)
}

func failed(ctx context.Context, what string, err error) error {
	var le *chromedp.LoadError
	if errors.As(err, &le) {
		return protocol.Errorf(protocol.CauseUnreachable, "%s: the page didn't load: %s", what, le.ErrorText)
	}
	return protocol.Errorf(errCause(ctx, err), "%s: %v", what, err)
}

func (d *Driver) buildCredential(a openArgs, cred *protocol.Credential) (credential, error) {
	var c credential
	place, err := parseAuth(a.Auth)
	if err != nil {
		return c, err
	}
	if cred == nil {
		if a.Auth != "" {
			return c, fmt.Errorf("auth %q is set but the step has no identity with a credential", a.Auth)
		}
		return c, nil
	}
	switch {
	case cred.Value == "":
		return c, fmt.Errorf("identity %q: the credential is empty", cred.Identity)
	case hasControl(cred.Value):
		return c, fmt.Errorf("identity %q: the credential contains a line break or control character", cred.Identity)
	case cred.Scheme != protocol.SchemeBearer && cred.Scheme != protocol.SchemeHeader:
		return c, fmt.Errorf("identity %q: unknown credential scheme %q", cred.Identity, cred.Scheme)
	}
	c.secret = cred.Value
	c.value = cred.Value
	switch place.kind {
	case "cookie":
		if !isCookieValue(cred.Value) {
			return c, fmt.Errorf("identity %q: the credential can't be sent as a cookie because it holds a space, a quote, a comma, a semicolon or a backslash", cred.Identity)
		}
		c.cookie = place.name
	default:
		c.header = place.name
		if cred.Scheme == protocol.SchemeBearer {
			c.value = "Bearer " + cred.Value
		}
	}
	return c, nil
}

func (d *Driver) pageURL(a openArgs) (string, error) {
	if err := checkPath(a.Path, true); err != nil {
		return "", err
	}
	q := url.Values{}
	for _, k := range sortedKeys(a.Query) {
		v, err := scalarText(a.Query[k])
		if err != nil {
			return "", fmt.Errorf("query %s: %v", k, err)
		}
		q.Set(k, v)
	}
	target := d.base.Scheme + "://" + d.base.Host + d.basePath() + a.Path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	u, err := url.Parse(target)
	if err != nil {
		return "", err
	}
	if o, _ := originOf(u); o != d.baseOrigin || !strings.HasPrefix(u.EscapedPath(), d.basePath()+"/") {
		return "", fmt.Errorf("URL %s leaves base_url", target)
	}
	return target, nil
}

func shownPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	out := u.EscapedPath()
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

func (d *Driver) open(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	if p.Handle != "" {
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s opens a handle; it can't run on one", OpOpen)
	}
	var a openArgs
	if err := protocol.Decode(p.Args, &a); err != nil {
		return protocol.CallResult{}, invalid(OpOpen, err)
	}
	cred, err := d.buildCredential(a, p.Credential)
	if err != nil {
		return protocol.CallResult{}, invalid(OpOpen, err)
	}
	target, err := d.pageURL(a)
	if err != nil {
		return protocol.CallResult{}, invalid(OpOpen, err)
	}
	start := time.Now()
	t, err := d.openTab(ctx, cred)
	if err != nil {
		if lasting(err) {
			return protocol.CallResult{}, protocol.Lastingf(protocol.CauseNotConfigured, "open %s: %v", shownPath(target), err)
		}
		return protocol.CallResult{}, failed(ctx, "open "+shownPath(target), err)
	}
	cctx, cancel := within(ctx, t.ctx)
	defer cancel()
	resp, err := chromedp.Run(cctx, chromedp.NavigateResponse(target))
	if err != nil {
		t.close()
		return protocol.CallResult{}, failed(cctx, "open "+t.scrub(shownPath(target)), err)
	}
	status := int64(0)
	if resp != nil {
		status = resp.Status
	}
	if status < 100 || status > 999 {
		t.close()
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnreadableResponse, "open %s: the page loaded without an HTTP status", shownPath(target))
	}
	where, title, err := t.location(cctx)
	if err != nil {
		t.close()
		return protocol.CallResult{}, failed(cctx, "open "+shownPath(target), err)
	}
	id := d.add(t)
	return protocol.CallResult{
		Fact: map[string]any{"url": where, "status": status, "title": title},
		Evidence: protocol.Evidence{
			Summary:         fmt.Sprintf("open %s -> %d, %q, as %s", t.scrub(shownPath(target)), status, cut(title, 200), id),
			RequestExcerpt:  "GET " + t.scrub(target) + d.credentialNote(cred),
			ResponseExcerpt: fmt.Sprintf("HTTP %d\nurl: %s\ntitle: %s", status, where, title),
			DurationMS:      time.Since(start).Milliseconds(),
		},
		Handle: id,
	}, nil
}

func (d *Driver) credentialNote(c credential) string {
	switch {
	case c.header != "":
		return fmt.Sprintf("\n%s: %s (sent to %s only)", c.header, hidden, d.baseOrigin)
	case c.cookie != "":
		return fmt.Sprintf("\nCookie: %s=%s", c.cookie, hidden)
	}
	return ""
}

func (t *tab) location(ctx context.Context) (string, string, error) {
	type where struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	w, err := retryEval[where](ctx, `({url: location.href, title: document.title})`)
	if err != nil {
		return "", "", err
	}
	return t.scrub(w.URL), t.scrub(w.Title), nil
}

func retryEval[T any](ctx context.Context, expr string) (T, error) {
	for {
		v, err := chromedp.Run(ctx, chromedp.Evaluate[T](expr))
		if err == nil || ctx.Err() != nil {
			return v, err
		}
		if !transient(err) {
			return v, err
		}
		select {
		case <-ctx.Done():
			return v, ctx.Err()
		case <-time.After(pollEvery):
		}
	}
}

func transient(err error) bool {
	msg := err.Error()
	for _, s := range []string{"context was destroyed", "Cannot find context", "Execution context", "Inspected target navigated", "uniqueContextId"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

const fillScript = `(function (sel, value, typed) {
	const el = document.querySelector(sel);
	if (!el) return {state: "missing"};
	const tag = el.tagName;
	const type = (el.getAttribute("type") || "").toLowerCase();
	const fire = (name) => el.dispatchEvent(new Event(name, {bubbles: true}));
	if (tag === "INPUT" && ["checkbox", "radio", "file", "submit", "button", "image", "reset", "hidden"].includes(type)) {
		return {state: "unsupported", kind: "input type " + type};
	}
	if (!typed) {
		return {state: "done", filled: (el.isContentEditable ? el.textContent : el.value) === value};
	}
	el.focus();
	if (tag === "SELECT" || (tag === "INPUT" && ["date", "time", "datetime-local", "month", "week", "color", "range"].includes(type))) {
		el.value = value;
		fire("input");
		fire("change");
		return {state: "done", filled: el.value === value};
	}
	if (el.isContentEditable) {
		const range = document.createRange();
		range.selectNodeContents(el);
		const s = window.getSelection();
		s.removeAllRanges();
		s.addRange(range);
		document.execCommand("delete");
		return {state: "type"};
	}
	if (tag === "INPUT" || tag === "TEXTAREA") {
		el.value = "";
		fire("input");
		return {state: "type"};
	}
	return {state: "unsupported", kind: tag.toLowerCase() + " element"};
})`

type fillState struct {
	State  string `json:"state"`
	Kind   string `json:"kind"`
	Filled bool   `json:"filled"`
}

func jsCall(fn string, args ...any) (string, error) {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		b, err := json.Marshal(a)
		if err != nil {
			return "", err
		}
		parts = append(parts, string(b))
	}
	return fn + "(" + strings.Join(parts, ", ") + ")", nil
}

func (d *Driver) fill(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	t, err := d.lookup(p.Handle)
	if err != nil {
		return protocol.CallResult{}, err
	}
	var a fillArgs
	if err := protocol.Decode(p.Args, &a); err != nil {
		return protocol.CallResult{}, invalid(OpFill, err)
	}
	switch {
	case strings.TrimSpace(a.Selector) == "":
		return protocol.CallResult{}, invalid(OpFill, errors.New("selector is empty"))
	case typedControl(a.Value):
		return protocol.CallResult{}, invalid(OpFill, errors.New("value contains a control character other than a tab or a line break"))
	case len(a.Value) > maxValue:
		return protocol.CallResult{}, invalid(OpFill, fmt.Errorf("value is %d bytes; fill types at most %d", len(a.Value), maxValue))
	}
	start := time.Now()
	cctx, cancel := within(ctx, t.ctx)
	defer cancel()
	what := fmt.Sprintf("fill %s on %s", a.Selector, p.Handle)
	if err := chromedp.Do(cctx, chromedp.WaitVisible(chromedp.CSS(a.Selector))); err != nil {
		return protocol.CallResult{}, t.waitFailed(cctx, what, "the element isn't visible", err)
	}
	prep, err := jsCall(fillScript, a.Selector, a.Value, true)
	if err != nil {
		return protocol.CallResult{}, invalid(OpFill, err)
	}
	st, err := chromedp.Run(cctx, chromedp.Evaluate[fillState](prep))
	if err != nil {
		return protocol.CallResult{}, failed(cctx, what, err)
	}
	switch st.State {
	case "missing":
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseQueryError, "%s: no element matches the selector any more", what)
	case "unsupported":
		return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "%s: fill types into text fields, text areas, selects and editable elements, not a %s; use %s.%s for it", what, st.Kind, Name, OpClick)
	case "type":
		if a.Value != "" {
			if _, err := chromedp.Call(cctx, input.InsertText, input.InsertTextParams{Text: a.Value}); err != nil {
				return protocol.CallResult{}, failed(cctx, what, err)
			}
		}
		check, err := jsCall(`(function (sel, value) {
			const el = document.querySelector(sel);
			if (!el) return {state: "missing"};
			el.dispatchEvent(new Event("change", {bubbles: true}));
			return {state: "done", filled: (el.isContentEditable ? el.textContent : el.value) === value};
		})`, a.Selector, a.Value)
		if err != nil {
			return protocol.CallResult{}, invalid(OpFill, err)
		}
		if st, err = chromedp.Run(cctx, chromedp.Evaluate[fillState](check)); err != nil {
			return protocol.CallResult{}, failed(cctx, what, err)
		}
		if st.State == "missing" {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseQueryError, "%s: the element went away while typing", what)
		}
	}
	return protocol.CallResult{
		Fact: map[string]any{"filled": st.Filled},
		Evidence: protocol.Evidence{
			Summary:        fmt.Sprintf("%s: typed %d characters, filled %t", what, len([]rune(a.Value)), st.Filled),
			RequestExcerpt: fmt.Sprintf("fill %s with %d characters", a.Selector, len([]rune(a.Value))),
			DurationMS:     time.Since(start).Milliseconds(),
		},
	}, nil
}

func (t *tab) waitFailed(ctx context.Context, what, why string, err error) error {
	if ctx.Err() != nil {
		return protocol.Errorf(protocol.CauseTimeout, "%s: %s before the deadline", what, why)
	}
	return failed(ctx, what, err)
}

func (d *Driver) click(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	t, err := d.lookup(p.Handle)
	if err != nil {
		return protocol.CallResult{}, err
	}
	var a clickArgs
	if err := protocol.Decode(p.Args, &a); err != nil {
		return protocol.CallResult{}, invalid(OpClick, err)
	}
	if strings.TrimSpace(a.Selector) == "" {
		return protocol.CallResult{}, invalid(OpClick, errors.New("selector is empty"))
	}
	waitPath := ""
	if strings.HasPrefix(a.WaitFor, "/") {
		if err := checkPath(a.WaitFor, true); err != nil {
			return protocol.CallResult{}, invalid(OpClick, fmt.Errorf("wait_for: %v", err))
		}
		waitPath = d.basePath() + a.WaitFor
	}
	start := time.Now()
	cctx, cancel := within(ctx, t.ctx)
	defer cancel()
	what := fmt.Sprintf("click %s on %s", a.Selector, p.Handle)
	navs, loads := t.counters()
	if err := chromedp.Do(cctx, chromedp.Click(chromedp.CSS(a.Selector))); err != nil {
		return protocol.CallResult{}, t.waitFailed(cctx, what, "the element isn't visible", err)
	}
	switch {
	case waitPath != "":
		if err := t.waitPath(cctx, waitPath); err != nil {
			return protocol.CallResult{}, t.waitFailed(cctx, what, fmt.Sprintf("the page didn't reach wait_for %s", a.WaitFor), err)
		}
	case a.WaitFor != "":
		if err := chromedp.Do(cctx, chromedp.WaitVisible(chromedp.CSS(a.WaitFor))); err != nil {
			return protocol.CallResult{}, t.waitFailed(cctx, what, fmt.Sprintf("wait_for %s didn't appear", a.WaitFor), err)
		}
	default:
		if err := t.settle(cctx, navs, loads); err != nil {
			return protocol.CallResult{}, t.waitFailed(cctx, what, "the page it started loading didn't finish", err)
		}
	}
	where, title, err := t.location(cctx)
	if err != nil {
		return protocol.CallResult{}, failed(cctx, what, err)
	}
	after, _ := t.counters()
	fact := map[string]any{"url": where, "title": title, "navigated": after > navs}
	summary := fmt.Sprintf("%s -> %s", what, shownPath(where))
	if status, ok := t.lastStatus(navs); ok {
		fact["status"] = status
		summary += fmt.Sprintf(" (%d)", status)
	}
	return protocol.CallResult{
		Fact: fact,
		Evidence: protocol.Evidence{
			Summary:         summary,
			RequestExcerpt:  fmt.Sprintf("click %s\nwait_for: %s", a.Selector, a.WaitFor),
			ResponseExcerpt: fmt.Sprintf("url: %s\ntitle: %s", where, title),
			DurationMS:      time.Since(start).Milliseconds(),
		},
	}, nil
}

func (t *tab) waitPath(ctx context.Context, want string) error {
	for {
		p, err := chromedp.Run(ctx, chromedp.Evaluate[string](`location.pathname`))
		if err == nil && p == want {
			ready, rerr := chromedp.Run(ctx, chromedp.Evaluate[string](`document.readyState`))
			if rerr == nil && ready == "complete" {
				return nil
			}
		}
		if err != nil && !transient(err) && ctx.Err() == nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollEvery):
		}
	}
}

func (t *tab) settle(ctx context.Context, navs, loads int) error {
	grace := time.After(settleWait)
	for {
		n, l := t.counters()
		if n > navs && l > loads {
			ready, err := chromedp.Run(ctx, chromedp.Evaluate[string](`document.readyState`))
			if err == nil && ready == "complete" {
				return nil
			}
		}
		select {
		case <-grace:
			if n == navs {
				return nil
			}
			grace = nil
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollEvery):
		}
	}
}

const pageScript = `(function (sels, textLimit, attrLimit) {
	const out = {url: location.href, title: document.title, selectors: {}};
	for (const name of Object.keys(sels)) {
		let all;
		try {
			all = document.querySelectorAll(sels[name]);
		} catch (e) {
			out.selectors[name] = {invalid: String(e && e.message || e)};
			continue;
		}
		const r = {count: all.length, text: "", visible: false, attributes: {}};
		if (all.length > 0) {
			const el = all[0];
			const text = (typeof el.innerText === "string" ? el.innerText : el.textContent) || "";
			r.text = text.length > textLimit ? text.slice(0, textLimit + 1) : text;
			if (typeof el.checkVisibility === "function") {
				r.visible = el.checkVisibility({opacityProperty: true, visibilityProperty: true});
			} else {
				const st = getComputedStyle(el);
				r.visible = !!(el.offsetWidth || el.offsetHeight || el.getClientRects().length) && st.visibility !== "hidden";
			}
			for (const a of el.attributes) {
				r.attributes[a.name] = a.value.length > attrLimit ? a.value.slice(0, attrLimit + 1) : a.value;
			}
		}
		out.selectors[name] = r;
	}
	return out;
})`

type element struct {
	Invalid    string            `json:"invalid"`
	Count      int               `json:"count"`
	Text       string            `json:"text"`
	Visible    bool              `json:"visible"`
	Attributes map[string]string `json:"attributes"`
}

type pageRead struct {
	URL       string             `json:"url"`
	Title     string             `json:"title"`
	Selectors map[string]element `json:"selectors"`
}

func (d *Driver) page(ctx context.Context, p protocol.CallParams) (protocol.CallResult, error) {
	t, err := d.lookup(p.Handle)
	if err != nil {
		return protocol.CallResult{}, err
	}
	var a pageArgs
	if p.Args != nil {
		if err := protocol.Decode(p.Args, &a); err != nil {
			return protocol.CallResult{}, invalid(OpPage, err)
		}
	}
	if len(a.Selectors) > maxSelectors {
		return protocol.CallResult{}, invalid(OpPage, fmt.Errorf("selectors names %d elements; a read takes at most %d", len(a.Selectors), maxSelectors))
	}
	for _, name := range sortedKeys(a.Selectors) {
		if !nameRE.MatchString(name) || strings.TrimSpace(a.Selectors[name]) == "" {
			return protocol.CallResult{}, invalid(OpPage, fmt.Errorf("selector %q needs a name matching ^[a-z][a-z0-9_]{0,62}$ and a CSS selector", name))
		}
	}
	sels := a.Selectors
	if sels == nil {
		sels = map[string]string{}
	}
	start := time.Now()
	cctx, cancel := within(ctx, t.ctx)
	defer cancel()
	expr, err := jsCall(pageScript, sels, textBytes, attrBytes)
	if err != nil {
		return protocol.CallResult{}, invalid(OpPage, err)
	}
	read, err := retryEval[pageRead](cctx, expr)
	if err != nil {
		return protocol.CallResult{}, failed(cctx, "page on "+p.Handle, err)
	}
	console, failedReqs, overflow, reasons := t.snapshot()
	selectors := map[string]any{}
	var sb strings.Builder
	for _, name := range sortedKeys(sels) {
		el, ok := read.Selectors[name]
		if !ok {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseUnreadableResponse, "page on %s: the page gave no answer for selector %s", p.Handle, name)
		}
		if el.Invalid != "" {
			return protocol.CallResult{}, protocol.Errorf(protocol.CauseInvalidRequest, "page on %s: selector %s (%q) isn't valid CSS: %s", p.Handle, name, sels[name], el.Invalid)
		}
		text := t.scrub(el.Text)
		truncated := len(el.Text) > textBytes
		if truncated {
			text = cut(text, textBytes)
			overflow = true
			reasons = append(reasons, fmt.Sprintf("selector %s: text cut at %d bytes", name, textBytes))
		}
		attrs := map[string]string{}
		for _, k := range sortedKeys(el.Attributes) {
			v := el.Attributes[k]
			if len(v) > attrBytes {
				v = cut(v, attrBytes)
				overflow = true
				reasons = append(reasons, fmt.Sprintf("selector %s: attribute %s cut at %d bytes", name, k, attrBytes))
			}
			attrs[k] = t.scrub(v)
		}
		selectors[name] = map[string]any{"count": el.Count, "text": text, "text_truncated": truncated, "visible": el.Visible, "attributes": attrs}
		fmt.Fprintf(&sb, "%s (%s): count %d, visible %t, text %q\n", name, sels[name], el.Count, el.Visible, cut(text, 200))
	}
	fact := map[string]any{
		"url":             t.scrub(read.URL),
		"title":           t.scrub(read.Title),
		"selectors":       selectors,
		"console_errors":  console,
		"failed_requests": failedReqs,
		"overflow":        overflow,
	}
	if overflow {
		fact["overflow_reason"] = strings.Join(reasons, "; ")
	}
	state := ""
	if overflow {
		state = ", overflowed"
	}
	return protocol.CallResult{
		Fact: fact,
		Evidence: protocol.Evidence{
			Summary:         fmt.Sprintf("page on %s: %s, %d selectors, %d console errors, %d failed requests%s", p.Handle, shownPath(t.scrub(read.URL)), len(sels), len(console), len(failedReqs), state),
			ResponseExcerpt: excerpt(sb.String()),
			DurationMS:      time.Since(start).Milliseconds(),
		},
	}, nil
}
