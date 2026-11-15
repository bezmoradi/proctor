package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"proctor/internal/gitrepo"
	"proctor/internal/integrity"
	"proctor/internal/model"
	"proctor/internal/protdiff"
	"proctor/internal/runner"
	"proctor/internal/strictyaml"
)

var Version = "0.0.0-phase0"

const (
	exitOK        = 0
	exitInvalid   = 3
	exitIntegrity = 4
)

const defaultConfig = "proctor/proctor.yaml"

func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitInvalid
	}
	switch args[0] {
	case "validate":
		return validate(args[1:], stdout, stderr)
	case "integrity":
		return checkIntegrity(args[1:], stdout, stderr)
	case "diff":
		return diff(args[1:], stdout, stderr)
	case "run":
		return run(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, Version)
		return exitOK
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "proctor: unknown command %q\n", args[0])
		usage(stderr)
		return exitInvalid
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  proctor validate  [--repo DIR] [--config PATH] [--commit REV] [--json]
  proctor integrity [--repo DIR] [--config PATH] [--commit REV] [--json]
  proctor diff      [--repo DIR] [--config PATH] --base REV --head REV [--json]
  proctor diff      [--repo DIR] [--config PATH] --head TEST_MERGE --pr-head REV [--json]
  proctor run       [--repo DIR] [--config PATH] --binding FILE [--request-timeout 10s] [--query-timeout 5s] [--json]
  proctor run       --authoritative [--repo DIR] [--config PATH] --binding FILE --report FILE
                    (--pr-head REV | --base REV) [--commit REV] [--request-timeout 10s] [--query-timeout 5s] [--json]
  proctor version

Exit codes: 0 passed, 1 failed, 2 error, 3 invalid, 4 integrity violation.
`)
}

type common struct {
	repo, config string
	json         bool
}

func newFlags(name string, stderr io.Writer, c *common) *flag.FlagSet {
	fs := flag.NewFlagSet("proctor "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.repo, "repo", ".", "repository directory")
	fs.StringVar(&c.config, "config", defaultConfig, "path of proctor.yaml inside the repository")
	fs.BoolVar(&c.json, "json", false, "write machine-readable JSON")
	return fs
}

func validate(args []string, stdout, stderr io.Writer) int {
	var c common
	var commit string
	fs := newFlags("validate", stderr, &c)
	fs.StringVar(&commit, "commit", "", "read files from this commit instead of the working tree")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		return exitInvalid
	}
	repo, err := gitrepo.Open(c.repo)
	if err != nil {
		fmt.Fprintf(stderr, "proctor: %v\n", err)
		return exitInvalid
	}
	var src integrity.Source = integrity.WorkTreeSource{Root: repo.Dir()}
	if commit != "" {
		cs, err := integrity.NewCommitSource(repo, commit)
		if err != nil {
			fmt.Fprintf(stderr, "proctor: %v\n", err)
			return exitInvalid
		}
		src = cs
	}
	r := integrity.Load(src, c.config)
	code := exitOK
	if r.Has(integrity.Invalid) {
		code = exitInvalid
	}

	return write(stdout, c.json, "VALIDATE", r, code, resultName(code))
}

func checkIntegrity(args []string, stdout, stderr io.Writer) int {
	var c common
	var commit string
	fs := newFlags("integrity", stderr, &c)
	fs.StringVar(&commit, "commit", "HEAD", "commit to check")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		return exitInvalid
	}
	repo, err := gitrepo.Open(c.repo)
	if err != nil {
		fmt.Fprintf(stderr, "proctor: %v\n", err)
		return exitInvalid
	}
	r := integrity.CheckCommit(repo, commit, c.config)
	code := r.ExitCode()
	return write(stdout, c.json, "INTEGRITY", r, code, resultName(code))
}

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
		fmt.Fprintf(w, "Commit:          %s\n", r.Commit)
	}
	fmt.Fprintf(w, "Configuration:   %s\n", r.ConfigPath)
	fmt.Fprintf(w, "Specifications:  %d   Scenarios: %d   Unlisted: %d\n", len(r.Specs), len(r.ScenarioIDs), len(r.Unlisted))
	for _, u := range r.Unlisted {
		fmt.Fprintf(w, "  unlisted (not executed): %s\n", u)
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
		fmt.Fprintf(w, "%s %-7s %s%s\n", label, f.Rule, loc, f.Message)
	}
	fmt.Fprintf(w, "\nRESULT: %s (exit %d)\n", strings.ToUpper(result), code)
	return code
}

func diff(args []string, stdout, stderr io.Writer) int {
	var c common
	var base, head, prHead string
	fs := newFlags("diff", stderr, &c)
	fs.StringVar(&base, "base", "", "base commit")
	fs.StringVar(&head, "head", "", "head commit, or the test merge commit when --pr-head is given")
	fs.StringVar(&prHead, "pr-head", "", "pull request head; makes --head a test merge whose first parent is the base")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		return exitInvalid
	}
	if head == "" || (base == "") == (prHead == "") {
		fmt.Fprintln(stderr, "proctor diff: give --head and exactly one of --base or --pr-head")
		return exitInvalid
	}
	repo, err := gitrepo.Open(c.repo)
	if err != nil {
		fmt.Fprintf(stderr, "proctor: %v\n", err)
		return exitInvalid
	}
	if prHead != "" {
		base, err = protdiff.BaseForTestMerge(repo, head, prHead)
		if err != nil {
			return diffError(stdout, stderr, c.json, err)
		}
	}
	res, err := protdiff.Compute(repo, base, head, c.config)
	if err != nil {
		return diffError(stdout, stderr, c.json, err)
	}
	if c.json {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"result": "passed", "exit_code": exitOK, "protected_changes": res.Count(), "weakenings": len(res.Weakenings), "diff": res})
		return exitOK
	}
	fmt.Fprintf(stdout, "PROCTOR DIFF\n\nBase: %s\nHead: %s\n\n", res.Base, res.Head)
	if res.Count() == 0 {
		fmt.Fprintln(stdout, "Protected changes against base: none")
		return exitOK
	}
	for _, f := range res.Files {
		line := fmt.Sprintf("  %-9s %s", f.Change, f.Path)
		if f.From != "" {
			line += " (from " + f.From + ")"
		}
		if f.OldMode != "" || f.NewMode != "" {
			line += fmt.Sprintf(" [mode %s -> %s]", orDash(f.OldMode), orDash(f.NewMode))
		}
		fmt.Fprintln(stdout, line+"  approval required")
	}
	if len(res.Scenarios) > 0 {
		fmt.Fprintln(stdout, "\nScenarios:")
		for _, s := range res.Scenarios {
			line := fmt.Sprintf("  %-9s %s  %s", s.Change, s.ID, s.File)
			if s.FromFile != "" {
				line += " (from " + s.FromFile + ")"
				if s.Modified {
					line += ", modified"
				}
			}
			fmt.Fprintln(stdout, line)
		}
	}
	if len(res.Weakenings) > 0 {
		fmt.Fprintln(stdout, "\nWeakenings (review these first):")
		for _, wk := range res.Weakenings {
			loc := wk.Scenario
			if wk.Step != "" {
				loc += " " + wk.Step
			}
			line := fmt.Sprintf("  %-20s %s: %s", wk.Kind, loc, wk.Detail)
			if wk.AssertionsLost > 0 {
				line += fmt.Sprintf(" (%d assertion(s) lost)", wk.AssertionsLost)
			}
			fmt.Fprintln(stdout, line)
		}
	}
	if len(res.Expectations) > 0 {
		fmt.Fprintln(stdout, "\nChanged requests and expectations:")
		for _, e := range res.Expectations {
			fmt.Fprintf(stdout, "  %s %s %s: %s -> %s\n", e.Scenario, e.Step, e.What, e.From, e.To)
		}
	}
	for _, u := range res.Unparsed {
		fmt.Fprintf(stdout, "  scenarios not compared (file doesn't parse on one side): %s\n", u)
	}
	fmt.Fprintf(stdout, "\nProtected changes against base: %d (approval required)\n", res.Count())
	return exitOK
}

func diffError(stdout, stderr io.Writer, asJSON bool, err error) int {
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"result": "integrity_violation", "exit_code": exitIntegrity, "error": err.Error()})
	} else {
		fmt.Fprintf(stderr, "proctor diff: integrity violation (INT-08): %v\n", err)
	}
	return exitIntegrity
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func run(args []string, stdout, stderr io.Writer) int {
	var c common
	var bindingPath, commit, base, prHead, report string
	var authoritative bool
	var timeout, qtimeout time.Duration
	fs := newFlags("run", stderr, &c)
	fs.StringVar(&bindingPath, "binding", "", "environment binding file (required)")
	fs.DurationVar(&timeout, "request-timeout", 10*time.Second, "time limit for each request")
	fs.DurationVar(&qtimeout, "query-timeout", 5*time.Second, "time limit for each database query")
	fs.BoolVar(&authoritative, "authoritative", false, "check a commit for the trusted CI job (§5.2)")
	fs.StringVar(&commit, "commit", "HEAD", "with --authoritative: the tested commit")
	fs.StringVar(&base, "base", "", "with --authoritative: the base commit, when the pull request head is tested directly")
	fs.StringVar(&prHead, "pr-head", "", "with --authoritative: the pull request head, when the tested commit is GitHub's test merge")
	fs.StringVar(&report, "report", "", "with --authoritative: where to write the JSON report (required)")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		return exitInvalid
	}
	if timeout <= 0 || qtimeout <= 0 {
		fmt.Fprintln(stderr, "proctor run: --request-timeout and --query-timeout must be positive")
		return exitInvalid
	}
	if authoritative {
		return runAuthoritative(authOptions{c: c, binding: bindingPath, commit: commit, base: base, prHead: prHead, report: report, timeout: timeout, qtimeout: qtimeout}, stdout, stderr)
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	for _, name := range []string{"commit", "base", "pr-head", "report"} {
		if set[name] {
			fmt.Fprintf(stderr, "proctor run: --%s is only for --authoritative\n", name)
			return exitInvalid
		}
	}
	if bindingPath == "" {
		fmt.Fprintln(stderr, "proctor run: --binding is required in local mode")
		return exitInvalid
	}
	repo, err := gitrepo.Open(c.repo)
	if err != nil {
		fmt.Fprintf(stderr, "proctor: %v\n", err)
		return exitInvalid
	}
	rep := integrity.Load(integrity.WorkTreeSource{Root: repo.Dir()}, c.config)
	if code := rep.ExitCode(); code != exitOK {

		return write(stdout, c.json, "RUN", rep, code, resultName(code))
	}

	data, err := os.ReadFile(bindingPath)
	if err != nil {
		return configError(stdout, stderr, c.json, rep, []string{fmt.Sprintf("binding: %v", err)})
	}
	var b model.Binding
	if err := strictyaml.Decode(data, &b); err != nil {
		return configError(stdout, stderr, c.json, rep, []string{fmt.Sprintf("binding %s: %v", bindingPath, err)})
	}
	rn, err := runner.New(rep, &b, runner.Options{RequestTimeout: timeout, QueryTimeout: qtimeout, LookupEnv: os.LookupEnv})
	if err != nil {
		msgs := []string{err.Error()}
		var ce *runner.ConfigError
		if errors.As(err, &ce) {
			msgs = ce.Msgs
		}
		return configError(stdout, stderr, c.json, rep, msgs)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	out := rn.Run(ctx)
	if c.json {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{
			"result":    out.Result,
			"exit_code": out.ExitCode,
			"mode":      "local",
			"trust":     map[string]any{"authoritative": false, "reason": "local mode reads the working tree; the result is not evidence for a commit"},
			"provenance": map[string]any{
				"proctor_version": Version,
				"config":          rep.ConfigPath,
				"sha256":          rep.Hashes,
			},
			"run_id":     out.RunID,
			"target":     out.Target,
			"readiness":  out.Readiness,
			"violations": out.Violations,
			"summary":    out.Summary,
			"scenarios":  out.Scenarios,
		})
		return out.ExitCode
	}
	writeRun(stdout, rep, out, "local, non-authoritative")
	return out.ExitCode
}

func configError(stdout, stderr io.Writer, asJSON bool, rep *integrity.Report, msgs []string) int {
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"result": "invalid", "exit_code": exitInvalid, "mode": "local", "errors": msgs})
		return exitInvalid
	}
	fmt.Fprintf(stdout, "PROCTOR RUN  (local, non-authoritative)\n\nConfiguration:   %s\n\n", rep.ConfigPath)
	for _, m := range msgs {
		fmt.Fprintf(stdout, "INVALID   %s\n", m)
	}
	fmt.Fprintf(stdout, "\nNothing was sent to the application.\nRESULT: INVALID (exit %d)\n", exitInvalid)
	return exitInvalid
}

func writeRun(w io.Writer, rep *integrity.Report, out *runner.Outcome, mode string) {
	if mode != "authoritative" {
		fmt.Fprintf(w, "PROCTOR RUN  (%s)\n\n", mode)
	}
	fmt.Fprintf(w, "Target:          %s\n", out.Target)
	fmt.Fprintf(w, "Configuration:   %s\n", rep.ConfigPath)
	if out.Readiness != "" {
		fmt.Fprintf(w, "Readiness:       %s\n", out.Readiness)
	}
	fmt.Fprintf(w, "Run ID:          %s\n", out.RunID)
	fmt.Fprintf(w, "Specifications:  %d   Scenarios: %d\n\n", len(rep.Specs), out.Summary.Scenarios)
	for _, v := range out.Violations {
		fmt.Fprintf(w, "INTEGRITY %s\n", v)
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
		line := fmt.Sprintf("%-8s %-10s %s", sc.Result, sc.ID, sc.Name)
		if sc.Reason != "" {
			line += "  (" + sc.Reason + causeText(sc.Cause) + ")"
		} else if sc.Cause != "" && sc.Result == runner.Error {
			line += "  (" + sc.Cause + ")"
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
		label := st.ID
		if st.Phase == "setup" {
			label = "setup " + st.ID
		}
		req := ""
		if st.Request != nil {
			req = "  " + st.Request.Method + " " + st.Request.URL
			if st.Request.Credential != "" {
				req += " [" + st.Request.Identity + ", " + st.Request.Credential + " credential]"
			}
			if st.Status != 0 {
				req += fmt.Sprintf(" -> %d", st.Status)
			}
		}
		if st.Check != nil {
			req = "  postgres " + st.Check.Connection
		}
		stepLine := fmt.Sprintf("           %-8s %s%s", st.Result, label, req)
		if st.Reason != "" {
			stepLine += "  (" + st.Reason + causeText(st.Cause) + ")"
		} else if st.Cause != "" && st.Result == runner.Error {
			stepLine += "  (" + st.Cause + ")"
		}
		fmt.Fprintln(w, stepLine)
		if st.Message != "" {
			fmt.Fprintf(w, "                    %s\n", st.Message)
		}
		for _, a := range st.Assertions {
			if a.Result == runner.Pass {
				continue
			}
			fmt.Fprintf(w, "                    %s %s\n", strings.TrimSpace(a.Kind+" "+a.Target)+":", describe(a))
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
	line := fmt.Sprintf("%-8s %-10s %s  (table", t.Result, t.ID, t.Name)
	if t.SetupFrom != "" {
		line += ", setup from " + t.SetupFrom
	}
	if t.Reason != "" {
		line += ", " + t.Reason
	}
	fmt.Fprintln(w, line+")")
	widths := make([]int, len(t.Columns))
	for i, c := range t.Columns {
		widths[i] = len(c)
	}
	for _, r := range t.Rows {
		for i, c := range r.Cells {
			if i < len(widths) && len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}
	cells := func(vals []string) string {
		var b strings.Builder
		for i, v := range vals {
			if i < len(widths) {
				fmt.Fprintf(&b, "  %-*s", widths[i], v)
			}
		}
		return strings.TrimRight(b.String(), " ")
	}
	fmt.Fprintf(w, "           %-8s%s\n", "", cells(t.Columns))
	for _, r := range t.Rows {
		fmt.Fprintf(w, "           %-8s%s\n", r.Result, cells(r.Cells))
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
		fmt.Fprintf(w, "           case %s:\n", sc.ID)
		writeSteps(w, sc)
	}
}

func causeText(c string) string {
	if c == "" {
		return ""
	}
	return ", cause " + c
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
