package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"proctor/internal/gitrepo"
	"proctor/internal/integrity"
	"proctor/internal/model"
	"proctor/internal/runner"
	"proctor/internal/strictyaml"
)

type runOptions struct {
	common
	binding, commit, base, prHead, report string
	authoritative, noProgress             bool
	callTimeout, readyTimeout             time.Duration
	set                                   func(name string) bool
}

func newRunCmd(stdout, stderr io.Writer) *cobra.Command {
	var o runOptions
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run every listed scenario against a running application",
		Example: `  proctor run --binding proctor/environments/local.yaml
  proctor run --authoritative --binding "$RUNNER_TEMP/binding.yaml" --pr-head "$PR_HEAD" --report "$RUNNER_TEMP/report.json"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.set = cmd.Flags().Changed
			return exitWith(run(o, stdout, stderr))
		},
	}
	addCommon(cmd, &o.common)
	f := cmd.Flags()
	f.StringVarP(&o.binding, "binding", "b", "", "environment binding file (required)")
	f.DurationVar(&o.callTimeout, "call-timeout", 10*time.Second, "time limit for each driver call")
	f.DurationVar(&o.readyTimeout, "ready-timeout", 30*time.Second, "how long to wait for every driver to report its target ready")
	f.BoolVar(&o.authoritative, "authoritative", false, "check a commit for the trusted CI job")
	f.StringVar(&o.commit, "commit", "HEAD", "with --authoritative: the tested commit")
	f.StringVar(&o.base, "base", "", "with --authoritative: the base commit, when the pull request head is tested directly")
	f.StringVar(&o.prHead, "pr-head", "", "with --authoritative: the pull request head, when the tested commit is GitHub's test merge")
	f.StringVar(&o.report, "report", "", "where to write the JSON report: required with --authoritative; in local mode it gets the document --json prints, with mode 0600")
	f.BoolVar(&o.noProgress, "no-progress", false, "don't write a line to stderr as each scenario finishes")
	return cmd
}

func run(o runOptions, stdout, stderr io.Writer) int {
	c, bindingPath := o.common, o.binding
	if o.callTimeout <= 0 || o.readyTimeout <= 0 {
		fmt.Fprintln(stderr, "proctor run: --call-timeout and --ready-timeout must be positive")
		return exitInvalid
	}
	progress, endProgress := newProgress(stderr, o.noProgress)
	defer endProgress()
	if o.authoritative {
		return runAuthoritative(authOptions{c: c, binding: bindingPath, commit: o.commit, base: o.base, prHead: o.prHead, report: o.report,
			callTimeout: o.callTimeout, readyTimeout: o.readyTimeout, progress: progress}, stdout, stderr)
	}
	for _, name := range []string{"commit", "base", "pr-head"} {
		if o.set(name) {
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
		fmt.Fprintf(stderr, "proctor: %s\n", safe(err.Error()))
		return exitInvalid
	}
	emit := func(render func(w io.Writer, asJSON bool) int) int {
		return emitLocal(o.report, c.json, stdout, stderr, render)
	}
	rep := integrity.Load(integrity.WorkTreeSource{Root: repo.Dir()}, c.config)
	if code := rep.ExitCode(); code != exitOK {
		return emit(func(w io.Writer, asJSON bool) int { return write(w, asJSON, "RUN", rep, nil, code, resultName(code)) })
	}

	b, err := readBinding(bindingPath)
	if err != nil {
		se := &runner.SetupError{ExitCode: exitInvalid, Msgs: []string{err.Error()}}
		return emit(func(w io.Writer, asJSON bool) int { return setupFailed(w, asJSON, rep, se) })
	}
	opt := runner.Options{CallTimeout: o.callTimeout, ReadyTimeout: o.readyTimeout, LookupEnv: os.LookupEnv, Progress: progress}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	info, drivers, se := describeSteps(ctx, rep, b, opt)
	if se != nil {
		return emit(func(w io.Writer, asJSON bool) int { return setupFailed(w, asJSON, rep, se) })
	}
	if code := rep.ExitCode(); code != exitOK {
		return emit(func(w io.Writer, asJSON bool) int {
			return write(w, asJSON, "RUN", rep, &driverCheck{drivers: drivers}, code, resultName(code))
		})
	}
	opt.Described = info
	rn, err := runner.New(rep.Config, specFiles(rep), b, opt)
	if err != nil {
		se := asSetupError(err)
		return emit(func(w io.Writer, asJSON bool) int { return setupFailed(w, asJSON, rep, se) })
	}
	defer rn.Close()
	out := rn.Run(ctx)
	return emit(func(w io.Writer, asJSON bool) int { return writeLocalRun(w, asJSON, rep, out) })
}

func writeLocalRun(w io.Writer, asJSON bool, rep *integrity.Report, out *runner.Outcome) int {
	if asJSON {
		enc := json.NewEncoder(w)
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
			"drivers":    out.Drivers,
			"summary":    out.Summary,
			"scenarios":  out.Scenarios,
			"tables":     out.Tables,
		})
		return out.ExitCode
	}
	writeRun(w, rep, out, "local, non-authoritative")
	return out.ExitCode
}

func emitLocal(report string, asJSON bool, stdout, stderr io.Writer, render func(w io.Writer, asJSON bool) int) int {
	code := render(stdout, asJSON)
	if report == "" {
		return code
	}
	var buf bytes.Buffer
	render(&buf, true)
	if err := writePrivate(report, buf.Bytes()); err != nil {
		fmt.Fprintf(stderr, "proctor: writing the report: %s\n", safe(err.Error()))
		if code == exitOK {
			return exitError
		}
	}
	return code
}

func writePrivate(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func readBinding(p string) (*model.Binding, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("binding: %v", err)
	}
	var b model.Binding
	if err := strictyaml.Decode(data, &b); err != nil {
		return nil, fmt.Errorf("binding %s: %v", p, err)
	}
	return &b, nil
}

func specFiles(rep *integrity.Report) []runner.SpecFile {
	out := make([]runner.SpecFile, 0, len(rep.Specs))
	for _, s := range rep.Specs {
		out = append(out, runner.SpecFile{Path: s.Path, Spec: s.Spec})
	}
	return out
}

func describeSteps(ctx context.Context, rep *integrity.Report, b *model.Binding, opt runner.Options) (map[model.StepRef]model.StepInfo, []runner.DriverInfo, *runner.SetupError) {
	info, drivers, err := runner.Describe(ctx, rep.Config, specFiles(rep), b, opt)
	if err != nil {
		return nil, nil, asSetupError(err)
	}
	rep.ApplyStepInfo(info)
	if drivers == nil {
		drivers = []runner.DriverInfo{}
	}
	return info, drivers, nil
}

func asSetupError(err error) *runner.SetupError {
	var se *runner.SetupError
	if errors.As(err, &se) {
		return se
	}
	return &runner.SetupError{ExitCode: exitError, Msgs: []string{err.Error()}}
}

func setupFailed(stdout io.Writer, asJSON bool, rep *integrity.Report, se *runner.SetupError) int {
	code := se.ExitCode
	if code < exitError {
		code = exitError
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"result": resultName(code), "exit_code": code, "mode": "local", "errors": se.Msgs})
		return code
	}
	fmt.Fprintf(stdout, "PROCTOR RUN  (local, non-authoritative)\n\nConfiguration:   %s\n\n", safe(rep.ConfigPath))
	for _, m := range se.Msgs {
		fmt.Fprintf(stdout, "%s %s\n", setupLabel(code), safe(m))
	}
	fmt.Fprintf(stdout, "\nNothing was sent to the application.\nRESULT: %s (exit %d)\n", strings.ToUpper(resultName(code)), code)
	return code
}
