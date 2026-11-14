// Package protdiff lists changes to protected inputs between two commits
// (INT-08 and INT-09 in prd-revised.md).
//
// It compares the two commits' trees entry by entry, by blob ID and mode,
// and never uses Git's rename detection: a removed path and an added path
// with the same blob ID and mode are reported as a move; anything else is a
// removal plus an addition. It also lists scenario IDs that were added,
// removed, modified or moved between specification files. Any Git error is
// returned as an error, which callers report as an integrity violation,
// never as "no changes".
package protdiff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"proctor/internal/gitrepo"
	"proctor/internal/integrity"
	"proctor/internal/model"
	"proctor/internal/strictyaml"
)

// FileChange is one changed protected path.
type FileChange struct {
	Change  string `json:"change"` // added, removed, modified, moved
	Path    string `json:"path"`
	From    string `json:"from,omitempty"` // for moves
	OldMode string `json:"old_mode,omitempty"`
	NewMode string `json:"new_mode,omitempty"`
}

// ScenarioChange is one changed scenario, matched by ID across all
// specification files.
type ScenarioChange struct {
	Change   string `json:"change"` // added, removed, modified, moved
	ID       string `json:"id"`
	File     string `json:"file"`
	FromFile string `json:"from_file,omitempty"`
	Modified bool   `json:"modified,omitempty"` // for moves
}

// Weakening is a change that makes a scenario check less (§7.5): a removed
// scenario, step or assertion, a verification step moved into setup, or an
// added no_state_check or waive entry. A reviewer reads these first.
type Weakening struct {
	Scenario       string `json:"scenario"`
	Step           string `json:"step,omitempty"`
	Kind           string `json:"kind"`
	Detail         string `json:"detail"`
	AssertionsLost int    `json:"assertions_lost"`
}

// ExpectationChange is a changed request or expectation that isn't simply
// a removal: a different status, value, identity, credential or query.
type ExpectationChange struct {
	Scenario string `json:"scenario"`
	Step     string `json:"step"`
	What     string `json:"what"`
	From     string `json:"from"`
	To       string `json:"to"`
}

// Result lists every protected change between Base and Head.
type Result struct {
	Base         string              `json:"base"`
	Head         string              `json:"head"`
	Files        []FileChange        `json:"files"`
	Scenarios    []ScenarioChange    `json:"scenarios"`
	Weakenings   []Weakening         `json:"weakenings"`
	Expectations []ExpectationChange `json:"expectation_changes"`
	// Unparsed lists specification files whose scenarios couldn't be
	// compared because one side doesn't parse; their file change still shows.
	Unparsed []string `json:"unparsed,omitempty"`
}

// Count returns the number of changed protected paths.
func (r *Result) Count() int { return len(r.Files) }

// BaseForTestMerge returns the base commit for GitHub's test merge commit:
// its first parent, after checking that its second parent is the pull
// request's head (INT-09).
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

// Compute compares the protected inputs of base and head.
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
	// Paths protected on either side count, so that removing an entry from
	// protected_paths doesn't hide the change it permits.
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
	// Exact moves only: same blob and mode at a new path.
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
	res.Weakenings, res.Expectations = []Weakening{}, []ExpectationChange{}
	for _, c := range res.Scenarios {
		b, h := bs[c.ID].typed, hs[c.ID].typed
		switch {
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
	typed *model.Scenario // nil when the file doesn't decode strictly
}

// scenarios maps scenario ID to its file and canonical form, for every
// specification candidate under the protected root.
func scenarios(repo *gitrepo.Repo, tree map[string]gitrepo.Entry, root, configPath string) (map[string]scenarioVersion, []string, error) {
	out := map[string]scenarioVersion{}
	var unparsed []string
	for _, p := range sortedPaths(tree) {
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
			continue // not a specification (for example the manifest)
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
			for i := range spec.Scenarios {
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
	for _, id := range sortedPaths(base) {
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
	for _, id := range sortedPaths(head) {
		if _, ok := base[id]; !ok {
			out = append(out, ScenarioChange{Change: "added", ID: id, File: head[id].file})
		}
	}
	return out
}

// protectedPaths reads protected_paths from the commit's proctor.yaml, or
// returns nil if it can't be read; the configuration file's own change is
// listed either way.
func protectedPaths(repo *gitrepo.Repo, tree map[string]gitrepo.Entry, configPath string) []string {
	e, ok := tree[configPath]
	if !ok || !e.Regular() {
		return nil
	}
	data, err := repo.Blob(e.OID)
	if err != nil {
		return nil
	}
	var cfg model.Config
	if strictyaml.Decode(data, &cfg) != nil {
		return nil
	}
	return cfg.ProtectedPaths
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

// stripPositions copies a node without line, column and comment data, so
// that only content changes make two scenarios differ.
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
	return sortedPaths(seen)
}

func sortedPaths[V any](m map[string]V) []string {
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

// stepAssertions lists a step's assertions by name, for counting and
// matching across versions.
func stepAssertions(st model.Step) []string {
	var out []string
	if ex := st.Expect; ex != nil {
		if ex.Status != nil {
			out = append(out, "status")
		}
		for k := range ex.Headers {
			out = append(out, "header "+strings.ToLower(k))
		}
		for k := range ex.JSON {
			out = append(out, "json "+k)
		}
		for _, k := range ex.Present {
			out = append(out, "present "+k)
		}
		for _, k := range ex.Absent {
			out = append(out, "absent "+k)
		}
		for k := range ex.Length {
			out = append(out, "length "+k)
		}
		for k := range ex.Keys {
			out = append(out, "keys "+k)
		}
		for _, h := range ex.AbsentHeaders {
			out = append(out, "absent_header "+strings.ToLower(h))
		}
	}
	if st.Verify != nil && st.Verify.Postgres != nil && st.Verify.Postgres.Expect != nil {
		ex := st.Verify.Postgres.Expect
		if ex.Rows != nil {
			out = append(out, "rows")
		}
		for k := range ex.FirstRow {
			out = append(out, "first_row "+k)
		}
	}
	sort.Strings(out)
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

// compareSteps matches steps by ID and reports what each lost or changed.
// Setup steps carry no evidence (FIX-04), so only verification assertions
// count as lost.
func compareSteps(id string, b, h *model.Scenario) ([]Weakening, []ExpectationChange) {
	var ws []Weakening
	var es []ExpectationChange
	bm, border := indexSteps(b)
	hm, _ := indexSteps(h)
	for _, sid := range border {
		bs := bm[sid]
		hs, ok := hm[sid]
		lost := 0
		if !bs.setup {
			lost = len(stepAssertions(bs.step))
		}
		switch {
		case !ok && bs.setup:
			ws = append(ws, Weakening{Scenario: id, Step: sid, Kind: "setup_step_removed", Detail: "a setup step was removed; the scenario's starting state changed"})
			continue
		case !ok:
			ws = append(ws, Weakening{Scenario: id, Step: sid, Kind: "step_removed", Detail: "a verification step was removed", AssertionsLost: lost})
			continue
		case !bs.setup && hs.setup:
			ws = append(ws, Weakening{Scenario: id, Step: sid, Kind: "step_moved_to_setup",
				Detail: "a verification step became setup; its expectations no longer count as evidence and a mismatch is ERROR, not FAIL", AssertionsLost: lost})
			continue
		}
		if !bs.setup {
			had := map[string]bool{}
			for _, a := range stepAssertions(hs.step) {
				had[a] = true
			}
			for _, a := range stepAssertions(bs.step) {
				if !had[a] {
					ws = append(ws, Weakening{Scenario: id, Step: sid, Kind: "assertion_removed", Detail: a + " is no longer asserted", AssertionsLost: 1})
				}
			}
		}
		if bs.step.NoStateCheck == "" && hs.step.NoStateCheck != "" {
			ws = append(ws, Weakening{Scenario: id, Step: sid, Kind: "state_check_waived", Detail: "no_state_check added: " + hs.step.NoStateCheck})
		}
		for _, rule := range sortedKeys(hs.step.Waive) {
			if _, had := bs.step.Waive[rule]; !had {
				ws = append(ws, Weakening{Scenario: id, Step: sid, Kind: "rule_waived", Detail: rule + " waived: " + hs.step.Waive[rule]})
			}
		}
		for _, c := range stepChanges(bs.step, hs.step) {
			c.Scenario, c.Step = id, sid
			es = append(es, c)
		}
	}
	return ws, es
}

func stepChanges(b, h model.Step) []ExpectationChange {
	var out []ExpectationChange
	diff := func(what string, x, y any) {
		xs, ys := canonical(x), canonical(y)
		if xs != ys {
			out = append(out, ExpectationChange{What: what, From: xs, To: ys})
		}
	}
	if b.Request != nil && h.Request != nil {
		diff("method", b.Request.Method, h.Request.Method)
		diff("path", b.Request.Path, h.Request.Path)
		diff("identity", b.Request.Identity, h.Request.Identity)
		diff("credential", b.Request.Credential, h.Request.Credential)
		diff("query", b.Request.Query, h.Request.Query)
		diff("body", b.Request.Body, h.Request.Body)
	}
	if b.Expect != nil && h.Expect != nil {
		diff("status", b.Expect.Status, h.Expect.Status)
		for k, v := range b.Expect.JSON {
			if w, ok := h.Expect.JSON[k]; ok {
				diff("json "+k, v, w)
			}
		}
		for k, v := range b.Expect.Headers {
			for hk, w := range h.Expect.Headers {
				if strings.EqualFold(k, hk) {
					diff("header "+k, v, w)
				}
			}
		}
		for k, v := range b.Expect.Keys {
			if w, ok := h.Expect.Keys[k]; ok {
				diff("keys "+k, v, w)
			}
		}
		for k, v := range b.Expect.Length {
			if w, ok := h.Expect.Length[k]; ok {
				diff("length "+k, v, w)
			}
		}
	}
	if b.Verify != nil && h.Verify != nil && b.Verify.Postgres != nil && h.Verify.Postgres != nil {
		bp, hp := b.Verify.Postgres, h.Verify.Postgres
		diff("connection", bp.Connection, hp.Connection)
		diff("query", strings.TrimSpace(bp.Query), strings.TrimSpace(hp.Query))
		diff("args", bp.Args, hp.Args)
		if bp.Expect != nil && hp.Expect != nil {
			diff("rows", bp.Expect.Rows, hp.Expect.Rows)
			for k, v := range bp.Expect.FirstRow {
				if w, ok := hp.Expect.FirstRow[k]; ok {
					diff("first_row "+k, v, w)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].What < out[j].What })
	return out
}

// canonical renders a decoded value for comparison and display.
func canonical(v any) string {
	if p, ok := v.(*int); ok {
		if p == nil {
			return "(none)"
		}
		return fmt.Sprint(*p)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
