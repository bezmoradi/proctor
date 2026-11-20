package integrity

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"proctor/internal/model"
	"proctor/internal/strictyaml"
)

type coverTable struct {
	file  string
	label string
	set   string
	table *model.Table
}

func tableLabel(t *model.Table, index int) string {
	if t.ID != "" {
		return t.ID
	}
	return fmt.Sprintf("tables[%d]", index)
}

func (r *Report) coverage(p, format string, args ...any) {
	r.add(Invalid, model.CoverageRule, p, format, args...)
}

func (r *Report) loadCoverage(src Source, c *model.Config) {
	var tables []coverTable
	for _, sf := range r.Specs {
		for ti := range sf.Spec.Tables {
			t := &sf.Spec.Tables[ti]
			label := tableLabel(t, ti)
			name, given, err := t.CoverSet()
			switch {
			case err != nil:
				r.coverage(sf.Path, "table %s: %v", label, err)
			case given:
				tables = append(tables, coverTable{file: sf.Path, label: label, set: name, table: t})
			}
		}
	}
	if c.Dimensions == "" {
		for _, ct := range tables {
			r.coverage(ct.file, "table %s: covers names set %q, but %s names no dimensions file; add dimensions: dimensions.yaml and define the set there", ct.label, ct.set, r.ConfigPath)
		}
		return
	}
	if err := checkRelPath(c.Dimensions); err != nil {
		r.coverage(r.ConfigPath, "dimensions: %v; give a path relative to %s/", err, r.ProtectedRoot)
		return
	}
	full := r.ProtectedRoot + "/" + c.Dimensions
	if full == r.ConfigPath || full == r.ProtectedRoot+"/"+c.Manifest || r.listed[full] || full == r.MutantsPath {
		r.coverage(r.ConfigPath, "dimensions names %s, which is already the configuration, the manifest, a specification or the mutants file", full)
		return
	}
	r.DimensionsPath = full
	data, ok, err := src.ReadFile(full)
	switch {
	case err != nil:
		r.coverage(full, "cannot read the dimensions file: %v", err)
		return
	case !ok:
		r.coverage(full, "the dimensions file named in %s isn't there", r.ConfigPath)
		return
	}
	r.Hashes[full] = sha(data)
	if doc, err := strictyaml.Parse(data); err == nil && len(doc.Content) > 0 && doc.Content[0].Kind != yaml.MappingNode {
		r.coverage(full, "the dimensions file must be a mapping with version, dimensions and sets")
		return
	}
	var d model.Dimensions
	if err := strictyaml.Decode(data, &d); err != nil {
		r.coverage(full, "%v", err)
		return
	}
	r.checkCoverage(full, &d, tables)
}

type coverHit struct{ row, table string }

type dimension struct {
	order  []string
	has    map[string]bool
	byText map[string]string
}

func (d dimension) hint(key string) string {
	_, text, _ := strings.Cut(key, "\x00")
	if other, ok := d.byText[text]; ok && other != key {
		return fmt.Sprintf(" (the dimension has %s, %s; a cell must be written as the dimension writes it)", model.ShowCell(other), kindText(other))
	}
	return ""
}

func kindText(key string) string {
	tag, _, _ := strings.Cut(key, "\x00")
	switch tag {
	case "!!str":
		return "text"
	case "!!int", "!!float":
		return "a number"
	case "!!bool":
		return "true or false"
	}
	return tag
}

func (r *Report) checkCoverage(p string, d *model.Dimensions, tables []coverTable) {
	if d.Version != model.DimensionsVersion {
		r.coverage(p, "version must be %d, got %d", model.DimensionsVersion, d.Version)
	}
	if len(d.Dimensions) == 0 {
		r.coverage(p, "the dimensions file lists no dimensions")
	}
	if len(d.Sets) == 0 {
		r.coverage(p, "the dimensions file lists no sets, so no table's coverage is checked; define a set and name it in the tables' covers")
	}
	dims := map[string]dimension{}
	for _, name := range sortedKeys(d.Dimensions) {
		if !model.CoverageName.MatchString(name) {
			r.coverage(p, "invalid dimension name %q (a lower-case letter, then lower-case letters, digits or _)", name)
		}
		values := d.Dimensions[name]
		if len(values) == 0 {
			r.coverage(p, "dimension %s lists no values", name)
		}
		dm := dimension{has: map[string]bool{}, byText: map[string]string{}}
		seen := map[string]int{}
		for i := range values {
			v := &values[i]
			key, err := model.CellKey(v)
			if err != nil {
				r.coverage(p, "dimension %s: values[%d] (line %d) %v; a dimension lists single values, as a table's cells hold them", name, i, v.Line, err)
				continue
			}
			if model.HoldsRowRef(v.Value) {
				r.coverage(p, "dimension %s: values[%d] %s holds ${row...}, which no cell can hold (TBL-05)", name, i, model.ShowCell(key))
				continue
			}
			if j, dup := seen[key]; dup {
				r.coverage(p, "dimension %s lists %s twice (values[%d] and values[%d])", name, model.ShowCell(key), j, i)
				continue
			}
			seen[key] = i
			dm.order = append(dm.order, key)
			dm.has[key] = true
			dm.byText[v.Value] = key
		}
		dims[name] = dm
	}
	bySet := map[string][]coverTable{}
	for _, ct := range tables {
		if _, ok := d.Sets[ct.set]; !ok {
			r.coverage(ct.file, "table %s: covers names set %q, which %s doesn't define (its sets: %s)", ct.label, ct.set, p, nameList(sortedKeys(d.Sets)))
			continue
		}
		bySet[ct.set] = append(bySet[ct.set], ct)
	}
	used := map[string]bool{}
	for _, name := range sortedKeys(d.Sets) {
		r.checkSet(p, name, d.Sets[name], dims, bySet[name], used)
	}
	for _, name := range sortedKeys(d.Dimensions) {
		if !used[name] {
			r.coverage(p, "dimension %s isn't a column of any set, so nothing checks it; remove it, or name it in a set's columns", name)
		}
	}
}

type capped struct {
	r      *Report
	set    string
	what   string
	shown  int
	hidden int
	path   string
}

func (c *capped) add(p, format string, args ...any) {
	if c.shown >= model.CoverageShown {
		c.hidden++
		c.path = p
		return
	}
	c.shown++
	c.r.coverage(p, format, args...)
}

func (c *capped) close() {
	if c.hidden > 0 {
		c.r.coverage(c.path, "set %s: %d more %s not listed (the first %d are above)", c.set, c.hidden, c.what, model.CoverageShown)
	}
}

func (r *Report) checkSet(p, name string, s model.CoverageSet, dims map[string]dimension, tables []coverTable, used map[string]bool) {
	where := "set " + name
	if !model.CoverageName.MatchString(name) {
		r.coverage(p, "invalid set name %q (a lower-case letter, then lower-case letters, digits or _)", name)
	}
	cols := s.Columns.Names
	if len(cols) == 0 {
		r.coverage(p, "%s: columns is required: map each table column the set covers to its dimension, such as { route: post_routes }", where)
		return
	}
	ok := true
	for _, col := range cols {
		dim := s.Columns.Dimension[col]
		used[dim] = true
		if _, known := dims[dim]; !known {
			r.coverage(p, "%s: column %s names dimension %q, which the dimensions file doesn't define (its dimensions: %s)", where, col, dim, nameList(sortedKeys(dims)))
			ok = false
		}
	}
	if len(tables) == 0 {
		r.coverage(p, "%s isn't covered: no table in the listed specifications declares covers: %s, so nothing checks that its combinations have rows", where, name)
		ok = false
	}
	if !ok {
		return
	}
	total := 1
	for _, col := range cols {
		total *= len(dims[s.Columns.Dimension[col]].order)
		if total > model.MaxCoverageCombinations {
			r.coverage(p, "%s has more than %d combinations; split it into smaller sets", where, model.MaxCoverageCombinations)
			return
		}
	}
	excepted := map[string]int{}
	for i, e := range s.Except {
		ew := fmt.Sprintf("%s except[%d]", where, i)
		good := true
		if strings.TrimSpace(e.Reason) == "" {
			r.coverage(p, "%s: reason is required: say why this combination needs no row", ew)
			good = false
		}
		for _, k := range sortedKeys(e.Cells) {
			if _, in := s.Columns.Dimension[k]; !in {
				r.coverage(p, "%s: cells names column %s, which isn't one of the set's columns (%s)", ew, k, strings.Join(cols, ", "))
				good = false
			}
		}
		keys := make([]string, len(cols))
		for ci, col := range cols {
			cell, has := e.Cells[col]
			if !has {
				r.coverage(p, "%s: cells has no value for column %s; an exception names one whole combination, a value for each of the set's columns (%s)", ew, col, strings.Join(cols, ", "))
				good = false
				continue
			}
			key, err := model.CellKey(&cell)
			if err != nil {
				r.coverage(p, "%s: column %s %v", ew, col, err)
				good = false
				continue
			}
			dim := s.Columns.Dimension[col]
			if !dims[dim].has[key] {
				r.coverage(p, "%s: %s=%s isn't a value of dimension %s%s, so the exception excepts nothing", ew, col, model.ShowCell(key), dim, dims[dim].hint(key))
				good = false
			}
			keys[ci] = key
		}
		if !good {
			continue
		}
		tk := model.TupleKey(keys)
		if j, dup := excepted[tk]; dup {
			r.coverage(p, "%s: %s is already excepted by except[%d]", ew, model.ShowTuple(cols, keys), j)
			continue
		}
		excepted[tk] = i
	}

	covered := map[string]coverHit{}
	outside := &capped{r: r, set: name, what: "cells outside their dimension"}
	twice := &capped{r: r, set: name, what: "combinations with more than one row"}
	complete := true
	for _, ct := range tables {
		t := ct.table
		at := map[string]int{}
		for i, c := range t.Columns {
			at[c] = i
		}
		acts := t.StepColumns()
		idx := make([]int, len(cols))
		tok := true
		for ci, col := range cols {
			i, has := at[col]
			switch {
			case !has:
				r.coverage(ct.file, "table %s: covers %s, whose column %s the table doesn't have (its columns: %s)", ct.label, where, col, strings.Join(t.Columns, ", "))
				tok = false
			case i == 0:
				r.coverage(ct.file, "table %s: covers %s, whose column %s is the table's case id column; a set covers the columns the step is built from", ct.label, where, col)
				tok = false
			case !acts[col]:
				r.coverage(ct.file, "table %s: column %s, which %s covers, isn't used by identity, variant or with, so its rows don't change what the step does; a set covers what the step is sent", ct.label, col, where)
				tok = false
			}
			idx[ci] = i
		}
		if !tok {
			complete = false
			continue
		}
		for ri := range t.Rows {
			row := &t.Rows[ri]
			if row.Kind != yaml.SequenceNode || len(row.Content) != len(t.Columns) || row.Content[0].Kind != yaml.ScalarNode || row.Content[0].Value == "" {
				complete = false
				continue
			}
			id := row.Content[0].Value
			keys := make([]string, len(cols))
			rowOK := true
			for ci, col := range cols {
				dim := s.Columns.Dimension[col]
				key, err := model.CellKey(row.Content[idx[ci]])
				if err != nil {
					outside.add(ct.file, "table %s row %s: column %s %v; it must hold one value of dimension %s, which %s covers", ct.label, id, col, err, dim, where)
					rowOK = false
					continue
				}
				if !dims[dim].has[key] {
					outside.add(ct.file, "table %s row %s: %s=%s isn't a value of dimension %s, which %s covers%s; add the value to the dimension in %s, or correct the cell", ct.label, id, col, model.ShowCell(key), dim, where, dims[dim].hint(key), p)
					rowOK = false
				}
				keys[ci] = key
			}
			if !rowOK {
				continue
			}
			tk := model.TupleKey(keys)
			if h, dup := covered[tk]; dup {
				twice.add(ct.file, "%s: %s has two rows, %s (table %s) and %s (table %s); each combination needs exactly one row", where, model.ShowTuple(cols, keys), h.row, h.table, id, ct.label)
				continue
			}
			if j, ex := excepted[tk]; ex {
				r.coverage(ct.file, "%s: row %s (table %s) covers %s, which except[%d] says needs no row; remove the row or the exception", where, id, ct.label, model.ShowTuple(cols, keys), j)
			}
			covered[tk] = coverHit{row: id, table: ct.label}
		}
	}
	outside.close()
	twice.close()
	if !complete {
		r.coverage(p, "%s: missing combinations aren't listed, since a table that covers it has the problems above", where)
		return
	}
	r.listMissing(p, where, cols, s.Columns.Dimension, dims, tables, covered, excepted)
}

func (r *Report) listMissing(p, where string, cols []string, colDimension map[string]string, dims map[string]dimension, tables []coverTable, covered map[string]coverHit, excepted map[string]int) {
	values := make([][]string, len(cols))
	total := 1
	for i, col := range cols {
		values[i] = dims[colDimension[col]].order
		total *= len(values[i])
	}
	if total == 0 {
		return
	}
	var shown [][]string
	missing := 0
	at := make([]int, len(cols))
	for {
		keys := make([]string, len(cols))
		for i := range cols {
			keys[i] = values[i][at[i]]
		}
		tk := model.TupleKey(keys)
		_, has := covered[tk]
		_, skip := excepted[tk]
		if !has && !skip {
			missing++
			if len(shown) < model.CoverageShown {
				shown = append(shown, keys)
			}
		}
		i := len(cols) - 1
		for ; i >= 0; i-- {
			at[i]++
			if at[i] < len(values[i]) {
				break
			}
			at[i] = 0
		}
		if i < 0 {
			break
		}
	}
	if missing == 0 {
		return
	}
	names := make([]string, len(tables))
	for i, ct := range tables {
		names[i] = ct.label + " (" + ct.file + ")"
	}
	r.coverage(p, "%s: %d of %d combinations have no row in table %s, and no except entry; add a row for each, or an except entry with its reason:", where, missing, total, strings.Join(names, " or table "))
	for _, keys := range shown {
		r.coverage(p, "%s: no row for %s", where, model.ShowTuple(cols, keys))
	}
	if missing > len(shown) {
		r.coverage(p, "%s: %d more combinations without a row not listed (the first %d are above, in the dimensions' order)", where, missing-len(shown), len(shown))
	}
}

func nameList(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
