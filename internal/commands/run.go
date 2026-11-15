package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
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
	authoritative                         bool
	timeout, qtimeout                     time.Duration
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
	f.DurationVar(&o.timeout, "request-timeout", 10*time.Second, "time limit for each request")
	f.DurationVar(&o.qtimeout, "query-timeout", 5*time.Second, "time limit for each database query")
	f.BoolVar(&o.authoritative, "authoritative", false, "check a commit for the trusted CI job")
	f.StringVar(&o.commit, "commit", "HEAD", "with --authoritative: the tested commit")
	f.StringVar(&o.base, "base", "", "with --authoritative: the base commit, when the pull request head is tested directly")
	f.StringVar(&o.prHead, "pr-head", "", "with --authoritative: the pull request head, when the tested commit is GitHub's test merge")
	f.StringVar(&o.report, "report", "", "with --authoritative: where to write the JSON report (required)")
	return cmd
}

func run(o runOptions, stdout, stderr io.Writer) int {
	c, bindingPath, commit, base, prHead, report := o.common, o.binding, o.commit, o.base, o.prHead, o.report
	authoritative, timeout, qtimeout := o.authoritative, o.timeout, o.qtimeout
	if timeout <= 0 || qtimeout <= 0 {
		fmt.Fprintln(stderr, "proctor run: --request-timeout and --query-timeout must be positive")
		return exitInvalid
	}
	if authoritative {
		return runAuthoritative(authOptions{c: c, binding: bindingPath, commit: commit, base: base, prHead: prHead, report: report, timeout: timeout, qtimeout: qtimeout}, stdout, stderr)
	}
	for _, name := range []string{"commit", "base", "pr-head", "report"} {
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
			"tables":     out.Tables,
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
