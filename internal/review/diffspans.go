package review

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// SpansFromPatch derives unchanged spans from complete unified file patches.
// Context, inter-hunk gaps, and the final tail map old lines to new lines.
// An empty text patch preserves every line. Malformed patches return an error.
func SpansFromPatch(patches map[string]string) (map[string][]Span, error) {
	out := make(map[string][]Span, len(patches))
	for path, patch := range patches {
		spans, err := spansFromUnified(path, patch)
		if err != nil {
			return nil, fmt.Errorf("SpansFromPatch: %s: %w", path, err)
		}
		out[path] = spans
	}
	return out, nil
}

func spansFromUnified(path, patch string) ([]Span, error) {
	oldHeader := "--- a/" + path + "\n"
	newHeader := "+++ b/" + path + "\n"
	fileHeaders := true
	var added, deleted bool
	switch {
	case strings.HasPrefix(patch, oldHeader+newHeader):
		patch = patch[len(oldHeader)+len(newHeader):]
	case strings.HasPrefix(patch, "--- /dev/null\n"+newHeader):
		patch = patch[len("--- /dev/null\n")+len(newHeader):]
		added = true
	case strings.HasPrefix(patch, oldHeader+"+++ /dev/null\n"):
		patch = patch[len(oldHeader)+len("+++ /dev/null\n"):]
		deleted = true
	case patch == "" || strings.HasPrefix(patch, "@@ -"):
		fileHeaders = false
	default:
		return nil, fmt.Errorf("spansFromUnified: invalid file headers")
	}
	var spans []Span
	var oldCursor, newCursor int64 = 1, 1
	var oldRemaining, newRemaining int64
	var spanOld, spanNew int64
	var sawHunk, open, legacyEmpty bool
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
			if oldRemaining != 0 || newRemaining != 0 {
				return nil, fmt.Errorf("spansFromUnified: incomplete hunk")
			}
			if legacyEmpty {
				return nil, fmt.Errorf("spansFromUnified: legacy empty-file patch has multiple hunks")
			}
			oldPart, newPart, separated := strings.Cut(rest, " +")
			newPart, suffix, terminated := strings.Cut(newPart, " @@")
			if !separated || !terminated {
				return nil, fmt.Errorf("spansFromUnified: malformed hunk header")
			}
			oldStart, oldCount, oldOK := parseHunkRange(oldPart)
			newStart, newCount, newOK := parseHunkRange(newPart)
			if fileHeaders && !sawHunk && oldOK && newOK && suffix == "" {
				switch {
				case !deleted && oldPart == "1,0" && newStart == 1 && newCount > 0 && newPart == "1,"+strconv.FormatInt(newCount, 10):
					oldStart = 1
					legacyEmpty = true
				case !added && newPart == "1,0" && oldStart == 1 && oldCount > 0 && oldPart == "1,"+strconv.FormatInt(oldCount, 10):
					newStart = 1
					legacyEmpty = true
				}
			}
			if !oldOK || !newOK || oldStart < oldCursor || newStart < newCursor || oldStart-oldCursor != newStart-newCursor {
				return nil, fmt.Errorf("spansFromUnified: inconsistent hunk ranges")
			}
			closeSpan()
			if oldStart > oldCursor {
				gapOld, okOld := toUint32(oldCursor)
				gapNew, okNew := toUint32(newCursor)
				gapLen, okLen := toUint32(oldStart - oldCursor)
				if okOld && okNew && okLen {
					spans = append(spans, Span{OldStart: gapOld, NewStart: gapNew, Lines: gapLen})
				}
			}
			oldCursor, newCursor = oldStart, newStart
			oldRemaining, newRemaining = oldCount, newCount
			sawHunk = true
			continue
		}
		if line == "" {
			if oldRemaining != 0 || newRemaining != 0 {
				return nil, fmt.Errorf("spansFromUnified: truncated hunk")
			}
			continue
		}
		if !sawHunk {
			return nil, fmt.Errorf("spansFromUnified: unexpected patch header")
		}
		switch line[0] {
		case ' ':
			if oldRemaining == 0 || newRemaining == 0 {
				return nil, fmt.Errorf("spansFromUnified: context exceeds hunk")
			}
			if !open {
				spanOld, spanNew = oldCursor, newCursor
				open = true
			}
			oldCursor++
			newCursor++
			oldRemaining--
			newRemaining--
		case '-':
			if oldRemaining == 0 {
				return nil, fmt.Errorf("spansFromUnified: removal exceeds hunk")
			}
			closeSpan()
			oldCursor++
			oldRemaining--
		case '+':
			if newRemaining == 0 {
				return nil, fmt.Errorf("spansFromUnified: addition exceeds hunk")
			}
			closeSpan()
			newCursor++
			newRemaining--
		default:
			if line != "\\ No newline at end of file" {
				return nil, fmt.Errorf("spansFromUnified: unexpected hunk line")
			}
		}
	}
	if oldRemaining != 0 || newRemaining != 0 {
		return nil, fmt.Errorf("spansFromUnified: incomplete patch")
	}
	closeSpan()
	if added || deleted || legacyEmpty {
		return nil, nil
	}
	tail := min(int64(math.MaxUint32)+1-oldCursor, int64(math.MaxUint32)+1-newCursor)
	tailOld, okOld := toUint32(oldCursor)
	tailNew, okNew := toUint32(newCursor)
	tailLen, okLen := toUint32(tail)
	if tail > 0 && okOld && okNew && okLen {
		spans = append(spans, Span{OldStart: tailOld, NewStart: tailNew, Lines: tailLen})
	}
	return spans, nil
}

func toUint32(v int64) (uint32, bool) {
	if v < 0 || v > math.MaxUint32 {
		return 0, false
	}
	return uint32(v), true
}

func parseHunkRange(raw string) (int64, int64, bool) {
	startPart, countPart, found := strings.Cut(raw, ",")
	start, err := strconv.ParseInt(strings.TrimSpace(startPart), 10, 64)
	if err != nil || start < 0 || start > math.MaxUint32 {
		return 0, 0, false
	}
	count := int64(1)
	if found {
		count, err = strconv.ParseInt(strings.TrimSpace(countPart), 10, 64)
		if err != nil || count < 0 || count > math.MaxUint32 {
			return 0, 0, false
		}
	}
	if count == 0 {
		start++
	} else if start == 0 {
		return 0, 0, false
	}
	if start+count > int64(math.MaxUint32)+1 {
		return 0, 0, false
	}
	return start, count, true
}
