package supervisor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func tips(m map[string]string) func(context.Context) (map[string]string, error) {
	return func(context.Context) (map[string]string, error) {
		return m, nil
	}
}

func TestGitServiceStreamNeverReruns(t *testing.T) {
	committed := LookupResult{Found: true, ExitCode: 0}
	failed := LookupResult{Found: true, ExitCode: 1}
	cancelled := LookupResult{Found: true, ExitCode: 0, Cancelled: true}
	absent := LookupResult{}
	want := map[string]string{"refs/heads/main": "abc123"}
	landed := map[string]string{"refs/heads/main": "abc123"}
	moved := map[string]string{"refs/heads/main": "def456"}

	for _, tc := range []struct {
		lookupErr error
		tipsErr   error
		current   map[string]string
		want      map[string]string
		name      string
		lookup    LookupResult
		verdict   Verdict
		wantErr   bool
	}{
		{name: "clean lookup commits without touching tips", lookup: committed, current: moved, want: want, verdict: VerdictCommitted},
		{name: "failed run with landed tips commits", lookup: failed, current: landed, want: want, verdict: VerdictCommitted},
		{name: "cancelled run with landed tips commits", lookup: cancelled, current: landed, want: want, verdict: VerdictCommitted},
		{name: "absent lookup with landed tips commits", lookup: absent, current: landed, want: want, verdict: VerdictCommitted},
		{name: "absent lookup with moved tips stays unknown", lookup: absent, current: moved, want: want, verdict: VerdictUnknown},
		{name: "failed run with missing ref stays unknown", lookup: failed, current: map[string]string{}, want: want, verdict: VerdictUnknown},
		{name: "lookup error with landed tips commits", lookup: absent, lookupErr: errors.New("eof"), current: landed, want: want, verdict: VerdictCommitted},
		{name: "lookup error with moved tips stays unknown and keeps the error", lookup: absent, lookupErr: errors.New("eof"), current: moved, want: want, verdict: VerdictUnknown, wantErr: true},
		{name: "tips error stays unknown and keeps the error", lookup: absent, current: landed, tipsErr: errors.New("down"), want: want, verdict: VerdictUnknown, wantErr: true},
		{name: "empty want stays unknown without concluding", lookup: committed, current: landed, want: map[string]string{}, verdict: VerdictUnknown, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var lookupCalls, tipsCalls int
			lookup := func(context.Context) (LookupResult, error) {
				lookupCalls++
				return tc.lookup, tc.lookupErr
			}
			current := func(context.Context) (map[string]string, error) {
				tipsCalls++
				if tc.tipsErr != nil {
					return nil, tc.tipsErr
				}
				return tc.current, nil
			}
			got, err := ReconcileUnknownOutcome(context.Background(), lookup, current, tc.want)
			if got != tc.verdict {
				t.Fatalf("verdict = %v, want %v", got, tc.verdict)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if len(tc.want) == 0 {
				if lookupCalls != 0 || tipsCalls != 0 {
					t.Fatalf("empty want ran lookup %d and tips %d times, want 0: refuse before any I/O", lookupCalls, tipsCalls)
				}
				return
			}
			if lookupCalls != 1 {
				t.Fatalf("lookup ran %d times, want exactly 1: streams never re-run and lookups never retry", lookupCalls)
			}
			if tc.lookup.Found && tc.lookup.ExitCode == 0 && !tc.lookup.Cancelled && tc.lookupErr == nil && len(tc.want) > 0 {
				if tipsCalls != 0 {
					t.Fatalf("tips ran %d times after a clean lookup, want 0", tipsCalls)
				}
			} else if len(tc.want) > 0 && tipsCalls != 1 {
				t.Fatalf("tips ran %d times, want exactly 1", tipsCalls)
			}
		})
	}
}

func TestGitServiceStreamReconcileRespectsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReconcileUnknownOutcome(ctx,
		func(context.Context) (LookupResult, error) { return LookupResult{}, nil },
		tips(map[string]string{"refs/heads/main": "abc123"}),
		map[string]string{"refs/heads/main": "abc123"}); err == nil {
		t.Fatal("reconcile under a cancelled context returned nil error")
	}
}

func TestGitServiceStreamReconcileDeletionTarget(t *testing.T) {
	landed := "abc123"
	for _, tc := range []struct {
		name    string
		want    map[string]string
		current map[string]string
		verdict Verdict
	}{
		{"empty want target with absent tip commits", map[string]string{"refs/heads/gone": ""}, map[string]string{}, VerdictCommitted},
		{"zero want target with absent tip commits", map[string]string{"refs/heads/gone": strings.Repeat("0", 40)}, map[string]string{}, VerdictCommitted},
		{"empty want target with present tip stays unknown", map[string]string{"refs/heads/gone": ""}, map[string]string{"refs/heads/gone": landed}, VerdictUnknown},
		{"zero current tip counts as absent", map[string]string{"refs/heads/gone": ""}, map[string]string{"refs/heads/gone": strings.Repeat("0", 40)}, VerdictCommitted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReconcileUnknownOutcome(context.Background(),
				func(context.Context) (LookupResult, error) { return LookupResult{}, nil },
				tips(tc.current),
				tc.want)
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if got != tc.verdict {
				t.Fatalf("verdict = %v, want %v", got, tc.verdict)
			}
		})
	}
}
