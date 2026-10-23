package mirror

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Class string

const (
	ClassAuth         Class = "auth"
	ClassRateLimited  Class = "rate_limited"
	ClassRepoMissing  Class = "repo_missing"
	ClassRepoMoved    Class = "repo_moved"
	ClassServer       Class = "server"
	ClassNetwork      Class = "network"
	ClassUncertain    Class = "uncertain"
	ClassRefHidden    Class = "ref_hidden"
	ClassRefProtected Class = "ref_protected"
	ClassRefPolicy    Class = "ref_policy"
	ClassTooLarge     Class = "too_large"
	ClassRefHook      Class = "ref_hook"
	ClassRefOther     Class = "ref_other"
	ClassProtocol     Class = "protocol"
	ClassDiverged     Class = "diverged"
	ClassBusy         Class = "busy"
	ClassSecret       Class = "secret"
	ClassPolicy       Class = "policy"
	ClassOK           Class = "ok"
)

type Verdict struct {
	Class Class
	State string
	Retry bool
}

func Classify(code string, status int, retryAfter, reason string) Verdict {
	c := strings.ToUpper(code)
	r := strings.ToLower(reason)
	v := func(cl Class, retry bool, state string) Verdict {
		return Verdict{Class: cl, Retry: retry, State: state}
	}
	if status == 429 || (status == 403 && strings.TrimSpace(retryAfter) != "") || strings.Contains(strings.ToLower(reason), "rate limit") {
		return v(ClassRateLimited, true, "failing")
	}
	switch c {
	case "E_FORBIDDEN":
		return v(ClassAuth, false, "auth_failed")
	case "E_PUSH_REJECTED":
		return classifyReason(r)
	case "E_NOT_FOUND":
		return v(ClassRepoMissing, true, "failing")
	case "E_REMOTE_MOVED":
		return v(ClassRepoMoved, false, "needs_attention")
	case "E_OFFLINE":
		return v(ClassNetwork, true, "failing")
	case "E_PUSH_UNCERTAIN":
		return v(ClassUncertain, true, "failing")
	case "E_CORRUPT":
		return v(ClassProtocol, true, "failing")
	case "E_CAPACITY", "E_CANCELLED":
		return v(ClassBusy, true, "failing")
	case "E_DIVERGED":
		return v(ClassDiverged, false, "partial")
	case "E_SECRET":
		return v(ClassSecret, false, "needs_attention")
	case "E_POLICY":
		return v(ClassPolicy, false, "needs_attention")
	}
	if status >= 300 && status < 400 {
		return v(ClassRepoMoved, false, "needs_attention")
	}
	if status >= 500 {
		return v(ClassServer, true, "failing")
	}
	if strings.Contains(c, "TIMEOUT") || strings.Contains(r, "timeout") {
		return v(ClassNetwork, true, "failing")
	}
	for _, g := range reasonGroups {
		for _, t := range g.terms {
			if strings.Contains(r, t) {
				return v(g.class, g.retry, g.state)
			}
		}
	}
	if c == "" && status == 0 && strings.TrimSpace(reason) == "" {
		return v(ClassOK, false, "ok")
	}
	return v(ClassRefOther, false, "partial")
}

type reasonGroup struct {
	class Class
	state string
	terms []string
	retry bool
}

var reasonGroups = []reasonGroup{
	{class: ClassRefHidden, state: "partial", terms: []string{"deny updating a hidden ref", "hidden ref"}, retry: false},
	{class: ClassRefProtected, state: "partial", terms: []string{"protected branch", "force push", "not allowed to push", "ruleset", "codeowners", "required status"}, retry: false},
	{class: ClassRefPolicy, state: "needs_attention", terms: []string{"push protection", "secret scanning", "gh013"}, retry: false},
	{class: ClassTooLarge, state: "needs_attention", terms: []string{"exceeds", "file size limit", "pack exceeds", "too large", "100 mb", "100 mib"}, retry: false},
	{class: ClassRefHook, state: "partial", terms: []string{"hook declined", "pre-receive", "update hook"}, retry: false},
}

func classifyReason(reason string) Verdict {
	for _, g := range reasonGroups {
		for _, t := range g.terms {
			if strings.Contains(reason, t) {
				return Verdict{Class: g.class, Retry: false, State: g.state}
			}
		}
	}
	return Verdict{Class: ClassRefOther, Retry: false, State: "partial"}
}
func ParseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds < 0 || seconds > int64((1<<63-1)/int64(time.Second)) {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds < 0 {
		return 0, false
	}
	t, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	d := t.Sub(now)
	if d < 0 {
		return 0, false
	}
	return d, true
}
