package api

import (
	"cmp"
	"html"
	"strconv"
	"strings"
)

const ciRefreshMinSeconds = 15

type ciPageKind uint8

const (
	ciPageRunsTab ciPageKind = iota + 1
	ciPageRunJob
	ciPageJobLog
	ciPageBadge
)

func matchCIPage(path string) (kind ciPageKind, owner, repo, run, job string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if parts[0] == "" || len(parts) < 2 || parts[1] == "" {
		return 0, "", "", "", "", false
	}
	owner, repo = parts[0], parts[1]
	switch {
	case len(parts) == 3 && parts[2] == "ci":
		return ciPageRunsTab, owner, repo, "", "", true
	case len(parts) == 4 && parts[2] == "ci" && parts[3] == "badge.svg":
		return ciPageBadge, owner, repo, "", "", true
	case len(parts) == 7 && parts[2] == "ci" && parts[3] == "runs" && parts[4] != "" && parts[5] == "jobs" && parts[6] != "":
		return ciPageRunJob, owner, repo, parts[4], parts[6], true
	case len(parts) == 8 && parts[2] == "ci" && parts[3] == "runs" && parts[4] != "" && parts[5] == "jobs" && parts[6] != "" && parts[7] == "log":
		return ciPageJobLog, owner, repo, parts[4], parts[6], true
	}
	return 0, "", "", "", "", false
}

func ciAttemptLive(state string) bool {
	switch state {
	case "granted", "started", "suspect":
		return true
	}
	return false
}

func ciRefreshMeta(seconds int) string {
	if seconds < ciRefreshMinSeconds {
		return ""
	}
	return "<meta http-equiv=\"refresh\" content=\"" + strconv.Itoa(seconds) + "\">\n"
}

func ciPageShell(title, body string, refresh int) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString(ciRefreshMeta(refresh))
	b.WriteString("<title>")
	b.WriteString(html.EscapeString(title))
	b.WriteString(" — rukie</title>\n")
	b.WriteString(fontLink)
	b.WriteString("\n</head>\n<body>\n<main>\n")
	b.WriteString(body)
	b.WriteString("\n</main>\n</body>\n</html>\n")
	return b.String()
}

func ciLogPage(path, data, attemptState string, refresh int) string {
	var body strings.Builder
	body.WriteString(heading("Job log"))
	body.WriteString("<p>State: " + esc(attemptState) + ". ")
	live := ciAttemptLive(attemptState)
	if live {
		if refresh >= ciRefreshMinSeconds {
			body.WriteString("Refresh every " + strconv.Itoa(refresh) + " seconds is on " +
				"(<a href=\"" + esc(path) + "?refresh_seconds=0\">turn off</a>).")
		} else {
			body.WriteString("<a href=\"" + esc(path) + "?refresh_seconds=" +
				strconv.Itoa(ciRefreshMinSeconds) + "\">Refresh every " +
				strconv.Itoa(ciRefreshMinSeconds) + " seconds</a>.")
		}
	}
	body.WriteString("</p>\n")
	body.WriteString("<pre>" + html.EscapeString(data) + "</pre>\n")
	meta := 0
	if live {
		meta = refresh
	}
	return ciPageShell("Job log", body.String(), meta)
}

func ciBadgeColor(status string) string {
	switch status {
	case "succeeded":
		return "#2da44e"
	case "failed":
		return "#cf222e"
	case "running", "pending", "cancelling":
		return "#bf8700"
	default:
		return "#6e7781"
	}
}

func ciBadgeSVG(status string) []byte {
	label := cmp.Or(status, "unknown")
	text := html.EscapeString(label)
	width := len(label)*7 + 20
	var b strings.Builder
	b.WriteString("<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"")
	b.WriteString(strconv.Itoa(width))
	b.WriteString("\" height=\"20\" role=\"img\" aria-label=\"ci: ")
	b.WriteString(text)
	b.WriteString("\"><title>ci: ")
	b.WriteString(text)
	b.WriteString("</title><rect width=\"")
	b.WriteString(strconv.Itoa(width))
	b.WriteString("\" height=\"20\" fill=\"")
	b.WriteString(ciBadgeColor(status))
	b.WriteString("\"/></svg>\n")
	return []byte(b.String())
}
