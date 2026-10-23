package zerojs

import (
	"github.com/gosuda/rukie/internal/web/htmlcheck"
)

func Assert(src []byte) error {
	return htmlcheck.Check(src)
}
