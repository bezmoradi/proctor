package runner

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"proctor/internal/model"
)

const (
	RestartDone   = "restarted"
	RestartFailed = "failed"
)

type DriverRestart struct {
	After   string `json:"after"`
	Before  string `json:"before"`
	At      string `json:"at"`
	Result  string `json:"result"`
	Message string `json:"message,omitempty"`

	label, step string
}

func (r *Runner) markDead(h *host) {
	h.dead = true
	if h.deadBy == "" {
		h.deadBy, h.deadLabel, h.deadStep = r.at, r.at, r.atStep
		if r.atStep != "" {
			h.deadBy += "/" + r.atStep
		}
	}
}

func (r *Runner) revive(ctx context.Context, before string) {
	if ctx.Err() != nil {
		return
	}
	for _, h := range r.sess.ordered() {
		if !h.dead || h.closed || h.stopped != "" {
			continue
		}
		rec := DriverRestart{After: h.deadBy, Before: before, At: time.Now().UTC().Format(time.RFC3339Nano), label: h.deadLabel, step: h.deadStep}
		if msg := r.restart(ctx, h); msg != "" {
			rec.Result, rec.Message = RestartFailed, r.scrub.clean(msg)
			h.stopped = h.deadBy
		} else {
			rec.Result = RestartDone
			h.dead, h.deadBy, h.deadLabel, h.deadStep = false, "", "", ""
		}
		h.restarts = append(h.restarts, rec)
	}
}

func (r *Runner) restart(ctx context.Context, h *host) string {
	old := h.client
	_ = old.Close()
	h.earlierStderr += string(old.Stderr())
	h.earlierDropped += old.StderrDropped()
	h.client = nil
	hello, ops, entries := h.hello, h.ops, h.entries
	var probs problems
	r.sess.start(ctx, h, h.policy, h.conn, h.secrets, &probs)
	if len(probs.msgs) > 0 {
		h.closeAfterFailedStart()
		return strings.Join(probs.msgs, "; ")
	}
	switch {
	case !reflect.DeepEqual(hello, h.hello) || !reflect.DeepEqual(ops, h.ops):
		h.closeAfterFailedStart()
		return "the driver started again, but its hello or operations differ from the first start's"
	case !reflect.DeepEqual(entries, h.entries):
		h.closeAfterFailedStart()
		return "the driver started again, but its policy entries differ from the first start's"
	}
	if msg := r.readyHost(ctx, h); msg != "" {
		h.closeAfterFailedStart()
		return msg
	}
	proofs, violations, msg := r.proveHost(ctx, h)
	switch {
	case msg != "":
		h.closeAfterFailedStart()
		return msg
	case len(violations) > 0:
		h.closeAfterFailedStart()
		return strings.Join(violations, "; ")
	case !sameProofs(proofs, h.proofs):
		h.closeAfterFailedStart()
		return "the driver started again, but its proofs differ from the first start's"
	}
	return ""
}

func (h *host) closeAfterFailedStart() {
	if h.client == nil {
		return
	}
	_ = h.client.Close()
	h.earlierStderr += string(h.client.Stderr())
	h.earlierDropped += h.client.StderrDropped()
	h.client = nil
	h.dead = true
}

func (r *Runner) stoppedDriver(sc model.Scenario) *host {
	for _, st := range allSteps(sc) {
		name, _, ok := model.SplitDo(st.Do)
		if !ok {
			continue
		}
		if h := r.sess.hosts[name]; h != nil && h.stopped != "" {
			return h
		}
	}
	return nil
}

func sameTable(scs []model.Scenario, i, j int) bool {
	if j < 0 || j >= len(scs) {
		return false
	}
	a, b := scs[i].Case, scs[j].Case
	return a != nil && b != nil && a.Key == b.Key
}

func stoppedMessage(h *host) string {
	return fmt.Sprintf("driver %s was stopped after %s and couldn't be started again", h.name, h.stopped)
}

func sameProofs(a, b []Proof) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Status != b[i].Status {
			return false
		}
	}
	return true
}

func (r *Runner) noteRestarts(out *Outcome) {
	for _, h := range r.sess.ordered() {
		for _, rs := range h.restarts {
			note := fmt.Sprintf("; Proctor started driver %s again before %s", h.name, rs.Before)
			if rs.Result != RestartDone {
				note = fmt.Sprintf("; driver %s couldn't be started again: %s", h.name, rs.Message)
			}
			for i := range out.Scenarios {
				sc := &out.Scenarios[i]
				label := sc.ID
				if sc.Table != "" {
					label = sc.Table + "/" + sc.ID
				}
				if label != rs.label {
					continue
				}
				for j := range sc.Steps {
					st := &sc.Steps[j]
					if st.ID == rs.step && st.Driver == h.name && st.Result == Error {
						st.Message += note
					}
				}
			}
		}
	}
}
