package mirror

import "slices"

func (e *execution) remainingApprovedDigest(plan Plan) string {
	approved := e.loaded.run.ConfirmedDigest
	if approved == "" || approved != plan.Digest || planDigest(plan, e.cfg.TargetURL) != approved {
		return ""
	}
	byRef := make(map[string]*trackedRef, len(e.tracked))
	for _, tracked := range e.tracked {
		byRef[tracked.plan.Ref] = tracked
	}
	completed := func(ref RefPlan) bool {
		tracked := byRef[ref.Ref]
		if tracked == nil || tracked.plan.Old != ref.Old || tracked.plan.New != ref.New {
			return false
		}
		if ref.Action == ActionDelete || ref.Action == ActionPrune {
			return tracked.status == "deleted" && tracked.confirmed == nil
		}
		return tracked.status == "ok" && tracked.confirmed != nil && *tracked.confirmed == ref.New
	}
	remaining := plan
	remaining.Updates = slices.Clone(plan.Updates)
	remaining.Deletes = make([]RefPlan, 0, len(plan.Deletes))
	changed := false
	for _, ref := range plan.Deletes {
		if completed(ref) {
			changed = true
			continue
		}
		remaining.Deletes = append(remaining.Deletes, ref)
	}
	for i, ref := range remaining.Updates {
		if completed(ref) {
			remaining.Updates[i] = RefPlan{Ref: ref.Ref, Old: ref.New, New: ref.New, Action: ActionInSync}
			changed = true
		} else if ref.Action == ActionBlockedDF && dfBlockerFor(ref, remaining.Deletes) == "" {
			remaining.Updates[i] = RefPlan{Ref: ref.Ref, New: ref.New, Action: ActionCreate}
		}
	}
	if !changed {
		return ""
	}
	return planDigest(remaining, e.cfg.TargetURL)
}
