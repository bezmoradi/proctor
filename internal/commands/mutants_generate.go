package commands

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"proctor/internal/gitrepo"
	"proctor/internal/integrity"
	"proctor/internal/model"
	"proctor/internal/mutate"
	"proctor/internal/mutgen"
	"proctor/internal/strictyaml"
)

type generateOptions struct {
	common
	out, index, outJSON, coverage string
	include, exclude              []string
	buildCheck, force             bool
	goos, goarch                  string
	jobs, limit                   int
	seed                          uint64
}

func newMutantsCmd(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mutants",
		Short: "Work with the mutants proctor mutate runs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SetOut(stderr)
			_ = cmd.Help()
			return exitCode(exitInvalid)
		},
	}
	cmd.AddCommand(newMutantsGenerateCmd(stdout, stderr))
	return cmd
}

func newMutantsGenerateCmd(stdout, stderr io.Writer) *cobra.Command {
	var o generateOptions
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Read the project's Go code and write candidate mutants to a separate file, for a person to review and merge into the mutants file",
		Example: `  proctor mutants generate --out /tmp/gen.yaml --coverage /tmp/cover.txt --build-check
  proctor mutants generate --out /tmp/gen.yaml --index /tmp/gen.md --limit 100 --seed 7 --exclude 'dev/**'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitWith(generateRun(o, stdout, stderr))
		},
	}
	addCommon(cmd, &o.common)
	f := cmd.Flags()
	f.StringVar(&o.out, "out", "", "where to write the generated mutants as YAML (required; never the configured mutants file)")
	f.StringVar(&o.index, "index", "", "also write an index of the mutants, a .csv or .md table of id, file:line, operator, function and coverage")
	f.StringVar(&o.outJSON, "out-json", "", "also write the generated mutants as JSON")
	f.StringVar(&o.coverage, "coverage", "", "a Go coverage profile in text form (go tool covdata textfmt, or -coverprofile); only covered lines get mutants")
	f.BoolVar(&o.buildCheck, "build-check", false, "keep a mutant only if go build ./... and go vet of its package pass on a copy with it applied")
	f.StringVar(&o.goos, "goos", "linux", "GOOS for the build check and for choosing which files are built")
	f.StringVar(&o.goarch, "goarch", "arm64", "GOARCH for the build check and for choosing which files are built")
	f.IntVar(&o.jobs, "jobs", 4, "how many build checks run at once, each in its own copy")
	f.IntVar(&o.limit, "limit", 300, "the most mutants to write")
	f.Uint64Var(&o.seed, "seed", 1, "the seed of the order mutants are chosen in; the same input and flags give the same file")
	f.StringSliceVar(&o.include, "include", nil, "only files matching these path globs (** matches any folders), comma-separated or repeated")
	f.StringSliceVar(&o.exclude, "exclude", nil, "skip files matching these path globs, comma-separated or repeated")
	f.BoolVar(&o.force, "force", false, "allow --out, --index and --out-json inside the protected root (never the configured mutants file)")
	return cmd
}

var goName = regexp.MustCompile(`^[a-z0-9]+$`)

func generateRun(o generateOptions, stdout, stderr io.Writer) int {
	refuse := func(msg string) int {
		fmt.Fprintln(stderr, "proctor mutants generate: "+safe(msg))
		return exitInvalid
	}
	if o.out == "" {
		return refuse("--out is required: the generated mutants go to a separate file for a person to review, never into the mutants file itself")
	}
	if o.limit < 1 || o.jobs < 1 {
		return refuse("--limit and --jobs must be 1 or more")
	}
	if !goName.MatchString(o.goos) || !goName.MatchString(o.goarch) {
		return refuse("--goos and --goarch must be Go platform names, such as linux and arm64")
	}
	repo, err := gitrepo.Open(o.repo)
	if err != nil {
		return refuse(err.Error())
	}
	root, err := filepath.EvalSymlinks(repo.Dir())
	if err != nil {
		return refuse(err.Error())
	}
	rep := integrity.Load(integrity.WorkTreeSource{Root: repo.Dir()}, o.config)
	if rep.ProtectedRoot == "" {
		return refuse(fmt.Sprintf("%s isn't a usable configuration path, so the protected root is unknown", o.config))
	}
	var extra []string
	mutantsFile := ""
	if rep.Config != nil {
		extra = rep.Config.ProtectedPaths
		if m := rep.Config.Mutation; m != nil && m.Mutants != "" {
			mutantsFile = rep.ProtectedRoot + "/" + m.Mutants
		}
	}
	protected := func(p string) bool { return integrity.Protected(p, rep.ProtectedRoot, extra) }
	outputs := []struct{ flag, path string }{{"--out", o.out}, {"--index", o.index}, {"--out-json", o.outJSON}}
	seen := map[string]string{}
	for _, out := range outputs {
		if out.path == "" {
			continue
		}
		abs, rel, err := resolveOutput(root, out.path)
		if err != nil {
			return refuse(out.flag + ": " + err.Error())
		}
		if prev, ok := seen[abs]; ok {
			return refuse(fmt.Sprintf("%s and %s name the same file", prev, out.flag))
		}
		seen[abs] = out.flag
		if rel != "" && mutantsFile != "" && rel == mutantsFile {
			return refuse(fmt.Sprintf("%s is %s, the configured mutants file; generated mutants go to a separate file for a person to review and merge, so choose another path", out.flag, mutantsFile))
		}
		if rel != "" && protected(rel) && !o.force {
			return refuse(fmt.Sprintf("%s %s is inside the protected inputs (%s/ and the other protected paths); write it elsewhere, or pass --force", out.flag, rel, rep.ProtectedRoot))
		}
		if o.coverage != "" {
			if cabs, _, err := resolveOutput(root, o.coverage); err == nil && cabs == abs {
				return refuse(out.flag + " is the coverage profile")
			}
		}
		if err := refuseNonRegular(out.path); err != nil {
			return refuse(err.Error())
		}
	}
	switch ext := strings.ToLower(filepath.Ext(o.index)); {
	case o.index == "", ext == ".csv", ext == ".md", ext == ".markdown":
	default:
		return refuse("--index must end in .csv or .md, which picks the table's format")
	}
	files, err := mutate.ListFiles(repo.Dir())
	if err != nil {
		return refuse(err.Error())
	}
	var cov *mutgen.Coverage
	if o.coverage != "" {
		if cov, err = mutgen.ReadCoverage(o.coverage, root, files); err != nil {
			return refuse(err.Error())
		}
	}
	var warnings []string
	if rep.Config != nil && rep.Config.Mutation != nil && rep.Mutants == nil && mutantsFile != "" {
		warnings = append(warnings, fmt.Sprintf("%s didn't load, so generated mutants aren't checked against it for repeats; run proctor validate", mutantsFile))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	began := time.Now()
	res, err := mutgen.Run(ctx, mutgen.Options{
		Root: root, Files: files, Protected: protected, Include: o.include, Exclude: o.exclude,
		Coverage: cov, GOOS: o.goos, GOARCH: o.goarch, Seed: o.seed, Limit: o.limit,
		Existing: rep.Mutants, BuildCheck: o.buildCheck, Jobs: o.jobs, Log: stderr,
	})
	if err != nil {
		fmt.Fprintln(stderr, "proctor mutants generate: "+safe(err.Error()))
		if strings.HasPrefix(err.Error(), "--") {
			return exitInvalid
		}
		return exitError
	}
	warnings = append(warnings, res.Warnings...)
	if cov == nil {
		warnings = append(warnings, "no --coverage: mutants are placed on every line, including lines no scenario runs, which can only survive; give a profile (build with -cover, run the scenarios with GOCOVERDIR set, then go tool covdata textfmt)")
	}
	if !o.buildCheck {
		warnings = append(warnings, "no --build-check: the mutants weren't built, so some may not compile or pass go vet, and proctor mutate stops at the first up script that fails")
	}
	doc, data, err := encodeGenerated(res.Mutants, root)
	if err != nil {
		fmt.Fprintln(stderr, "proctor mutants generate: "+safe(err.Error()))
		return exitError
	}
	if err := writePrivate(o.out, data); err != nil {
		fmt.Fprintln(stderr, "proctor mutants generate: "+safe(err.Error()))
		return exitError
	}
	if o.outJSON != "" {
		js, err := json.MarshalIndent(doc, "", "  ")
		if err == nil {
			err = writePrivate(o.outJSON, append(js, '\n'))
		}
		if err != nil {
			fmt.Fprintln(stderr, "proctor mutants generate: "+safe(err.Error()))
			return exitError
		}
	}
	if o.index != "" {
		if err := writePrivate(o.index, indexTable(res.Mutants, o.index)); err != nil {
			fmt.Fprintln(stderr, "proctor mutants generate: "+safe(err.Error()))
			return exitError
		}
	}
	for _, w := range warnings {
		fmt.Fprintln(stderr, "proctor mutants generate: warning: "+safe(w))
	}
	sum := generateSummary{
		Repository: root, Out: o.out, Index: o.index, OutJSON: o.outJSON, Coverage: o.coverage,
		BuildCheck: o.buildCheck, GOOS: o.goos, GOARCH: o.goarch, Seed: o.seed, Limit: o.limit,
		FilesScanned: res.Files, FilesSkipped: res.Skipped, Candidates: res.Candidates, Dropped: res.Dropped,
		BuildChecked: res.BuildCheck, Written: len(res.Mutants), PerOperator: mutgen.Counts(res.Mutants),
		Warnings: warnings, DurationMS: time.Since(began).Milliseconds(),
	}
	if o.json {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(sum)
		return exitOK
	}
	writeGenerateSummary(stdout, sum)
	return exitOK
}

func resolveOutput(root, p string) (abs, rel string, err error) {
	a, err := filepath.Abs(p)
	if err != nil {
		return "", "", err
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(a))
	if err != nil {
		return "", "", fmt.Errorf("the folder of %s: %v", p, err)
	}
	abs = filepath.Join(dir, filepath.Base(a))
	if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
	}
	r, err := filepath.Rel(root, abs)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return abs, "", nil
	}
	if r == "." {
		return "", "", fmt.Errorf("%s is the repository folder", p)
	}
	return abs, filepath.ToSlash(r), nil
}

type genEdit struct {
	File    string `yaml:"file" json:"file"`
	Line    int    `yaml:"line" json:"line"`
	Find    string `yaml:"find" json:"find"`
	Replace string `yaml:"replace" json:"replace"`
}

type genMutant struct {
	ID    string    `yaml:"id" json:"id"`
	Title string    `yaml:"title" json:"title"`
	Edits []genEdit `yaml:"edits" json:"edits"`
}

func encodeGenerated(ms []mutgen.Mutant, root string) ([]genMutant, []byte, error) {
	doc := make([]genMutant, 0, len(ms))
	for _, m := range ms {
		doc = append(doc, genMutant{ID: m.ID, Title: m.Title, Edits: []genEdit{{File: m.File, Line: m.Line, Find: m.Find, Replace: m.Replace}}})
	}
	if len(doc) == 0 {
		return doc, []byte("[]\n"), nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, nil, err
	}
	var back []model.Mutant
	if err := strictyaml.Decode(buf.Bytes(), &back); err != nil {
		return nil, nil, fmt.Errorf("the generated YAML doesn't read back: %v", err)
	}
	if len(back) != len(doc) {
		return nil, nil, fmt.Errorf("the generated YAML reads back as %d mutants, not %d", len(back), len(doc))
	}
	texts := map[string]string{}
	for i, b := range back {
		g := doc[i]
		if b.ID != g.ID || b.Title != g.Title || len(b.Edits) != 1 || b.KilledBy != nil {
			return nil, nil, fmt.Errorf("mutant %s doesn't read back as written", g.ID)
		}
		e := b.Edits[0]
		if e.File != g.Edits[0].File || e.Find != g.Edits[0].Find || e.Replace != g.Edits[0].Replace || e.Line == nil || *e.Line != g.Edits[0].Line || e.Count != nil {
			return nil, nil, fmt.Errorf("mutant %s's edit doesn't read back as written", g.ID)
		}
		if !model.MutantID.MatchString(b.ID) {
			return nil, nil, fmt.Errorf("mutant id %s isn't a valid id", b.ID)
		}
		text, ok := texts[e.File]
		if !ok {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(e.File)))
			if err != nil {
				return nil, nil, err
			}
			text = string(data)
			texts[e.File] = text
		}
		if _, err := model.LocateAtLine(text, e.Find, *e.Line); err != nil {
			return nil, nil, fmt.Errorf("mutant %s: %s: %v (did the file change while generating?)", b.ID, e.File, err)
		}
	}
	return doc, buf.Bytes(), nil
}

func indexTable(ms []mutgen.Mutant, p string) []byte {
	var buf bytes.Buffer
	if strings.EqualFold(filepath.Ext(p), ".csv") {
		w := csv.NewWriter(&buf)
		_ = w.Write([]string{"id", "location", "operator", "function", "covered"})
		for _, m := range ms {
			_ = w.Write([]string{m.ID, fmt.Sprintf("%s:%d", m.File, m.Line), m.Operator, m.Func, m.Covered})
		}
		w.Flush()
		return buf.Bytes()
	}
	cell := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "|", "\\|") }
	buf.WriteString("| id | location | operator | function | covered |\n|---|---|---|---|---|\n")
	for _, m := range ms {
		fmt.Fprintf(&buf, "| %s | `%s:%d` | %s | `%s` | %s |\n", m.ID, cell(m.File), m.Line, m.Operator, cell(m.Func), m.Covered)
	}
	return buf.Bytes()
}

type generateSummary struct {
	Repository   string         `json:"repository"`
	Out          string         `json:"out"`
	Index        string         `json:"index,omitempty"`
	OutJSON      string         `json:"out_json,omitempty"`
	Coverage     string         `json:"coverage,omitempty"`
	BuildCheck   bool           `json:"build_check"`
	GOOS         string         `json:"goos"`
	GOARCH       string         `json:"goarch"`
	Seed         uint64         `json:"seed"`
	Limit        int            `json:"limit"`
	FilesScanned int            `json:"files_scanned"`
	FilesSkipped map[string]int `json:"files_skipped"`
	Candidates   map[string]int `json:"candidates"`
	Dropped      map[string]int `json:"dropped"`
	BuildChecked map[string]int `json:"build_checked"`
	Written      int            `json:"written"`
	PerOperator  map[string]int `json:"per_operator"`
	Warnings     []string       `json:"warnings"`
	DurationMS   int64          `json:"duration_ms"`
}

func writeGenerateSummary(w io.Writer, s generateSummary) {
	total := 0
	for _, n := range s.Candidates {
		total += n
	}
	fmt.Fprintf(w, "PROCTOR MUTANTS GENERATE\n\nRepository:    %s\nFiles scanned: %d Go files\n", safe(s.Repository), s.FilesScanned)
	for _, k := range mutgen.SortedKeys(s.FilesSkipped) {
		fmt.Fprintf(w, "  skipped %-6d %s\n", s.FilesSkipped[k], safe(k))
	}
	fmt.Fprintf(w, "Candidates:    %d\n", total)
	for _, op := range mutgen.Operators {
		if n := s.Candidates[op]; n > 0 {
			fmt.Fprintf(w, "  %-12s %d\n", op, n)
		}
	}
	cov := "none"
	if s.Coverage != "" {
		cov = s.Coverage
	}
	fmt.Fprintf(w, "Coverage:      %s\n", safe(cov))
	if len(s.Dropped) > 0 {
		fmt.Fprintln(w, "Not used:")
		for _, k := range mutgen.SortedKeys(s.Dropped) {
			fmt.Fprintf(w, "  %-6d %s\n", s.Dropped[k], safe(k))
		}
	}
	if s.BuildCheck {
		dropped := 0
		for k, n := range s.BuildChecked {
			if strings.HasPrefix(k, "dropped") {
				dropped += n
			}
		}
		fmt.Fprintf(w, "Build check:   GOOS=%s GOARCH=%s CGO_ENABLED=0, %d checked, %d dropped\n", s.GOOS, s.GOARCH, s.BuildChecked["checked"], dropped)
		for _, k := range mutgen.SortedKeys(s.BuildChecked) {
			if k != "checked" {
				fmt.Fprintf(w, "  %-6d %s\n", s.BuildChecked[k], safe(k))
			}
		}
	} else {
		fmt.Fprintln(w, "Build check:   off")
	}
	fmt.Fprintf(w, "Written:       %d mutants to %s (limit %d, seed %d)\n", s.Written, safe(s.Out), s.Limit, s.Seed)
	for _, op := range mutgen.Operators {
		if n := s.PerOperator[op]; n > 0 {
			fmt.Fprintf(w, "  %-12s %d\n", op, n)
		}
	}
	if s.Index != "" {
		fmt.Fprintf(w, "Index:         %s\n", safe(s.Index))
	}
	if s.OutJSON != "" {
		fmt.Fprintf(w, "JSON:          %s\n", safe(s.OutJSON))
	}
	fmt.Fprintf(w, "Took:          %s\n", (time.Duration(s.DurationMS) * time.Millisecond).Round(100*time.Millisecond))
	fmt.Fprintln(w, "Review each mutant before adding it to the mutants file; proctor validate checks the merged file.")
}
