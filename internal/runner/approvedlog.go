package runner

import (
	"strings"

	"proctor/internal/applogs"
	"proctor/internal/expr"
	"proctor/internal/model"
	"proctor/internal/transcript"
)

const (
	ReasonLogNotApproved = "log_not_approved"
	ReasonLogChanged     = "log_changed"
	ReasonLogTooLarge    = "log_too_large"
	ReasonLogNotWritten  = "log_not_written"
	ReasonLogUnusable    = "log_unusable"
)

var logKind = approvedKind{noun: "log", suffix: model.ApprovedLogSuffix, limitKey: "app_logs.approved.max_bytes",
	notApproved: ReasonLogNotApproved, changed: ReasonLogChanged, tooLarge: ReasonLogTooLarge,
	notWritten: ReasonLogNotWritten, unusable: ReasonLogUnusable}

type logRecord struct {
	window []string
	read   bool
	cause  string
	vars   []*scenarioRecord
}

func SensitiveField(name string) bool {
	return sensitiveName(name) || credentialHeaders[strings.ToLower(name)]
}

func (r *Runner) keepLogVars(res *ScenarioResult, sc model.Scenario, vars expr.Vars) {
	if !sc.ApprovedLogWanted() || vars == nil {
		return
	}
	snapshot := expr.Vars{}
	for k, v := range vars {
		snapshot[k] = v
	}
	run, _ := vars[expr.RunID].(string)
	res.logVars = &scenarioRecord{vars: snapshot, run: run}
}

func newLogRecord(al *AppLogResult) *logRecord {
	switch {
	case al == nil:
		return &logRecord{cause: "app_logs isn't set up for this run (proctor.yaml app_logs and the binding's app_logs.file), so there is no log to compare"}
	case !al.kept:
		return &logRecord{cause: "the application's log couldn't be read (" + al.Message + "), so there is no log to compare"}
	}
	rec := &logRecord{window: al.window, read: true}
	al.window = nil
	return rec
}

func (o *Outcome) keepTableLog(p, key string, rows []int, al *AppLogResult) {
	for i := range o.Tables {
		t := &o.Tables[i]
		if t.key != key || t.Spec != p {
			continue
		}
		rec := newLogRecord(al)
		for _, idx := range rows {
			if v := o.Scenarios[idx].logVars; v != nil {
				rec.vars = append(rec.vars, v)
			}
		}
		t.logRec = rec
	}
}

func ownResults(out *Outcome) []Result {
	own := make([]Result, len(out.Scenarios))
	for i := range out.Scenarios {
		own[i] = out.Scenarios[i].Result
	}
	return own
}

func (r *Runner) applyApprovedLogs(out *Outcome, own []Result) {
	settings := r.cfg.AppLogs.ApprovedSettings()
	var rules []transcript.LogRule
	var probs []string
	if settings != nil {
		rules, probs = transcript.CompileLogRules(settings.Scrub, settings.Fields, r.logRules.MessageField)
	}
	for i := range out.Scenarios {
		res := &out.Scenarios[i]
		rec := res.logRec
		if rec != nil && res.logVars != nil {
			rec.vars = append(rec.vars, res.logVars)
		}
		res.logRec, res.logVars = nil, nil
		if res.tableKey != "" || own[i] == NotRun {
			continue
		}
		sc := r.scenarioOf(res.Spec, res.ID)
		if sc == nil || sc.Case != nil || !sc.ApprovedLogWanted() {
			continue
		}
		lr := &TranscriptResult{Own: own[i]}
		res.ApprovedLog = lr
		fail := func(result Result, reason, kind, msg string) {
			lr.Result, lr.Message = kind, msg
			if out.Scenarios[i].Result == Pass {
				out.setResult(i, result, reason)
			}
		}
		r.compareLog(sc.LogApproval, sc.ID, rec, settings, rules, probs, lr, fail)
	}
	for t := range out.Tables {
		tb := &out.Tables[t]
		rec := tb.logRec
		tb.logRec = nil
		var rows []int
		var first *model.Scenario
		worst := NotRun
		for i := range out.Scenarios {
			sr := &out.Scenarios[i]
			if sr.Spec != tb.Spec || sr.tableKey != tb.key {
				continue
			}
			if first == nil {
				first = r.scenarioOf(sr.Spec, sr.ID)
			}
			rows = append(rows, i)
			if own[i] == NotRun {
				continue
			}
			if worst == NotRun || tableRank[own[i]] > tableRank[worst] {
				worst = own[i]
			}
		}
		if first == nil || first.Case == nil || !first.ApprovedLogWanted() || worst == NotRun {
			continue
		}
		lr := &TranscriptResult{Own: worst}
		tb.ApprovedLog = lr
		fail := func(result Result, reason, kind, msg string) {
			lr.Result, lr.Message = kind, msg
			for _, i := range rows {
				if out.Scenarios[i].Result == Pass {
					out.setResult(i, result, reason)
				}
			}
		}
		r.compareLog(first.LogApproval, first.Case.Table, rec, settings, rules, probs, lr, fail)
	}
}

func (r *Runner) compareLog(a *model.Approval, owner string, rec *logRecord, settings *model.ApprovedLogs, rules []transcript.LogRule, probs []string, lr *TranscriptResult, fail func(result Result, reason, kind, msg string)) {
	switch {
	case a == nil:
		fail(Error, ReasonLogUnusable, TranscriptUnusable, "the approved log wasn't loaded with the specifications, so it can't be compared")
		return
	case settings == nil:
		fail(Error, ReasonLogUnusable, TranscriptUnusable, "proctor.yaml has no app_logs.approved, so the log can't be rendered")
		return
	}
	lr.SpecID, lr.ApprovedPath = a.SpecID, a.Path
	if a.Found {
		lr.ApprovedSHA256 = a.SHA256
	}
	switch {
	case len(probs) > 0:
		fail(Error, ReasonLogUnusable, TranscriptUnusable, "app_logs.approved.scrub is invalid: "+strings.Join(probs, "; "))
		return
	case rec == nil:
		fail(Error, ReasonLogUnusable, TranscriptUnusable, "no log window was recorded for it, so there is no log to compare")
		return
	case !rec.read:
		fail(Error, ReasonLogUnusable, TranscriptUnusable, rec.cause)
		return
	}
	var subs []transcript.Sub
	for _, v := range rec.vars {
		subs = append(subs, r.placeholders(v)...)
	}
	if len(rec.vars) == 0 {
		subs = r.placeholders(&scenarioRecord{vars: expr.Vars{}})
	}
	col := r.logRules.Collect(rec.window, settings.Fields, SensitiveField, nil)
	data := applogs.Render(col, applogs.Scrub{Clean: r.scrub.clean, Subs: transcript.OrderSubs(subs), Rules: rules,
		Message: r.logRules.MessageField, Sorted: settings.Sorted()})
	r.judgeApproved(logKind, a, owner, data, settings.Limit(), lr, fail)
}
