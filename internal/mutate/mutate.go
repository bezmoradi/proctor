package mutate

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"proctor/internal/model"
	"proctor/internal/runner"
)

type Verdict string

const (
	Killed          Verdict = "killed"
	Survived        Verdict = "survived"
	KilledElsewhere Verdict = "killed_elsewhere"
	Errored         Verdict = "error"
	NotRun          Verdict = "not_run"
	Passed          Verdict = "passed"
	Failed          Verdict = "failed"
)

const BaselineID = "baseline"

type Script struct {
	Path       string `json:"path"`
	ExitCode   int    `json:"exit_code"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	Stopped    bool   `json:"interrupted,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

func (s *Script) ok() bool { return s != nil && s.ExitCode == 0 && s.Error == "" }

type FileHash struct {
	File   string `json:"file"`
	Before string `json:"sha256_before"`
	After  string `json:"sha256_after"`
}

type Result struct {
	ID         string          `json:"id"`
	Title      string          `json:"title,omitempty"`
	Verdict    Verdict         `json:"verdict"`
	Message    string          `json:"message,omitempty"`
	KilledBy   []string        `json:"killed_by,omitempty"`
	Edits      []model.Edit    `json:"edits,omitempty"`
	Failed     []string        `json:"failed_scenarios"`
	Errors     []string        `json:"error_scenarios,omitempty"`
	Files      []FileHash      `json:"files,omitempty"`
	Up         *Script         `json:"up,omitempty"`
	Down       *Script         `json:"down,omitempty"`
	RunResult  string          `json:"run_result,omitempty"`
	Summary    *runner.Summary `json:"summary,omitempty"`
	DurationMS int64           `json:"duration_ms"`

	infraStop string
}

type Outcome struct {
	Baseline  Result          `json:"baseline"`
	Mutants   []Result        `json:"mutants"`
	Counts    map[Verdict]int `json:"counts"`
	Stopped   string          `json:"stopped,omitempty"`
	Leftovers []string        `json:"leftovers,omitempty"`
	ExitCode  int             `json:"exit_code"`
}

type Plan struct {
	RepoDir     string
	Files       []string
	Up, Down    string
	Mutants     []model.Mutant
	UpTimeout   time.Duration
	DownTimeout time.Duration
	KeepGoing   bool
	Output      *os.File
	Log         io.Writer
	Scenarios   func(ctx context.Context, dir string) (*runner.Outcome, error)
}

func Run(ctx context.Context, p Plan) (*Outcome, error) {
	if err := Supported(); err != nil {
		return nil, err
	}
	src, err := snapshot(p.RepoDir, p.Files)
	if err != nil {
		return nil, err
	}
	out := &Outcome{Mutants: []Result{}, Counts: map[Verdict]int{}}
	defer func() {
		if err := removeTree(src); err != nil {
			out.Leftovers = append(out.Leftovers, src)
		}
	}()
	p.logf("baseline: starting")
	out.Baseline = p.one(ctx, src, nil, out)
	p.logf("baseline: %s", describe(out.Baseline))
	switch {
	case out.Baseline.Verdict != Passed:
		out.Stopped = "the baseline (the unchanged copy) didn't pass, so no mutant ran: " + describe(out.Baseline)
	case out.Baseline.infraStop != "":
		out.Stopped = out.Baseline.infraStop
	}
	for i := range p.Mutants {
		m := &p.Mutants[i]
		if out.Stopped != "" || ctx.Err() != nil {
			reason := out.Stopped
			if reason == "" {
				reason = "the run was interrupted"
			}
			out.Mutants = append(out.Mutants, Result{ID: m.ID, Title: m.Title, Verdict: NotRun, KilledBy: m.KilledBy, Edits: m.Edits, Failed: []string{}, Message: reason})
			continue
		}
		p.logf("mutant %s: starting (%d of %d)", m.ID, i+1, len(p.Mutants))
		res := p.one(ctx, src, m, out)
		p.logf("mutant %s: %s", m.ID, describe(res))
		out.Mutants = append(out.Mutants, res)
		if res.infraStop != "" {
			out.Stopped = res.infraStop
		}
	}
	for _, r := range out.Mutants {
		out.Counts[r.Verdict]++
	}
	switch {
	case out.Baseline.Verdict != Passed, out.Stopped != "":
		out.ExitCode = 2
	case out.Counts[Survived]+out.Counts[KilledElsewhere] > 0:
		out.ExitCode = 1
	case out.Counts[Errored]+out.Counts[NotRun] > 0:
		out.ExitCode = 2
	default:
		out.ExitCode = 0
	}
	return out, nil
}

func (p Plan) logf(format string, args ...any) {
	if p.Log != nil {
		fmt.Fprintf(p.Log, "proctor mutate: "+format+"\n", args...)
	}
}

func describe(r Result) string {
	s := string(r.Verdict)
	if r.Message != "" {
		s += ": " + r.Message
	}
	return s
}

func (p Plan) one(ctx context.Context, src string, m *model.Mutant, out *Outcome) (res Result) {
	began := time.Now()
	label := BaselineID
	res = Result{ID: BaselineID, Failed: []string{}}
	if m != nil {
		label = m.ID
		res = Result{ID: m.ID, Title: m.Title, KilledBy: m.KilledBy, Edits: m.Edits, Failed: []string{}}
	}
	defer func() { res.DurationMS = time.Since(began).Milliseconds() }()
	dir, err := os.MkdirTemp("", "proctor-mutant-*")
	if err != nil {
		res.Verdict, res.Message = Errored, "cannot create the copy: "+err.Error()
		res.infraStop = "the copy for " + label + " couldn't be created: " + err.Error()
		return res
	}
	defer func() {
		if err := removeTree(dir); err != nil {
			out.Leftovers = append(out.Leftovers, dir)
		}
	}()
	if err := clone(src, dir); err != nil {
		res.Verdict, res.Message = Errored, "cannot copy the working tree: "+err.Error()
		res.infraStop = "the copy for " + label + " couldn't be made: " + err.Error()
		return res
	}
	if m != nil {
		files, err := applyEdits(dir, m.Edits)
		res.Files = files
		if err != nil {
			res.Verdict, res.Message = Errored, err.Error()
			return res
		}
	}
	env := scriptEnv(label, dir)
	res.Up = ptr(runScript(ctx, p.Up, p.RepoDir, []string{dir}, env, p.UpTimeout, p.Output))
	if res.Up.ok() {
		run, err := p.Scenarios(ctx, dir)
		judge(&res, run, err, m != nil)
	} else {
		res.Verdict, res.Message = Errored, "the up script "+scriptProblem(res.Up)
		if m == nil || !p.KeepGoing || res.Up.TimedOut || ctx.Err() != nil {
			res.infraStop = "the up script " + scriptProblem(res.Up) + " for " + label
		}
	}
	res.Down = ptr(runScript(context.WithoutCancel(ctx), p.Down, p.RepoDir, []string{dir}, env, p.DownTimeout, p.Output))
	if !res.Down.ok() {
		msg := "the down script " + scriptProblem(res.Down)
		if res.Message != "" {
			msg = res.Message + "; " + msg
		}
		res.Message = msg
		if res.Verdict != Errored {
			res.Message += " (the run had been " + string(res.Verdict) + ")"
		}
		res.Verdict = Errored
		res.infraStop = "the down script " + scriptProblem(res.Down) + " for " + label + "; it may have left containers, processes or images behind, so no further copy was started"
	}
	return res
}

func scriptProblem(s *Script) string {
	switch {
	case s.TimedOut:
		return "timed out"
	case s.Stopped:
		return "was interrupted"
	case s.Error != "" && s.ExitCode == -1:
		return "couldn't run: " + s.Error
	case s.Error != "" && s.ExitCode == 0:
		return s.Error
	}
	return fmt.Sprintf("exited %d", s.ExitCode)
}

func judge(res *Result, run *runner.Outcome, err error, mutant bool) {
	if err != nil {
		res.Verdict, res.Message = Errored, "the scenarios couldn't run: "+err.Error()
		return
	}
	res.RunResult, res.Summary = run.Result, &run.Summary
	for _, sc := range run.Scenarios {
		switch sc.Result {
		case runner.Fail:
			res.Failed = append(res.Failed, sc.ID)
		case runner.Error:
			res.Errors = append(res.Errors, sc.ID)
		}
	}
	if !mutant {
		switch {
		case run.ExitCode == 0:
			res.Verdict = Passed
		case len(res.Failed) > 0:
			res.Verdict, res.Message = Failed, fmt.Sprintf("%d scenario(s) failed on the unchanged copy", len(res.Failed))
		default:
			res.Verdict, res.Message = Errored, fmt.Sprintf("the run ended %s (exit %d)", run.Result, run.ExitCode)
		}
		return
	}
	switch {
	case len(res.Failed) > 0 && len(res.KilledBy) > 0 && !anyIn(res.KilledBy, res.Failed):
		res.Verdict = KilledElsewhere
		res.Message = "failed only scenarios outside killed_by (" + strings.Join(res.KilledBy, ", ") + "); counted as survived"
	case len(res.Failed) > 0:
		res.Verdict = Killed
	case run.ExitCode == 0:
		res.Verdict = Survived
	default:
		res.Verdict = Errored
		res.Message = fmt.Sprintf("no scenario failed, and the run ended %s (exit %d), so nothing shows the mutant was caught", run.Result, run.ExitCode)
	}
}

func anyIn(want, got []string) bool {
	for _, w := range want {
		for _, g := range got {
			if w == g {
				return true
			}
		}
	}
	return false
}

func applyEdits(dir string, edits []model.Edit) ([]FileHash, error) {
	var order []string
	byFile := map[string][]int{}
	for i, e := range edits {
		if _, ok := byFile[e.File]; !ok {
			order = append(order, e.File)
		}
		byFile[e.File] = append(byFile[e.File], i)
	}
	var hashes []FileHash
	for _, f := range order {
		full := filepath.Join(dir, filepath.FromSlash(f))
		fi, err := os.Lstat(full)
		if err != nil || !fi.Mode().IsRegular() {
			return hashes, fmt.Errorf("the edit didn't apply: %s isn't a regular file in the copy", f)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return hashes, err
		}
		var es []model.Edit
		for _, i := range byFile[f] {
			es = append(es, edits[i])
		}
		changed, err := model.ApplyEdits(string(data), es, byFile[f])
		if err != nil {
			return hashes, fmt.Errorf("%s: %v", f, err)
		}
		if err := os.WriteFile(full, []byte(changed), fi.Mode().Perm()); err != nil {
			return hashes, err
		}
		hashes = append(hashes, FileHash{File: f, Before: sum(data), After: sum([]byte(changed))})
	}
	return hashes, nil
}

func scriptEnv(label, dir string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PROCTOR_MUTANT=") || strings.HasPrefix(kv, "PROCTOR_MUTANT_DIR=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "PROCTOR_MUTANT="+label, "PROCTOR_MUTANT_DIR="+dir)
}

func ptr[T any](v T) *T { return &v }
