package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
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
	report                        string
	callTimeout, readyTimeout     time.Duration
}

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type authReport struct {
	Result           string                  `json:"result"`
	ExitCode         int                     `json:"exit_code"`
	Mode             string                  `json:"mode"`
	Trust            authTrust               `json:"trust"`
	Provenance       authProvenance          `json:"provenance"`
	ProtectedChanges *protdiff.Result        `json:"protected_changes"`
	Violations       []string                `json:"violations"`
	Errors           []string                `json:"errors"`
	Drivers          []runner.DriverInfo     `json:"drivers"`
	Target           string                  `json:"target,omitempty"`
	Readiness        string                  `json:"readiness,omitempty"`
	Summary          *runner.Summary         `json:"summary,omitempty"`
	Scenarios        []runner.ScenarioResult `json:"scenarios"`
	Tables           []runner.TableResult    `json:"tables,omitempty"`
}

type authTrust struct {
	WorkflowIdentity any `json:"workflow_identity"`

	WorkflowChanged            any      `json:"workflow_changed"`
	CodeownersErrors           []string `json:"codeowners_errors"`
	UnownedProtectedPaths      []string `json:"unowned_protected_paths"`
	ProtectedDiffersFromPRHead *bool    `json:"protected_differs_from_pr_head"`
	ApprovalRequired           bool     `json:"approval_required"`
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

func runAuthoritative(o authOptions, stdout, stderr io.Writer) int {
	if o.report == "" {
		fmt.Fprintln(stderr, "proctor run: --report is required with --authoritative (MODE-05)")
		return exitInvalid
	}
	if o.binding == "" {
		fmt.Fprintln(stderr, "proctor run: --binding is required")
		return exitInvalid
	}
	repo, err := gitrepo.Open(o.c.repo)
	if err != nil {
		fmt.Fprintf(stderr, "proctor: %s\n", safe(err.Error()))
		return exitInvalid
	}
	ar := &authReport{Mode: "authoritative", Violations: []string{}, Errors: []string{}, Drivers: []runner.DriverInfo{}, Scenarios: []runner.ScenarioResult{}}
	ar.Trust = authTrust{WorkflowIdentity: "unverified", WorkflowChanged: "unknown", CodeownersErrors: []string{}, UnownedProtectedPaths: []string{}}
	ar.Provenance = authProvenance{ProctorVersion: Version, StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CIRunID: os.Getenv("GITHUB_RUN_ID"), CIRunAttempt: os.Getenv("GITHUB_RUN_ATTEMPT"), Repository: os.Getenv("GITHUB_REPOSITORY")}
	violate := func(rule, format string, args ...any) {
		ar.Violations = append(ar.Violations, rule+": "+fmt.Sprintf(format, args...))
	}
	invalid := func(format string, args ...any) { ar.Errors = append(ar.Errors, fmt.Sprintf(format, args...)) }

	if inside, err := within(repo.Dir(), o.binding); err != nil {
		invalid("binding: %v", err)
	} else if inside {
		invalid("MODE-01: the binding %s is inside the repository checkout; in authoritative mode it must come from outside it", o.binding)
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	client := github.Client(10 * time.Second)

	ident, err := github.FetchIdentity(ctx, client)
	switch {
	case errors.Is(err, github.ErrNoToken):
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
	case ar.Provenance.Repository == "" || token == "":
		violate("INT-11", "can't ask GitHub to check CODEOWNERS: GITHUB_REPOSITORY and GITHUB_TOKEN must be set (the job needs contents: read)")
	default:
		errs, err := github.CodeownersErrors(ctx, client, api, ar.Provenance.Repository, token, base)
		if err != nil {
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

	code := exitOK
	switch {
	case len(ar.Violations) > 0:
		code = exitIntegrity
	case len(ar.Errors) > 0:
		code = exitInvalid
	}
	var out *runner.Outcome
	if code == exitOK {
		code, out = authRun(ctx, o, ar, rep, &b)
	}
	ar.ExitCode, ar.Result = code, resultName(code)
	ar.Provenance.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)

	data, err := json.MarshalIndent(ar, "", "  ")
	if err == nil {
		err = os.WriteFile(o.report, append(data, '\n'), 0o644)
	}
	if err != nil {
		fmt.Fprintf(stderr, "proctor: writing the report: %s\n", safe(err.Error()))
		return exitError
	}
	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644); err == nil {
			writeSummary(f, ar, o.c.config)
			f.Close()
		}
	}
	if o.c.json {
		stdout.Write(append(data, '\n'))
		return code
	}
	writeAuthoritative(stdout, ar, rep, out)
	return code
}

func authRun(ctx context.Context, o authOptions, ar *authReport, rep *integrity.Report, b *model.Binding) (int, *runner.Outcome) {
	opt := runner.Options{CallTimeout: o.callTimeout, ReadyTimeout: o.readyTimeout, LookupEnv: os.LookupEnv, Authoritative: true}
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
	rn, err := runner.New(rep.Config, specFiles(rep), b, opt)
	if err != nil {
		return refused(asSetupError(err)), nil
	}
	defer rn.Close()
	out := rn.Run(ctx)
	ar.Violations = append(ar.Violations, out.Violations...)
	ar.Target, ar.Readiness, ar.Scenarios, ar.Tables, ar.Summary = out.Target, out.Readiness, out.Scenarios, out.Tables, &out.Summary
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
	writeDrivers(w, ar.Drivers)
	if ar.ProtectedChanges != nil {
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

func writeSummary(w io.Writer, ar *authReport, config string) {
	fmt.Fprintf(w, "## Proctor: %s (exit %d)\n\n", strings.ToUpper(ar.Result), ar.ExitCode)
	p := ar.Provenance
	fmt.Fprintf(w, "Tested commit %s, base %s", code(orDash(p.TestedCommit)), code(orDash(p.Base)))
	if p.PRHead != "" {
		fmt.Fprintf(w, ", pull request head %s", code(p.PRHead))
	}
	fmt.Fprintln(w, ".")
	fmt.Fprintln(w)
	if len(ar.Drivers) > 0 {
		fmt.Fprintln(w, "| Driver | Program | Version | Origin | Proofs |\n|---|---|---|---|---|")
		for _, d := range ar.Drivers {
			var proofs []string
			for _, p := range d.Proofs {
				proofs = append(proofs, p.Name+": "+p.Status)
			}
			fmt.Fprintf(w, "| %s | %s | %s | %s | %s |\n", md(d.Name), code("proctor-driver-"+d.Official), md(orDash(d.Version)), md(d.Origin), md(orDash(strings.Join(proofs, ", "))))
		}
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
		for _, f := range ar.ProtectedChanges.Files {
			fmt.Fprintf(w, "- %s %s\n", md(f.Change), code(f.Path))
		}
		for _, wk := range ar.ProtectedChanges.Weakenings {
			fmt.Fprintf(w, "- weakening in %s: %s\n", md(weakeningPlace(wk, config)), md(wk.Detail))
		}
		fmt.Fprintln(w)
	}
	for _, v := range ar.Violations {
		fmt.Fprintf(w, "- integrity violation: %s\n", md(v))
	}
	for _, e := range ar.Errors {
		fmt.Fprintf(w, "- %s: %s\n", strings.ToLower(strings.TrimSpace(errorLabel(ar.ExitCode))), md(e))
	}
	if len(ar.Scenarios) > 0 {
		fmt.Fprintln(w, "\n| Scenario | Result |\n|---|---|")
		for _, s := range ar.Scenarios {
			fmt.Fprintf(w, "| %s | %s |\n", md(s.ID), md(string(s.Result)))
		}
	}
}
