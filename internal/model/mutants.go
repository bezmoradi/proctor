package model

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type Mutation struct {
	Mutants string `yaml:"mutants" json:"mutants"`
	Up      string `yaml:"up" json:"up"`
	Down    string `yaml:"down" json:"down"`
	Binding string `yaml:"binding" json:"binding"`
}

type Mutant struct {
	ID       string   `yaml:"id" json:"id"`
	Title    string   `yaml:"title" json:"title"`
	Edits    []Edit   `yaml:"edits" json:"edits"`
	KilledBy []string `yaml:"killed_by" json:"killed_by,omitempty"`
}

type Edit struct {
	File    string `yaml:"file" json:"file"`
	Find    string `yaml:"find" json:"find"`
	Replace string `yaml:"replace" json:"replace"`
	Count   *int   `yaml:"count" json:"count,omitempty"`
	Line    *int   `yaml:"line" json:"line,omitempty"`
}

func (e Edit) Times() int {
	if e.Count == nil {
		return 1
	}
	return *e.Count
}

var MutantID = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[0-9]+$`)

type Span struct {
	Edit       int
	Start, End int
}

func LocateAtLine(text, find string, line int) (int, error) {
	start, end, lines := lineBounds(text, line)
	if start < 0 {
		return 0, fmt.Errorf("the edit didn't apply: find not at line %d (the file has %d lines)", line, lines)
	}
	var at []int
	for i := start; i < end; i++ {
		if strings.HasPrefix(text[i:], find) {
			at = append(at, i)
		}
	}
	switch len(at) {
	case 0:
		return 0, fmt.Errorf("the edit didn't apply: find not at line %d", line)
	case 1:
		return at[0], nil
	}
	return 0, fmt.Errorf("the edit didn't apply: find starts %d times on line %d; make find longer so one match starts there", len(at), line)
}

func lineBounds(text string, line int) (start, end, lines int) {
	lines = strings.Count(text, "\n")
	if text != "" && !strings.HasSuffix(text, "\n") {
		lines++
	}
	if line < 1 || line > lines {
		return -1, -1, lines
	}
	start = 0
	for n := 1; n < line; n++ {
		start += strings.IndexByte(text[start:], '\n') + 1
	}
	end = len(text)
	if i := strings.IndexByte(text[start:], '\n'); i >= 0 {
		end = start + i + 1
	}
	return start, end, lines
}

func ApplyEdits(text string, edits []Edit, index []int) (string, error) {
	var spans []Span
	for k, e := range edits {
		if e.Line != nil {
			at, err := LocateAtLine(text, e.Find, *e.Line)
			if err != nil {
				return "", fmt.Errorf("edits[%d]: %v", index[k], err)
			}
			spans = append(spans, Span{Edit: k, Start: at, End: at + len(e.Find)})
			continue
		}
		var found []Span
		for from := 0; ; {
			i := strings.Index(text[from:], e.Find)
			if i < 0 {
				break
			}
			found = append(found, Span{Edit: k, Start: from + i, End: from + i + len(e.Find)})
			from += i + len(e.Find)
		}
		if len(found) != e.Times() {
			return "", fmt.Errorf("edits[%d]: the edit didn't apply: found %d times, expected %d", index[k], len(found), e.Times())
		}
		spans = append(spans, found...)
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	for i := 1; i < len(spans); i++ {
		if spans[i].Start < spans[i-1].End {
			a, b := index[spans[i-1].Edit], index[spans[i].Edit]
			if a == b {
				return "", fmt.Errorf("edits[%d]: the edit didn't apply: its matches overlap", a)
			}
			if a > b {
				a, b = b, a
			}
			return "", fmt.Errorf("edits[%d] and edits[%d]: the edits didn't apply: they change overlapping text of the original file", a, b)
		}
	}
	var out strings.Builder
	last := 0
	for _, s := range spans {
		out.WriteString(text[last:s.Start])
		out.WriteString(edits[s.Edit].Replace)
		last = s.End
	}
	out.WriteString(text[last:])
	return out.String(), nil
}
