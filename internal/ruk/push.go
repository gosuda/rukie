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

// PushReconcileCursor identifies the next page of one durable observation snapshot.
type PushReconcileCursor struct {
	Session string `json:"session"`
	Request string `json:"request"`
	Page    uint32 `json:"page"`
}

// OriginalSSHIdentity records an operator-verified historical SSH destination.
type OriginalSSHIdentity struct {
	User string `json:"user"`
	Port uint16 `json:"port"`
}

// PushReconcileArgs observes one stored plan's remote refs.
type PushReconcileArgs struct {
	OriginalRemote *string              `json:"original_remote,omitempty"`
	OriginalSSH    *OriginalSSHIdentity `json:"original_ssh,omitempty"`
	Cursor         *PushReconcileCursor `json:"cursor,omitempty"`
	Plan           string               `json:"plan"`
}

// ReconcileObservation is what observation found for one ref; a nil
// Observed means the ref does not exist remotely.
type ReconcileObservation struct {
	Observed  *string `json:"observed"`
	RemoteRef string  `json:"remote_ref"`
	Verdict   string  `json:"verdict"`
}

// ReportedReconcileObservation compares one reported update with its observed tip.
type ReportedReconcileObservation struct {
	Old       *string `json:"old"`
	New       *string `json:"new"`
	Observed  *string `json:"observed"`
	RemoteRef string  `json:"remote_ref"`
	Verdict   string  `json:"verdict"`
}

// PushReconcileReply carries requested observations and reported comparisons.
// PushReconcile returns a complete snapshot with no continuation cursor.
type PushReconcileReply struct {
	Next                 *PushReconcileCursor           `json:"next,omitempty"`
	Plan                 string                         `json:"plan"`
	State                string                         `json:"state"`
	Observations         []ReconcileObservation         `json:"-"`
	ReportedObservations []ReportedReconcileObservation `json:"-"`
	wireBytes            uint64
	requestedBounds      reconcileBounds
	reportedBounds       reconcileBounds
	Rewritten            bool `json:"rewritten"`
}

// UnmarshalJSON reads one page without discarding observation bounds.
func (r *PushReconcileReply) UnmarshalJSON(data []byte) error {
	var raw struct {
		Next                 *PushReconcileCursor `json:"next"`
		Plan                 string               `json:"plan"`
		State                string               `json:"state"`
		Observations         jsonv1.RawMessage    `json:"observations"`
		ReportedObservations jsonv1.RawMessage    `json:"reported_observations"`
		Rewritten            jsonv1.RawMessage    `json:"rewritten"`
	}
	if err := jsonv2.Unmarshal(data, &raw, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("PushReconcileReply: %w", err)
	}
	rewritten, err := decodeReconcileFlag(raw.Rewritten, "rewritten")
	if err != nil {
		return fmt.Errorf("PushReconcileReply: %w", err)
	}
	if len(raw.Observations) == 0 {
		return fmt.Errorf("PushReconcileReply: missing observations")
	}
	observations, requestedBounds, err := decodeReconcileList[ReconcileObservation](raw.Observations)
	if err != nil {
		return fmt.Errorf("PushReconcileReply: %w", err)
	}
	var reported []ReportedReconcileObservation
	var reportedBounds reconcileBounds
	if len(raw.ReportedObservations) != 0 {
		reported, reportedBounds, err = decodeReconcileList[ReportedReconcileObservation](raw.ReportedObservations)
		if err != nil {
			return fmt.Errorf("PushReconcileReply: %w", err)
		}
	}
	*r = PushReconcileReply{
		Next:                 raw.Next,
		Plan:                 raw.Plan,
		State:                raw.State,
		Observations:         observations,
		ReportedObservations: reported,
		wireBytes:            uint64(len(data)),
		requestedBounds:      requestedBounds,
		reportedBounds:       reportedBounds,
		Rewritten:            rewritten,
	}
	return nil
}

// Push pushes updates to one explicit remote URL without touching local refs.
func Push(ctx context.Context, c *Client, repo Repo, args PushArgs, auth wire.TransportAuth) (PushReply, error) {
	return CallAuth[PushArgs, PushReply](ctx, c, repo, wire.MethodPush, args, auth)
}

// PushReconcile collects every page of one durable observation snapshot.
// Args.Cursor must be nil; CallAuth remains available for individual pages.
// Transport failures return no partial snapshot. Invalid snapshots require
// operator attention through IncompleteReconciliationError.
func PushReconcile(ctx context.Context, c *Client, repo Repo, args PushReconcileArgs, auth wire.TransportAuth) (PushReconcileReply, error) {
	reply, err := collectPushReconcile(ctx, args, func(ctx context.Context, pageArgs PushReconcileArgs) (PushReconcileReply, error) {
		return CallAuth[PushReconcileArgs, PushReconcileReply](ctx, c, repo, wire.MethodPushReconcile, pageArgs, auth)
	})
	if err != nil {
		return PushReconcileReply{}, fmt.Errorf("PushReconcile: %w", err)
	}
	return reply, nil
}

type boundedList[T any] struct {
	Items []T `json:"items"`
}

// PushDetailOf returns a typed push failure with a nonempty plan identity.
func PushDetailOf(err error) (PushReply, bool) {
	callErr, ok := errors.AsType[*Error](err)
	if !ok || callErr == nil || len(callErr.Detail) == 0 {
		return PushReply{}, false
	}
	var env struct {
		Push *PushReply `json:"push"`
	}
	if err := jsonv2.Unmarshal(callErr.Detail, &env, jsonv1.DefaultOptionsV1()); err != nil {
		return PushReply{}, false
	}
	if env.Push == nil || env.Push.Plan == "" {
		return PushReply{}, false
	}
	return *env.Push, true
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
