package protdiff

import (
	"fmt"
	"slices"
	"strings"

	"proctor/internal/model"
)

func compareApprovedLogs(b, h *model.Config) []Weakening {
	ba := b.AppLogs.ApprovedSettings()
	if ba == nil {
		return nil
	}
	ha := h.AppLogs.ApprovedSettings()
	if ha == nil {
		return []Weakening{{Kind: "approved_log_dropped", Detail: "app_logs.approved was removed, so no scenario's log can be compared with an approved one"}}
	}
	var out []Weakening
	for _, f := range ba.Fields {
		if !slices.Contains(ha.Fields, f) {
			out = append(out, Weakening{Kind: "scrub_widened", Detail: fmt.Sprintf("app_logs.approved.fields no longer lists %s, so its values are no longer compared", f)})
		}
	}
	was := map[string]model.LogScrubRule{}
	for _, r := range ba.Scrub {
		was[r.Name] = r
	}
	for _, r := range ha.Scrub {
		old, ok := was[r.Name]
		switch {
		case !ok:
			out = append(out, Weakening{Kind: "scrub_widened", Detail: fmt.Sprintf("app_logs.approved.scrub adds rule %s (%s); whether it hides more of the logs can't be decided, so the values it replaces are no longer compared", r.Name, logScrubText(r))})
		case old.Pattern != r.Pattern || old.Field != r.Field || old.IsNumbered() != r.IsNumbered():
			out = append(out, Weakening{Kind: "scrub_widened", Detail: fmt.Sprintf("app_logs.approved.scrub rule %s changed from %s to %s; whether it hides more of the logs can't be decided", r.Name, logScrubText(old), logScrubText(r))})
		}
	}
	if !ba.Sorted() && ha.Sorted() {
		out = append(out, Weakening{Kind: "log_order_relaxed", Detail: fmt.Sprintf("app_logs.approved.order %s became sorted, so lines written in another order now match", ba.OrderName())})
	}
	return out
}

func logScrubText(r model.LogScrubRule) string {
	var parts []string
	if r.Pattern != "" {
		parts = append(parts, "pattern "+r.Pattern)
	}
	if r.Field != "" {
		parts = append(parts, "field "+r.Field)
	}
	if !r.IsNumbered() {
		parts = append(parts, "numbered: false")
	}
	return strings.Join(parts, ", ")
}

func approvedLogDropped(c ScenarioChange, b, h *model.Scenario, file string, tables map[string]bool) []Weakening {
	if b == nil || h == nil || !b.ApprovedLogWanted() || h.ApprovedLogWanted() {
		return nil
	}
	if b.Case != nil && h.Case != nil {
		key := file + "\x00" + h.Case.Key
		if tables[key] {
			return nil
		}
		tables[key] = true
		return []Weakening{{Scenario: "table " + h.Case.Label(), Kind: "approved_log_dropped",
			Detail: "approved_log: true was removed from the table, so its log is no longer compared with the approved one"}}
	}
	return []Weakening{{Scenario: c.ID, Kind: "approved_log_dropped",
		Detail: "approved_log: true was removed, so the scenario's log is no longer compared with the approved one"}}
}
