package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"proctor/internal/integrity"
	"proctor/internal/runner"
)

func resultName(code int) string {
	switch code {
	case exitOK:
		return "passed"
	case exitFailed:
		return "failed"
	case exitError:
		return "error"
	case exitInvalid:
		return "invalid"
	default:
		return "integrity_violation"
	}
}

type driverCheck struct {
	drivers    []runner.DriverInfo
	setup      *runner.SetupError
	outsideGit bool
	stopped    *runner.Outcome
}

const localMode = "local, non-authoritative"

var localTrust = map[string]any{"authoritative": false, "reason": "local mode reads the working tree; the result is not evidence for a commit"}

func addStopped(doc map[string]any, out *runner.Outcome) {
	doc["trust"] = localTrust
	doc["summary"] = out.Summary
	doc["scenarios"] = out.Scenarios
	if len(out.Tables) > 0 {
		doc["tables"] = out.Tables
	}
}

const outsideGitNote = "not a Git repository: the folder was read as it is"

func write(w io.Writer, asJSON bool, title string, r *integrity.Report, dc *driverCheck, code int, result string) int {
	if dc == nil {
		dc = &driverCheck{}
	}
	var errs []string
	if dc.setup != nil {
		errs = dc.setup.Msgs
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		doc := map[string]any{"result": result, "exit_code": code, "mode": "local", "report": r}
		if dc.drivers != nil {
			doc["drivers"] = dc.drivers
		}
		if dc.setup != nil {
			doc["errors"] = errs
		}
		if dc.outsideGit {
			doc["git"] = false
			doc["note"] = outsideGitNote
		}
		if dc.stopped != nil {
			addStopped(doc, dc.stopped)
		}
		_ = enc.Encode(doc)
		return code
	}
	fmt.Fprintf(w, "PROCTOR %s  (local, non-authoritative)\n\n", title)
	if r.Commit != "" {
		fmt.Fprintf(w, "Commit:          %s\n", safe(r.Commit))
	}
	fmt.Fprintf(w, "Configuration:   %s\n", safe(r.ConfigPath))
	if dc.outsideGit {
		fmt.Fprintf(w, "Note:            %s\n", outsideGitNote)
	}
	writeDrivers(w, dc.drivers)
	fmt.Fprintf(w, "Specifications:  %d   Scenarios: %d   Unlisted: %d\n", len(r.Specs), len(r.ScenarioIDs), len(r.Unlisted))
	if len(r.Findings)+len(errs) > 0 {
		fmt.Fprintln(w)
	}
	for _, f := range r.Findings {
		label := "INVALID  "
		if f.Kind == integrity.Violation {
			label = "INTEGRITY"
		}
		loc := f.Path
		if loc != "" {
			loc += ": "
		}
		fmt.Fprintf(w, "%s %-7s %s%s\n", label, safe(f.Rule), safe(loc), safe(f.Message))
	}
	for _, m := range errs {
		fmt.Fprintf(w, "%s %s\n", setupLabel(dc.setup.ExitCode), safe(m))
	}
	if dc.setup != nil {
		fmt.Fprintln(w, "\nThe drivers didn't check the steps, so the specifications are only partly validated.")
	}
	fmt.Fprintf(w, "\nRESULT: %s (exit %d, %s)\n", strings.ToUpper(result), code, localMode)
	return code
}

func setupLabel(code int) string {
	switch code {
	case exitIntegrity:
		return "INTEGRITY"
	case exitInvalid:
		return "INVALID  "
	}
	return "ERROR    "
}

func writeDrivers(w io.Writer, drivers []runner.DriverInfo) {
	if len(drivers) == 0 {
		return
	}
	fmt.Fprintln(w, "Drivers:")
	for _, d := range drivers {
		sum := d.SHA256
		if len(sum) > 16 {
			sum = sum[:16]
		}
		fmt.Fprintf(w, "  %-14s %s %s  %s  sha256 %s\n", safe(d.Name), safe(programOf(d)), safe(orDash(d.Version)), safe(d.Origin), safe(orDash(sum)))
		for _, p := range d.Proofs {
			line := fmt.Sprintf("  %-14s proof %s: %s", "", safe(p.Name), safe(p.Status))
			if p.Detail != "" {
				line += " (" + safe(p.Detail) + ")"
			}
			fmt.Fprintln(w, line)
		}
	}
}

func programOf(d runner.DriverInfo) string {
	if d.Program != "" {
		return d.Program
	}
	return "proctor-driver-" + d.Official
}

func writeRun(w io.Writer, rep *integrity.Report, out *runner.Outcome, mode string) {
	if mode != "authoritative" {
		fmt.Fprintf(w, "PROCTOR RUN  (%s)\n\n", mode)
	}
	fmt.Fprintf(w, "Target:          %s\n", safe(out.Target))
	fmt.Fprintf(w, "Configuration:   %s\n", safe(rep.ConfigPath))
	if out.Readiness != "" {
		fmt.Fprintf(w, "Readiness:       %s\n", safe(out.Readiness))
	}
	writeIdentityCheck(w, out.Identity)
	fmt.Fprintf(w, "Run ID:          %s\n", safe(out.RunID))
	if out.Received != "" {
		fmt.Fprintf(w, "Received:        %s\n", safe(out.Received))
	}
	if mode != "authoritative" {
		writeDrivers(w, out.Drivers)
	}
	fmt.Fprintf(w, "Specifications:  %d   Scenarios: %d\n\n", len(rep.Specs), out.Summary.Scenarios)
	for _, v := range out.Violations {
		fmt.Fprintf(w, "INTEGRITY %s\n", safe(v))
	}
	if len(out.Violations) > 0 && !anyStarted(out) {
		fmt.Fprintln(w, "No scenario was run.")
	}
	if ic := out.Identity; ic != nil && (ic.Result == runner.Fail || ic.Result == runner.Error) {
		for _, e := range ic.Identities {
			if e.Result == runner.Fail || e.Result == runner.Error {
				label := "INVALID "
				if e.Result == runner.Error {
					label = "ERROR   "
				}
				fmt.Fprintf(w, "%s identity check: identity %q: %s (%s)\n", label, safe(e.Identity), safe(e.Message), safe(e.Cause))
			}
		}
		if ic.Message != "" && len(ic.Identities) == 0 {
			fmt.Fprintf(w, "INVALID  identity check: %s\n", safe(ic.Message))
		}
		fmt.Fprintln(w, "No scenario was run (AUTH-04).")
	}
	printed := map[string]bool{}
	for _, sc := range out.Scenarios {
		if sc.Table != "" {
			key := sc.Spec + "\x00" + sc.Table
			if !printed[key] {
				printed[key] = true
				writeTable(w, out, sc.Spec, sc.Table)
			}
			continue
		}
		line := fmt.Sprintf("%-8s %-10s %s", sc.Result, safe(sc.ID), safe(sc.Name))
		if sc.Reason != "" {
			line += "  (" + safe(sc.Reason) + causeText(sc.Cause) + ")"
		} else if sc.Cause != "" && sc.Result == runner.Error {
			line += "  (" + safe(sc.Cause) + ")"
		}
		fmt.Fprintln(w, line+cleanupNote(sc))
		if sc.Message != "" {
			fmt.Fprintf(w, "           %s\n", safe(sc.Message))
		}
		if sc.Result != runner.Pass {
			writeSteps(w, sc)
		}
		writeTranscript(w, sc)
		writeApprovedResult(w, sc.ApprovedLog, "approved log")
		writeHandles(w, sc)
		writeAppLogs(w, sc.AppLogs)
	}
	writeSamplingNote(w, out)
	s := out.Summary
	if out.Aborted {
		fmt.Fprintln(w, "\n"+abortedNote)
	}
	fmt.Fprintf(w, "\nScenarios: %d   passed %d   failed %d   errors %d   not run %d   assertions %d\n",
		s.Scenarios, s.Passed, s.Failed, s.Errors, s.NotRun, s.Assertions)
	fmt.Fprintf(w, "RESULT: %s (exit %d, %s)\n", strings.ToUpper(out.Result), out.ExitCode, mode)
}

func anyStarted(out *runner.Outcome) bool {
	for _, sc := range out.Scenarios {
		if sc.Result != runner.NotRun {
			return true
		}
	}
	return false
}

const abortedNote = "ABORTED  the run was interrupted (SIGINT or SIGTERM); no scenario started after it"

func writeIdentityCheck(w io.Writer, ic *runner.IdentityCheck) {
	if ic == nil {
		return
	}
	line := fmt.Sprintf("GET %s via %s, user_id %s: %s", safe(ic.Path), safe(ic.Driver), safe(ic.UserID), ic.Result)
	if ic.Reason != "" {
		line += " (" + safe(ic.Reason) + ")"
	}
	var parts []string
	for _, e := range ic.Identities {
		p := safe(e.Identity) + " " + string(e.Result)
		if e.Result == runner.NotRun && e.Cause != "" {
			p += " " + safe(e.Cause)
		}
		parts = append(parts, p)
	}
	if len(parts) > 0 {
		line += "; " + strings.Join(parts, ", ")
	}
	fmt.Fprintf(w, "Identity check:  %s\n", line)
}

func writeSamplingNote(w io.Writer, out *runner.Outcome) {
	n := 0
	for _, sc := range out.Scenarios {
		for _, st := range sc.Steps {
			if st.Notice != "" {
				n++
			}
		}
	}
	if n == 0 {
		return
	}
	fmt.Fprintf(w, "\nNote: %d consistently/never check%s passed on samples; a change between samples, or after a window, can't be seen (TIME-05)\n", n, plural(n))
}

func cleanupNote(sc runner.ScenarioResult) string {
	if sc.Cleanup == "" || sc.Cleanup == runner.Pass || sc.Reason == runner.ReasonCleanupFailed {
		return ""
	}
	return fmt.Sprintf("  [cleanup %s at %s, reported apart]", sc.Cleanup, safe(sc.CleanupFailure))
}

func stepLabel(st runner.StepResult) string {
	label := safe(st.ID)
	switch st.Phase {
	case runner.PhaseSetup:
		label = "setup " + label
	case runner.PhaseCleanup:
		label = "cleanup " + label
	}
	switch {
	case st.Case == "":
	case st.Phase == runner.PhaseVerification && st.ID == "case":
		label = "case " + safe(st.Case)
	default:
		label = "case " + safe(st.Case) + " " + label
	}
	return label
}

func writeFirstFailure(w io.Writer, sc runner.ScenarioResult) {
	if sc.Result != runner.Fail || sc.FirstFailure == "" {
		return
	}
	for _, st := range sc.Steps {
		if st.Phase == runner.PhaseVerification && st.Result == runner.Fail && st.ID == sc.FirstFailure {
			fmt.Fprintf(w, "           cause: %s failed first\n", stepLabel(st))
			return
		}
	}
}

func writeSteps(w io.Writer, sc runner.ScenarioResult) {
	writeFirstFailure(w, sc)
	inCleanup := false
	for _, st := range sc.Steps {
		if st.Phase == runner.PhaseCleanup && !inCleanup {
			inCleanup = true
			fmt.Fprintf(w, "           cleanup %s  (run after the steps and reported apart; it turns only a PASS into ERROR cleanup_failed)\n", sc.Cleanup)
		}
		label := stepLabel(st)
		act := actionText(st)
		stepLine := fmt.Sprintf("           %-8s %s%s", st.Result, label, act)
		if st.Reason != "" {
			stepLine += "  (" + safe(st.Reason) + causeText(st.Cause) + ")"
		} else if st.Cause != "" && st.Result == runner.Error {
			stepLine += "  (" + safe(st.Cause) + ")"
		}
		if st.AfterSkippedAct {
			stepLine += "  (after a skipped act)"
		}
		fmt.Fprintln(w, stepLine)
		if st.Message != "" {
			fmt.Fprintf(w, "                    %s\n", safe(st.Message))
		}
		for _, a := range st.Assertions {
			if a.Result == runner.Pass {
				continue
			}
			label := strings.TrimSpace(a.Kind + " " + a.Target)
			if a.Claim != nil {
				label += fmt.Sprintf(" claim[%d]", *a.Claim)
			}
			fmt.Fprintf(w, "                    %s %s\n", safe(label)+":", safe(describe(a)))
			if a.Response != "" {
				fmt.Fprintf(w, "                    response: %s\n", safe(a.Response))
			}
		}
	}
}

func writeAppLogs(w io.Writer, al *runner.AppLogResult) {
	if al == nil || (al.Result == runner.Pass && al.Note == "") {
		return
	}
	line := fmt.Sprintf("           %-8s %s", al.Result, runner.AppLogCheck)
	if al.Cause != "" {
		line += "  (" + safe(al.Cause) + ")"
	}
	if al.Message != "" {
		line += "  " + safe(al.Message)
	}
	fmt.Fprintln(w, line)
	if al.Note != "" {
		fmt.Fprintf(w, "                    note: %s\n", safe(al.Note))
	}
	if al.FirstUnparsed != "" {
		fmt.Fprintf(w, "                    first_unparsed: %s\n", safe(al.FirstUnparsed))
	}
	if al.FirstUnmapped != "" {
		fmt.Fprintf(w, "                    first_unmapped (level %s): %s\n", safe(al.UnmappedLevel), safe(al.FirstUnmapped))
	}
	for _, l := range al.Unexpected {
		at := ""
		if l.Time != "" {
			at = " at " + safe(l.Time)
		}
		fmt.Fprintf(w, "                    unexpected %s %s%s: %s\n", safe(l.Level), safe(show(l.Message)), at, safe(l.Line))
	}
	for _, e := range al.Expected {
		if e.Result == runner.Pass {
			continue
		}
		count := ""
		if e.Count != nil {
			count = fmt.Sprintf(" (count %d)", *e.Count)
		}
		fmt.Fprintf(w, "                    expect_logs %s %s%s: %s\n", safe(e.Level), safe(show(e.Message)), count, safe(e.Problem))
	}
}

func writeTranscript(w io.Writer, sc runner.ScenarioResult) {
	label := "case " + sc.ID + " transcript"
	if sc.Table == "" {
		label = "transcript"
	}
	writeApprovedResult(w, sc.Transcript, label)
}

func writeApprovedResult(w io.Writer, tr *runner.TranscriptResult, label string) {
	if tr == nil || tr.Result == runner.TranscriptApproved {
		return
	}
	fmt.Fprintf(w, "           %-8s %s  %s: %s\n", tr.Result, safe(label), safe(tr.ApprovedPath), safe(tr.Message))
	if tr.Received != "" {
		fmt.Fprintf(w, "                    received: %s (sha256 %s)\n", safe(tr.Received), safe(tr.ReceivedSHA256))
	}
	for _, l := range tr.Diff {
		fmt.Fprintf(w, "                    %s\n", safe(l))
	}
	if tr.DiffMore > 0 {
		fmt.Fprintf(w, "                    ... %d more diff lines; proctor approve shows them all\n", tr.DiffMore)
	}
}

func writeHandles(w io.Writer, sc runner.ScenarioResult) {
	for _, h := range sc.Handles {
		if h.Close == runner.HandleClosed {
			continue
		}
		name := h.Name
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(w, "           close %s (driver %s, opened by %s): %s: %s\n", safe(name), safe(h.Driver), safe(h.OpenedBy), safe(h.Close), safe(h.Message))
	}
}

func writeTable(w io.Writer, out *runner.Outcome, spec, id string) {
	var t *runner.TableResult
	for i := range out.Tables {
		if out.Tables[i].ID == id && out.Tables[i].Spec == spec {
			t = &out.Tables[i]
		}
	}
	if t == nil {
		return
	}
	line := fmt.Sprintf("%-8s %-10s %s  (table", t.Result, safe(t.ID), safe(t.Name))
	if t.SetupFrom != "" {
		line += ", setup from " + safe(t.SetupFrom)
	}
	if t.Reason != "" {
		line += ", " + safe(t.Reason)
	}
	fmt.Fprintln(w, line+")")
	columns := safeAll(t.Columns)
	rows := make([][]string, len(t.Rows))
	widths := make([]int, len(columns))
	for i, c := range columns {
		widths[i] = utf8.RuneCountInString(c)
	}
	for j, r := range t.Rows {
		rows[j] = safeAll(r.Cells)
		for i, c := range rows[j] {
			if n := utf8.RuneCountInString(c); i < len(widths) && n > widths[i] {
				widths[i] = n
			}
		}
	}
	cells := func(vals []string) string {
		var b strings.Builder
		for i, v := range vals {
			if i < len(widths) {
				b.WriteString("  " + v + strings.Repeat(" ", widths[i]-utf8.RuneCountInString(v)))
			}
		}
		return strings.TrimRight(b.String(), " ")
	}
	fmt.Fprintf(w, "           %-8s%s\n", "", cells(columns))
	for j, r := range t.Rows {
		fmt.Fprintf(w, "           %-8s%s\n", r.Result, cells(rows[j]))
	}
	for _, sc := range out.Scenarios {
		if sc.Table != id || sc.Spec != spec {
			continue
		}
		failedSetup := false
		for _, st := range sc.Steps {
			if st.Phase == runner.PhaseSetup && st.Result != runner.Pass {
				failedSetup = true
			}
		}
		cleanOK := sc.Cleanup == "" || sc.Cleanup == runner.Pass
		if sc.Result == runner.Pass || (sc.Reason == runner.ReasonSetupFailed && !failedSetup && cleanOK) {
			continue
		}
		writeSteps(w, sc)
		writeTranscript(w, sc)
	}
	writeApprovedResult(w, t.ApprovedLog, "approved log")
	writeAppLogs(w, t.AppLogs)
}

func causeText(c string) string {
	if c == "" {
		return ""
	}
	return ", cause " + safe(c)
}

func actionText(st runner.StepResult) string {
	a := st.Action
	if a == nil {
		if st.Driver == "" {
			return ""
		}
		return "  " + safe(st.Driver)
	}
	t := "  " + safe(a.Driver+"."+a.Op)
	switch {
	case a.Identity != "" && a.Variant != "":
		t += " [" + safe(a.Identity) + ", " + safe(a.Variant) + " credential]"
	case a.Identity != "":
		t += " [" + safe(a.Identity) + "]"
	}
	if st.Origin == runner.OriginUnpinned {
		t += " (unpinned driver)"
	}
	if a.Summary != "" {
		t += ": " + safe(a.Summary)
	}
	if st.Samples > 0 {
		t += fmt.Sprintf(" [%d sample%s]", st.Samples, plural(st.Samples))
	}
	return t
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

const excludedContext = 80

func describe(a runner.Assertion) string {
	msg := a.Message
	switch {
	case a.CutAt > 0 && a.Expected == nil:
		return msg
	case a.CutAt > 0, a.Overflow:
		if a.Expected == nil {
			return msg
		}
		return fmt.Sprintf("%s: expected %s", msg, show(a.Expected))
	case a.Present && a.Kind == "excludes" && a.Result == runner.Fail && isText(a.Actual):
		want, _ := a.Expected.(string)
		text := a.Actual.(string)
		if at := strings.Index(text, want); want != "" && at >= 0 {
			return fmt.Sprintf("%s: %s, in %s", msg, show(a.Expected), show(around(text, at, len(want))))
		}
		return fmt.Sprintf("%s: %s", msg, show(a.Expected))
	case a.Present && a.Kind == "differs" && a.Result == runner.Fail:
		return fmt.Sprintf("%s: got %s", msg, show(a.Actual))
	case !a.Present && (a.Kind == "differs" || a.Kind == "excludes"):
		return msg
	case !a.Present && a.Expected != nil:
		return fmt.Sprintf("expected %s, but %s", show(a.Expected), msg)
	case !a.Present:
		return msg
	case (a.Kind == "keys" || a.Kind == "length") && !strings.HasPrefix(msg, "not "):
		return msg
	case a.Expected == nil || strings.HasPrefix(msg, "not "):
		return fmt.Sprintf("%s; got %s", msg, show(a.Actual))
	}
	return fmt.Sprintf("%s: expected %s, got %s", msg, show(a.Expected), show(a.Actual))
}

func isText(v any) bool {
	_, ok := v.(string)
	return ok
}

func around(text string, at, n int) string {
	from, to := max(at-excludedContext, 0), min(at+n+excludedContext, len(text))
	for from > 0 && !utf8.RuneStart(text[from]) {
		from--
	}
	for to < len(text) && !utf8.RuneStart(text[to]) {
		to++
	}
	out := text[from:to]
	if from > 0 {
		out = "..." + out
	}
	if to < len(text) {
		out += "..."
	}
	return out
}

func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
