package review

// Span is one unchanged span shared by the old and new heads of a file.
// Only lines inside spans remap; everything else falls to the fallback
// bucket. Spans never overlap and stay sorted by OldStart.
type Span struct {
	OldStart uint32
	NewStart uint32
	Lines    uint32
}

// RemapLine maps an old 1-based line to the identical new line through
// unchanged spans only. A line outside every span, on a span boundary
// overflow, or at line zero never maps.
func RemapLine(line uint32, spans []Span) (uint32, bool) {
	if line == 0 {
		return 0, false
	}
	for _, span := range spans {
		if span.Lines == 0 {
			continue
		}
		if line < span.OldStart {
			return 0, false
		}
		offset := line - span.OldStart
		if offset < span.Lines {
			return span.NewStart + offset, true
		}
	}
	return 0, false
}
