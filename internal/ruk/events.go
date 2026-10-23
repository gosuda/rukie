package ruk

import (
	"context"
	"fmt"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

const (
	MaxTailLimit  = 1000
	MaxTailWaitMs = 30000
	MaxRefsLimit  = 1000
)

type EventsTailArgs struct {
	Prefix string
	After  uint64
	Limit  int
	WaitMs uint64
}

func (a EventsTailArgs) Validate() error {
	if a.Limit < 1 || a.Limit > MaxTailLimit {
		return fmt.Errorf("EventsTail: limit %d outside 1 to %d", a.Limit, MaxTailLimit)
	}
	if a.WaitMs > MaxTailWaitMs {
		return fmt.Errorf("EventsTail: wait_ms %d above %d", a.WaitMs, MaxTailWaitMs)
	}
	return nil
}

func (a EventsTailArgs) marshal() map[string]any {
	out := map[string]any{
		"after":   a.After,
		"limit":   a.Limit,
		"wait_ms": a.WaitMs,
	}
	if a.Prefix != "" {
		out["prefix"] = a.Prefix
	}
	return out
}

type RefTarget struct {
	Direct   *string `json:"direct,omitempty"`
	Symbolic *string `json:"symbolic,omitempty"`
}

func DirectTarget(hex string) RefTarget {
	direct := hex
	return RefTarget{Direct: &direct}
}

func (t RefTarget) Hex() string {
	if t.Direct != nil {
		return *t.Direct
	}
	if t.Symbolic != nil {
		return *t.Symbolic
	}
	return ""
}

func (t RefTarget) Null() bool {
	return t.Direct == nil && t.Symbolic == nil
}

type RefEvent struct {
	Old       RefTarget `json:"old"`
	New       RefTarget `json:"new"`
	Operation *uint64   `json:"operation,omitempty"`
	RequestID *string   `json:"request_id,omitempty"`
	Session   *string   `json:"session,omitempty"`
	Name      string    `json:"name"`
	Source    string    `json:"source"`
	At        uint64    `json:"at"`
	Seq       uint64    `json:"seq"`
}

type ViewInfo struct {
	Store string `json:"store"`
	View  uint64 `json:"view"`
}

type EventsTailReply struct {
	View    ViewInfo   `json:"view"`
	Events  []RefEvent `json:"events"`
	Head    uint64     `json:"head"`
	Oldest  uint64     `json:"oldest"`
	Expired bool       `json:"expired"`
}

func EventsTail(ctx context.Context, c *Client, repo Repo, args EventsTailArgs) (EventsTailReply, error) {
	var zero EventsTailReply
	if err := args.Validate(); err != nil {
		return zero, err
	}
	return Call[map[string]any, EventsTailReply](ctx, c, repo, wire.MethodEventsTail, args.marshal())
}

type RefsListArgs struct {
	After  *string
	Prefix string
	Limit  int
}

func (a RefsListArgs) Validate() error {
	if a.Limit < 0 || a.Limit > MaxRefsLimit {
		return fmt.Errorf("RefsList: limit %d outside 0 to %d", a.Limit, MaxRefsLimit)
	}
	return nil
}

func (a RefsListArgs) marshal() map[string]any {
	out := map[string]any{}
	if a.Prefix != "" {
		out["prefix"] = a.Prefix
	}
	if a.After != nil {
		out["after"] = *a.After
	}
	if a.Limit != 0 {
		out["limit"] = a.Limit
	}
	return out
}

type RefEntry struct {
	Target RefTarget `json:"target"`
	Name   string    `json:"name"`
}

type RefPage struct {
	NextAfter *string    `json:"next_after"`
	Items     []RefEntry `json:"items"`
}

type RefsListReply struct {
	Refs RefPage  `json:"refs"`
	View ViewInfo `json:"view"`
	AsOf uint64   `json:"as_of"`
}

func RefsList(ctx context.Context, c *Client, repo Repo, args RefsListArgs) (RefsListReply, error) {
	var zero RefsListReply
	if err := args.Validate(); err != nil {
		return zero, err
	}
	return Call[map[string]any, RefsListReply](ctx, c, repo, wire.MethodRefsList, args.marshal())
}
