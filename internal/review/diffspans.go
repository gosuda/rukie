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
			spans = append(spans, Span{
				OldStart: uint32(spanOld),
				NewStart: uint32(spanNew),
				Lines:    uint32(oldCursor - spanOld),
			})
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
			oldStart, _ := parseHunkRange(oldPart)
			newStart, _ := parseHunkRange(newPart)
			if oldStart > oldCursor || newStart > newCursor {
				closeSpan()
				length := oldStart - oldCursor
				if newStart-newCursor < length {
					length = newStart - newCursor
				}
				if length > 0 {
					spans = append(spans, Span{
						OldStart: uint32(oldCursor),
						NewStart: uint32(newCursor),
						Lines:    uint32(length),
					})
				}
			}
			oldCursor = oldStart
			newCursor = newStart
			continue
		}
		if line == "" {
			closeSpan()
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
