package protdiff

import (
	"fmt"
	"path"
	"strings"

	"proctor/internal/gitrepo"
	"proctor/internal/model"
	"proctor/internal/strictyaml"
)

func mutantsPath(c *model.Config, configPath string) string {
	if c == nil || c.Mutation == nil || c.Mutation.Mutants == "" {
		return ""
	}
	return path.Dir(configPath) + "/" + c.Mutation.Mutants
}

type mutantSet struct {
	path    string
	list    []model.Mutant
	decoded bool
	oid     string
}

func readMutants(repo *gitrepo.Repo, tree map[string]gitrepo.Entry, c *model.Config, configPath string) (mutantSet, error) {
	p := mutantsPath(c, configPath)
	s := mutantSet{path: p}
	if p == "" {
		s.decoded = true
		return s, nil
	}
	e, ok := tree[p]
	if !ok || !e.Regular() {
		return s, nil
	}
	s.oid = e.OID
	data, err := repo.Blob(e.OID)
	if err != nil {
		return s, err
	}
	if strictyaml.Decode(data, &s.list) == nil {
		s.decoded = true
	}
	return s, nil
}

func compareMutation(repo *gitrepo.Repo, bt, ht map[string]gitrepo.Entry, b, h *model.Config, configPath string) ([]Weakening, error) {
	var out []Weakening
	weaken := func(mutant, kind, format string, args ...any) {
		out = append(out, Weakening{Mutant: mutant, Kind: kind, Detail: fmt.Sprintf(format, args...)})
	}
	bm, hm := b.Mutation, h.Mutation
	switch {
	case bm == nil && hm == nil:
		return nil, nil
	case bm != nil && hm == nil:
		weaken("", "mutation_removed", "mutation was removed from %s; proctor mutate no longer has mutants to run", configPath)
	case bm != nil && hm != nil:
		for _, f := range []struct{ key, was, now string }{
			{"mutants", bm.Mutants, hm.Mutants}, {"up", bm.Up, hm.Up}, {"down", bm.Down, hm.Down}, {"binding", bm.Binding, hm.Binding},
		} {
			if f.was != f.now {
				weaken("", "mutation_changed", "mutation.%s %s became %s", f.key, orNone(f.was), orNone(f.now))
			}
		}
	}
	bs, err := readMutants(repo, bt, b, configPath)
	if err != nil {
		return nil, err
	}
	hs, err := readMutants(repo, ht, h, configPath)
	if err != nil {
		return nil, err
	}
	if !bs.decoded || !hs.decoded {
		if bs.oid != hs.oid || bs.path != hs.path {
			weaken("", "not_analysed", "the mutants file changed but one side doesn't decode as a list of mutants, so its mutants weren't compared; read the file diff")
		}
		return out, nil
	}
	head := map[string]model.Mutant{}
	for _, m := range hs.list {
		head[m.ID] = m
	}
	for _, was := range bs.list {
		now, ok := head[was.ID]
		if !ok {
			weaken(was.ID, "mutant_removed", "the mutant (%s) was removed; proctor mutate no longer checks that the specifications catch it", was.Title)
			continue
		}
		for _, d := range mutantChanges(was, now) {
			weaken(was.ID, "mutant_changed", "%s", d)
		}
	}
	return out, nil
}

func mutantChanges(b, h model.Mutant) []string {
	var out []string
	if len(b.Edits) != len(h.Edits) {
		out = append(out, fmt.Sprintf("its edits went from %d to %d; the defect it plants changed", len(b.Edits), len(h.Edits)))
	}
	for i := 0; i < len(b.Edits) && i < len(h.Edits); i++ {
		be, he := b.Edits[i], h.Edits[i]
		var what []string
		if be.File != he.File {
			what = append(what, fmt.Sprintf("file %s became %s", be.File, he.File))
		}
		if be.Find != he.Find {
			what = append(what, "find changed")
		}
		if be.Replace != he.Replace {
			what = append(what, "replace changed")
		}
		if be.Times() != he.Times() {
			what = append(what, fmt.Sprintf("count %d became %d", be.Times(), he.Times()))
		}
		if lineText(be.Line) != lineText(he.Line) {
			what = append(what, fmt.Sprintf("line %s became %s", lineText(be.Line), lineText(he.Line)))
		}
		if len(what) > 0 {
			out = append(out, fmt.Sprintf("edits[%d]: %s; the defect it plants changed", i, strings.Join(what, ", ")))
		}
	}
	if !equalStrings(b.KilledBy, h.KilledBy) {
		out = append(out, fmt.Sprintf("killed_by %s became %s; which scenarios must catch it changed", killedText(b.KilledBy), killedText(h.KilledBy)))
	}
	return out
}

func lineText(l *int) string {
	if l == nil {
		return "(none)"
	}
	return fmt.Sprint(*l)
}

func killedText(ids []string) string {
	if ids == nil {
		return "(any scenario)"
	}
	return "[" + strings.Join(ids, ", ") + "]"
}
