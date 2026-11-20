package commands

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"proctor/internal/applogs"
	"proctor/internal/gitrepo"
	"proctor/internal/integrity"
	"proctor/internal/model"
	"proctor/internal/runner"
	"proctor/internal/transcript"
)

const maxManifestBytes = 16 << 20

type approveOptions struct {
	common
	received string
	all, yes bool
}

func newApproveCmd(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	var o approveOptions
	cmd := &cobra.Command{
		Use:   "approve [<scenario id>...]",
		Short: "Show each received transcript's or log's difference from the approved one and, when you answer y, write it into approved/",
		Example: `  proctor approve REP-001 REP-002
  proctor approve --all --received "$TMPDIR/proctor-received/demo/0a1b2c3d4e5f"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitWith(approve(o, args, stdin, stdout, stderr))
		},
	}
	addCommon(cmd, &o.common)
	f := cmd.Flags()
	f.StringVar(&o.received, "received", "", "the folder proctor run wrote the received transcripts and logs to (default: the newest run's folder under $TMPDIR/proctor-received/<project>/)")
	f.BoolVar(&o.all, "all", false, "every received transcript or log that differs from its approved one, or has none")
	f.BoolVar(&o.yes, "yes", false, "approve without asking; needed when stdin isn't a terminal")
	return cmd
}

type approval struct {
	entry    receivedEntry
	log      bool
	received []byte
	approved []byte
	target   string
}

type choice struct {
	receivedEntry
	log bool
}

func (p choice) noun() string {
	if p.log {
		return "log"
	}
	return "transcript"
}

func approve(o approveOptions, ids []string, stdin io.Reader, stdout, stderr io.Writer) int {
	refuse := func(format string, args ...any) int {
		fmt.Fprintln(stderr, "proctor approve: "+safe(fmt.Sprintf(format, args...)))
		return exitInvalid
	}
	if o.json {
		return refuse("--json isn't available: approve asks a person about each file")
	}
	for _, name := range []string{"CI", "GITHUB_ACTIONS"} {
		if _, set := os.LookupEnv(name); set {
			return refuse("%s is set: proctor approve runs only on a person's machine, never in CI; the approval that counts is the code-owner review of the changed approved file", name)
		}
	}
	if o.all == (len(ids) > 0) {
		return refuse("name the scenarios or table cases to approve, or give --all, not both")
	}
	if !o.yes {
		f, ok := stdin.(*os.File)
		if !ok || !isTerminal(f) {
			return refuse("stdin isn't a terminal, so no one can answer; run it in a terminal, or give --yes to approve without asking")
		}
	}
	repo, err := gitrepo.Open(o.repo)
	if err != nil {
		return refuse("%v", err)
	}
	root, err := repoRoot(repo.Dir())
	if err != nil {
		return refuse("%v", err)
	}
	rep := integrity.Load(integrity.WorkTreeSource{Root: repo.Dir()}, o.config)
	rep.CheckTrackedWorkTree(repo.Index)
	if rep.Config == nil || rep.ExitCode() != exitOK {
		for _, f := range rep.Findings {
			fmt.Fprintf(stderr, "proctor approve: %s %s\n", safe(f.Rule), safe(strings.TrimPrefix(f.Path+": ", ": ")+f.Message))
		}
		return refuse("the specifications don't validate; fix them, run again, then approve")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if _, _, se := describeSteps(ctx, rep, nil, runner.Options{RepoDir: repo.Dir()}); se != nil || rep.ExitCode() != exitOK {
		if se != nil {
			for _, m := range se.Msgs {
				fmt.Fprintf(stderr, "proctor approve: %s\n", safe(m))
			}
		}
		for _, f := range rep.Findings {
			fmt.Fprintf(stderr, "proctor approve: %s %s\n", safe(f.Rule), safe(strings.TrimPrefix(f.Path+": ", ": ")+f.Message))
		}
		return refuse("the drivers don't accept the specifications; fix them, run again, then approve")
	}
	dir, err := receivedFolder(o.received, root, rep)
	if err != nil {
		return refuse("%v", err)
	}
	doc, err := readReceivedDoc(dir)
	if err != nil {
		return refuse("%v", err)
	}
	if doc.Repository != root {
		return refuse("the received transcripts in %s come from the repository %s, not %s", dir, doc.Repository, root)
	}
	if doc.Config != rep.ConfigPath {
		return refuse("the received transcripts in %s were made with %s, not %s", dir, doc.Config, rep.ConfigPath)
	}
	if changed := compareInputs(doc.Inputs, inputHashes(rep)); len(changed) > 0 {
		for _, c := range changed {
			fmt.Fprintf(stderr, "proctor approve: %s\n", safe(c))
		}
		return refuse("the specifications or configuration changed since run %s made these transcripts; run again, then approve", doc.RunID)
	}
	if doc.ScrubSHA256 != scrubSHA(rep.Config) {
		return refuse("transcripts in %s changed since run %s; run again, then approve", rep.ConfigPath, doc.RunID)
	}
	if doc.AppLogsSHA256 != appLogsSHA(rep.Config) {
		return refuse("app_logs in %s changed since run %s; run again, then approve", rep.ConfigPath, doc.RunID)
	}
	picked, notes, err := pickApprovals(doc, ids)
	if err != nil {
		return refuse("%v", err)
	}
	var todo []approval
	for _, e := range picked {
		a, already, err := checkApproval(e, dir, repo.Dir(), rep)
		if err != nil {
			return refuse("%v", err)
		}
		if already {
			notes = append(notes, fmt.Sprintf("%s: %s already holds the received %s", e.Scenario, e.Approved, e.noun()))
			continue
		}
		todo = append(todo, a)
	}
	fmt.Fprintf(stdout, "Received:  %s (run %s, %s mode)\n", safe(dir), safe(doc.RunID), safe(doc.Mode))
	fmt.Fprintln(stdout, "Proctor can't tell whether a person answers; the approval that counts is the code-owner review of each changed file under approved/.")
	for _, n := range notes {
		fmt.Fprintf(stdout, "skipped   %s\n", safe(n))
	}
	in := bufio.NewReader(stdin)
	written := 0
	for _, a := range todo {
		noun := choice{log: a.log}.noun()
		fmt.Fprintf(stdout, "\n%s  (%s)  %s\n", safe(a.entry.Scenario), safe(a.entry.Specification), safe(a.entry.Approved))
		if a.approved == nil {
			fmt.Fprintf(stdout, "no approved %s yet; the whole received %s:\n", noun, noun)
		}
		d := transcript.Compare(a.approved, a.received, 0, false)
		for _, l := range d.Lines {
			fmt.Fprintln(stdout, safe(l))
		}
		fmt.Fprintln(stdout, d.Changed())
		if !o.yes {
			fmt.Fprintf(stdout, "Approve %s? [y/N] ", safe(a.entry.Approved))
			answer, err := in.ReadString('\n')
			if err != nil && answer == "" {
				fmt.Fprintln(stdout)
				fmt.Fprintln(stdout, "No answer; stopping.")
				break
			}
			switch strings.ToLower(strings.TrimSpace(answer)) {
			case "y", "yes":
			default:
				fmt.Fprintf(stdout, "not approved  %s\n", safe(a.entry.Approved))
				continue
			}
		}
		if err := writeApproved(repo.Dir(), rep, a); err != nil {
			fmt.Fprintf(stderr, "proctor approve: writing %s: %s\n", safe(a.entry.Approved), safe(err.Error()))
			return exitError
		}
		written++
		fmt.Fprintf(stdout, "approved  %s\n", safe(a.entry.Approved))
	}
	fmt.Fprintf(stdout, "\n%d of %d written. Review them in the pull request like any protected change.\n", written, len(todo))
	return exitOK
}

func receivedFolder(flag, root string, rep *integrity.Report) (string, error) {
	if flag != "" {
		dir, isNew, err := checkReceivedFlag(flag, root)
		switch {
		case err != nil:
			return "", err
		case isNew:
			return "", fmt.Errorf("--received %s doesn't exist", flag)
		}
		return dir, nil
	}
	tmp, base, err := defaultReceivedBase(rep.Config.Project)
	if err != nil {
		return "", err
	}
	if err := refuseInside("the temporary folder", tmp, base, root); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("no received transcripts in %s (%v); run proctor run first, or give --received", base, err)
	}
	newest, at := "", int64(0)
	for _, e := range entries {
		if !e.IsDir() || !safeSegment.MatchString(e.Name()) {
			continue
		}
		fi, err := os.Lstat(filepath.Join(base, e.Name(), receivedManifest))
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if t := fi.ModTime().UnixNano(); newest == "" || t > at {
			newest, at = e.Name(), t
		}
	}
	if newest == "" {
		return "", fmt.Errorf("no run under %s left a %s; run proctor run first, or give --received", base, receivedManifest)
	}
	return filepath.Join(base, newest), nil
}

func readPrivateFile(p string, limit int) ([]byte, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is a %s, not a regular file", p, describeMode(fi.Mode()))
	}
	f, err := os.OpenFile(p, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(fi, opened) {
		return nil, fmt.Errorf("%s changed while it was read", p)
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", p, limit)
	}
	return data, nil
}

func readReceivedDoc(dir string) (*receivedDoc, error) {
	if err := privateFolder(dir); err != nil {
		return nil, err
	}
	data, err := readPrivateFile(filepath.Join(dir, receivedManifest), maxManifestBytes)
	if err != nil {
		return nil, fmt.Errorf("reading the received transcripts' manifest: %v", err)
	}
	var doc receivedDoc
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: %v", filepath.Join(dir, receivedManifest), err)
	}
	if doc.Format != receivedFormat {
		return nil, fmt.Errorf("%s: format %q isn't %s", filepath.Join(dir, receivedManifest), doc.Format, receivedFormat)
	}
	return &doc, nil
}

func compareInputs(was, now map[string]string) []string {
	var out []string
	keys := map[string]bool{}
	for k := range was {
		keys[k] = true
	}
	for k := range now {
		keys[k] = true
	}
	var sorted []string
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		w, inWas := was[k]
		n, inNow := now[k]
		switch {
		case !inWas:
			out = append(out, k+" is new since the run")
		case !inNow:
			out = append(out, k+" is gone since the run")
		case w != n:
			out = append(out, k+" changed since the run")
		}
	}
	return out
}

func approvable(e choice) (bool, string) {
	switch {
	case e.log && e.ScenarioResult != string(runner.Pass):
		return false, fmt.Sprintf("%s was %s before its log was compared, so the log records behavior the specification or the app-log check rejects; fix it, run again, then approve", e.Scenario, e.ScenarioResult)
	case e.ScenarioResult != string(runner.Pass):
		return false, fmt.Sprintf("%s was %s before its transcript was compared, so the transcript records behavior the specification rejects; fix it, run again, then approve", e.Scenario, e.ScenarioResult)
	case e.File == "" || e.SHA256 == "":
		return false, fmt.Sprintf("%s has no received %s (%s)", e.Scenario, e.noun(), e.Result)
	}
	return true, ""
}

func pickApprovals(doc *receivedDoc, ids []string) ([]choice, []string, error) {
	var notes []string
	var all []choice
	byID := map[string][]choice{}
	for _, list := range []struct {
		entries []receivedEntry
		log     bool
	}{{doc.Transcripts, false}, {doc.Logs, true}} {
		seen := map[string]bool{}
		for _, e := range list.entries {
			p := choice{receivedEntry: e, log: list.log}
			key := e.Scenario
			if list.log {
				key = e.Specification + "\x00" + e.Scenario
			}
			if seen[key] {
				return nil, nil, fmt.Errorf("the manifest lists the %s of %s twice", p.noun(), e.Scenario)
			}
			seen[key] = true
			byID[e.Scenario] = append(byID[e.Scenario], p)
			all = append(all, p)
		}
	}
	var out []choice
	if len(ids) == 0 {
		for _, e := range all {
			if e.Result == runner.TranscriptApproved {
				continue
			}
			if ok, why := approvable(e); !ok {
				notes = append(notes, why)
				continue
			}
			out = append(out, e)
		}
		return out, notes, nil
	}
	seen := map[string]bool{}
	for _, id := range ids {
		es, ok := byID[id]
		switch {
		case !ok:
			return nil, nil, fmt.Errorf("%s has no received transcript or log in this run; the run made %d transcripts and %d logs", id, len(doc.Transcripts), len(doc.Logs))
		case seen[id]:
			return nil, nil, fmt.Errorf("%s is named twice", id)
		}
		seen[id] = true
		for _, e := range es {
			if ok, why := approvable(e); !ok {
				return nil, nil, errors.New(why)
			}
			out = append(out, e)
		}
	}
	return out, notes, nil
}

func checkApproval(p choice, dir, repoDir string, rep *integrity.Report) (approval, bool, error) {
	if p.log {
		return checkLogApproval(p.receivedEntry, dir, repoDir, rep)
	}
	e := p.receivedEntry
	a := approval{entry: e}
	var sc *model.Scenario
	for _, sf := range rep.Specs {
		if rep.SpecIDs[sf.Path] != e.Specification {
			continue
		}
		for i := range sf.Spec.Scenarios {
			if sf.Spec.Scenarios[i].ID == e.Scenario {
				sc = &sf.Spec.Scenarios[i]
			}
		}
	}
	switch {
	case sc == nil:
		return a, false, fmt.Errorf("%s isn't a scenario of specification %s any more", e.Scenario, e.Specification)
	case !sc.TranscriptWanted() || sc.Approval == nil:
		return a, false, fmt.Errorf("%s no longer sets transcript: true", e.Scenario)
	case sc.Approval.Path != e.Approved:
		return a, false, fmt.Errorf("%s's approved transcript is %s, but the run recorded %s", e.Scenario, sc.Approval.Path, e.Approved)
	}
	if e.File != e.Specification+"/"+e.Scenario+model.TranscriptSuffix {
		return a, false, fmt.Errorf("the manifest names %q for %s, not the file a run writes", e.File, e.Scenario)
	}
	limit := rep.Config.Transcripts.Limit()
	data, err := readPrivateFile(filepath.Join(dir, filepath.FromSlash(e.File)), limit)
	if err != nil {
		return a, false, err
	}
	if hexSum(data) != e.SHA256 {
		return a, false, fmt.Errorf("%s changed after the run wrote it (sha256 %s, the run recorded %s)", e.File, hexSum(data), e.SHA256)
	}
	if !bytes.HasPrefix(data, []byte(model.TranscriptFirstLine+"\n")) {
		return a, false, fmt.Errorf("%s isn't a transcript", e.File)
	}
	a.received = data
	cur := ""
	if sc.Approval.Found {
		cur = sc.Approval.SHA256
		a.approved = sc.Approval.Data
	}
	if cur == e.SHA256 {
		return a, true, nil
	}
	if cur != e.ApprovedSHA256 {
		return a, false, fmt.Errorf("%s changed since the run (sha256 %s, the run compared with %s); run again, then approve", e.Approved, orNone(cur), orNone(e.ApprovedSHA256))
	}
	a.target = filepath.Join(repoDir, filepath.FromSlash(e.Approved))
	return a, false, nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func hexSum(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func writeApproved(repoDir string, rep *integrity.Report, a approval) error {
	rel := a.entry.Approved
	if !rep.IsApproved(rel) {
		return fmt.Errorf("%s isn't under %s/", rel, rep.ApprovedRoot())
	}
	parts := strings.Split(rel, "/")
	cur := repoDir
	for i, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist) && i >= len(strings.Split(rep.ProtectedRoot, "/")):
			if err := os.Mkdir(cur, 0o755); err != nil {
				return err
			}
			if fi, err = os.Lstat(cur); err != nil {
				return err
			}
		case err != nil:
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("%s is a %s, not a folder; approved files are written only through real folders of the repository", cur, describeMode(fi.Mode()))
		}
	}
	if err := writePrivate(a.target, a.received); err != nil {
		return err
	}
	fi, err := os.Lstat(a.target)
	if err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("%s isn't a regular file after writing it", a.target)
	}
	return nil
}

func checkLogApproval(e receivedEntry, dir, repoDir string, rep *integrity.Report) (approval, bool, error) {
	a := approval{entry: e, log: true}
	var owner *model.Scenario
	for _, sf := range rep.Specs {
		if rep.SpecIDs[sf.Path] != e.Specification {
			continue
		}
		for i := range sf.Spec.Scenarios {
			sc := &sf.Spec.Scenarios[i]
			if sc.ApprovedLogWanted() && sc.ApprovedLogOwner() == e.Scenario && (sc.Case == nil || sc.Case.FirstRow) {
				owner = sc
			}
		}
	}
	switch {
	case owner == nil:
		return a, false, fmt.Errorf("%s is no longer a scenario or table of specification %s that sets approved_log: true", e.Scenario, e.Specification)
	case owner.LogApproval == nil:
		return a, false, fmt.Errorf("%s's approved log wasn't loaded with the specifications", e.Scenario)
	case owner.LogApproval.Path != e.Approved:
		return a, false, fmt.Errorf("%s's approved log is %s, but the run recorded %s", e.Scenario, owner.LogApproval.Path, e.Approved)
	}
	if e.File != e.Specification+"/"+e.Scenario+model.ApprovedLogSuffix {
		return a, false, fmt.Errorf("the manifest names %q for the log of %s, not the file a run writes", e.File, e.Scenario)
	}
	data, err := readPrivateFile(filepath.Join(dir, filepath.FromSlash(e.File)), rep.Config.AppLogs.ApprovedSettings().Limit())
	if err != nil {
		return a, false, err
	}
	if hexSum(data) != e.SHA256 {
		return a, false, fmt.Errorf("%s changed after the run wrote it (sha256 %s, the run recorded %s)", e.File, hexSum(data), e.SHA256)
	}
	if err := applogs.ValidApprovedLog(data); err != nil {
		return a, false, fmt.Errorf("%s isn't an approved log: %v", e.File, err)
	}
	a.received = data
	cur := ""
	if owner.LogApproval.Found {
		cur = owner.LogApproval.SHA256
		a.approved = owner.LogApproval.Data
	}
	if cur == e.SHA256 {
		return a, true, nil
	}
	if cur != e.ApprovedSHA256 {
		return a, false, fmt.Errorf("%s changed since the run (sha256 %s, the run compared with %s); run again, then approve", e.Approved, orNone(cur), orNone(e.ApprovedSHA256))
	}
	a.target = filepath.Join(repoDir, filepath.FromSlash(e.Approved))
	return a, false, nil
}
