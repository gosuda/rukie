package diff

import (
	"cmp"
	"strconv"

	"github.com/gosuda/rukie/internal/web/vm"
)

func rowClass(ln Line) (old, fresh, sign, cls string) {
	if ln.Old > 0 {
		old = strconv.Itoa(ln.Old)
	}
	if ln.New > 0 {
		fresh = strconv.Itoa(ln.New)
	}
	switch ln.Kind {
	case '-':
		return old, fresh, "-", "del"
	case '+':
		return old, fresh, "+", "add"
	default:
		return old, fresh, " ", "ctx"
	}
}

func rowOld(ln Line) string {
	old, _, _, _ := rowClass(ln)
	return old
}

func rowNew(ln Line) string {
	_, fresh, _, _ := rowClass(ln)
	return fresh
}

func rowSign(ln Line) string {
	_, _, sign, _ := rowClass(ln)
	return sign
}

func rowCls(ln Line) string {
	_, _, _, cls := rowClass(ln)
	return cls
}

func rowID(ln Line, hl *vm.LineRange) string {
	if hl == nil {
		return ""
	}
	if hl.Contains(ln.Old) || hl.Contains(ln.New) {
		return "L" + strconv.Itoa(cmp.Or(ln.New, ln.Old))
	}
	return ""
}
