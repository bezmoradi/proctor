package transcript

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	ReportDiffLines = 40
	diffContext     = 2
	maxDiffLineLen  = 240
	maxTable        = 4 << 20
)

type Diff struct {
	Lines   []string
	Removed int
	Added   int
	More    int
}

func (d Diff) Changed() string {
	return fmt.Sprintf("%d lines changed (-%d +%d)", d.Removed+d.Added, d.Removed, d.Added)
}

func lines(b []byte) []string {
	s := string(b)
	if s == "" {
		return nil
	}
	out := strings.SplitAfter(s, "\n")
	if out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

type op struct {
	kind byte
	text string
	a, b int
}

func Compare(old, new []byte, limit int, cut bool) Diff {
	a, b := lines(old), lines(new)
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	var ops []op
	for i := 0; i < pre; i++ {
		ops = append(ops, op{kind: ' ', text: a[i], a: i, b: i})
	}
	ops = append(ops, middle(ma, mb, pre)...)
	for i := 0; i < suf; i++ {
		ia, ib := len(a)-suf+i, len(b)-suf+i
		ops = append(ops, op{kind: ' ', text: a[ia], a: ia, b: ib})
	}
	return render(ops, limit, cut)
}

func middle(a, b []string, base int) []op {
	var out []op
	if len(a)*len(b) > maxTable || len(a) == 0 || len(b) == 0 {
		for i, l := range a {
			out = append(out, op{kind: '-', text: l, a: base + i, b: base})
		}
		for j, l := range b {
			out = append(out, op{kind: '+', text: l, a: base + len(a), b: base + j})
		}
		return out
	}
	n, m := len(a), len(b)
	t := make([][]int32, n+1)
	for i := range t {
		t[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				t[i][j] = t[i+1][j+1] + 1
			} else {
				t[i][j] = max(t[i+1][j], t[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			out = append(out, op{kind: ' ', text: a[i], a: base + i, b: base + j})
			i++
			j++
		case i < n && (j == m || t[i+1][j] >= t[i][j+1]):
			out = append(out, op{kind: '-', text: a[i], a: base + i, b: base + j})
			i++
		default:
			out = append(out, op{kind: '+', text: b[j], a: base + i, b: base + j})
			j++
		}
	}
	return out
}

func render(ops []op, limit int, cut bool) Diff {
	var d Diff
	keep := make([]bool, len(ops))
	for i, o := range ops {
		if o.kind == ' ' {
			continue
		}
		if o.kind == '-' {
			d.Removed++
		} else {
			d.Added++
		}
		for k := max(0, i-diffContext); k <= min(len(ops)-1, i+diffContext); k++ {
			keep[k] = true
		}
	}
	var out []string
	for i := 0; i < len(ops); {
		if !keep[i] {
			i++
			continue
		}
		j := i
		for j < len(ops) && keep[j] {
			j++
		}
		ca, cb := 0, 0
		for _, o := range ops[i:j] {
			if o.kind != '+' {
				ca++
			}
			if o.kind != '-' {
				cb++
			}
		}
		out = append(out, fmt.Sprintf("@@ -%d,%d +%d,%d @@", ops[i].a+1, ca, ops[i].b+1, cb))
		for _, o := range ops[i:j] {
			text := strings.TrimSuffix(o.text, "\n")
			if cut {
				text = short(text)
			}
			out = append(out, string(o.kind)+text)
		}
		i = j
	}
	if limit > 0 && len(out) > limit {
		d.More = len(out) - limit
		out = out[:limit]
	}
	d.Lines = out
	return d
}

func short(s string) string {
	if len(s) <= maxDiffLineLen {
		return s
	}
	n := maxDiffLineLen
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + fmt.Sprintf(" [cut, %d more bytes]", len(s)-n)
}
