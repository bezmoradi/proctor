package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const minScrubbed = 4

type scrubber struct {
	values []string
	known  []knownValue
}

type knownValue struct {
	value, what string
}

func (s *scrubber) know(v, what string) {
	s.add(v)
	if len(v) < minScrubbed {
		return
	}
	for _, k := range s.known {
		if k.value == v {
			return
		}
	}
	s.known = append(s.known, knownValue{value: v, what: what})
	sort.SliceStable(s.known, func(i, j int) bool { return len(s.known[i].value) > len(s.known[j].value) })
}

func (s *scrubber) leaked(t string) (string, bool) {
	for _, k := range s.known {
		if strings.Contains(t, k.value) {
			return k.what, true
		}
	}
	return "", false
}

func (s *scrubber) add(v string) {
	if len(v) < minScrubbed || contains(s.values, v) {
		return
	}
	s.values = append(s.values, v)
	sort.Slice(s.values, func(i, j int) bool { return len(s.values[i]) > len(s.values[j]) })
}

func (s *scrubber) clean(t string) string {
	for _, v := range s.values {
		t = strings.ReplaceAll(t, v, redacted)
	}
	return t
}

func (s *scrubber) walk(v any) any {
	switch t := v.(type) {
	case string:
		return s.clean(t)
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = s.walk(t[i])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[s.clean(k)] = s.walk(x)
		}
		return out
	case []string:
		out := make([]string, len(t))
		for i := range t {
			out[i] = s.clean(t[i])
		}
		return out
	case json.Number:
		if c := s.clean(t.String()); c != t.String() {
			return c
		}
	}
	return v
}

func cut(t string, limit int) string {
	if len(t) <= limit {
		return t
	}
	n := limit
	for n > 0 && !utf8.RuneStart(t[n]) {
		n--
	}
	return t[:n] + fmt.Sprintf(" [cut at %d of %d bytes]", n, len(t))
}

func (r *Runner) scrubOutcome(out *Outcome) {
	s := r.scrub
	out.Readiness = s.clean(out.Readiness)
	out.Target = s.clean(out.Target)
	for i := range out.Violations {
		out.Violations[i] = s.clean(out.Violations[i])
	}
	if ic := out.Identity; ic != nil {
		ic.Message = s.clean(ic.Message)
		for i := range ic.Identities {
			e := &ic.Identities[i]
			e.Message, e.Expected, e.Actual = s.clean(e.Message), s.clean(e.Expected), s.clean(e.Actual)
		}
	}
	for i := range out.Drivers {
		d := &out.Drivers[i]
		d.Stderr = s.clean(d.Stderr)
		for j := range d.Proofs {
			d.Proofs[j].Detail = s.clean(d.Proofs[j].Detail)
		}
	}
	for i := range out.Tables {
		r.scrubAppLogs(out.Tables[i].AppLogs)
		s.scrubApproved(out.Tables[i].ApprovedLog)
	}
	for i := range out.Scenarios {
		out.Scenarios[i].Message = s.clean(out.Scenarios[i].Message)
		r.scrubAppLogs(out.Scenarios[i].AppLogs)
		s.scrubApproved(out.Scenarios[i].Transcript)
		s.scrubApproved(out.Scenarios[i].ApprovedLog)
		for j := range out.Scenarios[i].Handles {
			hr := &out.Scenarios[i].Handles[j]
			hr.Handle, hr.Message = s.clean(hr.Handle), s.clean(hr.Message)
		}
		for j := range out.Scenarios[i].Steps {
			st := &out.Scenarios[i].Steps[j]
			st.Message = s.clean(st.Message)
			if st.Action != nil {
				st.Action.Summary = s.clean(st.Action.Summary)
			}
			if st.Evidence != nil {
				st.Evidence.RequestExcerpt = cut(s.clean(st.Evidence.RequestExcerpt), r.excerpt)
				st.Evidence.ResponseExcerpt = cut(s.clean(st.Evidence.ResponseExcerpt), r.excerpt)
			}
			for k := range st.Assertions {
				a := &st.Assertions[k]
				a.Expected, a.Actual, a.Message, a.Target = s.walk(a.Expected), s.walk(a.Actual), s.clean(a.Message), s.clean(a.Target)
				if a.Response != "" {
					a.Response = cut(s.clean(a.Response), r.excerpt)
				}
			}
		}
	}
}

func (s *scrubber) scrubApproved(tr *TranscriptResult) {
	if tr == nil {
		return
	}
	tr.Message = s.clean(tr.Message)
	for j := range tr.Diff {
		tr.Diff[j] = s.clean(tr.Diff[j])
	}
}

func (r *Runner) scrubAppLogs(al *AppLogResult) {
	if al == nil {
		return
	}
	s := r.scrub
	al.Message, al.Note = s.clean(al.Message), s.clean(al.Note)
	al.FirstUnparsed = cut(s.clean(al.FirstUnparsed), r.excerpt)
	al.UnmappedLevel, al.FirstUnmapped = cut(s.clean(al.UnmappedLevel), r.excerpt), cut(s.clean(al.FirstUnmapped), r.excerpt)
	for i := range al.Unexpected {
		l := &al.Unexpected[i]
		l.Level, l.Time = cut(s.clean(l.Level), r.excerpt), cut(s.clean(l.Time), r.excerpt)
		l.Message, l.Line = cut(s.clean(l.Message), r.excerpt), cut(s.clean(l.Line), r.excerpt)
	}
	for i := range al.Expected {
		e := &al.Expected[i]
		e.Message = s.clean(e.Message)
	}
}

func normalizeJSON(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
