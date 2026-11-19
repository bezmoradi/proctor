package runner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"proctor/internal/applogs"
	"proctor/internal/model"
)

type logWindow struct {
	mark applogs.Mark
	err  error
}

type tableWindow struct {
	w    *logWindow
	rows []int
	done []Progress
}

func (r *Runner) openLogs() *logWindow {
	if r.logs == nil {
		return nil
	}
	m, err := r.logs.Mark()
	return &logWindow{mark: m, err: err}
}

func (r *Runner) closeLogs(ctx context.Context, w *logWindow, expect []model.LogExpectation, keep bool) *AppLogResult {
	if w == nil {
		return nil
	}
	res := &AppLogResult{Check: AppLogCheck, Result: Pass}
	if w.err != nil {
		res.Result, res.Cause = Error, CauseAppLogsUnreadable
		res.Message = fmt.Sprintf("cannot read the application's log when the scenario began: %v", w.err)
		return res
	}
	select {
	case <-ctx.Done():
	case <-time.After(logFlushWait):
	}
	win, err := r.logs.Since(w.mark)
	if err != nil {
		res.Result, res.Cause = Error, CauseAppLogsUnreadable
		res.Message = fmt.Sprintf("cannot read the application's log after the scenario: %v", err)
		return res
	}
	res.Note = win.Note
	res.LinesRead = len(win.Lines)
	index := map[string]int{}
	seen := make([]int, len(expect))
	for i, e := range expect {
		index[e.Key()] = i
	}
	if keep {
		res.window, res.kept = win.Lines, true
	}
	nonEmpty := 0
	var tr applogs.Tracker
	for _, line := range win.Lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty++
		}
		rd := r.logRules.Read(line)
		switch tr.Next(line, rd) {
		case applogs.LineParsed:
			res.LinesParsed++
		case applogs.LineUnparsed:
			res.UnparsedCount++
			if res.FirstUnparsed == "" {
				res.FirstUnparsed = line
			}
		}
		if rd.Unmapped != "" {
			res.UnmappedCount++
			if res.FirstUnmapped == "" {
				res.UnmappedLevel, res.FirstUnmapped = rd.Unmapped, line
			}
		}
		if !rd.Failing {
			continue
		}
		c := rd.Entry
		if i, allowed := index[strings.ToUpper(c.Level)+"\x00"+c.Message]; allowed {
			seen[i]++
			continue
		}
		res.UnexpectedCount++
		if len(res.Unexpected) < logLinesShown {
			res.Unexpected = append(res.Unexpected, AppLogLine{Level: c.Level, Message: c.Message, Time: c.Time, Line: c.Line})
		}
	}
	unseen := 0
	for i, e := range expect {
		x := AppLogExpected{Level: e.Level, Message: e.Message, Count: e.Count, Seen: seen[i], Result: Pass}
		switch {
		case e.Count == nil && seen[i] == 0:
			x.Result, x.Problem = Fail, "expected log not seen"
		case e.Count != nil && seen[i] != *e.Count:
			x.Result, x.Problem = Fail, fmt.Sprintf("expected log not seen exactly %d time(s); seen %d time(s)", *e.Count, seen[i])
		}
		if x.Result == Fail {
			unseen++
		}
		res.Expected = append(res.Expected, x)
	}
	var parts []string
	if res.UnexpectedCount > 0 {
		parts = append(parts, fmt.Sprintf("%d unexpected log line(s) at a failing level", res.UnexpectedCount))
		if res.UnexpectedCount > len(res.Unexpected) {
			parts[0] += fmt.Sprintf(" (the first %d are listed)", len(res.Unexpected))
		}
	}
	if unseen > 0 {
		parts = append(parts, fmt.Sprintf("%d expected log(s) not seen", unseen))
	}
	if len(parts) > 0 {
		res.Result, res.Message = Fail, strings.Join(parts, "; ")
	}
	var errs []string
	cause := ""
	switch {
	case nonEmpty > 0 && res.LinesParsed == 0:
		cause = CauseAppLogsUnparsed
		errs = append(errs, fmt.Sprintf("none of the %d line(s) written during the scenario reads as %s with a %q field, so the log wasn't checked; the first is under first_unparsed. Check app_logs.format and app_logs.level_field", nonEmpty, r.logRules.Format, r.logRules.LevelField))
	case res.UnparsedCount > 0 && !r.logLoose:
		cause = CauseAppLogsUnparsed
		errs = append(errs, fmt.Sprintf("%d of the %d line(s) written during the scenario don't read as %s with a %q field, so they weren't checked; the first is under first_unparsed. Fix the line's format, or set app_logs.unparsed: allow", res.UnparsedCount, nonEmpty, r.logRules.Format, r.logRules.LevelField))
	default:
		res.FirstUnparsed = ""
	}
	if res.UnmappedCount > 0 {
		if cause == "" {
			cause = CauseAppLogsUnmapped
		}
		errs = append(errs, fmt.Sprintf("%d line(s) have a numeric level that app_logs.level_numbers doesn't map (the first is %s), so they weren't judged; that line is under first_unmapped. Map every number the application writes", res.UnmappedCount, res.UnmappedLevel))
	}
	switch {
	case cause == "":
	case res.Result == Fail:
		res.Message = strings.Join(append([]string{res.Message}, errs...), "; ")
	default:
		res.Result, res.Cause, res.Message = Error, cause, strings.Join(errs, "; ")
	}
	return res
}

func applyLogs(al *AppLogResult, result *Result, reason, cause *string) {
	if al == nil || *result != Pass {
		return
	}
	switch al.Result {
	case Fail:
		*result, *reason = Fail, ReasonAppLogs
	case Error:
		*result, *reason, *cause = Error, ReasonAppLogs, al.Cause
	}
}

func (o *Outcome) applyTableLogs(p, key string, rows []int, al *AppLogResult) {
	var t *TableResult
	for i := range o.Tables {
		if o.Tables[i].key == key && o.Tables[i].Spec == p {
			t = &o.Tables[i]
		}
	}
	if t == nil || al == nil {
		return
	}
	t.AppLogs = al
	for _, idx := range rows {
		sc := &o.Scenarios[idx]
		applyLogs(al, &sc.Result, &sc.Reason, &sc.Cause)
		for j := range t.Rows {
			if t.Rows[j].Case == sc.ID {
				t.Rows[j].Result = sc.Result
			}
		}
		if tableRank[sc.Result] > tableRank[t.Result] {
			t.Result = sc.Result
		}
	}
}
