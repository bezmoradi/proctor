package browser

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const windowBinding = "__proctorBlockedWindow"

const windowGuard = `(() => {
	const report = (u) => { try { window.` + windowBinding + `(String(u || "")); } catch (e) {} };
	const own = new Set(["", "_self", "_parent", "_top"]);
	const opensWindow = (t) => {
		t = String(t || "").toLowerCase();
		if (own.has(t)) return false;
		try {
			return !document.querySelector('iframe[name="' + CSS.escape(t) + '"], frame[name="' + CSS.escape(t) + '"]');
		} catch (e) {
			return true;
		}
	};
	const absolute = (u) => { try { return new URL(String(u), location.href).href; } catch (e) { return String(u || ""); } };
	Object.defineProperty(window, "open", {value: function (u) { report(u ? absolute(u) : "about:blank"); return null; }, configurable: false, writable: false});
	document.addEventListener("click", (e) => {
		const a = e.target && e.target.closest ? e.target.closest("a[target], area[target]") : null;
		if (a && opensWindow(a.target)) {
			e.preventDefault();
			report(a.href);
		}
	}, true);
	document.addEventListener("submit", (e) => {
		const f = e.target;
		const t = (e.submitter && e.submitter.getAttribute("formtarget")) || (f && f.target);
		if (f && opensWindow(t)) {
			e.preventDefault();
			report(absolute(f.getAttribute("action") || location.href));
		}
	}, true);
})();`

func (d *Driver) allowPatterns() []*network.BlockPattern {
	origins := make([]string, 0, len(d.allowed))
	for o := range d.allowed {
		origins = append(origins, o)
	}
	sort.Strings(origins)
	var out []*network.BlockPattern
	for _, o := range origins {
		ws := "ws" + strings.TrimPrefix(o, "http")
		out = append(out, &network.BlockPattern{URLPattern: o + "/*", Block: false}, &network.BlockPattern{URLPattern: ws + "/*", Block: false})
	}
	return append(out, &network.BlockPattern{URLPattern: "*://*:*/*", Block: true})
}

func (t *tab) guard(ctx context.Context) error {
	if _, err := chromedp.Call(ctx, network.SetBlockedURLs, network.SetBlockedURLsParams{URLPatterns: t.d.allowPatterns()}); err != nil {
		return fmt.Errorf("blocking other origins: %v", err)
	}
	if _, err := chromedp.Call(ctx, network.SetBypassServiceWorker, network.SetBypassServiceWorkerParams{Bypass: true}); err != nil {
		return err
	}
	if _, err := chromedp.Call(ctx, fetch.Enable, fetch.EnableParams{Patterns: []*fetch.RequestPattern{{URLPattern: "*"}}}); err != nil {
		return fmt.Errorf("intercepting requests: %v", err)
	}
	if _, err := chromedp.Call(ctx, runtime.AddBinding, runtime.AddBindingParams{Name: windowBinding}); err != nil {
		return err
	}
	if _, err := chromedp.Call(ctx, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: windowGuard}); err != nil {
		return err
	}
	return nil
}

func (t *tab) blockedEntry(raw, method, kind, origin string) map[string]any {
	why := fmt.Sprintf("blocked: %s isn't base_url's origin or one of the policy's origins", origin)
	if origin == "" || strings.HasSuffix(origin, ":") {
		why = "blocked: the browser driver loads only http, https, ws and wss URLs of allowed origins"
	}
	if method == "" {
		method = "GET"
	}
	return map[string]any{"url": t.scrub(cut(raw, urlBytes)), "method": method, "type": kind, "status": 0, "error": why, "blocked": true}
}

func (t *tab) windowEntry(raw string) map[string]any {
	return map[string]any{"url": t.scrub(cut(raw, urlBytes)), "method": "GET", "type": "Window", "status": 0,
		"error": "blocked: the page tried to open a new window; a handle has one page", "blocked": true}
}

func iconRequest(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Path == "/favicon.ico"
}
