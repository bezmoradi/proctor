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

func (r *Runner) closeLogs(ctx context.Context, w *logWindow, expect []model.LogExpectation) *AppLogResult {
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
	for _, line := range win.Lines {
		c, ok := r.logRules.Candidate(line)
		if !ok {
			continue
		}
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

func (o *Outcome) applyTableLogs(p, table string, rows []int, al *AppLogResult) {
	var t *TableResult
	for i := range o.Tables {
		if o.Tables[i].ID == table && o.Tables[i].Spec == p {
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
