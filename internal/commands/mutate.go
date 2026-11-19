package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"proctor/internal/gitrepo"
	"proctor/internal/integrity"
	"proctor/internal/model"
	"proctor/internal/mutate"
	"proctor/internal/runner"
)

type mutateOptions struct {
	common
	only, scenarios, report              string
	keepGoing, authoritative, noProgress bool
	upTimeout, downTimeout               time.Duration
	callTimeout, readyTimeout            time.Duration
}

func newMutateCmd(stdout, stderr io.Writer) *cobra.Command {
	var o mutateOptions
	cmd := &cobra.Command{
		Use:   "mutate",
		Short: "Build and run each declared mutant of the application from a copy of the working tree, and check that the specifications fail it",
		Example: `  proctor mutate
  proctor mutate --only M-01,M-02 --report /tmp/mutate.json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitWith(mutateRun(o, stdout, stderr))
		},
	}
	addCommon(cmd, &o.common)
	f := cmd.Flags()
	f.StringVar(&o.only, "only", "", "run only these mutants, by id, comma-separated (the baseline always runs)")
	f.StringVar(&o.scenarios, "scenarios", "", "run only these scenarios or table cases, by id, comma-separated")
	f.StringVar(&o.report, "report", "", "where to write the JSON report, with mode 0600")
	f.BoolVar(&o.keepGoing, "keep-going", false, "go on to the next mutant after an up script fails (never after a down script fails or an up script times out)")
	f.DurationVar(&o.upTimeout, "up-timeout", 45*time.Minute, "time limit for each run of the up script")
	f.DurationVar(&o.downTimeout, "down-timeout", 10*time.Minute, "time limit for each run of the down script")
	f.DurationVar(&o.callTimeout, "call-timeout", 10*time.Second, "time limit for each driver call")
	f.DurationVar(&o.readyTimeout, "ready-timeout", 30*time.Second, "how long to wait for every driver to report its target ready")
	f.BoolVar(&o.noProgress, "no-progress", false, "don't write a line to stderr as each scenario finishes")
	f.BoolVar(&o.authoritative, "authoritative", false, "refused: proctor mutate runs in local mode only")
	_ = f.MarkHidden("authoritative")
	cmd.AddCommand(newMutateHTMLCmd(stdout, stderr))
	return cmd
}

func mutateRun(o mutateOptions, stdout, stderr io.Writer) int {
	refuse := func(msg string) int {
		fmt.Fprintln(stderr, "proctor mutate: "+safe(msg))
		return reportRefusal(o.report, false, exitInvalid, msg, stderr)
	}
	if o.authoritative {
		return refuse("--authoritative isn't available: mutate runs in local mode only, and an authoritative run doesn't run mutants (OD-15)")
	}
	for _, name := range []string{"CI", "GITHUB_ACTIONS"} {
		if _, set := os.LookupEnv(name); set {
			return refuse(name + " is set: proctor mutate runs in local mode only, never in CI, since its scripts build and start the application from a copy of the working tree")
		}
	}
	if o.report != "" {
		if err := refuseNonRegular(o.report); err != nil {
			fmt.Fprintln(stderr, "proctor mutate: "+safe(err.Error()))
			return exitInvalid
		}
	}
	if o.upTimeout <= 0 || o.downTimeout <= 0 || o.callTimeout <= 0 || o.readyTimeout <= 0 {
		return refuse("--up-timeout, --down-timeout, --call-timeout and --ready-timeout must be positive")
	}
	only, err := idList("--only", o.only)
	if err != nil {
		return refuse(err.Error())
	}
	picked, err := idList("--scenarios", o.scenarios)
	if err != nil {
		return refuse(err.Error())
	}
	repo, err := gitrepo.Open(o.repo)
	if err != nil {
		return refuse(err.Error())
	}
	emit := func(render func(w io.Writer, asJSON bool) int) int {
		return emitLocal(o.report, o.json, stdout, stderr, render)
	}
	src := integrity.WorkTreeSource{Root: repo.Dir()}
	rep := integrity.Load(src, o.config)
	if rep.Config != nil && rep.Config.Mutation == nil {
		return refuse(o.config + " has no mutation key, so there are no mutants to run; see MUT-01")
	}
	files, err := mutate.ListFiles(repo.Dir())
	if err != nil {
		return refuse(err.Error())
	}
	if rep.Config != nil {
		checkMutation(rep, repo.Dir(), src, false)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dc := &driverCheck{}
	if rep.Config != nil {
		_, dc.drivers, dc.setup = describeSteps(ctx, rep, nil, runner.Options{})
	}
	code := rep.ExitCode()
	if dc.setup != nil && dc.setup.ExitCode > code {
		code = dc.setup.ExitCode
	}
	if code != exitOK {
		return emit(func(w io.Writer, asJSON bool) int { return write(w, asJSON, "MUTATE", rep, dc, code, resultName(code)) })
	}
	mutants, specs, err := selectMutation(rep, only, picked)
	if err != nil {
		return refuse(err.Error())
	}
	progress, endProgress := newProgress(stderr, o.noProgress)
	defer endProgress()
	m := rep.Config.Mutation
	up, down := rep.MutationScripts()
	output, ok := stderr.(*os.File)
	if !ok {
		output = os.Stderr
	}
	opt := runner.Options{CallTimeout: o.callTimeout, ReadyTimeout: o.readyTimeout, LookupEnv: os.LookupEnv, Progress: progress}
	plan := mutate.Plan{
		RepoDir: repo.Dir(), Files: files, Mutants: mutants,
		Up: filepath.Join(repo.Dir(), filepath.FromSlash(up)), Down: filepath.Join(repo.Dir(), filepath.FromSlash(down)),
		UpTimeout: o.upTimeout, DownTimeout: o.downTimeout, KeepGoing: o.keepGoing, Output: output, Log: stderr,
		Scenarios: func(ctx context.Context) (*runner.Outcome, error) {
			b, err := readBinding(m.Binding)
			if err != nil {
				return nil, err
			}
			rn, err := runner.New(rep.Config, specs, b, opt)
			if err != nil {
				return nil, err
			}
			defer rn.Close()
			return rn.Run(ctx), nil
		},
	}
	began := time.Now().UTC()
	out, err := mutate.Run(ctx, plan)
	ended := time.Now().UTC()
	if err != nil {
		var te *mutate.TreeError
		if errors.As(err, &te) {
			return refuse(err.Error())
		}
		fmt.Fprintln(stderr, "proctor mutate: "+safe(err.Error()))
		return reportRefusal(o.report, false, exitError, err.Error(), stderr)
	}
	doc := mutateDoc{
		Result: resultName(out.ExitCode), ExitCode: out.ExitCode, Mode: "local", Command: "mutate",
		Trust: map[string]any{"authoritative": false, "reason": "local mode reads the working tree; the result is not evidence for a commit"},
		Provenance: mutateProvenance{ProctorVersion: Version, Repository: repo.Dir(), Config: rep.ConfigPath, Mutants: rep.MutantsPath, SHA256: rep.Hashes,
			StartedAt: began.Format(time.RFC3339Nano), EndedAt: ended.Format(time.RFC3339Nano), DurationMS: ended.Sub(began).Milliseconds()},
		Selected: mutateSelection{Mutants: mutantIDs(mutants), Scenarios: picked},
		Outcome:  out,
	}
	return emit(func(w io.Writer, asJSON bool) int { return writeMutate(w, asJSON, doc) })
}

type mutateDoc struct {
	Result     string           `json:"result"`
	ExitCode   int              `json:"exit_code"`
	Mode       string           `json:"mode"`
	Command    string           `json:"command"`
	Trust      map[string]any   `json:"trust"`
	Provenance mutateProvenance `json:"provenance"`
	Selected   mutateSelection  `json:"selected"`
	*mutate.Outcome
}

type mutateProvenance struct {
	ProctorVersion string            `json:"proctor_version"`
	Repository     string            `json:"repository,omitempty"`
	Config         string            `json:"config"`
	Mutants        string            `json:"mutants"`
	SHA256         map[string]string `json:"sha256"`
	StartedAt      string            `json:"started_at"`
	EndedAt        string            `json:"ended_at"`
	DurationMS     int64             `json:"duration_ms"`
}

type mutateSelection struct {
	Mutants   []string `json:"mutants"`
	Scenarios []string `json:"scenarios,omitempty"`
}

func writeMutate(w io.Writer, asJSON bool, doc mutateDoc) int {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(doc)
		return doc.ExitCode
	}
	fmt.Fprintf(w, "PROCTOR MUTATE  (local, non-authoritative)\n\nConfiguration:   %s\nMutants:         %s (%d selected)\n",
		safe(doc.Provenance.Config), safe(doc.Provenance.Mutants), len(doc.Selected.Mutants))
	if len(doc.Selected.Scenarios) > 0 {
		fmt.Fprintf(w, "Scenarios:       %s\n", safe(strings.Join(doc.Selected.Scenarios, ", ")))
	}
	fmt.Fprintf(w, "Baseline:        %s\n\n", safe(resultLine(doc.Baseline)))
	for _, r := range doc.Mutants {
		fmt.Fprintf(w, "  %-12s %-16s %s\n", safe(r.ID), r.Verdict, safe(mutantLine(r)))
	}
	if doc.Stopped != "" {
		fmt.Fprintf(w, "\nStopped: %s\n", safe(doc.Stopped))
	}
	for _, l := range doc.Leftovers {
		fmt.Fprintf(w, "Not removed: %s\n", safe(l))
	}
	c := doc.Counts
	fmt.Fprintf(w, "\nMutants: %d   killed %d   survived %d   killed_elsewhere %d   error %d   not_run %d   (%s)\n",
		len(doc.Mutants), c[mutate.Killed], c[mutate.Survived], c[mutate.KilledElsewhere], c[mutate.Errored], c[mutate.NotRun],
		(time.Duration(doc.Provenance.DurationMS) * time.Millisecond).Round(time.Second))
	fmt.Fprintf(w, "RESULT: %s (exit %d, local, non-authoritative)\n", strings.ToUpper(doc.Result), doc.ExitCode)
	return doc.ExitCode
}

func resultLine(r mutate.Result) string {
	s := string(r.Verdict)
	if r.Summary != nil {
		s += fmt.Sprintf(" (%d scenarios: passed %d, failed %d, errors %d, not run %d)", r.Summary.Scenarios, r.Summary.Passed, r.Summary.Failed, r.Summary.Errors, r.Summary.NotRun)
	}
	if r.Message != "" {
		s += "; " + r.Message
	}
	return s
}

func mutantLine(r mutate.Result) string {
	s := r.Title
	if len(r.Failed) > 0 {
		s += "  (failed: " + strings.Join(r.Failed, ", ") + ")"
	}
	if r.Message != "" && r.Verdict != mutate.NotRun {
		s += "  " + r.Message
	}
	return s
}

func idList(flag, raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" {
			return nil, fmt.Errorf("%s has an empty id; give ids separated by commas", flag)
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

func selectMutation(rep *integrity.Report, only, picked []string) ([]model.Mutant, []runner.SpecFile, error) {
	mutants := rep.Mutants
	if len(only) > 0 {
		known := map[string]bool{}
		for _, m := range rep.Mutants {
			known[m.ID] = true
		}
		want := map[string]bool{}
		for _, id := range only {
			if !known[id] {
				return nil, nil, fmt.Errorf("--only names %q, which isn't a mutant in %s", id, rep.MutantsPath)
			}
			want[id] = true
		}
		mutants = nil
		for _, m := range rep.Mutants {
			if want[m.ID] {
				mutants = append(mutants, m)
			}
		}
	}
	specs := specFiles(rep)
	if len(picked) == 0 {
		return mutants, specs, nil
	}
	known := map[string]bool{}
	for _, id := range rep.ScenarioIDs {
		known[id] = true
	}
	want := map[string]bool{}
	for _, id := range picked {
		if !known[id] {
			return nil, nil, fmt.Errorf("--scenarios names %q, which isn't a scenario or table case of the listed specifications", id)
		}
		want[id] = true
	}
	for _, m := range mutants {
		for _, id := range m.KilledBy {
			if !want[id] {
				return nil, nil, fmt.Errorf("mutant %s has killed_by %s, which --scenarios leaves out; select it too, or leave the mutant out with --only", m.ID, id)
			}
		}
	}
	var out []runner.SpecFile
	for _, f := range specs {
		cp := *f.Spec
		cp.Scenarios = nil
		for _, sc := range f.Spec.Scenarios {
			if want[sc.ID] {
				cp.Scenarios = append(cp.Scenarios, sc)
			}
		}
		if len(cp.Scenarios) > 0 {
			out = append(out, runner.SpecFile{Path: f.Path, Spec: &cp})
		}
	}
	return mutants, out, nil
}

func mutantIDs(ms []model.Mutant) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}
