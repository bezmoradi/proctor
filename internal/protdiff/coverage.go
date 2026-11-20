package protdiff

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"proctor/internal/gitrepo"
	"proctor/internal/model"
	"proctor/internal/strictyaml"
)

func dimensionsPath(c *model.Config, configPath string) string {
	if c == nil || c.Dimensions == "" {
		return ""
	}
	return path.Dir(configPath) + "/" + c.Dimensions
}

type coverageSide struct {
	path    string
	file    model.Dimensions
	decoded bool
	oid     string
	tables  map[string]string
}

func readCoverage(repo *gitrepo.Repo, tree map[string]gitrepo.Entry, c *model.Config, configPath string) (coverageSide, error) {
	s := coverageSide{path: dimensionsPath(c, configPath), tables: map[string]string{}}
	root := path.Dir(configPath)
	for _, p := range sortedKeys(tree) {
		e := tree[p]
		if !strings.HasPrefix(p, root+"/") || (!strings.HasSuffix(p, ".yaml") && !strings.HasSuffix(p, ".yml")) ||
			p == configPath || p == s.path || p == mutantsPath(c, configPath) || strings.HasPrefix(p, root+"/environments/") || strings.HasPrefix(p, approvedRoot(configPath)) || !e.Regular() {
			continue
		}
		data, err := repo.Blob(e.OID)
		if err != nil {
			return s, err
		}
		var spec model.Spec
		if strictyaml.Decode(data, &spec) != nil {
			continue
		}
		for i := range spec.Tables {
			t := &spec.Tables[i]
			label := t.ID
			if label == "" {
				label = fmt.Sprintf("tables[%d]", i)
			}
			set, _, err := t.CoverSet()
			if err != nil {
				set = ""
			}
			s.tables[p+"\x00"+label] = set
		}
	}
	if s.path == "" {
		return s, nil
	}
	e, ok := tree[s.path]
	if !ok || !e.Regular() {
		return s, nil
	}
	s.oid = e.OID
	data, err := repo.Blob(e.OID)
	if err != nil {
		return s, err
	}
	if strictyaml.Decode(data, &s.file) == nil {
		s.decoded = true
	}
	return s, nil
}

func dimensionKeys(d model.Dimensions, name string) ([]string, map[string]bool) {
	var order []string
	has := map[string]bool{}
	for i := range d.Dimensions[name] {
		key, err := model.CellKey(&d.Dimensions[name][i])
		if err != nil || has[key] {
			continue
		}
		order = append(order, key)
		has[key] = true
	}
	return order, has
}

func shownValues(keys []string) string {
	const most = 20
	out := make([]string, 0, most)
	for i, k := range keys {
		if i == most {
			return strings.Join(out, ", ") + fmt.Sprintf(" and %d more", len(keys)-most)
		}
		out = append(out, model.ShowCell(k))
	}
	return strings.Join(out, ", ")
}

func exceptionKey(e model.CoverageException) (string, string) {
	cols := make([]string, 0, len(e.Cells))
	for c := range e.Cells {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	keys := make([]string, len(cols))
	for i, c := range cols {
		cell := e.Cells[c]
		k, err := model.CellKey(&cell)
		if err != nil {
			k = "?" + cell.Value
		}
		keys[i] = k
	}
	return model.TupleKey(append(append([]string(nil), cols...), keys...)), model.ShowTuple(cols, keys)
}

func compareCoverage(repo *gitrepo.Repo, bt, ht map[string]gitrepo.Entry, b, h *model.Config, configPath string) ([]Weakening, error) {
	var out []Weakening
	weaken := func(place, kind, format string, args ...any) {
		out = append(out, Weakening{Coverage: place, Kind: kind, Detail: fmt.Sprintf(format, args...)})
	}
	bs, err := readCoverage(repo, bt, b, configPath)
	if err != nil {
		return nil, err
	}
	hs, err := readCoverage(repo, ht, h, configPath)
	if err != nil {
		return nil, err
	}
	for _, k := range sortedKeys(bs.tables) {
		was := bs.tables[k]
		now, still := hs.tables[k]
		if was == "" || !still || now == was {
			continue
		}
		file, label, _ := strings.Cut(k, "\x00")
		if now == "" {
			weaken("table "+label, "coverage_dropped", "the table in %s no longer declares covers: %s; its rows no longer count toward the set's combinations", file, was)
		} else {
			weaken("table "+label, "coverage_dropped", "the table in %s covers %s instead of %s", file, now, was)
		}
	}
	switch {
	case bs.path == "":
		return out, nil
	case hs.path == "":
		weaken("", "coverage_dropped", "dimensions was removed (it named %s); no table's coverage is checked", bs.path)
		return out, nil
	}
	if !bs.decoded || !hs.decoded {
		if bs.oid != hs.oid || bs.path != hs.path {
			weaken("", "not_analysed", "the dimensions file changed but one side doesn't decode as a dimensions file, so its sets weren't compared; read the file diff")
		}
		return out, nil
	}
	bd, hd := bs.file, hs.file
	for _, name := range sortedKeys(bd.Dimensions) {
		if _, kept := hd.Dimensions[name]; !kept {
			continue
		}
		order, _ := dimensionKeys(bd, name)
		_, now := dimensionKeys(hd, name)
		var gone []string
		for _, k := range order {
			if !now[k] {
				gone = append(gone, k)
			}
		}
		if len(gone) > 0 {
			weaken("dimension "+name, "dimension_value_removed", "%s no longer in the dimension; a table covering a set that uses it no longer needs a row for %s", shownValues(gone), plural(len(gone), "it", "them"))
		}
	}
	for _, name := range sortedKeys(bd.Sets) {
		was := bd.Sets[name]
		now, kept := hd.Sets[name]
		if !kept {
			weaken("set "+name, "coverage_dropped", "the set was removed; nothing checks any longer that its tables have a row for every combination of %s", columnsText(was.Columns))
			continue
		}
		for _, col := range was.Columns.Names {
			wd := was.Columns.Dimension[col]
			hdim, has := now.Columns.Dimension[col]
			switch {
			case !has:
				weaken("set "+name, "coverage_dropped", "column %s (dimension %s) was removed from the set; its values no longer have to be combined with the other columns", col, wd)
			case hdim != wd:
				order, _ := dimensionKeys(bd, wd)
				_, nowHas := dimensionKeys(hd, hdim)
				var gone []string
				for _, k := range order {
					if !nowHas[k] {
						gone = append(gone, k)
					}
				}
				if len(gone) > 0 {
					weaken("set "+name, "dimension_value_removed", "column %s takes dimension %s instead of %s, which lacks %s; rows for %s are no longer needed", col, hdim, wd, shownValues(gone), plural(len(gone), "it", "them"))
				}
			}
		}
		before := map[string]bool{}
		for _, e := range was.Except {
			k, _ := exceptionKey(e)
			before[k] = true
		}
		for _, e := range now.Except {
			k, shown := exceptionKey(e)
			if !before[k] {
				weaken("set "+name, "coverage_exception_added", "%s is excepted (%q); no row is needed for it any longer", shown, e.Reason)
			}
		}
	}
	return out, nil
}

func columnsText(c model.SetColumns) string {
	parts := make([]string, len(c.Names))
	for i, n := range c.Names {
		parts[i] = n + " (" + c.Dimension[n] + ")"
	}
	return strings.Join(parts, " × ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
