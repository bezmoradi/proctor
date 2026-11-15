package protdiff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"proctor/internal/expr"
	"proctor/internal/gitrepo"
	"proctor/internal/integrity"
	"proctor/internal/model"
	"proctor/internal/strictyaml"
)

type FileChange struct {
	Change  string `json:"change"`
	Path    string `json:"path"`
	From    string `json:"from,omitempty"`
	OldMode string `json:"old_mode,omitempty"`
	NewMode string `json:"new_mode,omitempty"`
}

type ScenarioChange struct {
	Change   string `json:"change"`
	ID       string `json:"id"`
	File     string `json:"file"`
	FromFile string `json:"from_file,omitempty"`
	Modified bool   `json:"modified,omitempty"`
}

type Weakening struct {
	Scenario       string `json:"scenario"`
	Step           string `json:"step,omitempty"`
	Driver         string `json:"driver,omitempty"`
	Kind           string `json:"kind"`
	Detail         string `json:"detail"`
	AssertionsLost int    `json:"assertions_lost"`
}

type ExpectationChange struct {
	Scenario string `json:"scenario"`
	Step     string `json:"step"`
	What     string `json:"what"`
	From     string `json:"from"`
	To       string `json:"to"`
}

type Result struct {
	Base         string              `json:"base"`
	Head         string              `json:"head"`
	Files        []FileChange        `json:"files"`
	Scenarios    []ScenarioChange    `json:"scenarios"`
	Weakenings   []Weakening         `json:"weakenings"`
	Expectations []ExpectationChange `json:"expectation_changes"`

	Unparsed []string `json:"unparsed,omitempty"`
}

func (r *Result) Count() int { return len(r.Files) }

func BaseForTestMerge(repo *gitrepo.Repo, merge, prHead string) (string, error) {
	m, err := repo.ResolveCommit(merge)
	if err != nil {
		return "", err
	}
	h, err := repo.ResolveCommit(prHead)
	if err != nil {
		return "", err
	}
	parents, err := repo.Parents(m)
	if err != nil {
		return "", err
	}
	if len(parents) != 2 {
		return "", fmt.Errorf("commit %s has %d parents; a test merge has exactly 2", m, len(parents))
	}
	if parents[1] != h {
		return "", fmt.Errorf("second parent of %s is %s, not the pull request head %s", m, parents[1], h)
	}
	return repo.ResolveCommit(parents[0])
}

func Compute(repo *gitrepo.Repo, base, head, configPath string) (*Result, error) {
	b, err := repo.ResolveCommit(base)
	if err != nil {
		return nil, err
	}
	h, err := repo.ResolveCommit(head)
	if err != nil {
		return nil, err
	}
	bt, err := repo.Tree(b)
	if err != nil {
		return nil, err
	}
	ht, err := repo.Tree(h)
	if err != nil {
		return nil, err
	}
	root := path.Dir(configPath)
	if root == "." || root == "" {
		return nil, fmt.Errorf("configuration %q must sit inside a protected root directory", configPath)
	}

	extra := append(protectedPaths(repo, bt, configPath), protectedPaths(repo, ht, configPath)...)

	res := &Result{Base: b, Head: h}
	var removed, added []FileChange
	for _, p := range union(bt, ht) {
		if !integrity.Protected(p, root, extra) {
			continue
		}
		be, inBase := bt[p]
		he, inHead := ht[p]
		switch {
		case inBase && !inHead:
			removed = append(removed, FileChange{Change: "removed", Path: p, OldMode: be.Mode})
		case !inBase && inHead:
			added = append(added, FileChange{Change: "added", Path: p, NewMode: he.Mode})
		case be.OID != he.OID || be.Mode != he.Mode:
			c := FileChange{Change: "modified", Path: p}
			if be.Mode != he.Mode {
				c.OldMode, c.NewMode = be.Mode, he.Mode
			}
			res.Files = append(res.Files, c)
		}
	}

	used := map[int]bool{}
	for _, rm := range removed {
		moved := false
		for i, ad := range added {
			if !used[i] && bt[rm.Path].OID == ht[ad.Path].OID && bt[rm.Path].Mode == ht[ad.Path].Mode {
				used[i] = true
				moved = true
				res.Files = append(res.Files, FileChange{Change: "moved", Path: ad.Path, From: rm.Path})
				break
			}
		}
		if !moved {
			res.Files = append(res.Files, rm)
		}
	}
	for i, ad := range added {
		if !used[i] {
			res.Files = append(res.Files, ad)
		}
	}
	sort.Slice(res.Files, func(i, j int) bool { return res.Files[i].Path < res.Files[j].Path })

	res.Weakenings, res.Expectations = []Weakening{}, []ExpectationChange{}
	bc, bFound, err := readConfig(repo, bt, configPath)
	if err != nil {
		return nil, err
	}
	hc, hFound, err := readConfig(repo, ht, configPath)
	if err != nil {
		return nil, err
	}
	switch {
	case bc != nil && hc != nil:
		res.Weakenings = append(res.Weakenings, compareDrivers(bc, hc)...)
	case bFound && hFound && (bt[configPath].OID != ht[configPath].OID || bt[configPath].Mode != ht[configPath].Mode):
		res.Weakenings = append(res.Weakenings, Weakening{Kind: "not_analysed",
			Detail: configPath + " changed but one side doesn't decode as a version 2 configuration, so its drivers weren't compared; read the file diff"})
	}

	bs, bUnparsed, err := scenarios(repo, bt, root, configPath)
	if err != nil {
		return nil, err
	}
	hs, hUnparsed, err := scenarios(repo, ht, root, configPath)
	if err != nil {
		return nil, err
	}
	res.Unparsed = dedupe(append(bUnparsed, hUnparsed...))
	res.Scenarios = compareScenarios(bs, hs)
	for _, c := range res.Scenarios {
		b, h := bs[c.ID].typed, hs[c.ID].typed
		switch {
		case c.Change == "removed" && b != nil && b.Case != nil:
			res.Weakenings = append(res.Weakenings, Weakening{Scenario: c.ID, Kind: "table_row_removed",
				Detail: "row " + c.ID + " of table " + b.Case.Table + " and its assertions were removed", AssertionsLost: scenarioAssertions(b)})
		case c.Change == "removed" && b != nil:
			res.Weakenings = append(res.Weakenings, Weakening{Scenario: c.ID, Kind: "scenario_removed",
				Detail: "the scenario and all its assertions were removed", AssertionsLost: scenarioAssertions(b)})
		case c.Change == "removed":
			res.Weakenings = append(res.Weakenings, Weakening{Scenario: c.ID, Kind: "scenario_removed", Detail: "the scenario was removed"})
		case (c.Change == "modified" || c.Modified) && b != nil && h != nil:
			w, e := compareSteps(c.ID, b, h)
			res.Weakenings = append(res.Weakenings, w...)
			res.Expectations = append(res.Expectations, e...)
		case c.Change == "modified" || c.Modified:
			res.Weakenings = append(res.Weakenings, Weakening{Scenario: c.ID, Kind: "not_analysed",
				Detail: "the scenario changed but one side doesn't decode as a specification; read the file diff"})
		}
	}
	return res, nil
}

type scenarioVersion struct {
	file  string
	canon []byte
	typed *model.Scenario
}

func scenarios(repo *gitrepo.Repo, tree map[string]gitrepo.Entry, root, configPath string) (map[string]scenarioVersion, []string, error) {
	out := map[string]scenarioVersion{}
	var unparsed []string
	for _, p := range sortedKeys(tree) {
		e := tree[p]
		if !strings.HasPrefix(p, root+"/") || !(strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml")) ||
			p == configPath || strings.HasPrefix(p, root+"/environments/") || !e.Regular() {
			continue
		}
		data, err := repo.Blob(e.OID)
		if err != nil {
			return nil, nil, err
		}
		doc, err := strictyaml.Parse(data)
		if err != nil {
			unparsed = append(unparsed, p)
			continue
		}
		seq := mappingValue(doc, "scenarios")
		if seq == nil || seq.Kind != yaml.SequenceNode {
			seq = &yaml.Node{}
		}
		for _, sc := range seq.Content {
			idNode := mappingValue(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{sc}}, "id")
			if idNode == nil || idNode.Kind != yaml.ScalarNode {
				unparsed = append(unparsed, p)
				continue
			}
			canon, err := yaml.Marshal(stripPositions(sc))
			if err != nil {
				return nil, nil, err
			}
			out[idNode.Value] = scenarioVersion{file: p, canon: canon}
		}
		var spec model.Spec
		if strictyaml.Decode(data, &spec) == nil {
			if len(spec.ExpandTables(strictyaml.Decode)) > 0 {
				unparsed = append(unparsed, p)
			}
			for i := range spec.Scenarios {
				if c := spec.Scenarios[i].Case; c != nil {
					if _, dup := out[spec.Scenarios[i].ID]; !dup {
						out[spec.Scenarios[i].ID] = scenarioVersion{file: p, canon: c.Canon, typed: &spec.Scenarios[i]}
					}
					continue
				}
				if v, ok := out[spec.Scenarios[i].ID]; ok && v.file == p {
					v.typed = &spec.Scenarios[i]
					out[spec.Scenarios[i].ID] = v
				}
			}
		}
	}
	return out, unparsed, nil
}

func compareScenarios(base, head map[string]scenarioVersion) []ScenarioChange {
	var out []ScenarioChange
	for _, id := range sortedKeys(base) {
		b := base[id]
		h, ok := head[id]
		switch {
		case !ok:
			out = append(out, ScenarioChange{Change: "removed", ID: id, File: b.file})
		case b.file != h.file:
			out = append(out, ScenarioChange{Change: "moved", ID: id, File: h.file, FromFile: b.file, Modified: !bytes.Equal(b.canon, h.canon)})
		case !bytes.Equal(b.canon, h.canon):
			out = append(out, ScenarioChange{Change: "modified", ID: id, File: h.file})
		}
	}
	for _, id := range sortedKeys(head) {
		if _, ok := base[id]; !ok {
			out = append(out, ScenarioChange{Change: "added", ID: id, File: head[id].file})
		}
	}
	return out
}

func protectedPaths(repo *gitrepo.Repo, tree map[string]gitrepo.Entry, configPath string) []string {
	e, ok := tree[configPath]
	if !ok || !e.Regular() {
		return nil
	}
	data, err := repo.Blob(e.OID)
	if err != nil {
		return nil
	}
	doc, err := strictyaml.Parse(data)
	if err != nil {
		return nil
	}
	n := mappingValue(doc, "protected_paths")
	if n == nil {
		return nil
	}
	var paths []string
	if n.Decode(&paths) != nil {
		return nil
	}
	return paths
}

func readConfig(repo *gitrepo.Repo, tree map[string]gitrepo.Entry, configPath string) (*model.Config, bool, error) {
	e, ok := tree[configPath]
	if !ok {
		return nil, false, nil
	}
	if !e.Regular() {
		return nil, true, nil
	}
	data, err := repo.Blob(e.OID)
	if err != nil {
		return nil, true, err
	}
	var cfg model.Config
	if strictyaml.Decode(data, &cfg) != nil || cfg.Version != 2 {
		return nil, true, nil
	}
	return &cfg, true, nil
}

func compareDrivers(b, h *model.Config) []Weakening {
	var out []Weakening
	for _, name := range sortedKeys(b.Drivers) {
		hd, ok := h.Drivers[name]
		if !ok {
			continue
		}
		bd := b.Drivers[name]
		if bd.Official != hd.Official {
			out = append(out, Weakening{Driver: name, Kind: "driver_origin_changed",
				Detail: fmt.Sprintf("driver %s: official %s became official %s; every step that names %s now runs another program",
					name, orNone(bd.Official), orNone(hd.Official), name)})
		}
		if bp, hp := canonical(bd.Policy), canonical(hd.Policy); bp != hp {
			out = append(out, Weakening{Driver: name, Kind: "driver_policy_changed",
				Detail: fmt.Sprintf("driver %s: policy %s became %s; judge whether the driver may now touch more", name, bp, hp)})
		}
	}
	return out
}

func mappingValue(doc *yaml.Node, key string) *yaml.Node {
	if doc == nil || len(doc.Content) == 0 {
		return nil
	}
	m := doc.Content[0]
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func stripPositions(n *yaml.Node) *yaml.Node {
	c := &yaml.Node{Kind: n.Kind, Tag: n.Tag, Value: n.Value}
	for _, child := range n.Content {
		c.Content = append(c.Content, stripPositions(child))
	}
	return c
}

func union(a, b map[string]gitrepo.Entry) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	return sortedKeys(seen)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func dedupe(s []string) []string {
	sort.Strings(s)
	var out []string
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

type assertion struct {
	matcher string
	path    string
	value   any
	valued  bool
}

func (a assertion) label() string { return a.matcher + " " + a.path }

func (a assertion) shown() string {
	if !a.valued {
		return "(asserted)"
	}
	return canonical(a.value)
}

func assertionsOf(ex *model.Expect) []assertion {
	if ex == nil {
		return nil
	}
	var out []assertion
	for p, v := range ex.Equal {
		out = append(out, assertion{"equal", p, v, true})
	}
	for _, p := range ex.Present {
		out = append(out, assertion{"present", p, nil, false})
	}
	for _, p := range ex.Absent {
		out = append(out, assertion{"absent", p, nil, false})
	}
	for p, v := range ex.Keys {
		out = append(out, assertion{"keys", p, v, true})
	}
	for p, v := range ex.Length {
		out = append(out, assertion{"length", p, v, true})
	}
	for p, v := range ex.Unordered {
		out = append(out, assertion{"unordered", p, v, true})
	}
	for p, v := range ex.Any {
		out = append(out, assertion{"any", p, v, true})
	}
	for p, v := range ex.None {
		out = append(out, assertion{"none", p, v, true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].label() < out[j].label() })
	return out
}

func stepAssertions(st model.Step) []string {
	var out []string
	for _, a := range assertionsOf(st.Expect) {
		out = append(out, a.label())
	}
	return out
}

func scenarioAssertions(sc *model.Scenario) int {
	n := 0
	for _, st := range sc.Steps {
		n += len(stepAssertions(st))
	}
	return n
}

type located struct {
	step  model.Step
	setup bool
}

func indexSteps(sc *model.Scenario) (map[string]located, []string) {
	m := map[string]located{}
	var order []string
	for _, st := range sc.Setup {
		m[st.ID] = located{st, true}
		order = append(order, st.ID)
	}
	for _, st := range sc.Steps {
		m[st.ID] = located{st, false}
		order = append(order, st.ID)
	}
	return m, order
}

type stepDiff struct {
	scenario, step string
	ws             []Weakening
	es             []ExpectationChange
}

func (d *stepDiff) weaken(kind string, lost int, format string, args ...any) {
	d.ws = append(d.ws, Weakening{Scenario: d.scenario, Step: d.step, Kind: kind, Detail: fmt.Sprintf(format, args...), AssertionsLost: lost})
}

func (d *stepDiff) change(what, from, to string) {
	if from != to {
		d.es = append(d.es, ExpectationChange{Scenario: d.scenario, Step: d.step, What: what, From: from, To: to})
	}
}

func compareSteps(id string, b, h *model.Scenario) ([]Weakening, []ExpectationChange) {
	var ws []Weakening
	var es []ExpectationChange
	bm, border := indexSteps(b)
	hm, _ := indexSteps(h)
	for _, sid := range border {
		bs := bm[sid]
		hs, ok := hm[sid]
		d := &stepDiff{scenario: id, step: sid}
		lost := 0
		if !bs.setup {
			lost = len(stepAssertions(bs.step))
		}
		switch {
		case !ok && bs.setup:
			d.weaken("setup_step_removed", 0, "a setup step was removed; the scenario's starting state changed")
		case !ok:
			d.weaken("step_removed", lost, "a verification step was removed")
		case !bs.setup && hs.setup:
			d.weaken("step_moved_to_setup", lost,
				"a verification step became setup; its expectations no longer count as evidence and a mismatch is ERROR, not FAIL")
		default:
			d.compare(bs.step, hs.step, !bs.setup)
		}
		sort.SliceStable(d.es, func(i, j int) bool { return d.es[i].What < d.es[j].What })
		ws = append(ws, d.ws...)
		es = append(es, d.es...)
	}
	return ws, es
}

func (d *stepDiff) compare(b, h model.Step, verify bool) {
	bDriver, _, _ := model.SplitDo(b.Do)
	hDriver, _, _ := model.SplitDo(h.Do)
	switch {
	case b.Do == h.Do:
	case bDriver != hDriver:
		d.weaken("driver_changed", 0, "do %s became %s; another driver now performs the step and reports its facts", orNone(b.Do), orNone(h.Do))
	default:
		d.change("do", b.Do, h.Do)
	}
	d.change("changed action", canonical(b.With), canonical(h.With))
	d.change("identity", orNone(b.Identity), orNone(h.Identity))
	switch {
	case b.Variant != "" && h.Variant == "":
		d.weaken("variant_removed", 0, "credential variant %s is no longer used; the step now acts with the identity's own credential", b.Variant)
	default:
		d.change("variant", orNone(b.Variant), orNone(h.Variant))
	}
	d.change("save", canonical(b.Save), canonical(h.Save))
	d.compareTime(b, h)
	d.compareAssertions(b.Expect, h.Expect, verify)
	switch {
	case b.NoStateCheck == "" && h.NoStateCheck != "":
		d.weaken("state_check_waived", 0, "no_state_check added: %s", h.NoStateCheck)
	case b.NoStateCheck != "" && h.NoStateCheck != "" && b.NoStateCheck != h.NoStateCheck:
		d.weaken("state_check_waiver_changed", 0, "no_state_check changed from %q to %q", b.NoStateCheck, h.NoStateCheck)
	}
	for _, rule := range sortedKeys(h.Waive) {
		was, had := b.Waive[rule]
		switch {
		case !had:
			d.weaken("rule_waived", 0, "%s waived: %s", rule, h.Waive[rule])
		default:
			d.change("waive "+rule, was, h.Waive[rule])
		}
	}
}

type timing struct {
	construct, field, window, every string
}

func timingOf(st model.Step) *timing {
	switch {
	case st.Eventually != nil:
		return &timing{"eventually", "within", st.Eventually.Within, st.Eventually.Every}
	case st.Consistently != nil:
		return &timing{"consistently", "for", st.Consistently.For, st.Consistently.Every}
	case st.Never != nil:
		return &timing{"never", "for", st.Never.For, st.Never.Every}
	}
	return nil
}

func (t *timing) String() string {
	if t == nil {
		return "(none)"
	}
	every := t.every
	if every == "" {
		every = "500ms (default)"
	}
	return fmt.Sprintf("%s %s %s every %s", t.construct, t.field, t.window, every)
}

func (d *stepDiff) compareTime(b, h model.Step) {
	bt, ht := timingOf(b), timingOf(h)
	switch {
	case bt == nil && ht == nil:
		return
	case bt == nil || ht == nil || bt.construct != ht.construct:
		d.change("time", bt.String(), ht.String())
		if (bt != nil && bt.construct != "eventually") || (bt == nil && ht.construct == "eventually") {
			d.weaken("time_check_changed", 0, "%s became %s", bt.String(), ht.String())
		}
		return
	}
	what := bt.construct + "." + bt.field
	bw, berr := time.ParseDuration(bt.window)
	hw, herr := time.ParseDuration(ht.window)
	switch {
	case berr != nil || herr != nil:
		d.change(what, bt.window, ht.window)
	case bt.construct == "eventually" && hw > bw:
		d.weaken("wait_longer", 0, "%s %s became %s; the step may now pass later", what, bt.window, ht.window)
	case bt.construct != "eventually" && hw < bw:
		d.weaken("window_shorter", 0, "%s %s became %s; the step now watches for less time", what, bt.window, ht.window)
	case bw != hw:
		d.change(what, bt.window, ht.window)
	}
	be, hev := orDefault(bt.every), orDefault(ht.every)
	what = bt.construct + ".every"
	bd, berr := time.ParseDuration(be)
	hd, herr := time.ParseDuration(hev)
	switch {
	case berr != nil || herr != nil:
		d.change(what, be, hev)
	case hd > bd:
		d.weaken("sampling_sparser", 0, "%s %s became %s; the step samples less often", what, be, hev)
	case hd != bd:
		d.change(what, be, hev)
	}
}

func orDefault(every string) string {
	if every == "" {
		return "500ms"
	}
	return every
}

func (d *stepDiff) compareAssertions(b, h *model.Expect, verify bool) {
	head := map[string]assertion{}
	onPath := map[string][]string{}
	for _, a := range assertionsOf(h) {
		head[a.label()] = a
		onPath[a.path] = append(onPath[a.path], a.label())
	}
	weaker := func(a assertion, format string, args ...any) {
		if verify {
			d.weaken("assertion_weakened", 0, format, args...)
			return
		}
		d.change(a.label(), a.shown(), canonical(head[a.label()].value))
	}
	for _, a := range assertionsOf(b) {
		if c, ok := head[a.label()]; ok {
			d.compareValues(a, c, weaker)
			continue
		}
		if cover := coveringEqual(a, h); cover != "" {
			d.change(a.label(), a.shown(), "(covered by equal "+cover+")")
			continue
		}
		switch {
		case !verify:
			d.change(a.label(), a.shown(), "(not asserted)")
		case len(onPath[a.path]) > 0:
			d.weaken("assertion_weakened", 0, "%s became %s", a.label(), strings.Join(onPath[a.path], " and "))
		default:
			d.weaken("assertion_removed", 1, "%s is no longer asserted", a.label())
		}
	}
}

func (d *stepDiff) compareValues(b, h assertion, weaker func(assertion, string, ...any)) {
	switch b.matcher {
	case "unordered":
		bv, hv := sortedCanon(b.value.([]any)), sortedCanon(h.value.([]any))
		if strings.Join(bv, "\x00") == strings.Join(hv, "\x00") {
			return
		}
		if lost := missing(bv, hv); len(hv) < len(bv) && len(lost) == len(bv)-len(hv) {
			weaker(b, "%s no longer lists %s", b.label(), strings.Join(lost, ", "))
			return
		}
		d.change(b.label(), "["+strings.Join(bv, ",")+"]", "["+strings.Join(hv, ",")+"]")
	case "keys":
		bk, hk := append([]string(nil), b.value.([]string)...), append([]string(nil), h.value.([]string)...)
		sort.Strings(bk)
		sort.Strings(hk)
		d.change(b.label(), canonical(bk), canonical(hk))
	case "any", "none":
		bm, hm := b.value.(map[string]any), h.value.(map[string]any)
		if canonical(bm) == canonical(hm) {
			return
		}
		small, large := hm, bm
		if b.matcher == "none" {
			small, large = bm, hm
		}
		if subMap(small, large) {
			if b.matcher == "any" {
				weaker(b, "%s now matches an element on fewer fields: %s became %s", b.label(), canonical(bm), canonical(hm))
			} else {
				weaker(b, "%s now forbids only elements that also match more fields: %s became %s", b.label(), canonical(bm), canonical(hm))
			}
			return
		}
		d.change(b.label(), canonical(bm), canonical(hm))
	default:
		d.change(b.label(), b.shown(), h.shown())
	}
}

func coveringEqual(a assertion, h *model.Expect) string {
	if h == nil {
		return ""
	}
	for _, p := range sortedKeys(h.Equal) {
		rel, ok := under(a.path, p)
		if !ok {
			continue
		}
		if a.matcher != "absent" {
			return p
		}
		if rel == "" {
			continue
		}
		rp, err := expr.ParsePath("$" + rel)
		if err != nil {
			continue
		}
		if _, found := rp.Select(h.Equal[p]); !found {
			return p
		}
	}
	return ""
}

func under(q, p string) (string, bool) {
	switch {
	case q == p:
		return "", true
	case strings.HasPrefix(q, p+".") || strings.HasPrefix(q, p+"["):
		return q[len(p):], true
	}
	return "", false
}

func sortedCanon(vs []any) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = canonical(v)
	}
	sort.Strings(out)
	return out
}

func missing(base, head []string) []string {
	left := map[string]int{}
	for _, v := range head {
		left[v]++
	}
	var out []string
	for _, v := range base {
		if left[v] > 0 {
			left[v]--
			continue
		}
		out = append(out, v)
	}
	return out
}

func subMap(small, large map[string]any) bool {
	if len(small) >= len(large) {
		return false
	}
	for k, v := range small {
		w, ok := large[k]
		if !ok || canonical(v) != canonical(w) {
			return false
		}
	}
	return true
}

func canonical(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
