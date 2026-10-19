package vm

import (
	"errors"
	"strconv"
	"strings"
)

var ErrLinesRange = errors.New("vm: invalid ?lines= range")

type Problem struct {
	Type     string
	Title    string
	Detail   string
	TraceID  string
	Recovery Recovery
	Status   int
}

type Recovery struct {
	Next        string
	CurrentETag string
	Holder      string
	ExpiresAt   string
	MergeBase   string
	Docs        string
	BackoffMs   int64
}

type OpEntry struct {
	Name    string
	Method  string
	Path    string
	Summary string
}

type APIIndex struct {
	Ops   []OpEntry
	Count int
}

func NewAPIIndex(ops []OpEntry) APIIndex {
	return APIIndex{Ops: ops, Count: len(ops)}
}

type LineRange struct {
	Start int
	End   int
}

func ParseLines(raw string) (LineRange, error) {
	var r LineRange
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "L"))
	if raw == "" {
		return r, ErrLinesRange
	}
	parts := strings.SplitN(raw, "-", 2)
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || start < 1 {
		return r, ErrLinesRange
	}
	r.Start = start
	r.End = start
	if len(parts) == 1 {
		return r, nil
	}
	end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || end < start {
		return r, ErrLinesRange
	}
	r.End = end
	return r, nil
}

func (r LineRange) Contains(n int) bool {
	return n >= r.Start && n <= r.End
}
