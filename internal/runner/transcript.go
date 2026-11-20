package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"proctor/internal/expr"
	"proctor/internal/model"
	"proctor/internal/transcript"
)

const (
	ReasonTranscriptNotApproved = "transcript_not_approved"
	ReasonTranscriptChanged     = "transcript_changed"
	ReasonTranscriptTooLarge    = "transcript_too_large"
	ReasonTranscriptNotWritten  = "transcript_not_written"
	ReasonTranscriptUnusable    = "transcript_unusable"
)

const (
	TranscriptApproved    = "approved"
	TranscriptNotApproved = "not_approved"
	TranscriptChanged     = "changed"
	TranscriptTooLarge    = "too_large"
	TranscriptNotWritten  = "not_written"
	TranscriptUnusable    = "unusable"
)

type TranscriptResult struct {
	Result         string   `json:"result"`
	ApprovedPath   string   `json:"approved_path"`
	ApprovedSHA256 string   `json:"approved_sha256,omitempty"`
	ReceivedSHA256 string   `json:"received_sha256,omitempty"`
	Received       string   `json:"received,omitempty"`
	Bytes          int      `json:"bytes"`
	LinesChanged   string   `json:"lines_changed,omitempty"`
	Diff           []string `json:"diff,omitempty"`
	DiffMore       int      `json:"diff_more_lines,omitempty"`
	Message        string   `json:"message,omitempty"`

	SpecID string `json:"-"`
	Own    Result `json:"-"`
}

type ReceivedWriter func(specID, name string, data []byte) (string, error)

type approvedKind struct {
	noun, suffix, limitKey                               string
	notApproved, changed, tooLarge, notWritten, unusable string
}

var transcriptKind = approvedKind{noun: "transcript", suffix: model.TranscriptSuffix, limitKey: "transcripts.max_bytes",
	notApproved: ReasonTranscriptNotApproved, changed: ReasonTranscriptChanged, tooLarge: ReasonTranscriptTooLarge,
	notWritten: ReasonTranscriptNotWritten, unusable: ReasonTranscriptUnusable}

func (r *Runner) SetReceived(w ReceivedWriter) { r.opt.Received = w }

type stepRecord struct {
	with    any
	hasWith bool
	fact    any
	hasFact bool
	poll    *transcript.Poll
}

type scenarioRecord struct {
	setup []StepResult
	steps []StepResult
	model []model.Step
	vars  expr.Vars
	run   string
}

var credentialHeaders = map[string]bool{"authorization": true, "proxy-authorization": true, "cookie": true, "set-cookie": true}

func (r *Runner) wantsTranscript(sc model.Scenario) bool {
	return sc.TranscriptWanted()
}

func recording(sr StepResult, on bool) StepResult {
	if on {
		sr.rec = &stepRecord{}
	}
	return sr
}

func (r *Runner) keepFact(sr *StepResult, fact any, red []string, t timing) {
	if sr.rec == nil {
		return
	}
	r.scrubRedacted("$", fact, red)
	sr.rec.fact = r.redactTranscript(r.redactValue("$", fact, red))
	sr.rec.hasFact = true
	if t.mode != "" {
		sr.rec.poll = &transcript.Poll{Mode: t.mode, Window: t.window}
	}
}

func (r *Runner) keepWith(sr *StepResult, args any) {
	if sr.rec == nil {
		return
	}
	sr.rec.with = r.redactTranscript(r.redactValue("$", args, nil))
	sr.rec.hasWith = true
}

func (r *Runner) redactTranscript(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			if credentialHeaders[strings.ToLower(k)] || r.headers[strings.ToLower(k)] || dottedHeader(k, r.headers) {
				out[k] = redacted
				continue
			}
			out[k] = r.redactTranscript(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = r.redactTranscript(e)
		}
		return out
	}
	return v
}

func (r *Runner) recordTranscript(res *ScenarioResult, sc model.Scenario, setup []StepResult, vars expr.Vars) {
	if !r.wantsTranscript(sc) || res.Result == NotRun {
		return
	}
	var own []StepResult
	for _, st := range res.Steps {
		if st.Phase == PhaseVerification || st.Phase == PhaseCleanup {
			own = append(own, st)
		}
	}
	snapshot := expr.Vars{}
	for k, v := range vars {
		snapshot[k] = v
	}
	run, _ := vars[expr.RunID].(string)
	res.rec = &scenarioRecord{setup: setup, steps: own, model: allSteps(sc), vars: snapshot, run: run}
}

func setupResults(steps []StepResult) []StepResult {
	var out []StepResult
	for _, st := range steps {
		if st.Phase == PhaseSetup {
			out = append(out, st)
		}
	}
	return out
}

func (r *Runner) transcriptDoc(sc model.Scenario, rec *scenarioRecord, specID string) transcript.Doc {
	doc := transcript.Doc{Spec: specID, Scenario: sc.ID}
	if sc.Case != nil {
		doc.Table = sc.Case.Label()
	}
	all := append(append([]StepResult{}, rec.setup...), rec.steps...)
	for i, sr := range all {
		var st model.Step
		if i < len(rec.model) {
			st = rec.model[i]
		}
		ts := transcript.Step{ID: sr.ID, Phase: sr.Phase, Do: st.Do, Identity: st.Identity, Variant: st.Variant, Saved: sr.Saved}
		if sr.Result != Pass {
			ts.Result, ts.Reason, ts.Cause = string(sr.Result), sr.Reason, sr.Cause
		}
		if sr.rec != nil {
			ts.With, ts.HasWith = r.scrub.walk(sr.rec.with), sr.rec.hasWith
			ts.Fact, ts.HasFact = r.scrub.walk(sr.rec.fact), sr.rec.hasFact
			ts.Poll = sr.rec.poll
		}
		doc.Steps = append(doc.Steps, ts)
	}
	return doc
}

func (r *Runner) placeholders(rec *scenarioRecord) []transcript.Sub {
	var subs []transcript.Sub
	add := func(v any, placeholder string) {
		switch x := v.(type) {
		case string:
			c := r.scrub.clean(x)
			if c != x || c == "" {
				return
			}
			subs = append(subs, transcript.Sub{Text: x, Placeholder: placeholder})
		case json.Number:
			subs = append(subs, transcript.Sub{Text: x.String(), Number: true, Placeholder: placeholder})
			subs = append(subs, transcript.Sub{Text: x.String(), Placeholder: placeholder})
		}
	}
	if rec.run != "" {
		add(rec.run, "${"+expr.RunID+"}")
	}
	names := make([]string, 0, len(rec.vars))
	for k := range rec.vars {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if k == expr.RunID || strings.HasPrefix(k, "secret.") {
			continue
		}
		if _, ident := r.values[k]; ident {
			continue
		}
		add(rec.vars[k], "${"+k+"}")
	}
	for _, k := range sortedKeys(r.values) {
		add(r.values[k], "${"+k+"}")
	}
	return subs
}

func hexSHA(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (r *Runner) applyTranscripts(out *Outcome) {
	rules, probs := transcript.Compile(r.cfg.Transcripts.Rules())
	limit := r.cfg.Transcripts.Limit()
	for i := range out.Scenarios {
		res := &out.Scenarios[i]
		if res.rec == nil {
			continue
		}
		sc := r.scenarioOf(res.Spec, res.ID)
		if sc == nil {
			continue
		}
		rec := res.rec
		res.rec = nil
		tr := &TranscriptResult{Own: res.Result}
		res.Transcript = tr
		r.compareTranscript(out, i, *sc, rec, tr, rules, probs, limit)
	}
}

func (r *Runner) scenarioOf(p, id string) *model.Scenario {
	for _, f := range r.specs {
		if f.Path != p || f.Spec == nil {
			continue
		}
		for i := range f.Spec.Scenarios {
			if f.Spec.Scenarios[i].ID == id {
				return &f.Spec.Scenarios[i]
			}
		}
	}
	return nil
}

func (r *Runner) compareTranscript(out *Outcome, i int, sc model.Scenario, rec *scenarioRecord, tr *TranscriptResult, rules []transcript.Rule, probs []string, limit int) {
	res := &out.Scenarios[i]
	fail := func(result Result, reason, kind, msg string) {
		tr.Result, tr.Message = kind, msg
		if res.Result == Pass {
			out.setResult(i, result, reason)
		}
	}
	a := sc.Approval
	if a == nil {
		fail(Error, ReasonTranscriptUnusable, TranscriptUnusable, "the approved transcript wasn't loaded with the specifications, so it can't be compared")
		return
	}
	tr.SpecID, tr.ApprovedPath = a.SpecID, a.Path
	if a.Found {
		tr.ApprovedSHA256 = a.SHA256
	}
	if len(probs) > 0 {
		fail(Error, ReasonTranscriptUnusable, TranscriptUnusable, "transcripts.scrub is invalid: "+strings.Join(probs, "; "))
		return
	}
	data, err := transcript.Build(r.transcriptDoc(sc, rec, a.SpecID), r.placeholders(rec), rules)
	if err != nil {
		fail(Error, ReasonTranscriptUnusable, TranscriptUnusable, "the transcript can't be written: "+err.Error())
		return
	}
	r.judgeApproved(transcriptKind, a, sc.ID, data, limit, tr, fail)
}

func (r *Runner) judgeApproved(k approvedKind, a *model.Approval, owner string, data []byte, limit int, tr *TranscriptResult, fail func(result Result, reason, kind, msg string)) {
	data = []byte(r.scrub.clean(string(data)))
	tr.Bytes = len(data)
	if len(data) > limit {
		fail(Error, k.tooLarge, TranscriptTooLarge, fmt.Sprintf("the %s is %d bytes, more than %s (%d); it wasn't written or compared", k.noun, len(data), k.limitKey, limit))
		return
	}
	tr.ReceivedSHA256 = hexSHA(data)
	if r.opt.Received != nil {
		rel, err := r.opt.Received(a.SpecID, owner+k.suffix, data)
		if err != nil {
			fail(Error, k.notWritten, TranscriptNotWritten, "the received "+k.noun+" can't be written: "+err.Error())
			return
		}
		tr.Received = rel
	}
	switch {
	case !a.Found:
		fail(Fail, k.notApproved, TranscriptNotApproved, "no approved "+k.noun+" yet; read the received one, then run proctor approve")
	case string(a.Data) == string(data):
		tr.Result = TranscriptApproved
	default:
		d := transcript.Compare(a.Data, data, transcript.ReportDiffLines, true)
		tr.Diff, tr.DiffMore, tr.LinesChanged = d.Lines, d.More, d.Changed()
		fail(Fail, k.changed, TranscriptChanged, "differs from the approved "+k.noun+": "+d.Changed())
	}
}

func (o *Outcome) setResult(i int, result Result, reason string) {
	sc := &o.Scenarios[i]
	sc.Result, sc.Reason, sc.Cause = result, reason, ""
	if sc.tableKey == "" {
		return
	}
	for t := range o.Tables {
		tb := &o.Tables[t]
		if tb.Spec != sc.Spec || tb.key != sc.tableKey {
			continue
		}
		for j := range tb.Rows {
			if tb.Rows[j].Case == sc.ID {
				tb.Rows[j].Result = result
			}
		}
		if tableRank[result] > tableRank[tb.Result] {
			tb.Result = result
		}
	}
}
