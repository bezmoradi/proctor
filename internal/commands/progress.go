package commands

import (
	"fmt"
	"io"
	"sync"
	"time"

	"proctor/internal/runner"
)

const progressEvery = 15 * time.Second

type progressLog struct {
	w    io.Writer
	mu   sync.Mutex
	stop chan struct{}
}

func newProgress(w io.Writer, off bool) (func(runner.Progress), func()) {
	if off {
		return nil, func() {}
	}
	p := &progressLog{w: w}
	return p.update, p.close
}

func (p *progressLog) update(ev runner.Progress) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.halt()
	if ev.Done {
		cleanup := ""
		if ev.Cleanup != "" {
			cleanup = "  cleanup " + string(ev.Cleanup)
		}
		fmt.Fprintf(p.w, "[%d/%d] %-8s %-10s %.1fs%s\n", ev.Index, ev.Total, ev.Result, safe(progressName(ev)), ev.Elapsed.Seconds(), cleanup)
		return
	}
	stop := make(chan struct{})
	p.stop = stop
	go p.watch(ev, time.Now(), stop)
}

func (p *progressLog) watch(ev runner.Progress, began time.Time, stop chan struct{}) {
	t := time.NewTicker(progressEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		p.mu.Lock()
		select {
		case <-stop:
		default:
			secs := int(time.Since(began).Round(time.Second) / time.Second)
			fmt.Fprintf(p.w, "[%d/%d] %-8s %-10s %ds\n", ev.Index, ev.Total, "running", safe(progressName(ev)), secs)
		}
		p.mu.Unlock()
	}
}

func progressName(ev runner.Progress) string {
	if ev.Table == "" {
		return ev.ID
	}
	return ev.Table + "/" + ev.ID
}

func (p *progressLog) halt() {
	if p.stop != nil {
		close(p.stop)
		p.stop = nil
	}
}

func (p *progressLog) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.halt()
}
