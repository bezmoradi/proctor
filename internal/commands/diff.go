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
}

func newDiffCmd(stdout, stderr io.Writer) *cobra.Command {
	var o diffOptions
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "List every changed protected file and scenario between two commits, weakenings first",
		Example: `  proctor diff --base main --head HEAD
  proctor diff --head "$TEST_MERGE" --pr-head "$PR_HEAD" --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitWith(diff(o, stdout, stderr))
		},
	}
	addCommon(cmd, &o.common)
	cmd.Flags().StringVar(&o.base, "base", "", "base commit")
	cmd.Flags().StringVar(&o.head, "head", "", "head commit, or the test merge commit when --pr-head is given")
	cmd.Flags().StringVar(&o.prHead, "pr-head", "", "pull request head; makes --head a test merge whose first parent is the base")
	return cmd
}

func diff(o diffOptions, stdout, stderr io.Writer) int {
	c, base, head, prHead := o.common, o.base, o.head, o.prHead
	if head == "" || (base == "") == (prHead == "") {
		fmt.Fprintln(stderr, "proctor diff: give --head and exactly one of --base or --pr-head")
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
	}
	res, err := protdiff.Compute(repo, base, head, c.config)
	if err != nil {
		return diffError(stdout, stderr, c.json, err)
	}
	if c.json {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"result": "passed", "exit_code": exitOK, "protected_changes": res.Count(), "weakenings": len(res.Weakenings), "diff": res})
		return exitOK
	}
	fmt.Fprintf(stdout, "PROCTOR DIFF\n\nBase: %s\nHead: %s\n\n", safe(res.Base), safe(res.Head))
	if res.Count() == 0 {
		fmt.Fprintln(stdout, "Protected changes against base: none")
		return exitOK
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
	if len(res.Weakenings) > 0 {
		fmt.Fprintln(stdout, "\nWeakenings (review these first):")
		for _, wk := range res.Weakenings {
			line := fmt.Sprintf("  %-20s %s: %s", safe(wk.Kind), safe(weakeningPlace(wk, c.config)), safe(wk.Detail))
			if wk.AssertionsLost > 0 {
				line += fmt.Sprintf(" (%d assertion(s) lost)", wk.AssertionsLost)
			}
			fmt.Fprintln(stdout, line)
		}
	}
	if len(res.Expectations) > 0 {
		fmt.Fprintln(stdout, "\nChanged actions and expectations:")
		for _, e := range res.Expectations {
			fmt.Fprintf(stdout, "  %s %s %s: %s -> %s\n", safe(e.Scenario), safe(e.Step), safe(e.What), safe(e.From), safe(e.To))
		}
	}
	for _, u := range res.Unparsed {
		fmt.Fprintf(stdout, "  scenarios not compared (file doesn't parse on one side): %s\n", safe(u))
	}
	fmt.Fprintf(stdout, "\nProtected changes against base: %d (approval required)\n", res.Count())
	return exitOK
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

func weakeningPlace(wk protdiff.Weakening, config string) string {
	switch {
	case wk.Scenario != "":
		return strings.TrimSpace(wk.Scenario + " " + wk.Step)
	case wk.Driver != "":
		return "driver " + wk.Driver
	}
	return config
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
