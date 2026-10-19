package shell

import (
	"strconv"

	"github.com/gosuda/rukie/internal/web/trusted"
)

const FontsLink = `<link rel="stylesheet" href="https://fonts.bunny.net/css?family=space-grotesk:500,600,700%7Csource-sans-3:400,600%7Cibm-plex-mono:400,500,600&display=swap" referrerpolicy="no-referrer">`

type Page struct {
	Title   string
	CSRF    string
	Build   string
	Refresh int
}

func Fonts() trusted.HTML {
	return trusted.HTML(FontsLink)
}

func refreshContent(seconds int) string {
	return strconv.Itoa(seconds)
}
