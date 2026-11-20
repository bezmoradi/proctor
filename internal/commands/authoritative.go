package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"proctor/internal/github"
	"proctor/internal/gitrepo"
	"proctor/internal/integrity"
	"proctor/internal/model"
	"proctor/internal/protdiff"
	"proctor/internal/runner"
	"proctor/internal/strictyaml"
)

type authOptions struct {
	c                             common
	binding, commit, base, prHead string
	report, received, repoDir     string
	programs                      string
	withheld                      map[string]string
	callTimeout, readyTimeout     time.Duration
	progress                      func(runner.Progress)
	kind, kindSource              string
	kindGiven, kindSourceGiven    bool
	earlier                       string
	earlierGiven                  bool
}

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

const protectedPathKey = "protected_paths:"

const (
	fromBaseNote  = "the tested commit changes this repository driver, but this run used the base commit's program, pins and operations; the change takes effect once merged"
	notInBaseNote = "the base commit has no such repository driver, so it wasn't started and every scenario that uses it is NOT_RUN (" + runner.ReasonDriverNotInBase + "); it takes effect once merged"
)

type authReport struct {
	Result           string                  `json:"result"`
	ExitCode         int                     `json:"exit_code"`
	Mode             string                  `json:"mode"`
	Aborted          bool                    `json:"aborted,omitempty"`
	Reason           string                  `json:"reason,omitempty"`
	Trust            authTrust               `json:"trust"`
	Provenance       authProvenance          `json:"provenance"`
	ProtectedChanges *protdiff.Result        `json:"protected_changes"`
	ChangeKind       *protdiff.KindCheck     `json:"change_kind"`
	EarlierAttempts  *earlierAttempts        `json:"earlier_attempts"`
	Violations       []string                `json:"violations"`
	Errors           []string                `json:"errors"`
	Drivers          []runner.DriverInfo     `json:"drivers"`
	Target           string                  `json:"target,omitempty"`
	Readiness        string                  `json:"readiness,omitempty"`
	IdentityCheck    *runner.IdentityCheck   `json:"identity_check,omitempty"`
	Summary          *runner.Summary         `json:"summary,omitempty"`
	Scenarios        []runner.ScenarioResult `json:"scenarios"`
	Tables           []runner.TableResult    `json:"tables,omitempty"`
	Received         string                  `json:"received,omitempty"`
}

type authTrust struct {
	WorkflowIdentity any `json:"workflow_identity"`

	WorkflowChanged            any      `json:"workflow_changed"`
	CodeownersErrors           []string `json:"codeowners_errors"`
	UnownedProtectedPaths      []string `json:"unowned_protected_paths"`
	ProtectedDiffersFromPRHead *bool    `json:"protected_differs_from_pr_head"`
	ApprovalRequired           bool     `json:"approval_required"`
	RepositoryDriversFromBase  []string `json:"repository_drivers_from_base"`
	RepositoryDriversNotInBase []string `json:"repository_drivers_not_in_base"`
}

type authProvenance struct {
	Repository     string              `json:"repository"`
	TestedCommit   string              `json:"tested_commit"`
	Parents        []string            `json:"parents"`
	PRHead         string              `json:"pr_head,omitempty"`
	Base           string              `json:"base"`
	Image          *model.BindingImage `json:"image"`
	SHA256         map[string]string   `json:"sha256"`
	ProctorVersion string              `json:"proctor_version"`
	Environment    string              `json:"environment"`
	CIRunID        string              `json:"ci_run_id,omitempty"`
	CIRunAttempt   string              `json:"ci_run_attempt,omitempty"`
	RunID          string              `json:"run_id,omitempty"`
	StartedAt      string              `json:"started_at"`
	EndedAt        string              `json:"ended_at"`
}

func runAuthoritative(ctx context.Context, o authOptions, stdout, stderr io.Writer) int {
	if o.report == "" {
		fmt.Fprintln(stderr, "proctor run: --report is required with --authoritative (MODE-05)")
		return exitInvalid
	}
	if o.binding == "" {
		fmt.Fprintln(stderr, "proctor run: --binding is required")
		return reportRefusal(o.report, true, exitInvalid, "--binding is required", stderr)
	}
	repo, err := gitrepo.Open(o.c.repo)
	if err != nil {
		fmt.Fprintf(stderr, "proctor: %s\n", safe(err.Error()))
		return reportRefusal(o.report, true, exitInvalid, err.Error(), stderr)
	}
	o.repoDir = repo.Dir()
	if o.programs == "" {
		o.programs = o.repoDir
	}
	ar := newAuthReport()
	violate := func(rule, format string, args ...any) {
		ar.Violations = append(ar.Violations, rule+": "+fmt.Sprintf(format, args...))
	}
	invalid := func(format string, args ...any) { ar.Errors = append(ar.Errors, fmt.Sprintf(format, args...)) }
	if kc, err := protdiff.ParseKind(o.kind, o.kindGiven, o.kindSource, o.kindSourceGiven); err != nil {
		invalid("CHG-01: %v", err)
	} else {
		ar.ChangeKind = kc
	}

	if inside, err := within(repo.Dir(), o.binding); err != nil {
		invalid("binding: %v", err)
	} else if inside {
		invalid("MODE-01: the binding %s is inside the repository checkout; in authoritative mode it must come from outside it", o.binding)
	}
	if fi, err := os.Stat(o.programs); err != nil || !fi.IsDir() {
		invalid("--driver-programs %s isn't a folder", o.programs)
	}
	if o.earlierGiven && o.earlier == "" {
		invalid("REP-10: --earlier-reports needs a folder")
	} else if o.earlierGiven {
		if inside, err := within(repo.Dir(), o.earlier); err == nil && inside {
			invalid("REP-10: the earlier reports' folder %s is inside the repository checkout; it must come from outside it", o.earlier)
		}
	}

	tested, err := repo.ResolveCommit(o.commit)
	if err != nil {
		violate("INT-09", "tested commit: %v", err)
	}
	ar.Provenance.TestedCommit = tested
	if tested != "" {
		ar.Provenance.Parents, _ = repo.Parents(tested)
	}
	base, prHead := "", ""
	switch {
	case o.base != "" && o.prHead != "":
		invalid("give --pr-head (the tested commit is GitHub's test merge) or --base (the head is tested directly), not both")
	case o.prHead != "" && tested != "":
		if prHead, err = repo.ResolveCommit(o.prHead); err != nil {
			violate("INT-09", "pull request head: %v", err)
		} else if base, err = protdiff.BaseForTestMerge(repo, tested, prHead); err != nil {
			violate("INT-09", "%v", err)
		}
	case o.base != "" && tested != "":
		if base, err = repo.ResolveCommit(o.base); err != nil {
			violate("INT-09", "base: %v", err)
		} else if err = protdiff.CheckMergeBase(repo, base, tested); err != nil {
			violate("INT-09", "%v", err)
		}
	case o.base == "" && o.prHead == "":
		invalid("INT-09: --pr-head or --base is required in authoritative mode, so protected changes are found against the right base")
	}
	ar.Provenance.Base, ar.Provenance.PRHead = base, prHead

	var rep *integrity.Report
	if tested != "" {
		rep = integrity.CheckCommit(repo, tested, o.c.config)
		for _, f := range rep.Findings {
			msg := f.Rule + ": " + strings.TrimPrefix(f.Path+": ", ": ") + f.Message
			if f.Kind == integrity.Violation {
				ar.Violations = append(ar.Violations, msg)
			} else {
				ar.Errors = append(ar.Errors, msg)
			}
		}
		for _, u := range rep.Unlisted {

			violate("INT-05", "%s looks like a specification but the manifest doesn't list it", u)
		}
		ar.Provenance.SHA256 = map[string]string{}
		for k, v := range rep.Hashes {
			ar.Provenance.SHA256[k] = v
		}
		for k, v := range rep.ProtectedPathHashes {
			ar.Provenance.SHA256[protectedPathKey+k] = v
		}
	}

	if rep != nil && rep.Config != nil {
		o.withheld = driversFromBase(repo, base, tested, rep, ar, o.c.config, violate)
	}

	var b model.Binding
	if data, err := os.ReadFile(o.binding); err != nil {
		invalid("binding: %v", err)
	} else if err := strictyaml.Decode(data, &b); err != nil {
		invalid("binding %s: %v", o.binding, err)
	} else {
		sum := sha256.Sum256(data)
		if ar.Provenance.SHA256 == nil {
			ar.Provenance.SHA256 = map[string]string{}
		}
		ar.Provenance.SHA256["binding"] = hex.EncodeToString(sum[:])
		ar.Provenance.Environment = b.Environment
		ar.Provenance.Image = b.Image

		switch {
		case b.Image == nil || b.Image.BuiltDigest == "" || b.Image.RunningDigest == "":
			violate("REP-11", "the binding must give image.built_digest and image.running_digest")
		case !digestPattern.MatchString(b.Image.BuiltDigest) || !digestPattern.MatchString(b.Image.RunningDigest):
			violate("REP-11", "image digests must be sha256: followed by 64 lowercase hex digits")
		case b.Image.BuiltDigest != b.Image.RunningDigest:
			violate("REP-11", "the running application was started from %s, not from the built image %s", b.Image.RunningDigest, b.Image.BuiltDigest)
		}
	}
	if ar.Provenance.Environment == "" {
		ar.Provenance.Environment = "unnamed"
	}

	if base != "" && tested != "" {
		if ch, err := protdiff.Compute(repo, base, tested, o.c.config); err != nil {
			violate("INT-08", "%v", err)
		} else {
			ar.ProtectedChanges = ch
			ar.Trust.ApprovalRequired = ch.Count() > 0
			checkChangeKind(repo, ar, ch, o.c.config, violate)
		}
	}
	if prHead != "" && tested != "" {
		if d, err := protdiff.Compute(repo, prHead, tested, o.c.config); err != nil {
			violate("INT-08", "comparing the tested commit with the pull request head: %v", err)
		} else {
			v := d.Count() > 0
			ar.Trust.ProtectedDiffersFromPRHead = &v
		}
	}

	client := github.Client(10 * time.Second)

	ident, err := (*github.Identity)(nil), github.ErrNoToken
	if ctx.Err() == nil {
		ident, err = github.FetchIdentity(ctx, client)
	}
	switch {
	case errors.Is(err, github.ErrNoToken):
	case err != nil && ctx.Err() != nil:
	case err != nil:
		violate("MODE-03", "the job offers an OIDC token, but it can't be verified: %v", err)
	default:
		ar.Trust.WorkflowIdentity = ident
		if ar.Provenance.Repository == "" {
			ar.Provenance.Repository = ident.Repository
		} else if !strings.EqualFold(ar.Provenance.Repository, ident.Repository) {
			violate("MODE-03", "GITHUB_REPOSITORY is %s, but the OIDC token is for %s", ar.Provenance.Repository, ident.Repository)
		}
		if tested != "" && ident.SHA != tested {
			violate("MODE-03", "the workflow ran for commit %s, but the tested commit is %s", ident.SHA, tested)
		}
		if ar.Provenance.CIRunID == "" {
			ar.Provenance.CIRunID, ar.Provenance.CIRunAttempt = ident.RunID, ident.RunAttempt
		} else if ar.Provenance.CIRunID != ident.RunID || ar.Provenance.CIRunAttempt != ident.RunAttempt {
			violate("MODE-03", "GITHUB_RUN_ID and GITHUB_RUN_ATTEMPT don't match the OIDC token")
		}
		if base != "" {
			if changed, err := workflowChanged(repo, ident, base); err != nil {
				violate("MODE-04", "%v", err)
			} else {
				ar.Trust.WorkflowChanged = changed
			}
		}
	}

	token := os.Getenv("GITHUB_TOKEN")
	api := os.Getenv("GITHUB_API_URL")
	if api == "" {
		api = "https://api.github.com"
	}
	switch {
	case base == "":
	case ctx.Err() != nil:
	case ar.Provenance.Repository == "" || token == "":
		violate("INT-11", "can't ask GitHub to check CODEOWNERS: GITHUB_REPOSITORY and GITHUB_TOKEN must be set (the job needs contents: read)")
	default:
		errs, err := github.CodeownersErrors(ctx, client, api, ar.Provenance.Repository, token, base)
		if err != nil && ctx.Err() == nil {
			violate("INT-11", "checking CODEOWNERS with GitHub: %v", err)
		}
		for _, e := range errs {
			s := fmt.Sprintf("%s line %d: %s", e.Path, e.Line, e.Kind)
			ar.Trust.CodeownersErrors = append(ar.Trust.CodeownersErrors, s)
			violate("INT-11", "GitHub reports a CODEOWNERS error: %s", s)
		}
	}
	if base != "" && rep != nil && rep.Config != nil {
		ar.Trust.UnownedProtectedPaths = unowned(repo, base, tested, rep)
	}
	if rep != nil && rep.Config != nil {
		ar.Violations = append(ar.Violations, runner.ResolveDrivers(rep.Config, runner.Options{Authoritative: true, RepoDir: o.programs, Withheld: o.withheld})...)
	}

	code := exitOK
	switch {
	case len(ar.Violations) > 0:
		code = exitIntegrity
	case len(ar.Errors) > 0:
		code = exitInvalid
	}
	var out *runner.Outcome
	if code == exitOK && ctx.Err() == nil {
		code, out = authRun(ctx, o, ar, rep, &b)
	}
	ar.Aborted = ctx.Err() != nil && (out == nil || out.Aborted)
	if ar.Aborted && code == exitOK {
		code = exitError
	}
	ar.ExitCode, ar.Result = code, resultName(code)
	if out == nil {
		reason := ""
		switch {
		case code == exitInvalid:
			reason = runner.ReasonConfigurationError
		case code == exitError && ar.Aborted:
			reason = runner.ReasonAborted
		}
		var specs []runner.SpecFile
		if rep != nil {
			specs = specFiles(rep)
		}
		stopped := runner.NotStarted(specs, reason, nil)
		ar.Scenarios, ar.Tables, ar.Summary = stopped.Scenarios, stopped.Tables, &stopped.Summary
	}
	ar.Summary.IntegrityViolations = append([]string{}, ar.Violations...)
	if ar.ProtectedChanges != nil {
		n := ar.ProtectedChanges.Count()
		ar.Summary.ProtectedChanges = &n
	}
	ar.EarlierAttempts = checkEarlier(o.earlier, o.earlierGiven, ar.Provenance)
	ar.Provenance.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)

	data, err := json.MarshalIndent(ar, "", "  ")
	if err == nil {
		err = writePrivate(o.report, append(data, '\n'))
	}
	if err != nil {
		fmt.Fprintf(stderr, "proctor: writing the report: %s\n", safe(err.Error()))
		return exitError
	}
	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		if f, err := openAppend(path); err == nil {
			writeSummary(f, ar, o.c.config)
			f.Close()
		} else {
			fmt.Fprintf(stderr, "proctor: not writing the job summary: %s\n", safe(err.Error()))
		}
	}
	if o.c.json {
		stdout.Write(append(data, '\n'))
		return code
	}
	writeAuthoritative(stdout, ar, rep, out)
	return code
}

func driversFromBase(repo *gitrepo.Repo, base, tested string, rep *integrity.Report, ar *authReport, config string, violate func(rule, format string, args ...any)) map[string]string {
	cfg := rep.Config
	var names []string
	for _, name := range sortedNames(cfg.Drivers) {
		if cfg.Drivers[name].Program != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	var baseCfg *model.Config
	var baseTree, tree map[string]gitrepo.Entry
	var baseEntries []gitrepo.Entry
	if base != "" {
		cs, err := integrity.NewCommitSource(repo, base)
		if err == nil {
			baseCfg = integrity.LoadConfig(cs, config).Config
			baseTree, err = repo.Tree(base)
		}
		if err == nil {
			tree, err = repo.Tree(tested)
		}
		if err != nil {
			violate("INT-08", "reading the repository drivers of the base commit: %v", err)
			baseCfg = nil
		}
		for _, e := range baseTree {
			baseEntries = append(baseEntries, e)
		}
	}
	eff := *cfg
	eff.Drivers = maps.Clone(cfg.Drivers)
	withheld := map[string]string{}
	for _, name := range names {
		d := cfg.Drivers[name]
		var was model.DriverConfig
		if baseCfg != nil {
			was = baseCfg.Drivers[name]
		}
		if was.Program == "" {
			delete(eff.Drivers, name)
			withheld[name] = runner.ReasonDriverNotInBase
			ar.Trust.RepositoryDriversNotInBase = append(ar.Trust.RepositoryDriversNotInBase, name)
			continue
		}
		if what, found := integrity.TrackedProgram(was.Program, baseEntries, "the base commit"); found {
			violate("INT-21", "%s: driver %q: %s; build it in the job from the reviewed source instead", was.Program, name, what)
		}
		if was.Program != d.Program || !maps.Equal(was.SHA256, d.SHA256) || !maps.Equal(was.Operations, d.Operations) ||
			folderChanged(baseTree, tree, driverSource(was.Program)) || folderChanged(baseTree, tree, driverSource(d.Program)) {
			ar.Trust.RepositoryDriversFromBase = append(ar.Trust.RepositoryDriversFromBase, name)
		}
		d.Program, d.SHA256, d.Operations = was.Program, was.SHA256, was.Operations
		eff.Drivers[name] = d
	}
	rep.Config = &eff
	return withheld
}

func driverSource(program string) string {
	dir := path.Dir(path.Dir(program))
	if dir == "." {
		return ""
	}
	return dir + "/"
}

func folderChanged(base, head map[string]gitrepo.Entry, prefix string) bool {
	for p, e := range head {
		if was, ok := base[p]; strings.HasPrefix(p, prefix) && (!ok || was.OID != e.OID || was.Mode != e.Mode) {
			return true
		}
	}
	for p := range base {
		if _, ok := head[p]; strings.HasPrefix(p, prefix) && !ok {
			return true
		}
	}
	return false
}

func sortedNames[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

func checkChangeKind(repo *gitrepo.Repo, ar *authReport, ch *protdiff.Result, config string, violate func(rule, format string, args ...any)) {
	k := ar.ChangeKind
	if k == nil {
		return
	}
	if err := k.Check(repo, ch, config); err != nil {
		violate("INT-08", "comparing the protected changes with change kind %s: %v", k.Kind, err)
		return
	}
	for _, m := range k.Mismatches {
		violate("CHG-02", "change kind %s (%s): %s: %s", k.Kind, k.SourceText(), m.Place, m.Reason)
	}
}

func changeKindText(k *protdiff.KindCheck) string {
	if k == nil {
		return "invalid (CHG-01)"
	}
	return changeKindLine(k)
}

func newAuthReport() *authReport {
	ar := &authReport{Mode: "authoritative", Violations: []string{}, Errors: []string{}, Drivers: []runner.DriverInfo{}, Scenarios: []runner.ScenarioResult{},
		EarlierAttempts: notCheckedEarlier("the run stopped before it read any earlier report")}
	ar.Trust = authTrust{WorkflowIdentity: "unverified", WorkflowChanged: "unknown", CodeownersErrors: []string{}, UnownedProtectedPaths: []string{},
		RepositoryDriversFromBase: []string{}, RepositoryDriversNotInBase: []string{}}
	ar.Provenance = authProvenance{ProctorVersion: Version, StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CIRunID: os.Getenv("GITHUB_RUN_ID"), CIRunAttempt: os.Getenv("GITHUB_RUN_ATTEMPT"), Repository: os.Getenv("GITHUB_REPOSITORY")}
	return ar
}

func authRun(ctx context.Context, o authOptions, ar *authReport, rep *integrity.Report, b *model.Binding) (int, *runner.Outcome) {
	opt := runner.Options{CallTimeout: o.callTimeout, ReadyTimeout: o.readyTimeout, LookupEnv: os.LookupEnv, Authoritative: true, Progress: o.progress, RepoDir: o.programs, Withheld: o.withheld}
	refused := func(se *runner.SetupError) int {
		if se.ExitCode == exitIntegrity {
			ar.Violations = append(ar.Violations, se.Msgs...)
		} else {
			ar.Errors = append(ar.Errors, se.Msgs...)
		}
		if se.ExitCode < exitError {
			return exitError
		}
		return se.ExitCode
	}
	mark := len(rep.Findings)
	info, drivers, se := describeSteps(ctx, rep, b, opt)
	if se != nil {
		return refused(se), nil
	}
	if ar.Provenance.SHA256 == nil {
		ar.Provenance.SHA256 = map[string]string{}
	}
	for k, v := range rep.Hashes {
		ar.Provenance.SHA256[k] = v
	}
	ar.Drivers = drivers
	for _, f := range rep.Findings[mark:] {
		msg := f.Rule + ": " + strings.TrimPrefix(f.Path+": ", ": ") + f.Message
		if f.Kind == integrity.Violation {
			ar.Violations = append(ar.Violations, msg)
		} else {
			ar.Errors = append(ar.Errors, msg)
		}
	}
	switch {
	case len(ar.Violations) > 0:
		return exitIntegrity, nil
	case len(ar.Errors) > 0:
		return exitInvalid, nil
	}
	opt.Described = info
	opt.RunID = runner.NewRunID()
	rn, err := runner.New(rep.Config, specFiles(rep), b, opt)
	if err != nil {
		return refused(asSetupError(err)), nil
	}
	defer rn.Close()
	rd, err := prepareReceived(o.received, o.repoDir, rep, opt.RunID)
	if err != nil {
		return refused(&runner.SetupError{ExitCode: pathErrorCode(err), Msgs: []string{"received transcripts: " + err.Error()}}), nil
	}
	if rd != nil {
		rn.SetReceived(rd.write)
	}
	out := rn.Run(ctx)
	if rd != nil {
		if err := rd.finish(out, rep, "authoritative", rep.Commit); err != nil {
			ar.Errors = append(ar.Errors, "writing the received transcripts' manifest: "+err.Error())
			if out.ExitCode == exitOK {
				out.ExitCode = exitError
			}
		}
		ar.Received = out.Received
	}
	ar.Violations = append(ar.Violations, out.Violations...)
	ar.Target, ar.Readiness, ar.Scenarios, ar.Tables, ar.Summary = out.Target, out.Readiness, out.Scenarios, out.Tables, &out.Summary
	ar.IdentityCheck = out.Identity
	ar.Provenance.RunID = out.RunID
	if out.Drivers != nil {
		ar.Drivers = out.Drivers
	}
	return out.ExitCode, out
}

func within(dir, path string) (bool, error) {
	d, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	p, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(d, p)
	if err != nil {
		return false, err
	}
	return rel == "." || !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..", nil
}

func workflowChanged(repo *gitrepo.Repo, id *github.Identity, base string) (any, error) {
	split := func(ref string) (string, string, error) {
		at := strings.LastIndex(ref, "@")
		if at < 0 {
			return "", "", fmt.Errorf("workflow ref %q has no @", ref)
		}
		parts := strings.SplitN(ref[:at], "/", 3)
		if len(parts) != 3 {
			return "", "", fmt.Errorf("workflow ref %q isn't owner/repo/path@ref", ref)
		}
		return parts[0] + "/" + parts[1], parts[2], nil
	}
	jobRepo, jobPath, err := split(id.JobWorkflowRef)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(jobRepo, id.Repository) {
		return "not_applicable", nil
	}
	_, wfPath, err := split(id.WorkflowRef)
	if err != nil {
		return nil, err
	}
	baseTree, err := repo.Tree(base)
	if err != nil {
		return nil, err
	}
	for _, f := range []struct{ path, sha string }{{wfPath, id.WorkflowSHA}, {jobPath, id.JobWorkflowSHA}} {
		commit, err := repo.ResolveCommit(f.sha)
		if err != nil {
			return nil, fmt.Errorf("the workflow's commit %s isn't in the checkout (fetch-depth too small?): %v", f.sha, err)
		}
		tree, err := repo.Tree(commit)
		if err != nil {
			return nil, err
		}
		ran, ok := tree[f.path]
		if !ok {
			return nil, fmt.Errorf("the workflow file %s isn't in commit %s", f.path, commit)
		}
		if was, ok := baseTree[f.path]; !ok || was.OID != ran.OID || was.Mode != ran.Mode {
			return true, nil
		}
	}
	return false, nil
}

func unowned(repo *gitrepo.Repo, base, tested string, rep *integrity.Report) []string {
	baseTree, err1 := repo.Tree(base)
	tree, err2 := repo.Tree(tested)
	if err1 != nil || err2 != nil {
		return []string{}
	}
	var codeowners []byte
	for _, p := range integrity.CodeownersPaths {
		if e, ok := baseTree[p]; ok && e.Regular() {
			codeowners, _ = repo.Blob(e.OID)
			break
		}
	}
	var paths []string
	for p := range tree {
		if integrity.Protected(p, rep.ProtectedRoot, rep.Config.ProtectedPaths) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	out := github.Unowned(codeowners, paths, rep.Config.ProtectedOwners)
	if out == nil {
		out = []string{}
	}
	return out
}

func writeAuthoritative(w io.Writer, ar *authReport, rep *integrity.Report, out *runner.Outcome) {
	fmt.Fprintf(w, "PROCTOR RUN  (authoritative)\n\n")
	p := ar.Provenance
	fmt.Fprintf(w, "Repository:      %s\n", safe(orDash(p.Repository)))
	fmt.Fprintf(w, "Tested commit:   %s\n", safe(orDash(p.TestedCommit)))
	fmt.Fprintf(w, "Base:            %s\n", safe(orDash(p.Base)))
	if p.PRHead != "" {
		fmt.Fprintf(w, "Pull request:    %s\n", safe(p.PRHead))
	}
	if id, ok := ar.Trust.WorkflowIdentity.(*github.Identity); ok {
		fmt.Fprintf(w, "Workflow:        %s (verified)\n", safe(id.JobWorkflowRef))
	} else {
		fmt.Fprintf(w, "Workflow:        unverified\n")
	}
	fmt.Fprintf(w, "Workflow changed: %v\n", ar.Trust.WorkflowChanged)
	fmt.Fprintf(w, "Change kind:     %s\n", changeKindText(ar.ChangeKind))
	writeEarlier(w, ar.EarlierAttempts)
	writeDrivers(w, ar.Drivers)
	for _, n := range ar.Trust.RepositoryDriversFromBase {
		fmt.Fprintf(w, "FROM BASE          driver %s: %s\n", safe(n), fromBaseNote)
	}
	for _, n := range ar.Trust.RepositoryDriversNotInBase {
		fmt.Fprintf(w, "NOT IN BASE        driver %s: %s\n", safe(n), notInBaseNote)
	}
	if ar.ProtectedChanges != nil && rep != nil {
		for _, wk := range ar.ProtectedChanges.Weakenings {
			line := fmt.Sprintf("WEAKENING          %s %s: %s", safe(wk.Kind), safe(weakeningPlace(wk, rep.ConfigPath)), safe(wk.Detail))
			if wk.AssertionsLost > 0 {
				line += fmt.Sprintf(" (%d assertion(s) lost)", wk.AssertionsLost)
			}
			fmt.Fprintln(w, line)
		}
		for _, f := range ar.ProtectedChanges.Files {
			fmt.Fprintf(w, "APPROVAL REQUIRED  %s %s\n", safe(f.Change), safe(f.Path))
		}
	}
	for _, u := range ar.Trust.UnownedProtectedPaths {
		fmt.Fprintf(w, "UNOWNED            %s (no owner from protected_owners; reported only, OD-16)\n", safe(u))
	}
	for _, v := range ar.Violations {
		fmt.Fprintf(w, "INTEGRITY %s\n", safe(v))
	}
	for _, e := range ar.Errors {
		fmt.Fprintf(w, "%s %s\n", errorLabel(ar.ExitCode), safe(e))
	}
	if out == nil {
		if ar.Aborted {
			fmt.Fprintln(w, abortedNote)
		}
		fmt.Fprintf(w, "\nNo scenario was run.\nRESULT: %s (exit %d, authoritative)\n", strings.ToUpper(ar.Result), ar.ExitCode)
		return
	}
	fmt.Fprintln(w)
	writeRun(w, rep, out, "authoritative")
}

func errorLabel(code int) string {
	if code == exitError {
		return "ERROR    "
	}
	return "INVALID  "
}

func writeKindSummary(w io.Writer, k *protdiff.KindCheck) {
	switch {
	case k == nil:
		fmt.Fprintln(w, "Change kind: invalid (CHG-01).")
	case k.Source == protdiff.KindFromLabel:
		fmt.Fprintf(w, "Change kind: %s, from the pull request label %s.\n", code(k.Kind), code(k.Label))
	case k.Source == protdiff.KindFromFlag:
		fmt.Fprintf(w, "Change kind: %s, from %s.\n", code(k.Kind), code("--change-kind"))
	default:
		fmt.Fprintf(w, "Change kind: %s (not given).\n", code(k.Kind))
	}
	fmt.Fprintln(w)
	if k != nil && len(k.Mismatches) > 0 {
		fmt.Fprintf(w, "**Change kind %s doesn't match the protected changes** (CHG-02: %s); each mismatch is listed as an integrity violation below.\n\n", code(k.Kind), md(k.Rule))
	}
}

func writeSummary(w io.Writer, ar *authReport, config string) {
	fmt.Fprintf(w, "## Proctor: %s (exit %d)\n\n", strings.ToUpper(ar.Result), ar.ExitCode)
	if ar.Aborted {
		fmt.Fprintln(w, "**The run was interrupted** (SIGINT or SIGTERM): no scenario started after it, so this result is incomplete.")
		fmt.Fprintln(w)
	}
	p := ar.Provenance
	fmt.Fprintf(w, "Tested commit %s, base %s", code(orDash(p.TestedCommit)), code(orDash(p.Base)))
	if p.PRHead != "" {
		fmt.Fprintf(w, ", pull request head %s", code(p.PRHead))
	}
	fmt.Fprintln(w, ".")
	fmt.Fprintln(w)
	writeKindSummary(w, ar.ChangeKind)
	writeEarlierSummary(w, ar.EarlierAttempts)
	if len(ar.Drivers) > 0 {
		fmt.Fprintln(w, "| Driver | Program | Version | Origin | Proofs |\n|---|---|---|---|---|")
		for _, d := range ar.Drivers {
			var proofs []string
			for _, p := range d.Proofs {
				proofs = append(proofs, p.Name+": "+p.Status)
			}
			fmt.Fprintf(w, "| %s | %s | %s | %s | %s |\n", md(d.Name), code(programOf(d)), md(orDash(d.Version)), md(d.Origin), md(orDash(strings.Join(proofs, ", "))))
		}
		fmt.Fprintln(w)
	}
	for _, n := range ar.Trust.RepositoryDriversFromBase {
		fmt.Fprintf(w, "- driver %s: %s\n", code(n), md(fromBaseNote))
	}
	for _, n := range ar.Trust.RepositoryDriversNotInBase {
		fmt.Fprintf(w, "- driver %s: %s\n", code(n), md(notInBaseNote))
	}
	if len(ar.Trust.RepositoryDriversFromBase)+len(ar.Trust.RepositoryDriversNotInBase) > 0 {
		fmt.Fprintln(w)
	}
	if ar.Trust.WorkflowChanged == true {
		fmt.Fprintln(w, "**This result is not authoritative until the workflow change is approved.** The workflow that ran differs from the base branch's.")
		fmt.Fprintln(w)
	}
	if _, ok := ar.Trust.WorkflowIdentity.(*github.Identity); !ok {
		fmt.Fprintln(w, "Workflow identity: unverified (the job didn't grant `id-token: write`).")
		fmt.Fprintln(w)
	}
	if ar.ProtectedChanges != nil && len(ar.ProtectedChanges.Files) > 0 {
		fmt.Fprintln(w, "### Protected changes: approval required")
		fmt.Fprintln(w)
		for _, wk := range ar.ProtectedChanges.Weakenings {
			fmt.Fprintf(w, "- weakening in %s: %s\n", md(weakeningPlace(wk, config)), md(wk.Detail))
		}
		for _, f := range ar.ProtectedChanges.Files {
			fmt.Fprintf(w, "- %s %s\n", md(f.Change), code(f.Path))
		}
		for _, d := range ar.ProtectedChanges.Drivers {
			fmt.Fprintf(w, "- %s %s: %s\n", md(d.Kind), code(d.Driver), md(d.Detail))
		}
		fmt.Fprintln(w)
	}
	for _, v := range ar.Violations {
		fmt.Fprintf(w, "- integrity violation: %s\n", md(v))
	}
	for _, e := range ar.Errors {
		fmt.Fprintf(w, "- %s: %s\n", strings.ToLower(strings.TrimSpace(errorLabel(ar.ExitCode))), md(e))
	}
	if ic := ar.IdentityCheck; ic != nil && (ic.Result == runner.Fail || ic.Result == runner.Error) {
		for _, e := range ic.Identities {
			if e.Result == runner.Fail || e.Result == runner.Error {
				fmt.Fprintf(w, "- identity check %s: %s: %s\n", md(string(e.Result)), code(e.Identity), md(e.Message))
			}
		}
	}
	if len(ar.Scenarios) > 0 {
		fmt.Fprintln(w, "\n| Scenario | Result |\n|---|---|")
		for _, s := range ar.Scenarios {
			fmt.Fprintf(w, "| %s | %s |\n", md(s.ID), md(string(s.Result)))
		}
	}
}
