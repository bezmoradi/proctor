package mutreport

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"path"
	"sort"
	"strings"
	"time"

	"proctor/internal/model"
	"proctor/internal/mutate"
	"proctor/internal/mutgen"
)

//go:embed assets/*.tmpl
var assets embed.FS

//go:embed assets/style.css
var styleCSS string

var pages = template.Must(template.ParseFS(assets, "assets/*.tmpl"))

type styleView struct {
	CSS  template.CSS
	Hash string
}

func inlineStyle() styleView {
	h := sha256.Sum256([]byte(styleCSS))
	return styleView{CSS: template.CSS(styleCSS), Hash: "sha256-" + base64.StdEncoding.EncodeToString(h[:])}
}

type Coverage interface {
	Profiled(file string) bool
	LineRan(file string, line int) (tracked, ran bool)
}

type Run struct {
	ProctorVersion string
	RenderedBy     string
	Repository     string
	Config         string
	MutantsFile    string
	StartedAt      string
	EndedAt        string
	DurationMS     int64
	Result         string
	ExitCode       int
	Stopped        string
	Baseline       string
	Coverage       string
}

type Input struct {
	Run      Run
	Mutants  []mutate.Result
	Sources  map[string]string
	Coverage Coverage
}

type Page struct {
	Name string
	Data []byte
}

type StaleError struct {
	Mutant, File, Reason string
}

func (e *StaleError) Error() string {
	return fmt.Sprintf("mutant %s: %s: %s; the source changed since the run, so the page would show the wrong lines; rerun proctor mutate, or render with the source as it was", e.Mutant, e.File, e.Reason)
}

var Verdicts = []mutate.Verdict{mutate.Survived, mutate.KilledElsewhere, mutate.Errored, mutate.NotRun, mutate.Killed}

func Label(v mutate.Verdict) string {
	return strings.ReplaceAll(string(v), "_", " ")
}

func severity(v mutate.Verdict) int {
	for i, w := range Verdicts {
		if w == v {
			return i
		}
	}
	return len(Verdicts)
}

func Known(v mutate.Verdict) bool { return severity(v) < len(Verdicts) }

type verdictView struct {
	Verdict string
	Label   string
}

type countView struct {
	verdictView
	N int
}

type mutantView struct {
	verdictView
	Anchor   string
	ID       string
	Title    string
	Operator string
	Message  string
	KilledBy []string
	Failed   []string
	Errors   []string
	Duration string
	Hunks    []Hunk
	Reason   string
	Changed  bool
}

type lineView struct {
	N        int
	Text     string
	Class    string
	Coverage string
	Verdicts []verdictView
	Mutants  []mutantView
}

type linkView struct {
	verdictView
	ID    string
	Title string
	Href  string
	File  string
	Line  int
}

type fileRow struct {
	Path   string
	Href   string
	Counts []int
	Total  int
	sort   int
}

type unplacedView struct {
	verdictView
	ID     string
	Title  string
	File   string
	Reason string
}

type headView struct {
	Title string
	Run   Run
	Style styleView
}

type indexView struct {
	headView
	Columns   []verdictView
	Counts    []countView
	Total     int
	Score     string
	Duration  string
	Survivors []linkView
	Files     []fileRow
	Unplaced  []unplacedView
}

type fileView struct {
	headView
	Path        string
	Columns     []verdictView
	Counts      []int
	Lines       []lineView
	Unplaced    []mutantView
	Changed     bool
	HasCoverage bool
	Profiled    bool
}

type placement struct {
	mutant int
	hunks  []Hunk
}

type fileState struct {
	path     string
	page     string
	text     string
	changed  bool
	mutants  map[int]bool
	placed   []placement
	unplaced []placement
	reasons  map[int]string
}

func Render(in Input) ([]Page, []string, error) {
	files := map[string]*fileState{}
	var order []string
	var warnings []string
	var homeless []unplacedView
	firstHref := map[int]string{}
	firstLine := map[int]int{}
	firstFile := map[int]string{}
	for i, m := range in.Mutants {
		byFile, fileOrder := groupEdits(m.Edits)
		for _, f := range fileOrder {
			idx := byFile[f]
			applied, before := appliedHash(m, f)
			text, present := in.Sources[f]
			if !present {
				if applied {
					return nil, nil, &StaleError{Mutant: m.ID, File: f, Reason: "the file is no longer in the repository's files"}
				}
				homeless = append(homeless, unplacedView{verdictView: verdict(m.Verdict), ID: m.ID, Title: m.Title, File: f, Reason: "the file isn't in the repository's files"})
				continue
			}
			st := files[f]
			if st == nil {
				st = &fileState{path: f, text: text, mutants: map[int]bool{}, reasons: map[int]string{}}
				files[f] = st
				order = append(order, f)
			}
			st.mutants[i] = true
			changed := applied && before != hashOf(text)
			if changed && !st.changed {
				st.changed = true
				warnings = append(warnings, fmt.Sprintf("%s changed since the run (its sha256 differs from the one the run recorded); each mutant's find is still where the run applied it, so the page shows the current file", f))
			}
			es := make([]model.Edit, len(idx))
			for k, j := range idx {
				es[k] = m.Edits[j]
			}
			hs, err := hunks(text, es, idx)
			if err != nil {
				if applied {
					return nil, nil, &StaleError{Mutant: m.ID, File: f, Reason: err.Error()}
				}
				st.unplaced = append(st.unplaced, placement{mutant: i})
				st.reasons[i] = err.Error()
				continue
			}
			st.placed = append(st.placed, placement{mutant: i, hunks: hs})
		}
	}
	sort.Strings(order)
	for n, f := range order {
		files[f].page = fmt.Sprintf("file-%04d.html", n+1)
	}
	for _, f := range order {
		st := files[f]
		for _, p := range st.placed {
			if _, ok := firstHref[p.mutant]; ok {
				continue
			}
			firstHref[p.mutant] = st.page + "#" + anchor(p.mutant, 0)
			firstFile[p.mutant] = f
			firstLine[p.mutant] = p.hunks[0].Line
		}
	}
	for _, f := range order {
		st := files[f]
		for _, p := range st.unplaced {
			if _, ok := firstHref[p.mutant]; !ok {
				firstHref[p.mutant] = st.page + "#" + anchor(p.mutant, 0)
				firstFile[p.mutant] = f
			}
		}
	}
	style := inlineStyle()
	head := func(title string) headView { return headView{Title: title, Run: in.Run, Style: style} }
	var out []Page
	idx := indexView{headView: head("Mutation report"), Columns: columns(), Total: len(in.Mutants), Duration: duration(in.Run.DurationMS), Unplaced: homeless}
	counts := map[mutate.Verdict]int{}
	for _, m := range in.Mutants {
		counts[m.Verdict]++
	}
	for _, v := range Verdicts {
		idx.Counts = append(idx.Counts, countView{verdictView: verdict(v), N: counts[v]})
	}
	idx.Score = score(counts)
	for i, m := range in.Mutants {
		if m.Verdict != mutate.Survived && m.Verdict != mutate.KilledElsewhere {
			continue
		}
		idx.Survivors = append(idx.Survivors, linkView{verdictView: verdict(m.Verdict), ID: m.ID, Title: m.Title, Href: firstHref[i], File: firstFile[i], Line: firstLine[i]})
	}
	sort.SliceStable(idx.Survivors, func(a, b int) bool {
		return severity(mutate.Verdict(idx.Survivors[a].Verdict)) < severity(mutate.Verdict(idx.Survivors[b].Verdict))
	})
	for _, f := range order {
		st := files[f]
		row := fileRow{Path: f, Href: st.page, Counts: make([]int, len(Verdicts))}
		for i := range st.mutants {
			row.Counts[severity(in.Mutants[i].Verdict)]++
			row.Total++
		}
		row.sort = row.Counts[0] + row.Counts[1]
		idx.Files = append(idx.Files, row)
		page, err := renderFile(in, st, head(f))
		if err != nil {
			return nil, nil, err
		}
		out = append(out, Page{Name: st.page, Data: page})
	}
	sort.SliceStable(idx.Files, func(a, b int) bool { return idx.Files[a].sort > idx.Files[b].sort })
	var buf bytes.Buffer
	if err := pages.ExecuteTemplate(&buf, "index.tmpl", idx); err != nil {
		return nil, nil, err
	}
	out = append([]Page{{Name: "index.html", Data: buf.Bytes()}}, out...)
	return out, warnings, nil
}

func renderFile(in Input, st *fileState, head headView) ([]byte, error) {
	v := fileView{headView: head, Path: st.path, Columns: columns(), Counts: make([]int, len(Verdicts)), Changed: st.changed, HasCoverage: in.Coverage != nil}
	for i := range st.mutants {
		v.Counts[severity(in.Mutants[i].Verdict)]++
	}
	if in.Coverage != nil {
		v.Profiled = in.Coverage.Profiled(st.path)
	}
	src := sourceLines(st.text)
	v.Lines = make([]lineView, len(src))
	for n, text := range src {
		v.Lines[n] = lineView{N: n + 1, Text: text}
		if v.Profiled {
			tracked, ran := in.Coverage.LineRan(st.path, n+1)
			switch {
			case ran:
				v.Lines[n].Coverage = "ran"
			case tracked:
				v.Lines[n].Coverage = "never ran"
				v.Lines[n].Class = "unrun"
			}
		}
	}
	seen := map[int]int{}
	type item struct {
		line   int
		mutant int
		hunks  []Hunk
	}
	var items []item
	for _, p := range st.placed {
		byLine := map[int][]Hunk{}
		var lines []int
		for _, h := range p.hunks {
			if _, ok := byLine[h.Line]; !ok {
				lines = append(lines, h.Line)
			}
			byLine[h.Line] = append(byLine[h.Line], h)
		}
		for _, l := range lines {
			items = append(items, item{line: l, mutant: p.mutant, hunks: byLine[l]})
		}
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].line < items[b].line })
	for _, it := range items {
		if it.line < 1 || it.line > len(v.Lines) {
			return nil, fmt.Errorf("mutant %s: line %d is outside %s", in.Mutants[it.mutant].ID, it.line, st.path)
		}
		lv := &v.Lines[it.line-1]
		mv := mutantOf(in.Mutants[it.mutant], anchor(it.mutant, seen[it.mutant]))
		seen[it.mutant]++
		mv.Hunks = it.hunks
		lv.Mutants = append(lv.Mutants, mv)
		lv.Verdicts = addVerdict(lv.Verdicts, in.Mutants[it.mutant].Verdict)
	}
	for n := range v.Lines {
		lv := &v.Lines[n]
		if len(lv.Verdicts) == 0 {
			continue
		}
		sort.SliceStable(lv.Mutants, func(a, b int) bool {
			return severity(mutate.Verdict(lv.Mutants[a].Verdict)) < severity(mutate.Verdict(lv.Mutants[b].Verdict))
		})
		cls := "mutated v-" + lv.Verdicts[0].Verdict
		if lv.Class != "" {
			cls += " " + lv.Class
		}
		lv.Class = cls
	}
	for _, p := range st.unplaced {
		mv := mutantOf(in.Mutants[p.mutant], anchor(p.mutant, seen[p.mutant]))
		seen[p.mutant]++
		mv.Reason = st.reasons[p.mutant]
		v.Unplaced = append(v.Unplaced, mv)
	}
	var buf bytes.Buffer
	if err := pages.ExecuteTemplate(&buf, "file.tmpl", v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func addVerdict(vs []verdictView, v mutate.Verdict) []verdictView {
	for _, w := range vs {
		if w.Verdict == string(v) {
			return vs
		}
	}
	vs = append(vs, verdict(v))
	sort.SliceStable(vs, func(a, b int) bool {
		return severity(mutate.Verdict(vs[a].Verdict)) < severity(mutate.Verdict(vs[b].Verdict))
	})
	return vs
}

func mutantOf(m mutate.Result, anchor string) mutantView {
	return mutantView{
		verdictView: verdict(m.Verdict), Anchor: anchor, ID: m.ID, Title: m.Title, Operator: operator(m),
		Message: m.Message, KilledBy: m.KilledBy, Failed: m.Failed, Errors: m.Errors, Duration: duration(m.DurationMS),
	}
}

func operator(m mutate.Result) string {
	if !strings.HasPrefix(m.ID, "GEN-") {
		return ""
	}
	for _, op := range mutgen.Operators {
		if strings.HasPrefix(m.Title, op+" in ") {
			return op
		}
	}
	return ""
}

func verdict(v mutate.Verdict) verdictView {
	return verdictView{Verdict: string(v), Label: Label(v)}
}

func columns() []verdictView {
	out := make([]verdictView, len(Verdicts))
	for i, v := range Verdicts {
		out[i] = verdict(v)
	}
	return out
}

func anchor(mutant, n int) string {
	if n == 0 {
		return fmt.Sprintf("m%d", mutant+1)
	}
	return fmt.Sprintf("m%d-%d", mutant+1, n+1)
}

func score(c map[mutate.Verdict]int) string {
	caught := c[mutate.Killed] + c[mutate.KilledElsewhere]
	all := caught + c[mutate.Survived]
	if all == 0 {
		return "none (no mutant was killed or survived)"
	}
	return fmt.Sprintf("%.1f%% (%d of %d)", float64(caught)*100/float64(all), caught, all)
}

func duration(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(100 * time.Millisecond).String()
}

func groupEdits(edits []model.Edit) (map[string][]int, []string) {
	by := map[string][]int{}
	var order []string
	for i, e := range edits {
		if _, ok := by[e.File]; !ok {
			order = append(order, e.File)
		}
		by[e.File] = append(by[e.File], i)
	}
	return by, order
}

func appliedHash(m mutate.Result, file string) (bool, string) {
	for _, h := range m.Files {
		if h.File == file {
			return true, h.Before
		}
	}
	return false, ""
}

func hashOf(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

func Files(ms []mutate.Result) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range ms {
		for _, e := range m.Edits {
			if !seen[e.File] {
				seen[e.File] = true
				out = append(out, e.File)
			}
		}
	}
	sort.Strings(out)
	return out
}

func CleanPath(f string) bool {
	return f != "" && !strings.HasPrefix(f, "/") && path.Clean(f) == f && f != "." && f != ".." && !strings.HasPrefix(f, "../") && !strings.Contains(f, "\\") && !strings.ContainsRune(f, 0)
}
