package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"proctor/internal/runner"
)

const maxReportBytes = 1 << 30

type reportOptions struct {
	report string
	json   bool
}

func newReportCmd(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Read a saved JSON report: one scenario's steps, or the list of what didn't pass",
	}
	cmd.AddCommand(newReportShowCmd(stdout, stderr), newReportFailedCmd(stdout, stderr))
	return cmd
}

func addReportFlags(cmd *cobra.Command, o *reportOptions) {
	f := cmd.Flags()
	f.StringVar(&o.report, "report", "", "the JSON report proctor run wrote with --report or --json (required)")
	f.BoolVar(&o.json, "json", false, "write machine-readable JSON")
}

func newReportShowCmd(stdout, stderr io.Writer) *cobra.Command {
	var o reportOptions
	cmd := &cobra.Command{
		Use:   "show <id>...",
		Short: "Print each named scenario, table or table case from a saved report, with its steps, expected and actual values, as a run's terminal output does",
		Example: `  proctor report show USR-001 --report /tmp/report.json
  proctor report show u5_fail U5-TABLE --report /tmp/report.json`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitWith(reportShow(o, args, stdout, stderr))
		},
	}
	addReportFlags(cmd, &o)
	return cmd
}

func newReportFailedCmd(stdout, stderr io.Writer) *cobra.Command {
	var o reportOptions
	cmd := &cobra.Command{
		Use:     "failed",
		Short:   "List every scenario and table case in a saved report that didn't pass, with its result and reason",
		Example: `  proctor report failed --report /tmp/report.json`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitWith(reportFailed(o, stdout, stderr))
		},
	}
	addReportFlags(cmd, &o)
	return cmd
}

func readReport(path string) (*runner.Outcome, error) {
	if path == "" {
		return nil, fmt.Errorf("--report is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxReportBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxReportBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxReportBytes)
	}
	var doc struct {
		runner.Outcome
		Scenarios *[]runner.ScenarioResult `json:"scenarios"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s isn't a Proctor report: %v", path, err)
	}
	if doc.Scenarios == nil {
		return nil, fmt.Errorf("%s isn't a Proctor run report: it has no scenarios", path)
	}
	out := doc.Outcome
	out.Scenarios = *doc.Scenarios
	return &out, nil
}

func reportShow(o reportOptions, ids []string, stdout, stderr io.Writer) int {
	out, err := readReport(o.report)
	if err != nil {
		fmt.Fprintf(stderr, "proctor report show: %s\n", safe(err.Error()))
		return exitInvalid
	}
	var picked []runner.ScenarioResult
	for _, id := range ids {
		found := false
		for _, sc := range out.Scenarios {
			if sc.ID == id || sc.Table == id {
				picked = append(picked, sc)
				found = true
			}
		}
		if !found {
			fmt.Fprintf(stderr, "proctor report show: %s has no scenario, table or table case %q\n", safe(o.report), safe(id))
			return exitInvalid
		}
	}
	if o.json {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"report": o.report, "scenarios": picked})
		return exitOK
	}
	for i, sc := range picked {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		writeScenarioDetail(stdout, sc)
	}
	return exitOK
}

func writeScenarioDetail(w io.Writer, sc runner.ScenarioResult) {
	id := safe(sc.ID)
	if sc.Table != "" {
		id = "table " + safe(sc.Table) + " case " + id
	}
	line := fmt.Sprintf("%-8s %s  %s  (%s)", sc.Result, id, safe(sc.Name), safe(sc.Spec))
	if sc.Reason != "" {
		line += "  (" + safe(sc.Reason) + causeText(sc.Cause) + ")"
	}
	fmt.Fprintln(w, line+cleanupNote(sc))
	if sc.Message != "" {
		fmt.Fprintf(w, "           %s\n", safe(sc.Message))
	}
	writeSteps(w, sc)
	writeTranscript(w, sc)
	writeApprovedResult(w, sc.ApprovedLog, "approved log")
	writeHandles(w, sc)
	writeAppLogs(w, sc.AppLogs)
}

func reportFailed(o reportOptions, stdout, stderr io.Writer) int {
	out, err := readReport(o.report)
	if err != nil {
		fmt.Fprintf(stderr, "proctor report failed: %s\n", safe(err.Error()))
		return exitInvalid
	}
	var failed []runner.ScenarioResult
	for _, sc := range out.Scenarios {
		if sc.Result != runner.Pass {
			failed = append(failed, sc)
		}
	}
	if o.json {
		type entry struct {
			ID     string        `json:"id"`
			Table  string        `json:"table,omitempty"`
			Spec   string        `json:"specification"`
			Result runner.Result `json:"result"`
			Reason string        `json:"reason,omitempty"`
		}
		list := []entry{}
		for _, sc := range failed {
			list = append(list, entry{ID: sc.ID, Table: sc.Table, Spec: sc.Spec, Result: sc.Result, Reason: sc.Reason})
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"report": o.report, "result": out.Result, "scenarios": len(out.Scenarios), "not_passed": list})
		return exitOK
	}
	for _, sc := range failed {
		id := safe(sc.ID)
		if sc.Table != "" {
			id = "table " + safe(sc.Table) + " case " + id
		}
		line := fmt.Sprintf("%-8s %s  %s", sc.Result, id, safe(sc.Spec))
		if sc.Reason != "" {
			line += "  (" + safe(sc.Reason) + causeText(sc.Cause) + ")"
		}
		fmt.Fprintln(stdout, line)
	}
	fmt.Fprintf(stdout, "\n%d of %d didn't pass; the run's result was %s. Read one with proctor report show <id> --report %s\n", len(failed), len(out.Scenarios), safe(out.Result), safe(o.report))
	return exitOK
}
