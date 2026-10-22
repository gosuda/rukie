package ruk

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

// RemoteRefsArgs asks for the advertised refs of one explicit remote URL.
type RemoteRefsArgs struct {
	URL string `json:"url"`
}

// RemoteRefsReply carries the advertised refs; refs is a plain array.
type RemoteRefsReply struct {
	Refs []RefEntry `json:"refs"`
	View ViewInfo   `json:"view"`
}

// RemoteRefs reads the advertised refs of an explicit remote URL.
func RemoteRefs(ctx context.Context, c *Client, repo Repo, args RemoteRefsArgs, auth wire.TransportAuth) (RemoteRefsReply, error) {
	return CallAuth[RemoteRefsArgs, RemoteRefsReply](ctx, c, repo, wire.MethodRemoteRefs, args, auth)
}

// PushSource is what a push update sends: a commit oid, or a deletion.
type PushSource struct {
	Commit *string
}

// DeleteSource builds a deletion push source.
func DeleteSource() PushSource {
	return PushSource{}
}

// CommitSource builds a commit push source for one oid.
func CommitSource(oid string) PushSource {
	dup := oid
	return PushSource{Commit: &dup}
}

// IsDelete reports a deletion source.
func (s PushSource) IsDelete() bool {
	return s.Commit == nil
}

// MarshalJSON writes "delete" or {"commit":{"oid":...}}.
func (s PushSource) MarshalJSON() ([]byte, error) {
	if s.Commit == nil {
		return []byte(`"delete"`), nil
	}
	var wire struct {
		Commit struct {
			OID string `json:"oid"`
		} `json:"commit"`
	}
	wire.Commit.OID = *s.Commit
	return jsonv2.Marshal(wire)
}

// UnmarshalJSON reads "delete" or {"commit":{"oid":...}}.
func (s *PushSource) UnmarshalJSON(data []byte) error {
	var tag string
	if err := jsonv2.Unmarshal(data, &tag, jsonv1.DefaultOptionsV1()); err == nil {
		if tag != "delete" {
			return fmt.Errorf("PushSource: unknown %q", tag)
		}
		s.Commit = nil
		return nil
	}
	var wire struct {
		Commit *struct {
			OID *string `json:"oid"`
		} `json:"commit"`
	}
	if err := jsonv2.Unmarshal(data, &wire, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("PushSource: %w", err)
	}
	if wire.Commit == nil || wire.Commit.OID == nil {
		return fmt.Errorf("PushSource: missing commit oid")
	}
	oid := *wire.Commit.OID
	s.Commit = &oid
	return nil
}

// RemoteExpect is the remote value a push update is conditional on:
// absent, or present at one oid.
type RemoteExpect struct {
	Present *string
}

// AbsentExpect builds an absent expectation.
func AbsentExpect() RemoteExpect {
	return RemoteExpect{}
}

// PresentExpect builds a present-at-oid expectation.
func PresentExpect(oid string) RemoteExpect {
	dup := oid
	return RemoteExpect{Present: &dup}
}

// IsAbsent reports an absent expectation.
func (e RemoteExpect) IsAbsent() bool {
	return e.Present == nil
}

// MarshalJSON writes "absent" or {"present":{"oid":...}}.
func (e RemoteExpect) MarshalJSON() ([]byte, error) {
	if e.Present == nil {
		return []byte(`"absent"`), nil
	}
	var wire struct {
		Present struct {
			OID string `json:"oid"`
		} `json:"present"`
	}
	wire.Present.OID = *e.Present
	return jsonv2.Marshal(wire)
}

// UnmarshalJSON reads "absent" or {"present":{"oid":...}}.
func (e *RemoteExpect) UnmarshalJSON(data []byte) error {
	var tag string
	if err := jsonv2.Unmarshal(data, &tag, jsonv1.DefaultOptionsV1()); err == nil {
		if tag != "absent" {
			return fmt.Errorf("RemoteExpect: unknown %q", tag)
		}
		e.Present = nil
		return nil
	}
	var wire struct {
		Present *struct {
			OID *string `json:"oid"`
		} `json:"present"`
	}
	if err := jsonv2.Unmarshal(data, &wire, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("RemoteExpect: %w", err)
	}
	if wire.Present == nil || wire.Present.OID == nil {
		return fmt.Errorf("RemoteExpect: missing present oid")
	}
	oid := *wire.Present.OID
	e.Present = &oid
	return nil
}

// PushUpdate is one remote ref update. A nil ExpectedOld serializes as
// null, meaning the remote's advertised old value.
type PushUpdate struct {
	RemoteRef   string        `json:"remote_ref"`
	Source      PushSource    `json:"source"`
	ExpectedOld *RemoteExpect `json:"expected_old"`
	Force       bool          `json:"force"`
}

// PushArgs pushes updates to one explicit remote URL. It marshals as
// {"remote":{"url":{"url":...}},"updates":[...],"atomicity":"non_atomic"}.
type PushArgs struct {
	RemoteURL string
	Atomicity string
	Updates   []PushUpdate
}

// MarshalJSON writes the daemon push args shape.
func (a PushArgs) MarshalJSON() ([]byte, error) {
	atomicity := cmp.Or(a.Atomicity, "non_atomic")
	updates := a.Updates
	if updates == nil {
		updates = []PushUpdate{}
	}
	var wire struct {
		Remote struct {
			URL struct {
				URL string `json:"url"`
			} `json:"url"`
		} `json:"remote"`
		Updates   []PushUpdate `json:"updates"`
		Atomicity string       `json:"atomicity"`
	}
	wire.Remote.URL.URL = a.RemoteURL
	wire.Updates = updates
	wire.Atomicity = atomicity
	return jsonv2.Marshal(wire)
}

// PushRefStatus is the outcome of one pushed ref: uncertain, confirmed at
// an oid (nil when a delete confirmed), or rejected with a reason.
type PushRefStatus struct {
	Kind   string
	New    *string
	Reason string
}

// UncertainStatus builds an uncertain status.
func UncertainStatus() PushRefStatus {
	return PushRefStatus{Kind: "uncertain"}
}

// ConfirmedStatus builds a confirmed status; a nil oid confirms a delete.
func ConfirmedStatus(oid *string) PushRefStatus {
	return PushRefStatus{Kind: "confirmed", New: oid}
}

// RejectedStatus builds a rejected status.
func RejectedStatus(reason string) PushRefStatus {
	return PushRefStatus{Kind: "rejected", Reason: reason}
}

// MarshalJSON writes "uncertain", {"confirmed":{"new":...}}, or
// {"rejected":{"reason":...}}.
func (s PushRefStatus) MarshalJSON() ([]byte, error) {
	switch s.Kind {
	case "uncertain":
		return []byte(`"uncertain"`), nil
	case "confirmed":
		var wire struct {
			Confirmed struct {
				New *string `json:"new"`
			} `json:"confirmed"`
		}
		wire.Confirmed.New = s.New
		return jsonv2.Marshal(wire)
	case "rejected":
		var wire struct {
			Rejected struct {
				Reason string `json:"reason"`
			} `json:"rejected"`
		}
		wire.Rejected.Reason = s.Reason
		return jsonv2.Marshal(wire)
	default:
		return nil, fmt.Errorf("PushRefStatus: unknown kind %q", s.Kind)
	}
}

// UnmarshalJSON reads "uncertain", {"confirmed":{"new":...}}, or
// {"rejected":{"reason":...}}.
func (s *PushRefStatus) UnmarshalJSON(data []byte) error {
	var tag string
	if err := jsonv2.Unmarshal(data, &tag, jsonv1.DefaultOptionsV1()); err == nil {
		if tag != "uncertain" {
			return fmt.Errorf("PushRefStatus: unknown %q", tag)
		}
		*s = PushRefStatus{Kind: "uncertain"}
		return nil
	}
	var wire struct {
		Confirmed *struct {
			New *string `json:"new"`
		} `json:"confirmed"`
		Rejected *struct {
			Reason *string `json:"reason"`
		} `json:"rejected"`
	}
	if err := jsonv2.Unmarshal(data, &wire, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("PushRefStatus: %w", err)
	}
	switch {
	case wire.Confirmed != nil:
		*s = PushRefStatus{Kind: "confirmed", New: wire.Confirmed.New}
		return nil
	case wire.Rejected != nil:
		if wire.Rejected.Reason == nil {
			return fmt.Errorf("PushRefStatus: rejected without reason")
		}
		*s = PushRefStatus{Kind: "rejected", Reason: *wire.Rejected.Reason}
		return nil
	default:
		return fmt.Errorf("PushRefStatus: unknown status")
	}
}

// PushRefOutcome is the outcome of one pushed ref.
type PushRefOutcome struct {
	RemoteRef string        `json:"remote_ref"`
	Status    PushRefStatus `json:"status"`
}

// PushReply answers push: the durable plan, its state, and per-ref
// outcomes. The mutation outcome header rides the receipt and is ignored.
type PushReply struct {
	Plan  string           `json:"plan"`
	State string           `json:"state"`
	Refs  []PushRefOutcome `json:"-"`
}

// UnmarshalJSON reads a push reply, flattening the refs BoundedList
// envelope {items, total, truncated}.
func (r *PushReply) UnmarshalJSON(data []byte) error {
	var raw struct {
		Plan  string            `json:"plan"`
		State string            `json:"state"`
		Refs  jsonv1.RawMessage `json:"refs"`
	}
	if err := jsonv2.Unmarshal(data, &raw, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("PushReply: %w", err)
	}
	if len(raw.Refs) == 0 {
		return fmt.Errorf("PushReply: missing refs")
	}
	var list boundedList[PushRefOutcome]
	if err := jsonv2.Unmarshal(raw.Refs, &list, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("PushReply: %w", err)
	}
	r.Plan = raw.Plan
	r.State = raw.State
	r.Refs = list.Items
	return nil
}

// PushReconcileArgs observes one stored plan's remote refs.
type PushReconcileArgs struct {
	Plan string `json:"plan"`
}

// ReconcileObservation is what observation found for one ref; a nil
// Observed means the ref does not exist remotely.
type ReconcileObservation struct {
	Observed  *string `json:"observed"`
	RemoteRef string  `json:"remote_ref"`
	Verdict   string  `json:"verdict"`
}

// PushReconcileReply answers push.reconcile: the plan, its state, and one
// observation per ref.
type PushReconcileReply struct {
	Plan         string                 `json:"plan"`
	State        string                 `json:"state"`
	Observations []ReconcileObservation `json:"-"`
}

// UnmarshalJSON reads a reconcile reply, flattening the observations
// BoundedList envelope {items, total, truncated}.
func (r *PushReconcileReply) UnmarshalJSON(data []byte) error {
	var raw struct {
		Plan         string            `json:"plan"`
		State        string            `json:"state"`
		Observations jsonv1.RawMessage `json:"observations"`
	}
	if err := jsonv2.Unmarshal(data, &raw, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("PushReconcileReply: %w", err)
	}
	if len(raw.Observations) == 0 {
		return fmt.Errorf("PushReconcileReply: missing observations")
	}
	var list boundedList[ReconcileObservation]
	if err := jsonv2.Unmarshal(raw.Observations, &list, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("PushReconcileReply: %w", err)
	}
	r.Plan = raw.Plan
	r.State = raw.State
	r.Observations = list.Items
	return nil
}

// Push pushes updates to one explicit remote URL without touching local refs.
func Push(ctx context.Context, c *Client, repo Repo, args PushArgs, auth wire.TransportAuth) (PushReply, error) {
	return CallAuth[PushArgs, PushReply](ctx, c, repo, wire.MethodPush, args, auth)
}

// PushReconcile observes the remote's current refs for an uncertain plan.
func PushReconcile(ctx context.Context, c *Client, repo Repo, args PushReconcileArgs, auth wire.TransportAuth) (PushReconcileReply, error) {
	return CallAuth[PushReconcileArgs, PushReconcileReply](ctx, c, repo, wire.MethodPushReconcile, args, auth)
}

type boundedList[T any] struct {
	Items []T `json:"items"`
}

// HTTPDetail carries the facts behind one HTTP refusal: the status, the
// Retry-After delay, the redirect target, and a scrubbed body preview.
type HTTPDetail struct {
	RetryAfterSeconds *uint64 `json:"retry_after_seconds"`
	Location          *string `json:"location"`
	BodyPreview       string  `json:"body_preview"`
	Status            uint16  `json:"status"`
}

// HTTPDetailOf extracts the {"http":{...}} detail envelope from a call
// error, reporting false when the error carries no HTTP detail.
func HTTPDetailOf(err error) (HTTPDetail, bool) {
	callErr, ok := errors.AsType[*Error](err)
	if !ok || callErr == nil {
		return HTTPDetail{}, false
	}
	if len(callErr.Detail) == 0 {
		return HTTPDetail{}, false
	}
	var env struct {
		HTTP *HTTPDetail `json:"http"`
	}
	if err := jsonv2.Unmarshal(callErr.Detail, &env, jsonv1.DefaultOptionsV1()); err != nil {
		return HTTPDetail{}, false
	}
	if env.HTTP == nil {
		return HTTPDetail{}, false
	}
	return *env.HTTP, true
}
