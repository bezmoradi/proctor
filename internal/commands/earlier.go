package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const earlierReportLimit = 64 << 20

const (
	earlierNotChecked   = "not_checked"
	earlierFirstAttempt = "first_attempt"
	earlierNoFail       = "no_fail"
	earlierFailed       = "failed"
	earlierUnknown      = "unknown"
)

type earlierAttempts struct {
	Status   string           `json:"status"`
	Reason   string           `json:"reason,omitempty"`
	Folder   string           `json:"folder,omitempty"`
	Attempts []earlierAttempt `json:"attempts"`
	Ignored  []earlierIgnored `json:"ignored"`

	current int
}

type earlierAttempt struct {
	Attempt int      `json:"attempt"`
	Status  string   `json:"status"`
	Result  string   `json:"result,omitempty"`
	Failed  []string `json:"failed,omitempty"`
	Files   []string `json:"files,omitempty"`
	Reason  string   `json:"reason,omitempty"`
}

type earlierIgnored struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

type earlierReport struct {
	Mode       *string `json:"mode"`
	Result     *string `json:"result"`
	Provenance *struct {
		Repository   string            `json:"repository"`
		TestedCommit string            `json:"tested_commit"`
		SHA256       map[string]string `json:"sha256"`
		CIRunID      string            `json:"ci_run_id"`
		CIRunAttempt string            `json:"ci_run_attempt"`
	} `json:"provenance"`
	Scenarios *[]struct {
		ID     string `json:"id"`
		Table  string `json:"table"`
		Result string `json:"result"`
	} `json:"scenarios"`
}

func notCheckedEarlier(reason string) *earlierAttempts {
	return &earlierAttempts{Status: earlierNotChecked, Reason: reason, Attempts: []earlierAttempt{}, Ignored: []earlierIgnored{}}
}

func checkEarlier(folder string, given bool, p authProvenance) *earlierAttempts {
	current, _ := strconv.Atoi(p.CIRunAttempt)
	if !given {
		ea := notCheckedEarlier("--earlier-reports wasn't given")
		ea.current = current
		return ea
	}
	ea := &earlierAttempts{Folder: folder, Attempts: []earlierAttempt{}, Ignored: []earlierIgnored{}, current: current}
	switch {
	case p.Repository == "" || p.CIRunID == "":
		ea.Status, ea.Reason = earlierUnknown, "this run has no GITHUB_REPOSITORY or GITHUB_RUN_ID, so no report can be told to be from the same run"
		return ea
	case current < 1 || strconv.Itoa(current) != p.CIRunAttempt:
		ea.Status, ea.Reason = earlierUnknown, fmt.Sprintf("this run's attempt %q (GITHUB_RUN_ATTEMPT) isn't a positive whole number", p.CIRunAttempt)
		return ea
	case p.TestedCommit == "":
		ea.Status, ea.Reason = earlierUnknown, "this run has no tested commit to compare with"
		return ea
	}
	found := map[int]*earlierAttempt{}
	names, err := earlierFolder(folder)
	if err != nil {
		ea.Reason = err.Error()
	}
	for _, name := range names {
		r, why := readEarlier(filepath.Join(folder, name))
		if why == "" {
			why = matchEarlier(r, p, current)
		}
		if why != "" {
			ea.Ignored = append(ea.Ignored, earlierIgnored{File: name, Reason: why})
			continue
		}
		n, _ := strconv.Atoi(r.Provenance.CIRunAttempt)
		a := found[n]
		if a == nil {
			a = &earlierAttempt{Attempt: n, Status: earlierNoFail, Result: *r.Result}
			found[n] = a
		} else if a.Result != *r.Result {
			a.Result = a.Result + ", " + *r.Result
		}
		a.Files = append(a.Files, name)
		for _, s := range *r.Scenarios {
			if s.Result != "FAIL" {
				continue
			}
			id := s.ID
			if s.Table != "" {
				id = s.Table + "/" + s.ID
			}
			if !slices.Contains(a.Failed, id) {
				a.Failed = append(a.Failed, id)
			}
			a.Status = earlierFailed
		}
	}
	for n := 1; n < current; n++ {
		if a := found[n]; a != nil {
			ea.Attempts = append(ea.Attempts, *a)
			continue
		}
		why := "no report for this attempt of the same run, commit and input hashes in the folder"
		if err != nil {
			why = "the folder couldn't be read"
		}
		ea.Attempts = append(ea.Attempts, earlierAttempt{Attempt: n, Status: earlierUnknown, Reason: why})
	}
	ea.Status = earlierFirstAttempt
	for _, a := range ea.Attempts {
		switch {
		case a.Status == earlierFailed:
			ea.Status = earlierFailed
		case a.Status == earlierUnknown && ea.Status != earlierFailed:
			ea.Status = earlierUnknown
		case ea.Status == earlierFirstAttempt:
			ea.Status = earlierNoFail
		}
	}
	return ea
}

func earlierFolder(folder string) ([]string, error) {
	fi, err := os.Lstat(folder)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("the folder %s doesn't exist", folder)
	case err != nil:
		return nil, err
	case fi.Mode()&fs.ModeSymlink != 0:
		return nil, fmt.Errorf("the folder %s is a symbolic link; Proctor reads earlier reports only from a real folder", folder)
	case !fi.IsDir():
		return nil, fmt.Errorf("%s isn't a folder", folder)
	}
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func readEarlier(path string) (*earlierReport, string) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err.Error()
	}
	if !fi.Mode().IsRegular() {
		return nil, "a " + describeMode(fi.Mode()) + "; Proctor reads only regular files directly in the folder"
	}
	if fi.Size() > earlierReportLimit {
		return nil, fmt.Sprintf("larger than %d MiB", earlierReportLimit>>20)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|noFollow|nonBlock, 0)
	if err != nil {
		return nil, err.Error()
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !os.SameFile(fi, st) || !st.Mode().IsRegular() {
		return nil, "it changed while being opened"
	}
	data, err := io.ReadAll(io.LimitReader(f, earlierReportLimit+1))
	if err != nil {
		return nil, err.Error()
	}
	if len(data) > earlierReportLimit {
		return nil, fmt.Sprintf("larger than %d MiB", earlierReportLimit>>20)
	}
	var r earlierReport
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&r); err != nil {
		return nil, "not a Proctor report: " + err.Error()
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, "not a Proctor report: data after the JSON document"
	}
	switch {
	case r.Mode == nil || r.Result == nil || r.Provenance == nil || r.Scenarios == nil:
		return nil, "not a Proctor report: mode, result, provenance or scenarios is missing"
	case *r.Mode != "authoritative":
		return nil, fmt.Sprintf("mode %q, not authoritative", *r.Mode)
	}
	known := false
	for _, c := range []int{exitOK, exitFailed, exitError, exitInvalid, exitIntegrity} {
		known = known || *r.Result == resultName(c)
	}
	if !known {
		return nil, fmt.Sprintf("result %q isn't a Proctor result", *r.Result)
	}
	for _, s := range *r.Scenarios {
		switch s.Result {
		case "PASS", "FAIL", "ERROR", "NOT_RUN":
		default:
			return nil, fmt.Sprintf("scenario %q has result %q, not a Proctor result", s.ID, s.Result)
		}
	}
	return &r, ""
}

func matchEarlier(r *earlierReport, p authProvenance, current int) string {
	e := r.Provenance
	n, err := strconv.Atoi(e.CIRunAttempt)
	switch {
	case !strings.EqualFold(e.Repository, p.Repository):
		return fmt.Sprintf("repository %q, not this run's %q", e.Repository, p.Repository)
	case e.CIRunID != p.CIRunID:
		return fmt.Sprintf("workflow run %q, not this run %q", e.CIRunID, p.CIRunID)
	case err != nil || n < 1 || strconv.Itoa(n) != e.CIRunAttempt:
		return fmt.Sprintf("attempt %q isn't a positive whole number", e.CIRunAttempt)
	case n >= current:
		return fmt.Sprintf("attempt %d isn't earlier than this attempt %d", n, current)
	case e.TestedCommit != p.TestedCommit:
		return fmt.Sprintf("tested commit %q, not this run's %q", e.TestedCommit, p.TestedCommit)
	}
	if diff := hashDiff(e.SHA256, p.SHA256); diff != "" {
		return "input hashes differ: " + diff
	}
	return ""
}

func hashDiff(earlier, now map[string]string) string {
	var changed, gone, added []string
	for k, v := range now {
		w, ok := earlier[k]
		switch {
		case !ok:
			added = append(added, k)
		case w != v:
			changed = append(changed, k)
		}
	}
	for k := range earlier {
		if _, ok := now[k]; !ok {
			gone = append(gone, k)
		}
	}
	var parts []string
	for _, g := range []struct {
		label string
		keys  []string
	}{{"changed", changed}, {"only in this run", added}, {"only in that report", gone}} {
		if len(g.keys) > 0 {
			sort.Strings(g.keys)
			parts = append(parts, g.label+" "+shortList(g.keys, 5))
		}
	}
	return strings.Join(parts, "; ")
}

func shortList(items []string, max int) string {
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:max], ", ") + fmt.Sprintf(" and %d more", len(items)-max)
}

func earlierHeadline(ea *earlierAttempts) string {
	switch ea.Status {
	case earlierNotChecked:
		return "not checked (" + ea.Reason + ")"
	case earlierFirstAttempt:
		return "none, this is attempt 1"
	case earlierNoFail:
		return "no behavioral FAIL recorded"
	case earlierFailed:
		return "an earlier attempt recorded a behavioral FAIL for the same commit and input hashes; reported only, the result doesn't change (REP-10, OD-15)"
	}
	if len(ea.Attempts) == 0 {
		return "unknown (" + ea.Reason + ")"
	}
	return "unknown: not every earlier attempt's report was found, so a behavioral FAIL can't be ruled out"
}

func earlierShown(ea *earlierAttempts) bool {
	return ea != nil && (ea.Status != earlierNotChecked || ea.current > 1)
}

func attemptText(a earlierAttempt) string {
	switch a.Status {
	case earlierFailed:
		return fmt.Sprintf("FAIL     %s  (result %s; %s)", shortList(a.Failed, 10), a.Result, strings.Join(a.Files, ", "))
	case earlierNoFail:
		return fmt.Sprintf("no FAIL  (result %s; %s)", a.Result, strings.Join(a.Files, ", "))
	}
	return "unknown  " + a.Reason
}

func writeEarlier(w io.Writer, ea *earlierAttempts) {
	if !earlierShown(ea) {
		return
	}
	fmt.Fprintf(w, "Earlier attempts: %s\n", safe(earlierHeadline(ea)))
	if ea.Reason != "" && ea.Status != earlierNotChecked && len(ea.Attempts) > 0 {
		fmt.Fprintf(w, "  folder      %s\n", safe(ea.Reason))
	}
	for _, a := range ea.Attempts {
		fmt.Fprintf(w, "  attempt %-3d %s\n", a.Attempt, safe(attemptText(a)))
	}
	for _, ig := range ea.Ignored {
		fmt.Fprintf(w, "  ignored     %s: %s\n", safe(ig.File), safe(ig.Reason))
	}
}

func writeEarlierSummary(w io.Writer, ea *earlierAttempts) {
	if !earlierShown(ea) {
		return
	}
	fmt.Fprintln(w, "### Earlier attempts")
	fmt.Fprintln(w)
	if ea.Status == earlierFailed {
		fmt.Fprintln(w, "**An earlier attempt of this workflow run recorded a behavioral FAIL for the same tested commit and input hashes.** This doesn't change the result (REP-10, OD-15).")
	} else {
		fmt.Fprintf(w, "%s.\n", md(strings.ToUpper(earlierHeadline(ea)[:1])+earlierHeadline(ea)[1:]))
	}
	fmt.Fprintln(w)
	if ea.Reason != "" && ea.Status != earlierNotChecked && len(ea.Attempts) > 0 {
		fmt.Fprintf(w, "- folder: %s\n", md(ea.Reason))
	}
	for _, a := range ea.Attempts {
		switch a.Status {
		case earlierFailed:
			ids := make([]string, 0, len(a.Failed))
			for _, id := range a.Failed[:min(len(a.Failed), 20)] {
				ids = append(ids, code(id))
			}
			more := ""
			if len(a.Failed) > 20 {
				more = fmt.Sprintf(" and %d more", len(a.Failed)-20)
			}
			fmt.Fprintf(w, "- attempt %d: **FAIL** %s%s (result %s)\n", a.Attempt, strings.Join(ids, ", "), more, md(a.Result))
		case earlierNoFail:
			fmt.Fprintf(w, "- attempt %d: no behavioral FAIL (result %s)\n", a.Attempt, md(a.Result))
		default:
			fmt.Fprintf(w, "- attempt %d: unknown, %s\n", a.Attempt, md(a.Reason))
		}
	}
	for _, ig := range ea.Ignored {
		fmt.Fprintf(w, "- ignored %s: %s\n", code(ig.File), md(ig.Reason))
	}
	fmt.Fprintln(w)
}
