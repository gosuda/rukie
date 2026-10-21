package review

import (
	"strconv"
	"strings"
)

// SpansFromPatch derives unchanged spans per path from unified diff patches.
// Each patch maps a file path to its unified diff text. Context lines map
// old to new one to one; added and removed lines break spans. Lines between
// hunks are unchanged by definition and join the spans. Renamed paths map
// under their new name only; the old name keeps no spans, so comments
// anchored there fall to the fallback bucket by definition.
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
	var spans []Span
	var oldCursor, newCursor int64 = 1, 1
	var spanOld, spanNew int64
	open := false
	closeSpan := func() {
		if open && oldCursor > spanOld {
			oldStart, okOld := toUint32(spanOld)
			newStart, okNew := toUint32(spanNew)
			length, okLen := toUint32(oldCursor - spanOld)
			if okOld && okNew && okLen {
				spans = append(spans, Span{OldStart: oldStart, NewStart: newStart, Lines: length})
			}
		}
		open = false
	}
	for line := range strings.SplitSeq(patch, "\n") {
		if rest, ok := strings.CutPrefix(line, "@@ -"); ok {
			oldPart, newPart, ok := strings.Cut(rest, " +")
			if !ok {
				continue
			}
			if head, _, found := strings.Cut(newPart, " @@"); found {
				newPart = head
			}
			oldStart, oldCount := parseHunkRange(oldPart)
			newStart, newCount := parseHunkRange(newPart)
			if oldStart > oldCursor || newStart > newCursor {
				closeSpan()
				length := oldStart - oldCursor
				if newStart-newCursor < length {
					length = newStart - newCursor
				}
				gapOld, okOld := toUint32(oldCursor)
				gapNew, okNew := toUint32(newCursor)
				gapLen, okLen := toUint32(length)
				if gapLen > 0 && okOld && okNew && okLen {
					spans = append(spans, Span{OldStart: gapOld, NewStart: gapNew, Lines: gapLen})
				}
			}
			oldCursor = oldStart
			newCursor = newStart
			if oldCount == 0 {
				oldCursor++
			}
			if newCount == 0 {
				newCursor++
			}
			continue
		}
		if line == "" {
			closeSpan()
			continue
		}
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			continue
		}
		switch line[0] {
		case ' ':
			if !open {
				spanOld, spanNew = oldCursor, newCursor
				open = true
			}
			oldCursor++
			newCursor++
		case '-':
			closeSpan()
			oldCursor++
		case '+':
			closeSpan()
			newCursor++
		default:
			closeSpan()
		}
	}
	closeSpan()
	return spans
}

// toUint32 converts a line number with an overflow guard.
func toUint32(v int64) (uint32, bool) {
	if v < 0 || v > int64(^uint32(0)) {
		return 0, false
	}
	return uint32(v), true
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
	if start < 0 || count < 0 {
		return 0, 0
	}
	return start, count
}
