package mirror

import (
	"errors"
	"fmt"

	"github.com/gosuda/rukie/internal/ruk"
)

type reconcileNeedsAttentionError struct {
	err  error
	plan string
}

func (e *reconcileNeedsAttentionError) Error() string {
	return fmt.Sprintf("push plan %s requires attention: %v", e.plan, e.err)
}

func (e *reconcileNeedsAttentionError) Unwrap() error { return e.err }

func checkReconcilePlan(plan string, reply ruk.PushReconcileReply, callErr error) error {
	_, incomplete := errors.AsType[*ruk.IncompleteReconciliationError](callErr)
	if incomplete || errors.Is(callErr, ruk.ErrInvalid) {
		return fmt.Errorf("checkReconcilePlan: %w", &reconcileNeedsAttentionError{plan: plan, err: callErr})
	}
	if callErr != nil {
		return fmt.Errorf("checkReconcilePlan: %w", callErr)
	}
	if reply.Plan != plan {
		return fmt.Errorf("checkReconcilePlan: %w", &reconcileNeedsAttentionError{
			plan: plan, err: fmt.Errorf("reconciliation returned plan %q", reply.Plan),
		})
	}
	if reply.Rewritten {
		return fmt.Errorf("checkReconcilePlan: %w", &reconcileNeedsAttentionError{
			plan: plan, err: errors.New("remote reported different refs or OIDs for this push"),
		})
	}
	return nil
}
