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
	drivers []runner.DriverInfo
	setup   *runner.SetupError
}

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
		_ = enc.Encode(doc)
		return code
	}
	fmt.Fprintf(w, "PROCTOR %s  (local, non-authoritative)\n\n", title)
	if r.Commit != "" {
		fmt.Fprintf(w, "Commit:          %s\n", safe(r.Commit))
	}
	fmt.Fprintf(w, "Configuration:   %s\n", safe(r.ConfigPath))
	writeDrivers(w, dc.drivers)
	fmt.Fprintf(w, "Specifications:  %d   Scenarios: %d   Unlisted: %d\n", len(r.Specs), len(r.ScenarioIDs), len(r.Unlisted))
	for _, u := range r.Unlisted {
		fmt.Fprintf(w, "  unlisted (not executed): %s\n", safe(u))
	}
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
	fmt.Fprintf(w, "\nRESULT: %s (exit %d)\n", strings.ToUpper(result), code)
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
		fmt.Fprintf(w, "  %-14s proctor-driver-%s %s  %s  sha256 %s\n", safe(d.Name), safe(d.Official), safe(orDash(d.Version)), safe(d.Origin), safe(orDash(sum)))
		for _, p := range d.Proofs {
			line := fmt.Sprintf("  %-14s proof %s: %s", "", safe(p.Name), safe(p.Status))
			if p.Detail != "" {
				line += " (" + safe(p.Detail) + ")"
			}
			fmt.Fprintln(w, line)
		}
	}
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
	if mode != "authoritative" {
		writeDrivers(w, out.Drivers)
	}
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
		writeHandles(w, sc)
	}
	s := out.Summary
	fmt.Fprintf(w, "\nScenarios: %d   passed %d   failed %d   errors %d   not run %d   assertions %d\n",
		s.Scenarios, s.Passed, s.Failed, s.Errors, s.NotRun, s.Assertions)
	fmt.Fprintf(w, "RESULT: %s (exit %d, %s)\n", strings.ToUpper(out.Result), out.ExitCode, mode)
}

func writeSteps(w io.Writer, sc runner.ScenarioResult) {
	for _, st := range sc.Steps {
		label := safe(st.ID)
		if st.Phase == runner.PhaseSetup {
			label = "setup " + label
		}
		switch {
		case st.Case == "":
		case st.Phase != runner.PhaseSetup && st.ID == "case":
			label = "case " + safe(st.Case)
		default:
			label = "case " + safe(st.Case) + " " + label
		}
		act := actionText(st)
		stepLine := fmt.Sprintf("           %-8s %s%s", st.Result, label, act)
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
			label := strings.TrimSpace(a.Kind + " " + a.Target)
			if a.Claim != nil {
				label += fmt.Sprintf(" claim[%d]", *a.Claim)
			}
			fmt.Fprintf(w, "                    %s %s\n", safe(label)+":", safe(describe(a)))
		}
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
		if sc.Result == runner.Pass || (sc.Reason == runner.ReasonSetupFailed && !failedSetup) {
			continue
		}
		writeSteps(w, sc)
	}
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

func describe(a runner.Assertion) string {
	msg := a.Message
	switch {
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

func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
