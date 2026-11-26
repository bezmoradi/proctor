package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"proctor/internal/gitrepo"
	"proctor/internal/protdiff"
)

type diffOptions struct {
	common
	base, head, prHead string
	kind, kindSource   string
	set                func(name string) bool
}

func newDiffCmd(stdout, stderr io.Writer) *cobra.Command {
	var o diffOptions
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "List every changed protected file and scenario between two commits, weakenings first",
		Example: `  proctor diff --base main --head HEAD
  proctor diff --head "$TEST_MERGE" --pr-head "$PR_HEAD" --json
  proctor diff --base main --head HEAD --change-kind fix`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.set = cmd.Flags().Changed
			return exitWith(diff(o, stdout, stderr))
		},
	}
	addCommon(cmd, &o.common)
	cmd.Flags().StringVar(&o.base, "base", "", "base commit")
	cmd.Flags().StringVar(&o.head, "head", "", "head commit, or the test merge commit when --pr-head is given")
	cmd.Flags().StringVar(&o.prHead, "pr-head", "", "pull request head; makes --head a test merge whose first parent is the base")
	addChangeKind(cmd, &o.kind, &o.kindSource)
	return cmd
}

func diff(o diffOptions, stdout, stderr io.Writer) int {
	c, base, head, prHead := o.common, o.base, o.head, o.prHead
	if head == "" || (base == "") == (prHead == "") {
		fmt.Fprintln(stderr, "proctor diff: give --head and exactly one of --base or --pr-head")
		return exitInvalid
	}
	kind, err := protdiff.ParseKind(o.kind, o.set("change-kind"), o.kindSource, o.set("change-kind-source"))
	if err != nil {
		fmt.Fprintf(stderr, "proctor diff: CHG-01: %s\n", safe(err.Error()))
		return exitInvalid
	}
	repo, err := gitrepo.Open(c.repo)
	if err != nil {
		fmt.Fprintf(stderr, "proctor: %s\n", safe(err.Error()))
		return exitInvalid
	}
	if prHead != "" {
		base, err = protdiff.BaseForTestMerge(repo, head, prHead)
		if err != nil {
			return diffError(stdout, stderr, c.json, err)
		}
	} else if err := protdiff.CheckMergeBase(repo, base, head); err != nil {
		return diffBaseError(stdout, stderr, c.json, err)
	}
	res, err := protdiff.Compute(repo, base, head, c.config)
	if err != nil {
		return diffError(stdout, stderr, c.json, err)
	}
	if err := kind.Check(repo, res, c.config); err != nil {
		return diffError(stdout, stderr, c.json, err)
	}
	code := exitOK
	if len(kind.Mismatches) > 0 {
		code = exitIntegrity
	}
	if c.json {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"result": resultName(code), "exit_code": code, "protected_changes": res.Count(), "weakenings": len(res.Weakenings), "change_kind": kind, "diff": res})
		return code
	}
	fmt.Fprintf(stdout, "PROCTOR DIFF\n\nBase: %s\nHead: %s\nChange kind: %s\n\n", safe(res.Base), safe(res.Head), changeKindLine(kind))
	if res.Count() == 0 {
		fmt.Fprintln(stdout, "Protected changes against base: none")
		return exitOK
	}
	if len(res.Weakenings) > 0 {
		fmt.Fprintln(stdout, "Weakenings (review these first):")
		for _, wk := range res.Weakenings {
			line := fmt.Sprintf("  %-20s %s: %s", safe(wk.Kind), safe(weakeningPlace(wk, c.config)), safe(wk.Detail))
			if wk.AssertionsLost > 0 {
				line += fmt.Sprintf(" (%d assertion(s) lost)", wk.AssertionsLost)
			}
			fmt.Fprintln(stdout, line)
		}
		fmt.Fprintln(stdout)
	}
	if len(res.Files) > 0 {
		fmt.Fprintln(stdout, "Changed files:")
	}
	for _, f := range res.Files {
		line := fmt.Sprintf("  %-9s %s", safe(f.Change), safe(f.Path))
		if f.From != "" {
			line += " (from " + safe(f.From) + ")"
		}
		if f.OldMode != "" || f.NewMode != "" {
			line += fmt.Sprintf(" [mode %s -> %s]", safe(orDash(f.OldMode)), safe(orDash(f.NewMode)))
		}
		fmt.Fprintln(stdout, line+"  approval required")
	}
	if len(res.Scenarios) > 0 {
		fmt.Fprintln(stdout, "\nScenarios:")
		for _, s := range res.Scenarios {
			line := fmt.Sprintf("  %-9s %s  %s", safe(s.Change), safe(s.ID), safe(s.File))
			if s.FromFile != "" {
				line += " (from " + safe(s.FromFile) + ")"
				if s.Modified {
					line += ", modified"
				}
			}
			fmt.Fprintln(stdout, line)
		}
	}
	if len(res.Added) > 0 {
		fmt.Fprintln(stdout, "\nAdded steps (read what each one asserts):")
		for _, a := range res.Added {
			fmt.Fprintln(stdout, "  "+safe(addedLine(a)))
		}
	}
	if len(res.Drivers) > 0 {
		fmt.Fprintln(stdout, "\nDrivers:")
		for _, d := range res.Drivers {
			fmt.Fprintf(stdout, "  %-20s driver %s: %s\n", safe(d.Kind), safe(d.Driver), safe(d.Detail))
		}
	}
	if len(res.Expectations) > 0 {
		fmt.Fprintln(stdout, "\nChanged actions and expectations:")
		for _, e := range res.Expectations {
			fmt.Fprintf(stdout, "  %s %s %s: %s -> %s\n", safe(e.Scenario), safe(e.Step), safe(e.What), safe(e.From), safe(e.To))
		}
	}
	if len(res.Moves) > 0 {
		fmt.Fprintln(stdout, "\nMoved assertions (not lost):")
		for _, m := range res.Moves {
			line := fmt.Sprintf("  assertion_moved %s %s -> %s: %s", safe(m.Scenario), safe(m.FromStep), safe(m.ToStep), safe(m.Assertion))
			if m.Value != "" {
				line += " " + safe(m.Value)
			}
			fmt.Fprintln(stdout, line)
		}
	}
	for _, u := range res.Unparsed {
		fmt.Fprintf(stdout, "  scenarios not compared (file doesn't parse on one side): %s\n", safe(u))
	}
	fmt.Fprintf(stdout, "\nProtected changes against base: %d (approval required)\n", res.Count())
	writeKindMismatches(stdout, kind)
	return code
}

func addChangeKind(cmd *cobra.Command, kind, source *string) {
	cmd.Flags().StringVar(kind, "change-kind", "", "the kind of change the author claims: refactor, docs, fix or feature; a refactor or docs change fails on any protected change and a fix on anything but added scenarios and assertions (CHG-02)")
	cmd.Flags().StringVar(source, "change-kind-source", protdiff.KindFromFlag, "where --change-kind came from, recorded in the report: flag, or label when the workflow mapped a proctor:<kind> pull request label")
}

func changeKindLine(k *protdiff.KindCheck) string {
	if k.Source == protdiff.KindFromNone {
		return k.Kind
	}
	return safe(k.Kind) + " (" + safe(k.SourceText()) + ")"
}

func writeKindMismatches(w io.Writer, k *protdiff.KindCheck) {
	if len(k.Mismatches) == 0 {
		return
	}
	fmt.Fprintf(w, "\nChange kind %s doesn't match the protected changes (CHG-02: %s): %d mismatch(es)\n", safe(k.Kind), safe(k.Rule), len(k.Mismatches))
	for _, m := range k.Mismatches {
		fmt.Fprintf(w, "  %s: %s\n", safe(m.Place), safe(m.Reason))
	}
}

func diffError(stdout, stderr io.Writer, asJSON bool, err error) int {
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"result": "integrity_violation", "exit_code": exitIntegrity, "error": err.Error()})
	} else {
		fmt.Fprintf(stderr, "proctor diff: integrity violation (INT-08): %s\n", safe(err.Error()))
	}
	return exitIntegrity
}

func diffBaseError(stdout, stderr io.Writer, asJSON bool, err error) int {
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"result": "integrity_violation", "exit_code": exitIntegrity, "error": "INT-09: " + err.Error()})
	} else {
		fmt.Fprintf(stderr, "proctor diff: integrity violation (INT-09): %s\n", safe(err.Error()))
	}
	return exitIntegrity
}

func weakeningPlace(wk protdiff.Weakening, config string) string {
	return wk.Place(config)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

const shownWithLimit = 240

func addedLine(a protdiff.AddedStep) string {
	var line string
	switch {
	case a.Table != "" && a.Scenario == "":
		line = "table " + a.Table + " " + a.Phase + " " + a.Step
	case a.Table != "":
		line = "table " + a.Table + " case " + a.Scenario
	case a.Phase != "verification":
		line = a.Scenario + " " + a.Phase + " " + a.Step
	default:
		line = a.Scenario + " " + a.Step
	}
	line += ": " + a.Do
	if a.Identity != "" {
		who := a.Identity
		if a.Variant != "" {
			who += " variant " + a.Variant
		}
		line += " [" + who + "]"
	}
	if a.With != "" {
		with := []rune(a.With)
		if len(with) > shownWithLimit {
			with = append(with[:shownWithLimit], []rune("…")...)
		}
		line += " " + string(with)
	}
	if a.Timing != "" {
		line += ", " + a.Timing
	}
	if a.UnlessPassed != "" {
		line += ", unless_passed " + a.UnlessPassed
	}
	if len(a.Expect) == 0 {
		return line + " -> (no expectation)"
	}
	return line + " -> " + strings.Join(a.Expect, "; ")
}
