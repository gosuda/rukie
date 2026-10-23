package trusted

import (
	"errors"

	"github.com/gosuda/rukie/internal/web/htmlcheck"
)

var ErrRejected = errors.New("trusted: markup failed the zero-JS check")

type HTML string

func Raw(s string) (HTML, error) {
	if err := htmlcheck.Check([]byte(s)); err != nil {
		return "", err
	}
	return HTML(s), nil
}

func Markdown(rendered string) (HTML, error) {
	if err := htmlcheck.Check([]byte(rendered)); err != nil {
		return "", errors.Join(ErrRejected, err)
	}
	return HTML(rendered), nil
}

func (h HTML) String() string {
	return string(h)
}
