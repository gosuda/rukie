package negotiate

import (
	"net/http"
	"strconv"
	"strings"
)

type Media int

const (
	MediaJSON Media = iota
	MediaMarkdown
	MediaHTML
)

func (m Media) ContentType() string {
	switch m {
	case MediaJSON:
		return "application/json"
	case MediaMarkdown:
		return "text/markdown; charset=utf-8"
	case MediaHTML:
		return "text/html; charset=utf-8"
	}
	return "application/json"
}

func parseQ(v string) float64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 1
	}
	f, err := strconv.ParseFloat(strings.TrimPrefix(v, "q="), 64)
	if err != nil || f < 0 || f > 1 {
		return 0
	}
	return f
}

func Negotiate(accept string) Media {
	if strings.TrimSpace(accept) == "" {
		return MediaJSON
	}
	var best = MediaJSON
	bestQ := -1.0
	bestRank := 3
	seen := 0
	consider := func(m Media, q float64) {
		seen++
		rank := int(m)
		if q > bestQ || (q == bestQ && rank < bestRank) {
			bestQ = q
			bestRank = rank
			best = m
		}
	}
	for part := range strings.SplitSeq(accept, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.Split(part, ";")
		name := strings.TrimSpace(strings.ToLower(fields[0]))
		q := 1.0
		for _, f := range fields[1:] {
			f = strings.TrimSpace(f)
			if strings.HasPrefix(f, "q=") {
				q = parseQ(f)
			}
		}
		switch name {
		case "text/html", "application/xhtml+xml":
			consider(MediaHTML, q)
		case "text/markdown", "text/x-markdown":
			consider(MediaMarkdown, q)
		case "application/json":
			consider(MediaJSON, q)
		case "*/*", "*":
			consider(MediaJSON, q)
		case "application/*":
			consider(MediaJSON, q)
		case "text/*":
			consider(MediaMarkdown, q)
		}
	}
	if seen == 0 {
		return MediaJSON
	}
	if bestQ <= 0 {
		return MediaJSON
	}
	return best
}

func VaryAccept(h http.Header) {
	h.Add("Vary", "Accept")
}
