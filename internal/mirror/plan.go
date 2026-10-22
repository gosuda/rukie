package mirror

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

type RefAction string

const (
	ActionCreate        RefAction = "create"
	ActionUpdate        RefAction = "update"
	ActionForceUpdate   RefAction = "force_update"
	ActionDelete        RefAction = "delete"
	ActionPrune         RefAction = "prune"
	ActionDiverged      RefAction = "diverged"
	ActionIgnoredHidden RefAction = "ignored_hidden"
	ActionBlockedDF     RefAction = "blocked_df"
	ActionInSync        RefAction = "in_sync"
)

type RefPlan struct {
	Ref, Old, New string
	Action        RefAction
	Reason        string
}
type Plan struct {
	Updates, Deletes  []RefPlan
	Diverged, Ignored []RefPlan
	Digest            string
	TargetNonEmpty    bool
}

var ErrNeedsConfirmation = errors.New("mirror plan needs confirmation")

var excludedPrefixes = []string{"refs/pull/", "refs/merge-requests/", "refs/for/", "refs/ruk/"}

func ComputePlan(local, remote map[string]string, m Mirror, remoteDefaultBranch, confirmedDigest string) (Plan, error) {
	p := Plan{TargetNonEmpty: len(remote) != 0}
	selected := func(ref string) bool {
		if hidden(ref) {
			return true
		}
		if !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/") {
			return false
		}
		switch m.RefSelection {
		case RefSelectionAll:
			return true
		case RefSelectionDefaultBranchTags:
			return strings.HasPrefix(ref, "refs/tags/") || ref == "refs/heads/"+remoteDefaultBranch
		case RefSelectionGlobs:
			for _, pattern := range m.Globs {
				ok, err := path.Match(pattern, ref)
				if err == nil && ok {
					return true
				}
			}
		}
		return false
	}
	for ref, oid := range local {
		if !selected(ref) {
			continue
		}
		if hidden(ref) {
			p.Ignored = append(p.Ignored, RefPlan{Ref: ref, New: oid, Action: ActionIgnoredHidden})
			continue
		}
		old, exists := remote[ref]
		if !exists {
			p.Updates = append(p.Updates, RefPlan{Ref: ref, New: oid, Action: ActionCreate})
			continue
		}
		if old == oid {
			p.Updates = append(p.Updates, RefPlan{Ref: ref, Old: old, New: oid, Action: ActionInSync})
			continue
		}
		if m.Mode == ModeFastForward {
			p.Diverged = append(p.Diverged, RefPlan{Ref: ref, Old: old, New: oid, Action: ActionDiverged, Reason: "ancestry must be checked by the daemon"})
		} else {
			p.Updates = append(p.Updates, RefPlan{Ref: ref, Old: old, New: oid, Action: ActionForceUpdate})
		}
	}
	if m.Mode == ModeExact {
		for ref, oid := range remote {
			if !selected(ref) {
				continue
			}
			if hidden(ref) {
				p.Ignored = append(p.Ignored, RefPlan{Ref: ref, Old: oid, Action: ActionIgnoredHidden})
				continue
			}
			if _, ok := local[ref]; !ok {
				action := ActionDelete
				if strings.HasPrefix(ref, "refs/tags/") {
					action = ActionPrune
				}
				p.Deletes = append(p.Deletes, RefPlan{Ref: ref, Old: oid, Action: action})
			}
		}
	}
	for i := range p.Updates {
		if p.Updates[i].Action != ActionCreate {
			continue
		}
		ref := p.Updates[i].Ref
		for _, d := range p.Deletes {
			if strings.HasPrefix(ref, d.Ref+"/") {
				p.Updates[i].Action = ActionBlockedDF
				p.Updates[i].Reason = "file/directory conflict with " + d.Ref
				break
			}
		}
	}
	sortPlans(p.Updates)
	sortPlans(p.Deletes)
	sortPlans(p.Diverged)
	sortPlans(p.Ignored)
	p.Digest = planDigest(p)
	planNonEmpty := len(p.Updates)+len(p.Deletes)+len(p.Diverged) > 0
	needConfirm := m.Mode == ModeExact && planNonEmpty && (len(p.Deletes) > 0 || p.TargetNonEmpty)
	if needConfirm && confirmedDigest != p.Digest && (!m.AdoptExistingRefs || len(p.Deletes) > 0) {
		return p, fmt.Errorf("ComputePlan: %w", ErrNeedsConfirmation)
	}
	return p, nil
}
func hidden(ref string) bool {
	for _, prefix := range excludedPrefixes {
		if strings.HasPrefix(ref, prefix) {
			return true
		}
	}
	return false
}
func sortPlans(p []RefPlan) {
	slices.SortFunc(p, func(a, b RefPlan) int { return cmp.Compare(a.Ref, b.Ref) })
}
func planDigest(p Plan) string {
	var b strings.Builder
	for _, list := range [][]RefPlan{p.Updates, p.Deletes, p.Diverged, p.Ignored} {
		for _, r := range list {
			fmt.Fprintf(&b, "%s\x00%s\x00%s\x00%s\x00%s\n", r.Ref, r.Old, r.New, r.Action, r.Reason)
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
