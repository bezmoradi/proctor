package mutgen

import (
	"context"
	"fmt"
	"io"
	"sort"

	"proctor/internal/model"
)

const (
	OpNegate     = "negate"
	OpBoundary   = "boundary"
	OpEquality   = "equality"
	OpLogic      = "logic"
	OpSwallow    = "swallow"
	OpRemoveCall = "remove-call"
	OpBool       = "bool"
	OpConstant   = "constant"
)

var Operators = []string{OpNegate, OpBoundary, OpEquality, OpLogic, OpSwallow, OpRemoveCall, OpBool, OpConstant}

const maxFindBytes = 512

type Options struct {
	Root       string
	Files      []string
	Protected  func(p string) bool
	Include    []string
	Exclude    []string
	Coverage   *Coverage
	GOOS       string
	GOARCH     string
	Seed       uint64
	Limit      int
	Existing   []model.Mutant
	BuildCheck bool
	Jobs       int
	IDPrefix   string
	Log        io.Writer
}

type Mutant struct {
	ID       string
	Title    string
	File     string
	Line     int
	Find     string
	Replace  string
	Operator string
	Func     string
	Covered  string
}

type Result struct {
	Mutants    []Mutant
	Files      int
	Skipped    map[string]int
	Candidates map[string]int
	Dropped    map[string]int
	BuildCheck map[string]int
	Warnings   []string
}

func Run(ctx context.Context, o Options) (*Result, error) {
	if o.Limit < 1 {
		return nil, fmt.Errorf("--limit must be 1 or more")
	}
	if o.Jobs < 1 {
		return nil, fmt.Errorf("--jobs must be 1 or more")
	}
	if o.IDPrefix == "" {
		o.IDPrefix = "GEN"
	}
	inc, err := compileGlobs("--include", o.Include)
	if err != nil {
		return nil, err
	}
	exc, err := compileGlobs("--exclude", o.Exclude)
	if err != nil {
		return nil, err
	}
	res := &Result{Skipped: map[string]int{}, Candidates: map[string]int{}, Dropped: map[string]int{}, BuildCheck: map[string]int{}}
	files, err := scan(o, inc, exc, res)
	if err != nil {
		return nil, err
	}
	if o.Coverage != nil {
		res.Warnings = append(res.Warnings, o.Coverage.check(files)...)
	}
	var cands []*candidate
	for _, f := range files {
		for _, c := range f.cands {
			res.Candidates[c.op]++
			cands = append(cands, c)
		}
	}
	existing := existingEdits(o.Existing)
	var pool []*candidate
	for _, c := range cands {
		if o.Coverage != nil {
			c.covered = o.Coverage.covers(c.file.path, c.line, c.col)
			if c.pkgLevel {
				c.covered = o.Coverage.packageRan(c.file.path)
			}
			if c.covered != "yes" {
				res.Dropped["not covered"]++
				continue
			}
		} else {
			c.covered = "unknown"
		}
		pool = append(pool, c)
	}
	seq := order(pool, o.Seed)
	picked, err := choose(ctx, o, seq, existing, res)
	if err != nil {
		return nil, err
	}
	for i, c := range picked {
		res.Mutants = append(res.Mutants, Mutant{
			ID:       fmt.Sprintf("%s-%04d", o.IDPrefix, i+1),
			Title:    fmt.Sprintf("%s in %s (%s:%d)", c.op, c.fn, c.file.path, c.line),
			File:     c.file.path,
			Line:     c.line,
			Find:     c.find,
			Replace:  c.replace,
			Operator: c.op,
			Func:     c.fn,
			Covered:  c.covered,
		})
	}
	return res, nil
}

type editKey struct {
	file, find, replace string
	line                int
}

func existingEdits(ms []model.Mutant) map[editKey]bool {
	out := map[editKey]bool{}
	for _, m := range ms {
		if len(m.Edits) != 1 {
			continue
		}
		e := m.Edits[0]
		k := editKey{file: e.File, find: e.Find, replace: e.Replace}
		if e.Line != nil {
			k.line = *e.Line
		}
		out[k] = true
	}
	return out
}

func (k editKey) seenIn(set map[editKey]bool) bool {
	if set[k] {
		return true
	}
	anyLine := k
	anyLine.line = 0
	return set[anyLine]
}

func SortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func Counts(ms []Mutant) map[string]int {
	out := map[string]int{}
	for _, m := range ms {
		out[m.Operator]++
	}
	return out
}
