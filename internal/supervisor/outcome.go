package supervisor

import (
	"context"
	"errors"
	"fmt"
)

// Verdict is the outcome of reconciling a git-service stream whose fate
// is unknown. There is no rerun verdict: a stream never re-runs.
type Verdict int

const (
	// VerdictUnknown means the outcome could not be established; the
	// caller must surface uncertainty, never replay the stream.
	VerdictUnknown Verdict = iota
	// VerdictCommitted means the effects are observably in place.
	VerdictCommitted
)

// LookupResult is one outcome-lookup answer for a stream request id.
type LookupResult struct {
	ExitCode  int
	Found     bool
	Cancelled bool
}

// ReconcileUnknownOutcome settles a git-service stream of unknown fate
// without ever re-running it: first an outcome Lookup on the request id,
// then a tip comparison of the pushed refs against current daemon tips.
// want holds the ref name to target OID map the stream intended to land;
// an empty (or all-zero) target is a deletion, where an absent tip means
// the push landed.
//
// The lookup wins when it records a clean exit. Otherwise the tips
// decide: every wanted ref at its wanted target means the push landed
// despite the unknown outcome. Anything else stays unknown.
func ReconcileUnknownOutcome(
	ctx context.Context,
	lookup func(context.Context) (LookupResult, error),
	currentTips func(context.Context) (map[string]string, error),
	want map[string]string,
) (Verdict, error) {
	if len(want) == 0 {
		return VerdictUnknown, fmt.Errorf("supervisor: reconcile needs at least one wanted ref")
	}
	result, lerr := lookup(ctx)
	if lerr == nil && result.Found && !result.Cancelled && result.ExitCode == 0 {
		return VerdictCommitted, nil
	}
	tips, terr := currentTips(ctx)
	if terr != nil {
		return VerdictUnknown, errors.Join(lerr, fmt.Errorf("supervisor: list tips: %w", terr))
	}
	for name, target := range want {
		if ctx.Err() != nil {
			return VerdictUnknown, fmt.Errorf("supervisor: reconcile: %w", ctx.Err())
		}
		current, ok := tips[name]
		if zeroTarget(target) {
			if !ok || zeroTarget(current) {
				continue
			}
			return VerdictUnknown, lerr
		}
		if !ok || current != target {
			return VerdictUnknown, lerr
		}
	}
	return VerdictCommitted, nil
}

func zeroTarget(target string) bool {
	if target == "" {
		return true
	}
	for i := range len(target) {
		if target[i] != '0' {
			return false
		}
	}
	return true
}
