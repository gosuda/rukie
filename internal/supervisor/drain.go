package supervisor

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// DefaultDrainTimeout bounds the full SIGTERM drain at 40 s. The systemd
// unit allows 90 s; the drain keeps margin for the child stop that runs
// inside it.
const DefaultDrainTimeout = 40 * time.Second

// Step is one ordered drain action. Steps run sequentially in slice
// order; every step runs even when an earlier one fails, and failures
// join into the returned error.
type Step struct {
	Run  func(ctx context.Context) error
	Name string
}

// DrainOrder is the ops-design §3.3 shutdown sequence with the K7
// workers spliced in: drain pack sessions, stop followers and queue
// workers, flush the spool, pause CI dispatch, flush the webhook
// outbox, checkpoint the store, then stop the daemon child.
var DrainOrder = []string{"ssh", "http", "followers", "queue", "spool", "scheduler", "webhooks", "store", "child"}

// Drain runs every step in order, even when ctx already expired or an
// earlier step failed; it returns the joined step errors, if any. Steps
// observe ctx themselves and return promptly on expiry, so a blown
// budget is reported as step errors instead of silently skipping the
// tail: the child step always runs and the child is always reaped.
func Drain(ctx context.Context, steps []Step) error {
	var errs []error
	for _, step := range steps {
		if err := step.Run(ctx); err != nil {
			errs = append(errs, fmt.Errorf("supervisor: drain %q: %w", step.Name, err))
		}
	}
	return errors.Join(errs...)
}

// OrderedDrain builds the Drain step list from the owned runners,
// sequenced by DrainOrder. Components wire their runners as they land
// (followers, queue workers, and the spool drain arrive with the K7
// serve wiring); unknown names fail fast so typos cannot silently
// reorder the shutdown.
func OrderedDrain(runners map[string]func(ctx context.Context) error) ([]Step, error) {
	for name := range runners {
		known := false
		for _, ordered := range DrainOrder {
			if name == ordered {
				known = true
				break
			}
		}
		if !known {
			return nil, fmt.Errorf("supervisor: unknown drain step %q", name)
		}
	}
	steps := make([]Step, 0, len(runners))
	for _, name := range DrainOrder {
		run, ok := runners[name]
		if !ok || run == nil {
			continue
		}
		steps = append(steps, Step{Name: name, Run: run})
	}
	return steps, nil
}
