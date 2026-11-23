package ruk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
)

const maxReconcileResponseBytes = 64 << 20
const maxReconcilePageItems = 1000
const maxReconcilePages = 1024

// IncompleteReconciliationError reports an invalid or incomplete observation
// snapshot. Automatic push recovery must stop until an operator resolves it.
type IncompleteReconciliationError struct {
	Reason string
}

func (e *IncompleteReconciliationError) Error() string {
	return "incomplete push reconciliation: " + e.Reason
}

func (e *IncompleteReconciliationError) Unwrap() error { return ErrProtocol }

type reconcileBounds struct {
	total    uint64
	explicit bool
	present  bool
}

func decodeReconcileFlag(raw jsonv1.RawMessage, field string) (bool, error) {
	switch string(bytes.TrimSpace(raw)) {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, fmt.Errorf("decodeReconcileFlag: %s must be a boolean", field)
	}
}

func decodeReconcileList[T any](data jsonv1.RawMessage) ([]T, reconcileBounds, error) {
	var raw struct {
		Items     jsonv1.RawMessage `json:"items"`
		Total     jsonv1.RawMessage `json:"total"`
		Truncated jsonv1.RawMessage `json:"truncated"`
	}
	if err := jsonv2.Unmarshal(data, &raw, jsonv1.DefaultOptionsV1()); err != nil {
		return nil, reconcileBounds{}, fmt.Errorf("decodeReconcileList: %w", err)
	}
	itemsJSON := bytes.TrimSpace(raw.Items)
	if len(itemsJSON) == 0 || itemsJSON[0] != '[' {
		return nil, reconcileBounds{}, fmt.Errorf("decodeReconcileList: items must be an array")
	}
	var items []T
	if err := jsonv2.Unmarshal(raw.Items, &items, jsonv1.DefaultOptionsV1()); err != nil {
		return nil, reconcileBounds{}, fmt.Errorf("decodeReconcileList: %w", err)
	}
	bounds := reconcileBounds{total: uint64(len(items)), explicit: len(raw.Total) != 0, present: true}
	if bounds.explicit {
		if bytes.Equal(bytes.TrimSpace(raw.Total), []byte("null")) {
			return nil, reconcileBounds{}, fmt.Errorf("decodeReconcileList: total must be an unsigned integer")
		}
		if err := jsonv2.Unmarshal(raw.Total, &bounds.total, jsonv1.DefaultOptionsV1()); err != nil {
			return nil, reconcileBounds{}, fmt.Errorf("decodeReconcileList: %w", err)
		}
	}
	truncated, err := decodeReconcileFlag(raw.Truncated, "truncated")
	if err != nil {
		return nil, reconcileBounds{}, fmt.Errorf("decodeReconcileList: %w", err)
	}
	if bounds.total < uint64(len(items)) || truncated != (bounds.total > uint64(len(items))) {
		return nil, reconcileBounds{}, fmt.Errorf("decodeReconcileList: inconsistent observation bounds")
	}
	return items, bounds, nil
}

func collectPushReconcile(
	ctx context.Context,
	args PushReconcileArgs,
	call func(context.Context, PushReconcileArgs) (PushReconcileReply, error),
) (PushReconcileReply, error) {
	incomplete := func(reason string) (PushReconcileReply, error) {
		return PushReconcileReply{}, fmt.Errorf("collectPushReconcile: %w", &IncompleteReconciliationError{Reason: reason})
	}
	if args.Cursor != nil {
		return incomplete("the complete-snapshot helper requires an initial request without a cursor")
	}
	var result PushReconcileReply
	var requestedTotal, reportedTotal uint64
	var wireBytes uint64
	var generation *PushReconcileCursor
	seen := make(map[string]struct{})
	first := true
	for {
		page, err := call(ctx, args)
		if err != nil {
			if errors.Is(err, ErrProtocol) {
				return incomplete(err.Error())
			}
			return PushReconcileReply{}, fmt.Errorf("collectPushReconcile: %w", err)
		}
		if page.wireBytes > maxReconcileResponseBytes-wireBytes {
			return incomplete("encoded observations exceed 64 MiB")
		}
		wireBytes += page.wireBytes
		if len(page.Observations)+len(page.ReportedObservations) > maxReconcilePageItems {
			return incomplete("one page exceeds 1000 observations")
		}
		if page.Plan != args.Plan {
			return incomplete("the reply identifies another push plan")
		}
		paged := args.Cursor != nil || page.Next != nil
		if paged && ((!page.requestedBounds.explicit && page.requestedBounds.present) ||
			(!page.reportedBounds.explicit && page.reportedBounds.present)) {
			return incomplete("a paginated observation list omits its total")
		}
		if page.reportedBounds.total > 0 && !page.Rewritten {
			return incomplete("reported updates omit the rewrite marker")
		}
		if first {
			result.Plan = page.Plan
			result.State = page.State
			result.Rewritten = page.Rewritten
			requestedTotal = page.requestedBounds.total
			reportedTotal = page.reportedBounds.total
			if requestedTotal > maxReconcileResponseBytes || reportedTotal > maxReconcileResponseBytes-requestedTotal {
				return incomplete("declared observations exceed the response bound")
			}
			first = false
		} else if page.State != result.State || page.Rewritten != result.Rewritten ||
			page.requestedBounds.total != requestedTotal || page.reportedBounds.total != reportedTotal {
			return incomplete("the observation snapshot changed between pages")
		}
		if uint64(len(page.Observations)) > requestedTotal-uint64(len(result.Observations)) ||
			uint64(len(page.ReportedObservations)) > reportedTotal-uint64(len(result.ReportedObservations)) {
			return incomplete("a page contains more observations than declared")
		}
		for _, observation := range page.Observations {
			if _, duplicate := seen[observation.RemoteRef]; duplicate {
				return incomplete("the requested observations repeat a ref")
			}
			seen[observation.RemoteRef] = struct{}{}
		}
		result.Observations = append(result.Observations, page.Observations...)
		result.ReportedObservations = append(result.ReportedObservations, page.ReportedObservations...)
		complete := uint64(len(result.Observations)) == requestedTotal &&
			uint64(len(result.ReportedObservations)) == reportedTotal
		if page.Next == nil {
			if !complete {
				return incomplete("the snapshot ends before all observations arrive")
			}
			result.wireBytes = wireBytes
			result.requestedBounds = reconcileBounds{total: requestedTotal, explicit: true, present: true}
			result.reportedBounds = reconcileBounds{total: reportedTotal, explicit: true, present: true}
			return result, nil
		}
		if complete {
			return incomplete("a continuation follows a complete snapshot")
		}
		if len(page.Observations)+len(page.ReportedObservations) == 0 {
			return incomplete("a continuation page makes no progress")
		}
		if page.Next.Session == "" || page.Next.Request == "" || page.Next.Page == 0 {
			return incomplete("the continuation cursor lacks its generation or page")
		}
		if page.Next.Page >= maxReconcilePages {
			return incomplete("the snapshot exceeds 1024 pages")
		}
		if generation == nil {
			if page.Next.Page != 1 {
				return incomplete("the first continuation page is not 1")
			}
			copyOfCursor := *page.Next
			generation = &copyOfCursor
		} else {
			if page.Next.Session != generation.Session || page.Next.Request != generation.Request {
				return incomplete("the continuation cursor changed generation")
			}
			if args.Cursor.Page == math.MaxUint32 || page.Next.Page != args.Cursor.Page+1 {
				return incomplete("the continuation cursor did not advance by one page")
			}
		}
		next := *page.Next
		args.Cursor = &next
		args.OriginalRemote = nil
		args.OriginalSSH = nil
	}
}
