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
	case 1:
		return "failed"
	case exitError:
		return "error"
	case exitInvalid:
		return "invalid"
	default:
		return "integrity_violation"
	}
}

func write(w io.Writer, asJSON bool, title string, r *integrity.Report, code int, result string) int {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"result": result, "exit_code": code, "mode": "local", "report": r})
		return code
	}
	fmt.Fprintf(w, "PROCTOR %s  (local, non-authoritative)\n\n", title)
	if r.Commit != "" {
		fmt.Fprintf(w, "Commit:          %s\n", safe(r.Commit))
	}
	fmt.Fprintf(w, "Configuration:   %s\n", safe(r.ConfigPath))
	fmt.Fprintf(w, "Specifications:  %d   Scenarios: %d   Unlisted: %d\n", len(r.Specs), len(r.ScenarioIDs), len(r.Unlisted))
	for _, u := range r.Unlisted {
		fmt.Fprintf(w, "  unlisted (not executed): %s\n", safe(u))
	}
	if len(r.Findings) > 0 {
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
	fmt.Fprintf(w, "\nRESULT: %s (exit %d)\n", strings.ToUpper(result), code)
	return code
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
	fmt.Fprintf(w, "Run ID:          %s\n", safe(out.RunID))
	fmt.Fprintf(w, "Specifications:  %d   Scenarios: %d\n\n", len(rep.Specs), out.Summary.Scenarios)
	for _, v := range out.Violations {
		fmt.Fprintf(w, "INTEGRITY %s\n", safe(v))
	}
	if len(out.Violations) > 0 {
		fmt.Fprintln(w, "No scenario was run.")
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
		fmt.Fprintln(w, line)
		if sc.Result != runner.Pass {
			writeSteps(w, sc)
		}
	}
	s := out.Summary
	fmt.Fprintf(w, "\nScenarios: %d   passed %d   failed %d   errors %d   not run %d   assertions %d\n",
		s.Scenarios, s.Passed, s.Failed, s.Errors, s.NotRun, s.Assertions)
	fmt.Fprintf(w, "RESULT: %s (exit %d, %s)\n", strings.ToUpper(out.Result), out.ExitCode, mode)
}

func writeSteps(w io.Writer, sc runner.ScenarioResult) {
	for _, st := range sc.Steps {
		label := safe(st.ID)
		if st.Phase == "setup" {
			label = "setup " + label
		}
		req := ""
		if st.Request != nil {
			req = "  " + safe(st.Request.Method) + " " + safe(st.Request.URL)
			if st.Request.Credential != "" {
				req += " [" + safe(st.Request.Identity) + ", " + safe(st.Request.Credential) + " credential]"
			}
			if st.Status != 0 {
				req += fmt.Sprintf(" -> %d", st.Status)
			}
		}
		if st.Check != nil {
			req = "  postgres " + safe(st.Check.Connection)
		}
		stepLine := fmt.Sprintf("           %-8s %s%s", st.Result, label, req)
		if st.Reason != "" {
			stepLine += "  (" + safe(st.Reason) + causeText(st.Cause) + ")"
		} else if st.Cause != "" && st.Result == runner.Error {
			stepLine += "  (" + safe(st.Cause) + ")"
		}
		fmt.Fprintln(w, stepLine)
		if st.Message != "" {
			fmt.Fprintf(w, "                    %s\n", safe(st.Message))
		}
		for _, a := range st.Assertions {
			if a.Result == runner.Pass {
				continue
			}
			fmt.Fprintf(w, "                    %s %s\n", safe(strings.TrimSpace(a.Kind+" "+a.Target))+":", safe(describe(a)))
		}
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
			if st.Phase == "setup" && st.Result != runner.Pass {
				failedSetup = true
			}
		}
		if sc.Result == runner.Pass || (sc.Reason == runner.ReasonSetupFailed && !failedSetup) {
			continue
		}
		fmt.Fprintf(w, "           case %s:\n", safe(sc.ID))
		writeSteps(w, sc)
	}
}

func causeText(c string) string {
	if c == "" {
		return ""
	}
	return ", cause " + safe(c)
}

func describe(a runner.Assertion) string {
	switch a.Kind {
	case "status", "body", "rows":
		return a.Message
	case "absent":
		return fmt.Sprintf("%s: got %s", a.Message, show(a.Actual))
	case "present":
		return a.Message
	}
	if a.Message != "" && a.Expected == nil {
		if a.Present {
			return fmt.Sprintf("%s; got %s", a.Message, show(a.Actual))
		}
		return a.Message
	}
	if !a.Present {
		return fmt.Sprintf("expected %s, but %s", show(a.Expected), a.Message)
	}
	return fmt.Sprintf("expected %s, got %s", show(a.Expected), show(a.Actual))
}

func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
