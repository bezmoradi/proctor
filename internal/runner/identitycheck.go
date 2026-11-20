package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/proctor/protocol"
	"proctor/internal/expr"
	"proctor/internal/model"
)

const (
	ReasonIdentityCheckFailed = "identity_check_failed"

	CauseUserIDMismatch   = "user_id_mismatch"
	CauseUnexpectedStatus = "unexpected_status"
	CauseUnreadableBody   = "unreadable_body"
	CauseNoUserID         = "no_user_id"
	CauseNoCredential     = "no_credential"
	CauseNotUsed          = "not_used"

	identityCheckOp = "request"
)

type IdentityCheck struct {
	Driver     string                `json:"driver"`
	Path       string                `json:"path"`
	UserID     string                `json:"user_id"`
	Result     Result                `json:"result"`
	Reason     string                `json:"reason,omitempty"`
	Message    string                `json:"message,omitempty"`
	Identities []IdentityCheckResult `json:"identities"`
}

type IdentityCheckResult struct {
	Identity   string  `json:"identity"`
	Result     Result  `json:"result"`
	Cause      string  `json:"cause,omitempty"`
	Message    string  `json:"message,omitempty"`
	Status     int     `json:"status,omitempty"`
	Expected   string  `json:"expected,omitempty"`
	Actual     string  `json:"actual,omitempty"`
	IDsHidden  bool    `json:"ids_hidden,omitempty"`
	DurationMS float64 `json:"duration_ms,omitempty"`
}

func (r *Runner) identityCheckNotRun(reason string) *IdentityCheck {
	ic := r.cfg.IdentityCheck
	name, _ := r.cfg.IdentityCheckDriver()
	return &IdentityCheck{Driver: name, Path: ic.Path, UserID: ic.UserID, Result: NotRun, Reason: reason, Identities: []IdentityCheckResult{}}
}

func (r *Runner) checkIdentities(ctx context.Context) *IdentityCheck {
	ic := r.cfg.IdentityCheck
	out := r.identityCheckNotRun("")
	out.Result = Pass
	h := r.sess.hosts[out.Driver]
	var op protocol.Operation
	ok := h != nil
	if ok {
		op, ok = h.ops[identityCheckOp]
	}
	path, err := expr.ParsePath(ic.UserID)
	switch {
	case !ok:
		out.Result, out.Message = Fail, fmt.Sprintf("driver %q has no %s operation to call %s with", out.Driver, identityCheckOp, ic.Path)
		return out
	case err != nil:
		out.Result, out.Message = Fail, fmt.Sprintf("identity_check.user_id: %v", err)
		return out
	}
	idents, _ := r.usedIdentities()
	used := map[string]bool{}
	for _, n := range idents {
		used[n] = true
	}
	hidden := r.idSensitive(path)
	for _, name := range sortedKeys(r.cfg.Identities) {
		res := IdentityCheckResult{Identity: name}
		switch {
		case r.cfg.Identities[name].Type == "none":
			res.Result, res.Cause, res.Message = NotRun, CauseNoCredential, "type none sends no credential, so there is no user to check"
		case !used[name]:
			res.Result, res.Cause, res.Message = NotRun, CauseNotUsed, "no listed scenario uses this identity, so its credential isn't loaded"
		case ctx.Err() != nil:
			res.Result, res.Cause = NotRun, ReasonAborted
		default:
			res = r.checkIdentity(ctx, h, op, name, path, hidden)
		}
		out.Identities = append(out.Identities, res)
	}
	failed, errored, aborted := 0, 0, false
	for _, res := range out.Identities {
		switch {
		case res.Result == Fail:
			failed++
		case res.Result == Error:
			errored++
		case res.Cause == ReasonAborted:
			aborted = true
		}
	}
	switch {
	case failed > 0:
		out.Result, out.Message = Fail, fmt.Sprintf("%d of the identities checked don't answer as the binding says (AUTH-04)", failed)
	case errored > 0:
		out.Result, out.Message = Error, fmt.Sprintf("%d of the identities couldn't be checked (AUTH-04)", errored)
	case aborted || ctx.Err() != nil:
		out.Result, out.Reason = NotRun, ReasonAborted
	}
	return out
}

func (r *Runner) idSensitive(p expr.Path) bool {
	canon := p.String()
	inFact := "$.body" + strings.TrimPrefix(canon, "$")
	return sensitivePath(canon) || r.listedPath(canon, nil) || r.listedPath(inFact, nil) || sensitivePath(inFact)
}

func (r *Runner) checkIdentity(ctx context.Context, h *host, op protocol.Operation, name string, path expr.Path, hidden bool) IdentityCheckResult {
	ic := r.cfg.IdentityCheck
	res := IdentityCheckResult{Identity: name, Result: Fail}
	fail := func(cause, format string, args ...any) IdentityCheckResult {
		res.Result, res.Cause, res.Message = Fail, cause, fmt.Sprintf(format, args...)
		return res
	}
	cred, ok := r.creds[credKey(name, "")]
	if !ok {
		res.Result, res.Cause, res.Message = Error, CauseInvalidRequest, "the identity has no loaded credential"
		return res
	}
	args := map[string]any{"method": "GET", "path": ic.Path}
	start := time.Now()
	s, perr := r.sample(ctx, h, op, args, nil, &cred, "", nil, r.opt.CallTimeout)
	res.DurationMS = millis(time.Since(start))
	if perr != nil {
		res.Cause, res.Message = perr.Data.Cause, perr.Message
		switch perr.Data.Cause {
		case CauseInvalidRequest, CauseNotConfigured:
			res.Result = Fail
		default:
			res.Result = Error
		}
		return res
	}
	fact, _ := s.fact.(map[string]any)
	status, _ := statusOf(fact["status"])
	res.Status = status
	if status < 200 || status > 299 {
		return fail(CauseUnexpectedStatus, "GET %s answered %d, not 2xx; check the identity's token and identity_check.path", ic.Path, status)
	}
	body, has := fact["body"]
	if !has {
		if why, ok := fact["body_invalid"].(string); ok && why != "" {
			return fail(CauseUnreadableBody, "GET %s answered %d, but its body isn't valid JSON: %s", ic.Path, status, why)
		}
		if _, ok := fact["body_text"]; ok {
			return fail(CauseUnreadableBody, "GET %s answered %d, but its body isn't JSON", ic.Path, status)
		}
		return fail(CauseUnreadableBody, "GET %s answered %d with no body", ic.Path, status)
	}
	got, ok := path.Select(body)
	if !ok {
		return fail(CauseNoUserID, "GET %s answered %d, but its body has no value at %s", ic.Path, status, path.String())
	}
	gotText, ok := idText(got)
	if !ok {
		return fail(CauseNoUserID, "GET %s answered %d, but the value at %s is %s, not a string or a number", ic.Path, status, path.String(), jsonKind(got))
	}
	want := r.b.Identities[name].Values[model.IdentityCheckValue]
	if gotText == want {
		res.Result = Pass
		return res
	}
	if hidden {
		res.IDsHidden = true
		return fail(CauseUserIDMismatch, "GET %s answers as another user: the user ID at %s isn't the binding's values.%s; both IDs are hidden, because %s is a sensitive path (REP-06)", ic.Path, path.String(), model.IdentityCheckValue, path.String())
	}
	res.Expected, res.Actual = want, gotText
	return fail(CauseUserIDMismatch, "GET %s answers as another user: the user ID at %s is %q, but the binding's values.%s is %q", ic.Path, path.String(), gotText, model.IdentityCheckValue, want)
}

func statusOf(v any) (int, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := strconv.Atoi(n.String())
		return i, err == nil
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

func idText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	}
	return "", false
}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case map[string]any:
		return "an object"
	case []any:
		return "a list"
	}
	return "not text"
}
