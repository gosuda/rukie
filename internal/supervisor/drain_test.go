package supervisor

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestGracefulDrain(t *testing.T) {
	if DefaultDrainTimeout != 40*time.Second {
		t.Fatalf("DefaultDrainTimeout = %s, want 40s per the ops-design shutdown budget", DefaultDrainTimeout)
	}
	// The ops-design §3.3 shutdown order with the K7 workers spliced
	// in, as a literal: any reorder of DrainOrder fails here.
	want := []string{"ssh", "http", "followers", "queue", "spool", "scheduler", "webhooks", "store", "child"}
	if !slices.Equal(DrainOrder, want) {
		t.Fatalf("DrainOrder = %v, want %v", DrainOrder, want)
	}
	// Runners arrive in hostile (reverse) order; the drain must still
	// follow DrainOrder.
	var order []string
	runners := map[string]func(ctx context.Context) error{}
	for i := len(want) - 1; i >= 0; i-- {
		name := want[i]
		runners[name] = func(context.Context) error {
			order = append(order, name)
			return nil
		}
	}
	steps, err := OrderedDrain(runners)
	if err != nil {
		t.Fatalf("OrderedDrain: %v", err)
	}
	if err := Drain(context.Background(), steps); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if !slices.Equal(order, want) {
		t.Fatalf("drain order = %v, want %v", order, want)
	}
}

func TestGracefulDrainRunsAllStepsDespiteFailure(t *testing.T) {
	var order []string
	boom := errors.New("store checkpoint failed")
	steps, err := OrderedDrain(map[string]func(ctx context.Context) error{
		"http":  func(context.Context) error { order = append(order, "http"); return nil },
		"store": func(context.Context) error { order = append(order, "store"); return boom },
		"child": func(context.Context) error { order = append(order, "child"); return nil },
	})
	if err != nil {
		t.Fatalf("OrderedDrain: %v", err)
	}
	err = Drain(context.Background(), steps)
	if !errors.Is(err, boom) {
		t.Fatalf("Drain error = %v, want it to carry the store failure", err)
	}
	if !slices.Equal(order, []string{"http", "store", "child"}) {
		t.Fatalf("drain order after failure = %v, want every step to run", order)
	}
}

func TestGracefulDrainStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	// DrainOrder runs ssh before http; http cancels, so the later
	// store step must not run.
	seq, err := OrderedDrain(map[string]func(ctx context.Context) error{
		"ssh": func(context.Context) error { calls++; return nil },
		"http": func(_ context.Context) error {
			calls++
			cancel()
			return nil
		},
		"store": func(context.Context) error { calls++; return nil },
	})
	if err != nil {
		t.Fatalf("OrderedDrain: %v", err)
	}
	if err := Drain(ctx, seq); err == nil {
		t.Fatal("Drain under a cancelled context returned nil")
	}
	if calls != 2 {
		t.Fatalf("drain ran %d steps after cancel, want 2 (ssh, http)", calls)
	}
}

func TestGracefulDrainRejectsUnknownSteps(t *testing.T) {
	if _, err := OrderedDrain(map[string]func(ctx context.Context) error{
		"http":   func(context.Context) error { return nil },
		"pages":  func(context.Context) error { return nil },
		"child":  func(context.Context) error { return nil },
		"ssh":    func(context.Context) error { return nil },
		"store":  func(context.Context) error { return nil },
		"spool":  func(context.Context) error { return nil },
		"queue":  func(context.Context) error { return nil },
		"follow": func(context.Context) error { return nil },
	}); err == nil {
		t.Fatal("OrderedDrain accepted unknown step names, want a fail-fast error")
	}
}

func TestGracefulDrainPartialWiringKeepsOrder(t *testing.T) {
	var order []string
	steps, err := OrderedDrain(map[string]func(ctx context.Context) error{
		"child": func(context.Context) error { order = append(order, "child"); return nil },
		"http":  func(context.Context) error { order = append(order, "http"); return nil },
		"store": func(context.Context) error { order = append(order, "store"); return nil },
	})
	if err != nil {
		t.Fatalf("OrderedDrain: %v", err)
	}
	if err := Drain(context.Background(), steps); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if !slices.Equal(order, []string{"http", "store", "child"}) {
		t.Fatalf("partial drain order = %v, want [http store child]", order)
	}
}
