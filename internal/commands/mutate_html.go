package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"proctor/internal/gitrepo"
	"proctor/internal/mutate"
	"proctor/internal/mutgen"
	"proctor/internal/mutreport"
)

type mutateHTMLOptions struct {
	from, out, coverage string
	force               bool
}

const maxMutateReport = 512 << 20

func newMutateHTMLCmd(stdout, stderr io.Writer) *cobra.Command {
	var o mutateHTMLOptions
	cmd := &cobra.Command{
		Use:   "html",
		Short: "Draw a JSON report of proctor mutate as HTML pages: a summary, and each mutated file's source with its mutants",
		Example: `  proctor mutate html --from /tmp/mutate.json --out /tmp/mutate-html
  proctor mutate html --from /tmp/mutate.json --out /tmp/mutate-html --coverage /tmp/cover.txt --force`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitWith(mutateHTMLRun(o, stdout, stderr))
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.from, "from", "", "the JSON report proctor mutate --report wrote (required)")
	f.StringVar(&o.out, "out", "", "the folder to write the pages to, outside the repository; new or empty unless --force (required)")
	f.StringVar(&o.coverage, "coverage", "", "a Go coverage profile in text form; lines the specifications never ran are greyed")
	f.BoolVar(&o.force, "force", false, "write into a folder that isn't empty, replacing the pages' files in it")
	return cmd
}

func mutateHTMLRun(o mutateHTMLOptions, stdout, stderr io.Writer) int {
	refuse := func(code int, msg string) int {
		fmt.Fprintln(stderr, "proctor mutate html: "+safe(msg))
		return code
	}
	if o.from == "" || o.out == "" {
		return refuse(exitInvalid, "--from and --out are required: --from names the JSON report of proctor mutate, --out the folder for the pages")
	}
	doc, err := readMutateReport(o.from)
	if err != nil {
		return refuse(exitInvalid, err.Error())
	}
	repo, err := gitrepo.Open(doc.Provenance.Repository)
	if err != nil {
		return refuse(exitInvalid, fmt.Sprintf("the report's repository %s can't be opened: %v", doc.Provenance.Repository, err))
	}
	root, err := filepath.EvalSymlinks(repo.Dir())
	if err != nil {
		return refuse(exitInvalid, err.Error())
	}
	out, err := htmlOutDir(o.out, root, o.force)
	if err != nil {
		return refuse(exitInvalid, "--out "+err.Error())
	}
	files, err := mutate.ListFiles(repo.Dir())
	if err != nil {
		return refuse(exitError, err.Error())
	}
	sources, err := readMutatedSources(root, files, doc.Mutants)
	if err != nil {
		return refuse(exitInvalid, err.Error())
	}
	var warnings []string
	var cov mutreport.Coverage
	if o.coverage != "" {
		c, err := mutgen.ReadCoverage(o.coverage, root, files)
		if err != nil {
			return refuse(exitInvalid, err.Error())
		}
		if n := c.Unmatched(); n > 0 {
			warnings = append(warnings, fmt.Sprintf("%d files named in the coverage profile aren't files of the repository; they were ignored", n))
		}
		cov = c
	}
	run := mutreport.Run{
		ProctorVersion: doc.Provenance.ProctorVersion, RenderedBy: Version, Repository: root,
		Config: doc.Provenance.Config, MutantsFile: doc.Provenance.Mutants,
		StartedAt: doc.Provenance.StartedAt, EndedAt: doc.Provenance.EndedAt, DurationMS: doc.Provenance.DurationMS,
		Result: doc.Result, ExitCode: doc.ExitCode, Stopped: doc.Stopped, Baseline: resultLine(doc.Baseline), Coverage: o.coverage,
	}
	pages, more, err := mutreport.Render(mutreport.Input{Run: run, Mutants: doc.Mutants, Sources: sources, Coverage: cov})
	if err != nil {
		var se *mutreport.StaleError
		if errors.As(err, &se) {
			return refuse(exitError, err.Error())
		}
		return refuse(exitError, "drawing the pages: "+err.Error())
	}
	warnings = append(warnings, more...)
	inputs := []string{o.from}
	if o.coverage != "" {
		inputs = append(inputs, o.coverage)
	}
	for _, p := range pages {
		target := filepath.Join(out, p.Name)
		if err := refuseNonRegular(target); err != nil {
			return refuse(exitInvalid, err.Error())
		}
		for _, in := range inputs {
			if sameFile(target, in) {
				return refuse(exitInvalid, fmt.Sprintf("%s would overwrite the input %s; choose another --out", target, in))
			}
		}
	}
	if err := os.Mkdir(out, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return refuse(exitError, err.Error())
	}
	if fi, err := os.Lstat(out); err != nil || !fi.IsDir() {
		return refuse(exitError, fmt.Sprintf("%s isn't a folder any more", out))
	}
	for _, p := range pages {
		if err := writePrivate(filepath.Join(out, p.Name), p.Data); err != nil {
			return refuse(exitError, "writing the pages: "+err.Error())
		}
	}
	for _, w := range warnings {
		fmt.Fprintln(stderr, "proctor mutate html: warning: "+safe(w))
	}
	fmt.Fprintf(stdout, "Wrote %d files (%d file pages) to %s; open %s\n", len(pages), len(pages)-1, safe(out), safe(filepath.Join(out, "index.html")))
	return exitOK
}

func readMutateReport(p string) (*mutateDoc, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("--from: %v", err)
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("--from %s isn't a regular file", p)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxMutateReport+1))
	if err != nil {
		return nil, fmt.Errorf("--from: %v", err)
	}
	if len(data) > maxMutateReport {
		return nil, fmt.Errorf("--from %s is over %d MiB, too large for a report of proctor mutate", p, maxMutateReport>>20)
	}
	notReport := func(why string) error {
		return fmt.Errorf("--from %s isn't a report of proctor mutate: %s", p, why)
	}
	var doc mutateDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, notReport("it isn't its JSON (" + err.Error() + ")")
	}
	if doc.Command != "mutate" || doc.Outcome == nil || doc.Mutants == nil || doc.Counts == nil {
		return nil, notReport("it has no \"command\": \"mutate\" with a mutants list and counts (a refused run's report has only its errors)")
	}
	if doc.Provenance.Repository == "" {
		return nil, fmt.Errorf("--from %s has no provenance.repository and no edits for its mutants: it was written by a Proctor older than mutate html; rerun proctor mutate with this build", p)
	}
	if !filepath.IsAbs(doc.Provenance.Repository) {
		return nil, notReport("its provenance.repository isn't an absolute path")
	}
	counts := map[mutate.Verdict]int{}
	for _, m := range doc.Mutants {
		if !mutreport.Known(m.Verdict) {
			return nil, notReport(fmt.Sprintf("mutant %s has the verdict %q, which isn't one proctor mutate gives", m.ID, m.Verdict))
		}
		if len(m.Edits) == 0 {
			return nil, fmt.Errorf("--from %s: mutant %s has no edits: the report was written by a Proctor older than mutate html; rerun proctor mutate with this build", p, m.ID)
		}
		for _, e := range m.Edits {
			if !mutreport.CleanPath(e.File) {
				return nil, notReport(fmt.Sprintf("mutant %s edits %q, which isn't a clean repository-relative path", m.ID, e.File))
			}
		}
		counts[m.Verdict]++
	}
	for v, n := range doc.Counts {
		if counts[v] != n {
			return nil, notReport(fmt.Sprintf("its counts say %d %s, but its mutants list has %d", n, v, counts[v]))
		}
	}
	for v, n := range counts {
		if doc.Counts[v] != n {
			return nil, notReport(fmt.Sprintf("its counts say %d %s, but its mutants list has %d", doc.Counts[v], v, n))
		}
	}
	return &doc, nil
}

func htmlOutDir(p, root string, force bool) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	existing := abs
	fi, err := os.Lstat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		existing = filepath.Dir(abs)
		pfi, err := os.Stat(existing)
		if err != nil || !pfi.IsDir() {
			return "", fmt.Errorf("%s: its folder %s doesn't exist; create it first", p, existing)
		}
	case err != nil:
		return "", err
	case fi.Mode()&fs.ModeSymlink != 0:
		return "", fmt.Errorf("%s is a symbolic link; give a real folder, new or empty", p)
	case !fi.IsDir():
		return "", fmt.Errorf("%s isn't a folder", p)
	default:
		entries, err := os.ReadDir(abs)
		if err != nil {
			return "", err
		}
		if len(entries) > 0 && !force {
			return "", fmt.Errorf("%s isn't empty (%d entries); give a new or empty folder, or --force to replace the pages' files in it", p, len(entries))
		}
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	if existing != abs {
		real = filepath.Join(real, filepath.Base(abs))
	}
	inside := func() bool {
		if r, err := filepath.Rel(root, real); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return true
		}
		rfi, err := os.Stat(root)
		if err != nil {
			return true
		}
		for d := existing; ; {
			if dfi, err := os.Stat(d); err == nil && os.SameFile(dfi, rfi) {
				return true
			}
			up := filepath.Dir(d)
			if up == d {
				return false
			}
			d = up
		}
	}
	if inside() {
		return "", fmt.Errorf("%s is inside the repository the report names (%s); the pages hold its source code, which shouldn't land in a commit, so write them outside it", p, root)
	}
	return real, nil
}

func readMutatedSources(root string, files []string, ms []mutate.Result) (map[string]string, error) {
	listed := map[string]bool{}
	for _, f := range files {
		listed[f] = true
	}
	out := map[string]string{}
	for _, f := range mutreport.Files(ms) {
		if !listed[f] {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(f))
		fi, err := os.Lstat(full)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		real, err := filepath.EvalSymlinks(full)
		if err != nil {
			return nil, err
		}
		if r, err := filepath.Rel(root, real); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s resolves to %s, outside the repository; Proctor reads only the repository's own files", f, real)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		out[f] = string(data)
	}
	return out, nil
}

func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}
