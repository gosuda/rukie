package ruk

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ruk/internal/fakedaemon"
	"github.com/gosuda/rukie/internal/ruk/wire"
)

type reconcileTestList[T any] struct {
	Items     []T    `json:"items"`
	Total     uint64 `json:"total"`
	Truncated bool   `json:"truncated"`
}

type reconcileTestPage struct {
	Next                 *PushReconcileCursor                            `json:"next,omitempty"`
	Plan                 string                                          `json:"plan"`
	State                string                                          `json:"state"`
	Observations         reconcileTestList[ReconcileObservation]         `json:"observations"`
	ReportedObservations reconcileTestList[ReportedReconcileObservation] `json:"reported_observations"`
	Rewritten            bool                                            `json:"rewritten"`
}

func reconcilePageJSON(t *testing.T, page reconcileTestPage) jsonv1.RawMessage {
	t.Helper()
	if page.Observations.Items == nil {
		page.Observations.Items = []ReconcileObservation{}
	}
	if page.ReportedObservations.Items == nil {
		page.ReportedObservations.Items = []ReportedReconcileObservation{}
	}
	page.Observations.Truncated = page.Observations.Total > uint64(len(page.Observations.Items))
	page.ReportedObservations.Truncated = page.ReportedObservations.Total > uint64(len(page.ReportedObservations.Items))
	return jsonv1.RawMessage(mustMarshalJSON(t, page))
}

func callReconcilePages(t *testing.T, args PushReconcileArgs, pages []jsonv1.RawMessage) (PushReconcileReply, []PushReconcileArgs, error) {
	t.Helper()
	var mu sync.Mutex
	var requests []PushReconcileArgs
	socket, _ := startFake(t, fakedaemon.Config{
		Answer: func(method string, raw jsonv1.RawMessage) jsonv1.RawMessage {
			if method != string(wire.MethodPushReconcile) {
				if method == string(wire.MethodPush) {
					t.Error("reconciliation sent a push")
				}
				return nil
			}
			var request PushReconcileArgs
			if err := jsonv2.Unmarshal(raw, &request, jsonv1.DefaultOptionsV1()); err != nil {
				t.Errorf("decode page request: %v", err)
				return jsonv1.RawMessage("{}")
			}
			mu.Lock()
			defer mu.Unlock()
			index := len(requests)
			requests = append(requests, request)
			if index >= len(pages) {
				t.Errorf("unexpected page request %d: %+v", index, request)
				return jsonv1.RawMessage("{}")
			}
			return pages[index]
		},
	})
	client := dialTest(t, socket)
	ctx, cancel := callCtx(t, 10*time.Second)
	defer cancel()
	reply, err := PushReconcile(ctx, client, Repo{Path: "/repo"}, args, wire.TransportAuth{})
	mu.Lock()
	defer mu.Unlock()
	return reply, slices.Clone(requests), err
}

func TestPushReconcilePaginationHappy(t *testing.T) {
	main := ReconcileObservation{RemoteRef: "refs/heads/main", Verdict: "equal_old"}
	side := ReconcileObservation{RemoteRef: "refs/heads/side", Verdict: "moved"}
	old, firstNew, secondNew := "old", "first-new", "second-new"
	reported := []ReportedReconcileObservation{
		{RemoteRef: main.RemoteRef, Old: &old, New: &firstNew, Observed: &firstNew, Verdict: "equal_new"},
		{RemoteRef: main.RemoteRef, Old: &old, New: &secondNew, Observed: &firstNew, Verdict: "moved"},
	}
	cursor := func(page uint32) *PushReconcileCursor {
		return &PushReconcileCursor{Session: "snapshot-session", Request: "snapshot-request", Page: page}
	}
	pages := []jsonv1.RawMessage{
		reconcilePageJSON(t, reconcileTestPage{
			Plan: "plan", State: "uncertain", Rewritten: true, Next: cursor(1),
			Observations:         reconcileTestList[ReconcileObservation]{Items: []ReconcileObservation{main}, Total: 2},
			ReportedObservations: reconcileTestList[ReportedReconcileObservation]{Total: 2},
		}),
		reconcilePageJSON(t, reconcileTestPage{
			Plan: "plan", State: "uncertain", Rewritten: true, Next: cursor(2),
			Observations:         reconcileTestList[ReconcileObservation]{Items: []ReconcileObservation{side}, Total: 2},
			ReportedObservations: reconcileTestList[ReportedReconcileObservation]{Items: reported[:1], Total: 2},
		}),
		reconcilePageJSON(t, reconcileTestPage{
			Plan: "plan", State: "uncertain", Rewritten: true,
			Observations:         reconcileTestList[ReconcileObservation]{Total: 2},
			ReportedObservations: reconcileTestList[ReportedReconcileObservation]{Items: reported[1:], Total: 2},
		}),
	}
	remote := "ssh://alice@example.test:2222/repository"
	ssh := OriginalSSHIdentity{User: "alice", Port: 2222}
	reply, calls, err := callReconcilePages(t, PushReconcileArgs{
		Plan: "plan", OriginalRemote: &remote, OriginalSSH: &ssh,
	}, pages)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Next != nil || !reply.Rewritten || len(reply.Observations) != 2 || len(reply.ReportedObservations) != 2 || len(calls) != 3 {
		t.Fatalf("complete snapshot = %+v, calls = %+v", reply, calls)
	}
	if !slices.Equal(reply.Observations, []ReconcileObservation{main, side}) {
		t.Fatalf("requested observations changed: %+v", reply.Observations)
	}
	for i, observation := range reply.ReportedObservations {
		if observation.RemoteRef != main.RemoteRef || observation.Old == nil || *observation.Old != old ||
			observation.New == nil || *observation.New != *reported[i].New ||
			observation.Observed == nil || *observation.Observed != firstNew || observation.Verdict != reported[i].Verdict {
			t.Fatalf("reported comparison %d = %+v", i, observation)
		}
	}
	var bytes uint64
	for _, page := range pages {
		bytes += uint64(len(page))
	}
	if reply.wireBytes != bytes {
		t.Fatalf("wire byte count = %d, want %d", reply.wireBytes, bytes)
	}
	if calls[0].Cursor != nil || calls[0].OriginalRemote == nil || *calls[0].OriginalRemote != remote ||
		calls[0].OriginalSSH == nil || *calls[0].OriginalSSH != ssh {
		t.Fatalf("initial historical identity = %+v", calls[0])
	}
	for i, call := range calls[1:] {
		if call.OriginalRemote != nil || call.OriginalSSH != nil || call.Cursor == nil ||
			call.Cursor.Session != "snapshot-session" || call.Cursor.Request != "snapshot-request" || call.Cursor.Page != uint32(i+1) {
			t.Fatalf("continuation request = %+v", call)
		}
	}
}

func TestPushReconcilePreservesRepeatedReports(t *testing.T) {
	old, newTip := "old", "new"
	report := ReportedReconcileObservation{
		RemoteRef: "refs/heads/main", Old: &old, New: &newTip, Observed: &newTip, Verdict: "equal_new",
	}
	first := reconcileTestPage{
		Plan: "plan", State: "uncertain", Rewritten: true,
		Next:                 &PushReconcileCursor{Session: "session", Request: "request", Page: 1},
		ReportedObservations: reconcileTestList[ReportedReconcileObservation]{Items: []ReportedReconcileObservation{report}, Total: 2},
	}
	second := first
	second.Next = nil
	reply, calls, err := callReconcilePages(t, PushReconcileArgs{Plan: "plan"}, []jsonv1.RawMessage{
		reconcilePageJSON(t, first), reconcilePageJSON(t, second),
	})
	if err != nil || len(calls) != 2 || len(reply.ReportedObservations) != 2 {
		t.Fatalf("repeated reported targets = %+v, %v, calls = %d", reply, err, len(calls))
	}
	for _, observed := range reply.ReportedObservations {
		if observed.RemoteRef != report.RemoteRef || observed.Old == nil || *observed.Old != old ||
			observed.New == nil || *observed.New != newTip || observed.Observed == nil ||
			*observed.Observed != newTip || observed.Verdict != report.Verdict {
			t.Fatalf("reported target changed: %+v", observed)
		}
	}
}

func TestPushReconcileRejectsPages(t *testing.T) {
	for _, name := range []string{
		"missing-next", "extra-next", "first-page", "empty-page", "oversized",
		"changed-plan", "changed-state", "changed-rewrite", "changed-total",
		"repeat-ref", "repeat-cursor", "changed-session", "changed-request",
		"skipped-page", "missing-final", "false-rewrite", "missing-total",
	} {
		t.Run(name, func(t *testing.T) {
			first := reconcileTestPage{
				Plan: "plan", State: "uncertain",
				Next: &PushReconcileCursor{Session: "session", Request: "request", Page: 1},
				Observations: reconcileTestList[ReconcileObservation]{
					Items: []ReconcileObservation{{RemoteRef: "main", Verdict: "equal_old"}}, Total: 2,
				},
			}
			second := reconcileTestPage{
				Plan: "plan", State: "uncertain",
				Observations: reconcileTestList[ReconcileObservation]{
					Items: []ReconcileObservation{{RemoteRef: "side", Verdict: "equal_old"}}, Total: 2,
				},
			}
			wantCalls := 2
			switch name {
			case "missing-next":
				first.Next = nil
				wantCalls = 1
			case "extra-next":
				first.Observations.Total = 1
				wantCalls = 1
			case "first-page":
				first.Next.Page = 2
				wantCalls = 1
			case "empty-page":
				first.Observations.Items = nil
				wantCalls = 1
			case "oversized":
				first.Next = nil
				first.Observations.Items = make([]ReconcileObservation, 1001)
				first.Observations.Total = 1001
				wantCalls = 1
			case "changed-plan":
				second.Plan = "other-plan"
			case "changed-state":
				second.State = "confirmed"
			case "changed-rewrite":
				second.Rewritten = true
			case "changed-total":
				second.Observations.Total = 3
			case "repeat-ref":
				second.Observations.Items[0].RemoteRef = "main"
			case "repeat-cursor", "changed-session", "changed-request", "skipped-page", "missing-final":
				first.Observations.Total = 3
				second.Observations.Total = 3
				second.Next = &PushReconcileCursor{Session: "session", Request: "request", Page: 2}
				switch name {
				case "repeat-cursor":
					second.Next.Page = 1
				case "changed-session":
					second.Next.Session = "other-session"
				case "changed-request":
					second.Next.Request = "other-request"
				case "skipped-page":
					second.Next.Page = 3
				case "missing-final":
					second.Next = nil
				}
			case "false-rewrite":
				first.ReportedObservations.Total = 1
				wantCalls = 1
			case "missing-total":
				wantCalls = 1
			}
			pages := []jsonv1.RawMessage{reconcilePageJSON(t, first), reconcilePageJSON(t, second)}
			if name == "missing-total" {
				pages[0] = jsonv1.RawMessage(`{"plan":"plan","state":"uncertain","observations":{"items":[{"remote_ref":"main","verdict":"equal_old"}]},"next":{"session":"s","request":"r","page":1}}`)
			}
			reply, calls, err := callReconcilePages(t, PushReconcileArgs{Plan: "plan"}, pages)
			if _, ok := errors.AsType[*IncompleteReconciliationError](err); !ok {
				t.Fatalf("invalid snapshot error = %v", err)
			}
			if reply.Plan != "" || reply.Next != nil || len(reply.Observations) != 0 || len(reply.ReportedObservations) != 0 || len(calls) != wantCalls {
				t.Fatalf("invalid snapshot returned partial success: %+v, calls = %d", reply, len(calls))
			}
		})
	}
}

func TestPushReconcileFinalPageFailure(t *testing.T) {
	first := reconcilePageJSON(t, reconcileTestPage{
		Plan: "plan", State: "uncertain",
		Next: &PushReconcileCursor{Session: "session", Request: "request", Page: 1},
		Observations: reconcileTestList[ReconcileObservation]{
			Items: []ReconcileObservation{{RemoteRef: "main", Verdict: "equal_new"}}, Total: 2,
		},
	})
	failure := jsonv1.RawMessage(`{"schema_version":{"major":1,"minor":1},"body":{"failure":{"code":"E_IO","message":"final page unavailable"}}}`)
	reply, calls, err := callReconcilePages(t, PushReconcileArgs{Plan: "plan"}, []jsonv1.RawMessage{first, failure})
	if !errors.Is(err, ErrIo) {
		t.Fatalf("final page failure = %v", err)
	}
	if _, terminal := errors.AsType[*IncompleteReconciliationError](err); terminal {
		t.Fatalf("transport failure became terminal: %v", err)
	}
	if reply.Plan != "" || len(reply.Observations) != 0 || len(calls) != 2 {
		t.Fatalf("failed final page exposed partial observations: %+v, calls = %d", reply, len(calls))
	}
}

func TestPushReconcileLegacySnapshot(t *testing.T) {
	page := jsonv1.RawMessage(`{"plan":"plan","state":"uncertain","rewritten":true,"observations":{"items":[{"remote_ref":"main","verdict":"equal_old"}],"total":1,"truncated":false}}`)
	reply, calls, err := callReconcilePages(t, PushReconcileArgs{Plan: "plan"}, []jsonv1.RawMessage{page})
	if err != nil || len(calls) != 1 || len(reply.Observations) != 1 || len(reply.ReportedObservations) != 0 || !reply.Rewritten {
		t.Fatalf("legacy snapshot = %+v, %v, calls = %d", reply, err, len(calls))
	}
	if calls[0].OriginalSSH != nil || calls[0].OriginalRemote != nil {
		t.Fatalf("automatic recovery invented a historical identity: %+v", calls[0])
	}
}

func TestPushReconcileRejectsInitialCursor(t *testing.T) {
	_, err := PushReconcile(t.Context(), nil, Repo{}, PushReconcileArgs{
		Plan: "plan", Cursor: &PushReconcileCursor{Session: "session", Request: "request", Page: 1},
	}, wire.TransportAuth{})
	if _, ok := errors.AsType[*IncompleteReconciliationError](err); !ok {
		t.Fatalf("suffix accepted as a complete snapshot: %v", err)
	}
}

func TestPushReconcileResponseBudget(t *testing.T) {
	for _, pages := range []uint32{64, 65} {
		t.Run(fmt.Sprint(pages), func(t *testing.T) {
			var calls uint32
			reply, err := collectPushReconcile(t.Context(), PushReconcileArgs{Plan: "plan"}, func(_ context.Context, args PushReconcileArgs) (PushReconcileReply, error) {
				calls++
				page := PushReconcileReply{
					Plan: "plan", State: "uncertain", wireBytes: 1 << 20,
					Observations:    []ReconcileObservation{{RemoteRef: fmt.Sprintf("refs/heads/%d", calls), Verdict: "equal_old"}},
					requestedBounds: reconcileBounds{total: uint64(pages), explicit: true, present: true},
				}
				if calls < pages {
					page.Next = &PushReconcileCursor{Session: "session", Request: "request", Page: calls}
				}
				if calls > 1 && (args.Cursor == nil || args.Cursor.Page != calls-1) {
					t.Fatalf("page cursor = %+v at call %d", args.Cursor, calls)
				}
				return page, nil
			})
			if calls != pages {
				t.Fatalf("budget stopped at call %d, want %d", calls, pages)
			}
			if pages == 64 {
				if err != nil || len(reply.Observations) != int(pages) || reply.wireBytes != maxReconcileResponseBytes {
					t.Fatalf("exact budget refused: %+v, %v", reply, err)
				}
			} else if _, ok := errors.AsType[*IncompleteReconciliationError](err); !ok || len(reply.Observations) != 0 {
				t.Fatalf("overflow returned success or partial data: %+v, %v", reply, err)
			}
		})
	}
}

func TestPushReconcileRejectsInvalidBounds(t *testing.T) {
	for _, fields := range []string{
		`"items":null`,
		`"items":[],"total":null`,
		`"items":[],"total":-1`,
		`"items":[],"total":"1"`,
		`"items":[],"total":0,"truncated":null`,
		`"items":[],"total":1,"truncated":false`,
		`"items":[],"total":0,"truncated":true`,
		`"items":[{"remote_ref":"main"}],"total":0,"truncated":false`,
	} {
		t.Run(fields, func(t *testing.T) {
			doc := `{"plan":"plan","state":"uncertain","observations":{` + fields + `}}`
			reply := PushReconcileReply{Plan: "unchanged"}
			if err := jsonv2.Unmarshal([]byte(doc), &reply, jsonv1.DefaultOptionsV1()); err == nil || reply.Plan != "unchanged" {
				t.Fatalf("invalid bounds changed the reply: %+v, %v", reply, err)
			}
		})
	}
}

func TestPushReconcilePageBudget(t *testing.T) {
	for _, pages := range []uint32{maxReconcilePages, maxReconcilePages + 1} {
		t.Run(fmt.Sprint(pages), func(t *testing.T) {
			var calls uint32
			reply, err := collectPushReconcile(t.Context(), PushReconcileArgs{Plan: "plan"}, func(_ context.Context, args PushReconcileArgs) (PushReconcileReply, error) {
				calls++
				page := PushReconcileReply{
					Plan: "plan", State: "uncertain", wireBytes: 256,
					Observations:    []ReconcileObservation{{RemoteRef: fmt.Sprintf("refs/heads/%d", calls), Verdict: "equal_old"}},
					requestedBounds: reconcileBounds{total: uint64(pages), explicit: true, present: true},
				}
				if calls < pages {
					page.Next = &PushReconcileCursor{Session: "session", Request: "request", Page: calls}
				}
				if calls > 1 && (args.Cursor == nil || args.Cursor.Page != calls-1) {
					t.Fatalf("page cursor = %+v at call %d", args.Cursor, calls)
				}
				return page, nil
			})
			if calls != maxReconcilePages {
				t.Fatalf("page budget stopped at call %d, want %d", calls, maxReconcilePages)
			}
			if pages == maxReconcilePages {
				if err != nil || len(reply.Observations) != int(pages) {
					t.Fatalf("exact page budget refused: %+v, %v", reply, err)
				}
			} else if _, ok := errors.AsType[*IncompleteReconciliationError](err); !ok || len(reply.Observations) != 0 {
				t.Fatalf("page overflow returned success or partial data: %+v, %v", reply, err)
			}
		})
	}
}
