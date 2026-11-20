package protdiff

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"proctor/internal/gitrepo"
	"proctor/internal/model"
	"proctor/internal/strictyaml"
)

const (
	KindRefactor    = "refactor"
	KindDocs        = "docs"
	KindFix         = "fix"
	KindFeature     = "feature"
	KindUnspecified = "unspecified"

	KindFromFlag  = "flag"
	KindFromLabel = "label"
	KindFromNone  = "none"

	KindLabelPrefix = "proctor:"
)

var Kinds = []string{KindRefactor, KindDocs, KindFix, KindFeature}

var kindRules = map[string]string{
	KindRefactor:    "a refactor changes no protected file",
	KindDocs:        "a docs change changes no protected file",
	KindFix:         "a fix only adds scenarios and assertions: no weakening, no changed action or expectation, and no changed protected file but specifications, added manifest entries and added approved files",
	KindFeature:     "no rule beyond code-owner approval of every protected change",
	KindUnspecified: "no rule beyond code-owner approval of every protected change",
}

type KindCheck struct {
	Kind       string         `json:"kind"`
	Source     string         `json:"source"`
	Label      string         `json:"label,omitempty"`
	Rule       string         `json:"rule"`
	Mismatches []KindMismatch `json:"mismatches"`
}

type KindMismatch struct {
	Place  string `json:"place"`
	Reason string `json:"reason"`
}

func ParseKind(kind string, kindGiven bool, source string, sourceGiven bool) (*KindCheck, error) {
	if !kindGiven {
		if sourceGiven {
			return nil, fmt.Errorf("--change-kind-source needs --change-kind")
		}
		return &KindCheck{Kind: KindUnspecified, Source: KindFromNone, Rule: kindRules[KindUnspecified], Mismatches: []KindMismatch{}}, nil
	}
	if !slices.Contains(Kinds, kind) {
		return nil, fmt.Errorf("unknown change kind %q; give refactor, docs, fix or feature", kind)
	}
	kc := &KindCheck{Kind: kind, Source: KindFromFlag, Rule: kindRules[kind], Mismatches: []KindMismatch{}}
	if sourceGiven {
		switch source {
		case KindFromFlag:
		case KindFromLabel:
			kc.Source, kc.Label = KindFromLabel, KindLabelPrefix+kind
		default:
			return nil, fmt.Errorf("unknown --change-kind-source %q; give flag or label", source)
		}
	}
	return kc, nil
}

func (kc *KindCheck) SourceText() string {
	switch kc.Source {
	case KindFromLabel:
		return "pull request label " + kc.Label
	case KindFromFlag:
		return "--change-kind"
	}
	return "not given"
}

func (kc *KindCheck) Check(repo *gitrepo.Repo, res *Result, configPath string) error {
	kc.Mismatches = []KindMismatch{}
	switch kc.Kind {
	case KindRefactor, KindDocs:
		for _, f := range res.Files {
			kc.add(f.Path, f.Change+"; "+kc.Rule)
		}
	case KindFix:
		return kc.checkFix(repo, res, configPath)
	}
	return nil
}

func (kc *KindCheck) add(place, reason string) {
	kc.Mismatches = append(kc.Mismatches, KindMismatch{Place: place, Reason: reason})
}

func (kc *KindCheck) checkFix(repo *gitrepo.Repo, res *Result, configPath string) error {
	if res.Count() == 0 {
		return nil
	}
	base, err := readKindSide(repo, res.Base, configPath)
	if err != nil {
		return err
	}
	head, err := readKindSide(repo, res.Head, configPath)
	if err != nil {
		return err
	}
	approved := approvedRoot(configPath)
	for _, f := range res.Files {
		switch {
		case f.Path == configPath:
			kc.add(f.Path, f.Change+"; Proctor can't show that a change to the configuration only adds scenarios or assertions")
		case f.Path == base.manifestPath || f.Path == head.manifestPath:
			if f.Change != "modified" || f.OldMode != "" || f.NewMode != "" || !onlyAddsEntries(base.manifest, head.manifest) {
				kc.add(f.Path, f.Change+"; a fix may only add entries to the manifest, keeping every entry it had, in order")
			}
		case base.specs[f.Path] || head.specs[f.Path]:
		case strings.HasPrefix(f.Path, approved) && f.Change == "added":
		case strings.HasPrefix(f.Path, approved):
			kc.add(f.Path, f.Change+"; a fix may add approved files, not change, move or remove them")
		default:
			kc.add(f.Path, f.Change+"; Proctor doesn't compare this file's content, so it can't show the change only adds scenarios or assertions")
		}
	}
	for _, w := range res.Weakenings {
		kc.add(w.Place(configPath), "weakening "+w.Kind+": "+w.Detail)
	}
	for _, u := range res.Unparsed {
		kc.add(u, "the file doesn't parse on one side, so its scenarios weren't compared")
	}
	for _, e := range res.Expectations {
		if e.approvedFile {
			continue
		}
		kc.add(strings.TrimSpace(e.Scenario+" "+e.Step), fmt.Sprintf("%s changed from %s to %s", e.What, e.From, e.To))
	}
	for _, m := range res.Moves {
		kc.add(m.Scenario+" "+m.FromStep, "assertion "+m.Assertion+" moved to step "+m.ToStep)
	}
	return nil
}

func (w Weakening) Place(configPath string) string {
	switch {
	case w.Scenario != "":
		return strings.TrimSpace(w.Scenario + " " + w.Step)
	case w.Driver != "":
		return "driver " + w.Driver
	case w.Mutant != "":
		return "mutant " + w.Mutant
	case w.Coverage != "":
		return w.Coverage
	}
	return configPath
}

type kindSide struct {
	manifestPath string
	manifest     *model.Manifest
	specs        map[string]bool
}

func readKindSide(repo *gitrepo.Repo, commit, configPath string) (kindSide, error) {
	side := kindSide{specs: map[string]bool{}}
	tree, err := repo.Tree(commit)
	if err != nil {
		return side, err
	}
	cfg, _, err := readConfig(repo, tree, configPath)
	if err != nil || cfg == nil || cfg.Manifest == "" {
		return side, err
	}
	root := path.Dir(configPath)
	side.manifestPath = root + "/" + cfg.Manifest
	e, ok := tree[side.manifestPath]
	if !ok || !e.Regular() {
		return side, nil
	}
	data, err := repo.Blob(e.OID)
	if err != nil {
		return side, err
	}
	var man model.Manifest
	if strictyaml.Decode(data, &man) != nil {
		return side, nil
	}
	side.manifest = &man
	mutants := mutantsPath(cfg, configPath)
	for _, s := range man.Specifications {
		p := root + "/" + s.Path
		if se, ok := tree[p]; ok && p != side.manifestPath && scenarioFile(p, se, root, configPath, mutants) {
			side.specs[p] = true
		}
	}
	return side, nil
}

func onlyAddsEntries(base, head *model.Manifest) bool {
	if base == nil || head == nil || base.Version != head.Version {
		return false
	}
	kept := 0
	for _, h := range head.Specifications {
		if kept < len(base.Specifications) && h == base.Specifications[kept] {
			kept++
			continue
		}
		for _, b := range base.Specifications {
			if h.ID == b.ID || h.Path == b.Path {
				return false
			}
		}
	}
	return kept == len(base.Specifications)
}
