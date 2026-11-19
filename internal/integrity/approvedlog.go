package integrity

import (
	"path"
	"strings"

	"proctor/internal/applogs"
	"proctor/internal/model"
	"proctor/internal/transcript"
)

func (r *Report) validateApprovedLogs(c *model.Config) {
	a := c.AppLogs.ApprovedSettings()
	if a == nil {
		return
	}
	p := r.ConfigPath
	if len(a.Fields) == 0 {
		r.add(Invalid, "ALG-02", p, "app_logs.approved.fields is required: list the fields of each log line to keep, in the order to show them; every other field is left out of the approved log")
	}
	if len(a.Fields) > model.MaxApprovedLogFields {
		r.add(Invalid, "ALG-02", p, "app_logs.approved.fields lists %d fields, more than %d", len(a.Fields), model.MaxApprovedLogFields)
	}
	seen := map[string]bool{}
	for i, f := range a.Fields {
		switch {
		case strings.TrimSpace(f) == "":
			r.add(Invalid, "ALG-02", p, "app_logs.approved.fields[%d] is empty", i)
		case strings.TrimSpace(f) != f:
			r.add(Invalid, "ALG-02", p, "app_logs.approved.fields[%d] %q has leading or trailing spaces", i, f)
		case strings.ContainsAny(f, "= \"\\") || !printable(f):
			r.add(Invalid, "ALG-02", p, "app_logs.approved.fields[%d] %q can't be shown as key=value: no =, space, quote, backslash or control character", i, f)
		case f == c.AppLogs.LevelKey() || f == c.AppLogs.MessageKey():
			r.add(Invalid, "ALG-02", p, "app_logs.approved.fields[%d] %q is the level or message field, which every approved log line already starts with", i, f)
		case seen[f]:
			r.add(Invalid, "ALG-02", p, "app_logs.approved.fields lists %q twice", f)
		}
		seen[f] = true
	}
	switch a.Order {
	case "", model.ApprovedLogOrderWritten, model.ApprovedLogOrderSorted:
	default:
		r.add(Invalid, "ALG-02", p, "app_logs.approved.order %q isn't written or sorted", a.Order)
	}
	if a.MaxBytes < 0 || a.MaxBytes > model.MaxApprovedLogBytes {
		r.add(Invalid, "ALG-02", p, "app_logs.approved.max_bytes %d must be between 1 and %d; leave it out for the default %d", a.MaxBytes, model.MaxApprovedLogBytes, model.DefaultApprovedLogBytes)
	}
	_, probs := transcript.CompileLogRules(a.Scrub, a.Fields, c.AppLogs.MessageKey())
	for _, m := range probs {
		r.add(Invalid, "ALG-02", p, "%s", m)
	}
}

func printable(s string) bool {
	for _, c := range s {
		if c < ' ' || c == 0x7f {
			return false
		}
	}
	return true
}

func (r *Report) wantLogs(sf SpecFile, specID string, c *model.Config, root string, want map[string]owned, folded map[string]string, order *[]string) {
	for i := range sf.Spec.Scenarios {
		sc := &sf.Spec.Scenarios[i]
		reports := sc.Case == nil || sc.Case.FirstRow
		on, err := model.FlagOn(&sc.ApprovedLog, "approved_log")
		switch {
		case err != nil:
			if reports {
				r.add(Invalid, "ALG-01", sf.Path, "%s: %v", tableOrScenario(sc), err)
			}
			continue
		case !on || !reports:
			continue
		case c.AppLogs == nil || c.AppLogs.Approved == nil:
			r.add(Invalid, "ALG-01", sf.Path, "%s: approved_log: true needs app_logs.approved in %s (at least its fields), and the binding's app_logs.file when it runs", tableOrScenario(sc), r.ConfigPath)
			continue
		case sc.Case != nil && sc.Case.Table == "":
			r.add(Invalid, "ALG-01", sf.Path, "%s: a table with approved_log: true needs an id, which names its approved log", tableOrScenario(sc))
			continue
		}
		owner := sc.ApprovedLogOwner()
		if !idPattern.MatchString(specID) || !idPattern.MatchString(owner) {
			continue
		}
		ap := model.ApprovedLogPath(r.ProtectedRoot, specID, owner)
		if err := checkRelPath(ap); err != nil || path.Dir(path.Dir(ap)) != root || path.Base(path.Dir(ap)) != specID {
			r.add(Invalid, "ALG-03", sf.Path, "%s: the approved log's path %q isn't inside %s/", tableOrScenario(sc), ap, root)
			continue
		}
		if other, clash := folded[strings.ToLower(ap)]; clash {
			if other == ap {
				r.add(Invalid, "ALG-03", sf.Path, "%s: its approved log %s is also the approved log of another scenario or table of this specification; give one of them another id", tableOrScenario(sc), ap)
			} else {
				r.add(Invalid, "ALG-03", sf.Path, "%s: its approved log %s differs from %s only in letter case, which a case-insensitive file system stores as one file; give one of them another id", tableOrScenario(sc), ap, other)
			}
			continue
		}
		folded[strings.ToLower(ap)] = ap
		want[ap] = owned{spec: specID, scenario: owner, sc: sc, log: true}
		*order = append(*order, ap)
	}
}

func (r *Report) readApprovedLog(c *model.Config, o owned, a *model.Approval, data []byte, found bool, err error) bool {
	limit := c.AppLogs.ApprovedSettings().Limit()
	switch {
	case err != nil:
		r.add(Invalid, "ALG-03", a.Path, "the approved log of %s can't be read: %v", o.scenario, err)
		return false
	case !found:
		return true
	case len(data) > limit:
		r.add(Invalid, "ALG-03", a.Path, "the approved log is %d bytes, more than app_logs.approved.max_bytes (%d), so no run could match it", len(data), limit)
		return false
	}
	if err := applogs.ValidApprovedLog(data); err != nil {
		r.add(Invalid, "ALG-03", a.Path, "isn't an approved log: %v; approve the received file with proctor approve instead of writing it by hand", err)
		return false
	}
	a.Found, a.Data, a.SHA256 = true, data, sha(data)
	r.Hashes[a.Path] = a.SHA256
	return true
}

func (r *Report) attachLogApproval(o owned, a *model.Approval) {
	for _, sf := range r.Specs {
		if r.SpecIDs[sf.Path] != o.spec {
			continue
		}
		for i := range sf.Spec.Scenarios {
			sc := &sf.Spec.Scenarios[i]
			switch {
			case o.sc.Case == nil && sc.Case == nil && sc.ID == o.scenario:
				sc.LogApproval = a
			case o.sc.Case != nil && sc.Case != nil && sc.Case.Key == o.sc.Case.Key:
				sc.LogApproval = a
			}
		}
	}
}
