package review

import (
	"strconv"
	"strings"
)

// SpansFromPatch derives unchanged spans per path from unified diff patches.
// Each patch maps a file path to its unified diff text. Changed ranges come
// from hunk headers; spans cover the gaps before, between, and (never
// past the last hunk, whose tail length is unknown) after them. Renamed
// paths map under their new name only; the old name keeps no spans, so
// comments anchored there fall to the fallback bucket by definition.
func SpansFromPatch(patches map[string]string) map[string][]Span {
	out := map[string][]Span{}
	for path, patch := range patches {
		spans := spansFromUnified(patch)
		if len(spans) > 0 {
			out[path] = spans
		}
	}
	return out
}

func spansFromUnified(patch string) []Span {
	type hunk struct {
		oldStart, oldCount, newStart, newCount int64
	}
	var hunks []hunk
	for line := range strings.SplitSeq(patch, "\n") {
		rest, ok := strings.CutPrefix(line, "@@ -")
		if !ok {
			continue
		}
		oldPart, newPart, ok := strings.Cut(rest, " +")
		if !ok {
			continue
		}
		if head, _, found := strings.Cut(newPart, " @@"); found {
			newPart = head
		}
		oldStart, oldCount := parseHunkRange(oldPart)
		newStart, newCount := parseHunkRange(newPart)
		if oldStart <= 0 || newStart <= 0 {
			continue
		}
		hunks = append(hunks, hunk{oldStart, oldCount, newStart, newCount})
	}
	var spans []Span
	var oldCursor, newCursor int64 = 1, 1
	for _, h := range hunks {
		if h.oldStart > oldCursor {
			spans = append(spans, Span{
				OldStart: uint32(oldCursor),
				NewStart: uint32(newCursor),
				Lines:    uint32(h.oldStart - oldCursor),
			})
		}
		oldCursor = h.oldStart + h.oldCount
		newCursor = h.newStart + h.newCount
	}
	return spans
}

func parseHunkRange(raw string) (int64, int64) {
	startPart, countPart, found := strings.Cut(raw, ",")
	start, _ := strconv.ParseInt(strings.TrimSpace(startPart), 10, 64)
	if !found {
		if start <= 0 {
			return 0, 0
		}
		return start, 1
	}
	count, _ := strconv.ParseInt(strings.TrimSpace(countPart), 10, 64)
	if start <= 0 || count < 0 {
		return 0, 0
	}
	return start, count
}
