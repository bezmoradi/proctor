package runner

import (
	"context"
	"fmt"

	"proctor/internal/model"
	"proctor/protocol"
)

const (
	HandleClosed = "closed"
	HandleFailed = "failed"
	HandleGone   = "gone"
)

type HandleRecord struct {
	Name     string `json:"name,omitempty"`
	Driver   string `json:"driver"`
	OpenedBy string `json:"opened_by"`
	Handle   string `json:"handle"`
	Close    string `json:"close"`
	Message  string `json:"message,omitempty"`
}

type openHandle struct {
	name, step, id string
	h              *host
}

type handleSet struct {
	open    map[string]*openHandle
	order   []*openHandle
	missing map[string]string
}

func newHandleSet() *handleSet {
	return &handleSet{open: map[string]*openHandle{}, missing: map[string]string{}}
}

func (hs *handleSet) keep(st model.Step, h *host, op protocol.Operation, id string) *protocol.Error {
	ref := &openHandle{name: st.As, step: st.ID, id: id, h: h}
	hs.order = append(hs.order, ref)
	switch {
	case !op.OpensHandle:
		ref.name = ""
		return protocol.Errorf(protocol.CauseDriverFailed, "driver %s returned a handle for %s, which doesn't declare opens_handle; the handle is closed at the end of the scenario", h.name, op.Name)
	case st.As == "":
		return protocol.Errorf(protocol.CauseDriverFailed, "driver %s returned a handle for step %s, which has no as to name it; the handle is closed at the end of the scenario", h.name, st.ID)
	case hs.open[st.As] != nil:
		ref.name = ""
		return protocol.Errorf(protocol.CauseDriverFailed, "driver %s returned a second handle for %q; the extra handle is closed at the end of the scenario", h.name, st.As)
	}
	for _, other := range hs.order[:len(hs.order)-1] {
		if other.h == h && other.id == id {
			hs.order = hs.order[:len(hs.order)-1]
			return protocol.Errorf(protocol.CauseDriverFailed, "driver %s returned handle %q for %q, but it already returned it for %q", h.name, id, st.As, other.name)
		}
	}
	hs.open[st.As] = ref
	delete(hs.missing, st.As)
	return nil
}

func (hs *handleSet) use(name string, h *host) (*openHandle, string) {
	ref := hs.open[name]
	switch {
	case ref != nil && ref.h != h:
		return nil, fmt.Sprintf("on %q: the handle belongs to driver %s, not %s", name, ref.h.name, h.name)
	case ref != nil:
		return ref, ""
	case hs.missing[name] != "":
		return nil, fmt.Sprintf("on %q: the handle isn't open, because driver %s returned no handle to step %s (as %q); see that step's fact", name, h.name, hs.missing[name], name)
	}
	return nil, fmt.Sprintf("on %q: no earlier step of this scenario opened a handle with that name", name)
}

func (r *Runner) closeHandles(ctx context.Context, hs *handleSet) []HandleRecord {
	var out []HandleRecord
	for i := len(hs.order) - 1; i >= 0; i-- {
		ref := hs.order[i]
		rec := HandleRecord{Name: ref.name, Driver: ref.h.name, OpenedBy: ref.step, Handle: ref.id}
		if ref.h.dead || ref.h.closed {
			rec.Close, rec.Message = HandleGone, "the driver stopped, so its handles are gone; close was not sent"
			out = append(out, rec)
			continue
		}
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.opt.CallTimeout+callMargin)
		err := ref.h.client.Call(cctx, protocol.MethodClose, protocol.CloseParams{Handle: ref.id}, nil)
		cancel()
		if err != nil {
			if brokenClient(err) {
				ref.h.dead = true
			}
			pe := asProtocolError(err)
			rec.Close, rec.Message = HandleFailed, pe.Data.Cause+": "+pe.Message
		} else {
			rec.Close = HandleClosed
		}
		out = append(out, rec)
	}
	return out
}
