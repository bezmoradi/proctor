package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

type lastingError struct{ msg string }

func (e *lastingError) Error() string { return e.msg }

func lasting(err error) bool {
	var le *lastingError
	return errors.As(err, &le)
}

type engine struct {
	d *Driver

	mu       sync.Mutex
	starting chan struct{}
	err      error
	ctx      context.Context
	cancel   context.CancelFunc
	alloc    context.CancelFunc
	detail   string
	stopped  bool
	gate     *gate
	openers  map[target.ID]*tab
}

func (e *engine) ensure(ctx context.Context) (string, error) {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return "", errors.New("the browser driver is stopping")
	}
	if e.starting == nil {
		e.starting = make(chan struct{})
		go e.start(e.starting)
	}
	ch := e.starting
	e.mu.Unlock()
	select {
	case <-ch:
	case <-ctx.Done():
		return "", fmt.Errorf("the browser is still starting: %w", ctx.Err())
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		err := e.err
		if !lasting(err) {
			e.starting = nil
			e.err = nil
		}
		return "", err
	}
	return e.detail, nil
}

func (e *engine) browserCtx(ctx context.Context) (context.Context, error) {
	if _, err := e.ensure(ctx); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ctx, nil
}

func (e *engine) start(done chan struct{}) {
	defer close(done)
	var allocCtx context.Context
	var allocCancel context.CancelFunc
	detail := ""
	if e.d.remoteURL != "" {
		allocCtx, allocCancel = remote.NewAllocator(context.Background(), e.d.remoteURL)
		detail = "connected to the browser at remote_url"
	} else {
		path, err := findChrome(e.d.chromePath)
		if err != nil {
			e.fail(err, nil, nil)
			return
		}
		g, err := openGate(e.d.allowed)
		if err != nil {
			e.fail(fmt.Errorf("the browser driver can't listen for the browser's connections: %v", err), nil, nil)
			return
		}
		e.mu.Lock()
		e.gate = g
		e.mu.Unlock()
		opts := append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(path),
			chromedp.WindowSize(1280, 800),
			chromedp.Flag("proxy-server", "socks5://"+g.addr()),
			chromedp.Flag("proxy-bypass-list", "<-loopback>"),
			chromedp.Flag("host-resolver-rules", resolverRules(e.d.hosts)),
			chromedp.Flag("disable-component-update", true),
			chromedp.Flag("no-pings", true),
			chromedp.Flag("disable-domain-reliability", true),
			chromedp.Flag("disable-notifications", true),
		)
		allocCtx, allocCancel = chromedp.NewExecAllocator(context.Background(), opts...)
		detail = "started " + path
	}
	bctx, bcancel := chromedp.NewContext(allocCtx)
	v, err := chromedp.CallBrowser(bctx, browser.GetVersion, cdp.Empty{})
	if err == nil {
		detail += " (" + v.Product + ")"
		if e.d.remoteURL == "" {
			_, err = chromedp.CallBrowser(bctx, browser.SetDownloadBehavior, browser.SetDownloadBehaviorParams{Behavior: browser.SetDownloadBehaviorBehaviorDeny})
		}
	}
	if err == nil {
		_, err = chromedp.CallBrowser(bctx, target.SetDiscoverTargets, target.SetDiscoverTargetsParams{Discover: true})
	}
	if err == nil {
		err = chromedp.ListenBrowser(bctx, e.onBrowserEvent)
	}
	if err != nil {
		e.fail(fmt.Errorf("the browser didn't start: %v", err), bcancel, allocCancel)
		return
	}
	e.mu.Lock()
	e.ctx, e.cancel, e.alloc, e.detail = bctx, bcancel, allocCancel, detail
	e.openers = map[target.ID]*tab{}
	e.mu.Unlock()
}

func (e *engine) fail(err error, bcancel, allocCancel context.CancelFunc) {
	if bcancel != nil {
		stopWithin(bcancel, closeWait)
	}
	if allocCancel != nil {
		allocCancel()
	}
	e.mu.Lock()
	e.err = err
	g := e.gate
	e.gate = nil
	e.mu.Unlock()
	if g != nil {
		g.close()
	}
}

func stopWithin(f func(), d time.Duration) {
	done := make(chan struct{})
	go func() {
		f()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}

func (e *engine) onBrowserEvent(ev any) {
	var info *target.Info
	switch ev := ev.(type) {
	case *target.EventTargetCreated:
		info = ev.TargetInfo
	case *target.EventTargetInfoChanged:
		info = ev.TargetInfo
	default:
		return
	}
	if info == nil || info.Type != "page" || info.OpenerID == "" {
		return
	}
	e.mu.Lock()
	owner := e.openers[info.OpenerID]
	ctx := e.ctx
	e.mu.Unlock()
	if owner == nil || ctx == nil {
		return
	}
	if !owner.popupSeen(info.TargetID) {
		return
	}
	go func() {
		cctx, cancel := context.WithTimeout(ctx, closeWait)
		defer cancel()
		_, _ = chromedp.CallBrowser(cctx, target.CloseTarget, target.CloseTargetParams{TargetID: info.TargetID})
	}()
	owner.addFailed(owner.windowEntry(info.URL))
}

func (e *engine) register(id target.ID, t *tab) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.openers != nil {
		e.openers[id] = t
	}
}

func (e *engine) unregister(id target.ID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.openers, id)
}

func (e *engine) stop() {
	e.mu.Lock()
	e.stopped = true
	ch := e.starting
	e.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		case <-time.After(70 * time.Second):
		}
	}
	e.mu.Lock()
	ctx, cancel, alloc, g := e.ctx, e.cancel, e.alloc, e.gate
	e.ctx, e.cancel, e.alloc, e.gate = nil, nil, nil, nil
	e.mu.Unlock()
	if g != nil {
		defer g.close()
	}
	if ctx == nil {
		return
	}
	stopWithin(func() {
		cctx, ccancel := context.WithTimeout(ctx, closeWait)
		defer ccancel()
		_ = chromedp.Cancel(cctx)
	}, closeWait+time.Second)
	stopWithin(cancel, closeWait)
	alloc()
}

func resolverRules(hosts []string) string {
	parts := []string{"MAP * ~NOTFOUND"}
	seen := map[string]bool{}
	for _, h := range hosts {
		h = strings.Trim(h, "[]")
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		parts = append(parts, "EXCLUDE "+h)
	}
	return strings.Join(parts, ", ")
}

func findChrome(given string) (string, error) {
	if given != "" {
		st, err := os.Stat(given)
		if err != nil {
			return "", &lastingError{fmt.Sprintf("chrome_path %s: %v", given, err)}
		}
		if !st.Mode().IsRegular() || st.Mode().Perm()&0o111 == 0 {
			return "", &lastingError{fmt.Sprintf("chrome_path %s is not an executable file", given)}
		}
		return given, nil
	}
	for _, name := range chromeNames(runtime.GOOS) {
		if found, err := exec.LookPath(name); err == nil {
			return found, nil
		}
	}
	return "", &lastingError{"no Chrome or Chromium found on PATH or in the usual places; install one, or set chrome_path or remote_url in the binding"}
}

func chromeNames(goos string) []string {
	names := []string{"google-chrome-stable", "google-chrome", "chromium", "chromium-browser", "chrome", "headless_shell", "headless-shell"}
	switch goos {
	case "darwin":
		names = append(names,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
		)
	case "windows":
		names = append(names, "chrome.exe",
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		)
	default:
		names = append(names, "/usr/bin/google-chrome", "/usr/bin/chromium", "/usr/bin/chromium-browser", "/snap/bin/chromium", "/opt/google/chrome/chrome")
	}
	return names
}
