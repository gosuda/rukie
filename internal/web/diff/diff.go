package diff

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	diffpkg "github.com/sourcegraph/go-diff/diff"
)

const (
	MaxFilesPerPage = 25
	MaxFilePatch    = 1 << 18
	MaxHunksPerFile = 64
)

var ErrDiffCap = errors.New("diff: patch exceeds the render cap")

type Line struct {
	Text string
	Old  int
	New  int
	Kind byte
}

type Hunk struct {
	Section  string
	Lines    []Line
	OldStart int
	OldLines int
	NewStart int
	NewLines int
}

type File struct {
	Path   string
	Anchor string
	Hunks  []Hunk
	Binary bool
	Trunc  bool
}

func anchorFor(path string) string {
	sum := sha256.Sum256([]byte(path))
	return "f-" + hex.EncodeToString(sum[:])[:8]
}

func Parse(name string, patch []byte) (File, error) {
	f := File{Path: name, Anchor: anchorFor(name)}
	if len(patch) > MaxFilePatch {
		f.Trunc = true
		return f, nil
	}
	hunks, err := diffpkg.ParseHunks(patch)
	if err != nil {
		return f, err
	}
	for _, h := range hunks {
		if len(f.Hunks) >= MaxHunksPerFile {
			f.Trunc = true
			break
		}
		out := Hunk{
			OldStart: int(h.OrigStartLine),
			OldLines: int(h.OrigLines),
			NewStart: int(h.NewStartLine),
			NewLines: int(h.NewLines),
			Section:  h.Section,
		}
		old := int(h.OrigStartLine)
		newn := int(h.NewStartLine)
		for raw := range bytes.SplitSeq(h.Body, []byte{'\n'}) {
			if len(raw) == 0 || raw[0] == '\\' {
				continue
			}
			ln := Line{Kind: raw[0], Text: string(raw[1:])}
			switch raw[0] {
			case '-':
				ln.Old = old
				old++
			case '+':
				ln.New = newn
				newn++
			default:
				ln.Kind = ' '
				ln.Old = old
				ln.New = newn
				old++
				newn++
			}
			out.Lines = append(out.Lines, ln)
		}
		f.Hunks = append(f.Hunks, out)
	}
	return f, nil
}

func (f File) Title() string {
	return f.Path
}

type RichLine struct {
	Pre    string
	Mid    string
	Post   string
	Line   Line
	Paired bool
}

func commonPrefix(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

func commonSuffix(a, b string, prefix int) int {
	i := 0
	for i+prefix < len(a) && i+prefix < len(b) && a[len(a)-1-i] == b[len(b)-1-i] {
		i++
	}
	return i
}

func splitChanged(del, add string) (pre, midDel, post, midAdd string) {
	p := commonPrefix(del, add)
	s := commonSuffix(del, add, p)
	pre = del[:p]
	post = del[len(del)-s:]
	midDel = del[p : len(del)-s]
	midAdd = add[p : len(add)-s]
	if midDel == "" && midAdd == "" {
		return "", "", del, ""
	}
	return pre, midDel, post, midAdd
}

type SplitRow struct {
	Pre    string
	DelMid string
	AddMid string
	Post   string
	Del    Line
	Add    Line
	HasDel bool
	HasAdd bool
	Paired bool
}

func pairCount(dels, adds []Line) int {
	if len(dels) < len(adds) {
		return len(dels)
	}
	return len(adds)
}

func Rich(h Hunk) []RichLine {
	out := make([]RichLine, 0, len(h.Lines))
	var dels, adds []Line
	flush := func() {
		paired := pairCount(dels, adds)
		for i := range paired {
			pre, midDel, post, midAdd := splitChanged(dels[i].Text, adds[i].Text)
			out = append(out, RichLine{Line: dels[i], Pre: pre, Mid: midDel, Post: post, Paired: true})
			out = append(out, RichLine{Line: adds[i], Pre: pre, Mid: midAdd, Post: post, Paired: true})
		}
		for _, ln := range dels[paired:] {
			out = append(out, RichLine{Line: ln})
		}
		for _, ln := range adds[paired:] {
			out = append(out, RichLine{Line: ln})
		}
		dels = nil
		adds = nil
	}
	for _, ln := range h.Lines {
		switch ln.Kind {
		case '-':
			dels = append(dels, ln)
		case '+':
			adds = append(adds, ln)
		default:
			flush()
			out = append(out, RichLine{Line: ln})
		}
	}
	flush()
	return out
}

func Split(h Hunk) []SplitRow {
	var out []SplitRow
	var dels, adds []Line
	flush := func() {
		paired := pairCount(dels, adds)
		for i := range paired {
			pre, midDel, post, midAdd := splitChanged(dels[i].Text, adds[i].Text)
			out = append(out, SplitRow{
				Del: dels[i], Add: adds[i], HasDel: true, HasAdd: true,
				Pre: pre, DelMid: midDel, AddMid: midAdd, Post: post, Paired: true,
			})
		}
		for _, ln := range dels[paired:] {
			out = append(out, SplitRow{Del: ln, HasDel: true})
		}
		for _, ln := range adds[paired:] {
			out = append(out, SplitRow{Add: ln, HasAdd: true})
		}
		dels = nil
		adds = nil
	}
	for _, ln := range h.Lines {
		switch ln.Kind {
		case '-':
			dels = append(dels, ln)
		case '+':
			adds = append(adds, ln)
		default:
			flush()
			out = append(out, SplitRow{Del: ln, Add: ln, HasDel: true, HasAdd: true})
		}
	}
	flush()
	return out
}
