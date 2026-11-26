package runner

import (
	"context"
	"fmt"
	"strings"

	"proctor/internal/model"
)

const DirtyAtStart = "the start of the run"

func (r *Runner) checkInvariants(ctx context.Context, runID string) (failed []StepResult, errored []string) {
	for _, inv := range r.cfg.Invariants {
		if ctx.Err() != nil {
			return failed, errored
		}
		sr := r.step(ctx, inv, StepResult{ID: inv.ID, Phase: model.InvariantsPhase}, r.baseVars(runID+"-invariants"), newHandleSet())
		if sr.Result != Pass {
			failed = append(failed, sr)
		}
		if sr.Result == Error {
			errored = append(errored, inv.ID)
		}
	}
	return failed, errored
}

func (r *Runner) afterScenario(ctx context.Context, res *ScenarioResult, runID, dirty string) string {
	if len(r.cfg.Invariants) == 0 {
		return dirty
	}
	failed, errored := r.checkInvariants(ctx, runID)
	if ctx.Err() != nil {
		return dirty
	}
	if len(errored) > 0 {
		res.InvariantErrors = failed
		if dirty != "" {
			res.DirtySince = dirty
		}
		if res.Result == Pass {
			res.Result, res.Reason, res.Cause = Error, ReasonInvariantError, ""
			res.Message = fmt.Sprintf("the environment invariant %s couldn't be checked after this scenario, so whether it left state behind is unknown", strings.Join(errored, ", "))
		}
		return ""
	}
	if dirty != "" {
		res.DirtySince = dirty
		if len(failed) == 0 {
			return ""
		}
		return dirty
	}
	if len(failed) == 0 {
		return ""
	}
	res.LeftStateBehind = failed
	ids := make([]string, 0, len(failed))
	for _, f := range failed {
		ids = append(ids, f.ID)
	}
	note := fmt.Sprintf("the environment invariant %s didn't hold after this scenario, so it left state behind that later scenarios may meet", strings.Join(ids, ", "))
	if res.Result == Pass {
		res.Result, res.Reason, res.Cause, res.Message = Error, ReasonLeftStateBehind, "", note
	}
	name := res.ID
	if res.Table != "" {
		name = res.Table + "/" + res.ID
	}
	return name
}
