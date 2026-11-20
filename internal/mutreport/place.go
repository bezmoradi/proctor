package mutreport

import (
	"errors"
	"fmt"
	"strings"

	"proctor/internal/model"
)

type Hunk struct {
	Line   int
	Before []string
	After  []string
}

func locate(text string, e model.Edit) ([][2]int, error) {
	if e.Find == "" {
		return nil, errors.New("the edit's find is empty")
	}
	if e.Line != nil {
		at, err := model.LocateAtLine(text, e.Find, *e.Line)
		if err != nil {
			return nil, err
		}
		return [][2]int{{at, at + len(e.Find)}}, nil
	}
	var out [][2]int
	for from := 0; ; {
		i := strings.Index(text[from:], e.Find)
		if i < 0 {
			break
		}
		out = append(out, [2]int{from + i, from + i + len(e.Find)})
		from += i + len(e.Find)
	}
	if len(out) != e.Times() {
		return nil, fmt.Errorf("the edit didn't apply: found %d times, expected %d", len(out), e.Times())
	}
	return out, nil
}

func hunks(text string, edits []model.Edit, index []int) ([]Hunk, error) {
	if _, err := model.ApplyEdits(text, edits, index); err != nil {
		return nil, err
	}
	var out []Hunk
	for k, e := range edits {
		spans, err := locate(text, e)
		if err != nil {
			return nil, fmt.Errorf("edits[%d]: %v", index[k], err)
		}
		for _, sp := range spans {
			out = append(out, hunkAt(text, sp[0], sp[1], e.Replace))
		}
	}
	return out, nil
}

func hunkAt(text string, start, end int, replace string) Hunk {
	ls := strings.LastIndexByte(text[:start], '\n') + 1
	le := len(text)
	switch {
	case end > start && text[end-1] == '\n':
		le = end
	default:
		if i := strings.IndexByte(text[end:], '\n'); i >= 0 {
			le = end + i + 1
		}
	}
	return Hunk{
		Line:   1 + strings.Count(text[:start], "\n"),
		Before: splitLines(text[ls:le]),
		After:  splitLines(text[ls:start] + replace + text[end:le]),
	}
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func sourceLines(text string) []string {
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}
