package mutgen

import (
	"context"
	"encoding/binary"
	"go/parser"
	"go/token"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"

	"proctor/internal/model"
)

func rank(seed uint64, parts ...string) uint64 {
	h := fnv.New64a()
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], seed)
	h.Write(b[:])
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return h.Sum64()
}

func identity(c *candidate) string {
	return c.file.path + "\x00" + strconv.Itoa(c.start) + "\x00" + c.op + "\x00" + c.repl
}

type function struct {
	key   string
	rank  uint64
	queue []*candidate
	ops   int
}

func order(pool []*candidate, seed uint64) []*candidate {
	byFn := map[string]*function{}
	byOp := map[string]map[string][]*candidate{}
	for _, c := range pool {
		c.rank = rank(seed, "mutant", identity(c))
		f := byFn[c.fnKey]
		if f == nil {
			f = &function{key: c.fnKey, rank: rank(seed, "function", c.fnKey)}
			byFn[c.fnKey] = f
			byOp[c.fnKey] = map[string][]*candidate{}
		}
		byOp[c.fnKey][c.op] = append(byOp[c.fnKey][c.op], c)
	}
	fns := make([]*function, 0, len(byFn))
	for key, f := range byFn {
		ops := make([]string, 0, len(byOp[key]))
		for op, cs := range byOp[key] {
			sort.Slice(cs, func(i, j int) bool { return less(cs[i], cs[j]) })
			ops = append(ops, op)
		}
		sort.Slice(ops, func(i, j int) bool {
			ri, rj := rank(seed, "operator", key, ops[i]), rank(seed, "operator", key, ops[j])
			if ri != rj {
				return ri < rj
			}
			return ops[i] < ops[j]
		})
		f.ops = len(ops)
		for k := 0; ; k++ {
			added := false
			for _, op := range ops {
				if cs := byOp[key][op]; k < len(cs) {
					f.queue = append(f.queue, cs[k])
					added = true
				}
			}
			if !added {
				break
			}
		}
		fns = append(fns, f)
	}
	sort.Slice(fns, func(i, j int) bool {
		if fns[i].rank != fns[j].rank {
			return fns[i].rank < fns[j].rank
		}
		return fns[i].key < fns[j].key
	})
	var seq []*candidate
	for k := 0; ; k++ {
		added := false
		for _, f := range fns {
			if k < f.ops {
				seq = append(seq, f.queue[k])
				added = true
			}
		}
		if !added {
			break
		}
	}
	for k := 0; ; k++ {
		added := false
		for _, f := range fns {
			if i := f.ops + k; i < len(f.queue) {
				seq = append(seq, f.queue[i])
				added = true
			}
		}
		if !added {
			break
		}
	}
	for i, c := range seq {
		c.position = i
	}
	return seq
}

func less(a, b *candidate) bool {
	if a.rank != b.rank {
		return a.rank < b.rank
	}
	return identity(a) < identity(b)
}

func choose(ctx context.Context, o Options, seq []*candidate, existing map[editKey]bool, res *Result) ([]*candidate, error) {
	claimed := map[editKey]bool{}
	next := 0
	ready := func() *candidate {
		for next < len(seq) {
			c := seq[next]
			next++
			if reason := prepare(c, existing, claimed); reason != "" {
				res.Dropped[reason]++
				continue
			}
			return c
		}
		return nil
	}
	var picked []*candidate
	if !o.BuildCheck {
		for len(picked) < o.Limit {
			c := ready()
			if c == nil {
				break
			}
			picked = append(picked, c)
		}
		return picked, nil
	}
	k, err := newChecker(ctx, o)
	if err != nil {
		return nil, err
	}
	defer k.close()
	for len(picked) < o.Limit {
		need := o.Limit - len(picked)
		size := max(need, o.Jobs)
		var batch []*candidate
		for len(batch) < size {
			c := ready()
			if c == nil {
				break
			}
			batch = append(batch, c)
		}
		if len(batch) == 0 {
			break
		}
		verdicts, err := k.check(ctx, batch)
		if err != nil {
			return nil, err
		}
		for i, c := range batch {
			if verdicts[i] != "" {
				res.BuildCheck[verdicts[i]]++
				continue
			}
			if len(picked) < o.Limit {
				picked = append(picked, c)
			}
		}
	}
	res.BuildCheck["checked"] = k.checked
	return picked, nil
}

func prepare(c *candidate, existing, claimed map[editKey]bool) string {
	if existing != nil {
		k := editKey{file: c.file.path, line: c.line, find: c.text, replace: c.repl}
		if k.seenIn(existing) {
			return "already in the mutants file"
		}
	}
	if !widen(c) {
		return "find not unique on its line"
	}
	k := editKey{file: c.file.path, line: c.line, find: c.find, replace: c.replace}
	if k.seenIn(existing) {
		return "already in the mutants file"
	}
	if len(c.find) > maxFindBytes {
		return "find longer than 512 bytes"
	}
	same := editKey{file: c.file.path, find: c.find, replace: c.replace}
	if claimed[same] {
		return "same file, find and replace as an earlier mutant"
	}
	line := c.line
	text := string(c.file.src)
	mutated, err := model.ApplyEdits(text, []model.Edit{{File: c.file.path, Find: c.find, Replace: c.replace, Line: &line}}, []int{0})
	if err != nil {
		return "find not unique on its line"
	}
	if _, err := parser.ParseFile(token.NewFileSet(), c.file.path, mutated, parser.SkipObjectResolution); err != nil {
		return "the changed file doesn't parse"
	}
	claimed[same] = true
	c.mutated = mutated
	return ""
}

func widen(c *candidate) bool {
	src := string(c.file.src)
	lineStart := strings.LastIndexByte(src[:c.start], '\n') + 1
	lineEnd := len(src)
	if i := strings.IndexByte(src[c.start:], '\n'); i >= 0 {
		lineEnd = c.start + i
	}
	tail := ""
	if c.end < lineEnd {
		tail = src[c.end:lineEnd]
	}
	head := src[lineStart:c.start]
	indent := len(head) - len(strings.TrimLeft(head, " \t"))
	lead := head[indent:]
	tries := []struct{ find, replace string }{
		{c.text, c.repl},
		{c.text + tail, c.repl + tail},
		{lead + c.text, lead + c.repl},
		{lead + c.text + tail, lead + c.repl + tail},
	}
	for i, t := range tries {
		if t.find == "" || t.find == t.replace {
			continue
		}
		at := c.start
		if i >= 2 {
			at = lineStart + indent
		}
		line := c.line
		got, err := model.LocateAtLine(src, t.find, line)
		if err == nil && got == at {
			c.find, c.replace = t.find, t.replace
			return true
		}
	}
	return false
}
