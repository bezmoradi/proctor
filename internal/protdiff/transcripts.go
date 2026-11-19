package protdiff

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	"proctor/internal/gitrepo"
	"proctor/internal/model"
	"proctor/internal/transcript"
)

func approvedRoot(configPath string) string {
	return path.Dir(configPath) + "/" + model.ApprovedDir + "/"
}

func compareScrub(b, h *model.Config) []Weakening {
	var out []Weakening
	was := map[string]model.ScrubRule{}
	for _, r := range b.Transcripts.Rules() {
		was[r.Name] = r
	}
	for _, r := range h.Transcripts.Rules() {
		old, ok := was[r.Name]
		switch {
		case !ok:
			out = append(out, Weakening{Kind: "scrub_widened", Detail: fmt.Sprintf("transcripts.scrub adds rule %s (%s); whether it hides more of the transcripts can't be decided, so the values it replaces are no longer compared", r.Name, scrubText(r))})
		case old.Pattern != r.Pattern || old.Path != r.Path || old.IsNumbered() != r.IsNumbered():
			out = append(out, Weakening{Kind: "scrub_widened", Detail: fmt.Sprintf("transcripts.scrub rule %s changed from %s to %s; whether it hides more of the transcripts can't be decided", r.Name, scrubText(old), scrubText(r))})
		}
	}
	return out
}

func scrubText(r model.ScrubRule) string {
	var parts []string
	if r.Pattern != "" {
		parts = append(parts, "pattern "+r.Pattern)
	}
	if r.Path != "" {
		parts = append(parts, "path "+r.Path)
	}
	if !r.IsNumbered() {
		parts = append(parts, "numbered: false")
	}
	return strings.Join(parts, ", ")
}

func transcriptDropped(c ScenarioChange, b, h *model.Scenario, file string, tables map[string]bool) []Weakening {
	if b == nil || h == nil || !b.TranscriptWanted() || h.TranscriptWanted() {
		return nil
	}
	if b.Case != nil && h.Case != nil {
		key := file + "\x00" + h.Case.Key
		if tables[key] {
			return nil
		}
		tables[key] = true
		return []Weakening{{Scenario: "table " + h.Case.Label(), Kind: "transcript_dropped",
			Detail: "transcript: true was removed from the table, so its rows' transcripts are no longer compared with the approved ones"}}
	}
	return []Weakening{{Scenario: c.ID, Kind: "transcript_dropped",
		Detail: "transcript: true was removed, so the scenario's transcript is no longer compared with the approved one"}}
}

func shortSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

func approvedChanges(repo *gitrepo.Repo, bt, ht map[string]gitrepo.Entry, configPath string) ([]ExpectationChange, error) {
	prefix := approvedRoot(configPath)
	var out []ExpectationChange
	read := func(e gitrepo.Entry, ok bool) ([]byte, error) {
		if !ok || !e.Regular() {
			return nil, nil
		}
		return repo.Blob(e.OID)
	}
	for _, p := range union(bt, ht) {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		be, inBase := bt[p]
		he, inHead := ht[p]
		if inBase && inHead && be.OID == he.OID && be.Mode == he.Mode {
			continue
		}
		was, err := read(be, inBase)
		if err != nil {
			return nil, err
		}
		now, err := read(he, inHead)
		if err != nil {
			return nil, err
		}
		id, what, reason := strings.TrimSuffix(path.Base(p), model.TranscriptSuffix), "approved transcript ", "removed; the scenario fails transcript_not_approved until one is approved, or its transcript is dropped"
		if logID, isLog := strings.CutSuffix(path.Base(p), model.ApprovedLogSuffix); isLog {
			id, what, reason = logID, "approved log ", "removed; the scenario or table fails log_not_approved until one is approved, or its approved_log is dropped"
		}
		d := transcript.Compare(was, now, 0, true)
		c := ExpectationChange{Scenario: id, What: what + p, approvedFile: true}
		switch {
		case !inBase:
			c.From, c.To = "none", fmt.Sprintf("added, %d lines", d.Added)
		case !inHead:
			c.From, c.To = fmt.Sprintf("%d lines", d.Removed), reason
		default:
			c.From, c.To = "sha256 "+shortSHA(was), d.Changed()+", sha256 "+shortSHA(now)
		}
		out = append(out, c)
	}
	return out, nil
}
