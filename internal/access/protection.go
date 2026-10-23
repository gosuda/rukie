package access

import (
	"cmp"
	"path"
	"slices"
	"strings"
)

type AllowEntry struct {
	Users []int64
	Teams []int64
	Roles []Role
}

type AttestationReq struct {
	Kind       string
	Result     string
	Trusted    AllowEntry
	MinSigners int
}

type Attestation struct {
	Kind       string
	Result     string
	OID        string
	SignerID   int64
	SignerRole Role
}

type BookmarkRule struct {
	Pattern     string
	Mode        string
	PushAllow   []AllowEntry
	LandAllow   []AllowEntry
	Required    []AttestationReq
	ID          int64
	RepoID      int64
	Version     int64
	BypassAdmin bool
}

func isGlob(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

func isGlobRank(pattern string) int {
	if isGlob(pattern) {
		return 1
	}
	return 0
}

func MatchRule(rules []BookmarkRule, ref string) *BookmarkRule {
	ordered := slices.Clone(rules)
	slices.SortStableFunc(ordered, func(a, b BookmarkRule) int {
		return cmp.Or(
			cmp.Compare(isGlobRank(a.Pattern), isGlobRank(b.Pattern)),
			cmp.Compare(len(b.Pattern), len(a.Pattern)),
			cmp.Compare(a.ID, b.ID),
		)
	})
	for i := range ordered {
		pattern := ordered[i].Pattern
		matched := pattern == ref
		if !matched && isGlob(pattern) {
			matched, _ = path.Match(pattern, ref)
		}
		if matched {
			out := ordered[i]
			return &out
		}
	}
	return nil
}

func matchAllow(res Resolver, userID int64, grant Role, entries []AllowEntry) bool {
	if len(entries) == 0 {
		return true
	}
	for _, e := range entries {
		if len(e.Users) == 0 && len(e.Teams) == 0 && len(e.Roles) == 0 {
			return true
		}
		for _, u := range e.Users {
			if u == userID {
				return true
			}
		}
		for _, t := range e.Teams {
			if res.IsTeamMember(t, userID) {
				return true
			}
		}
		if len(e.Roles) > 0 {
			need := e.Roles[0]
			for _, r := range e.Roles[1:] {
				need = min(need, r)
			}
			if grant >= need {
				return true
			}
		}
	}
	return false
}

func protectionPass(res Resolver, c Caller, grant Role, action string, r Resource, repo RepoRecord) string {
	if action == "change.review" {
		return ""
	}
	rule := MatchRule(res.Rules(repo.ID), r.Ref)
	if rule == nil {
		return ""
	}
	switch action {
	case "repo.push":
		if rule.Mode == "LandOnly" {
			if !rule.BypassAdmin || len(rule.PushAllow) == 0 {
				return "protected:allowlist"
			}
			if !matchAllow(res, c.UserID, grant, rule.PushAllow) {
				return "protected:allowlist"
			}
			return ""
		}
		if !matchAllow(res, c.UserID, grant, rule.PushAllow) {
			return "protected:allowlist"
		}
		return ""
	case "change.land":
		if !matchAllow(res, c.UserID, grant, rule.LandAllow) {
			return "protected:allowlist"
		}
		for _, req := range rule.Required {
			if !attested(res, c, r, req) {
				return "protected:attestation"
			}
		}
		return ""
	default:
		return ""
	}
}

func attested(res Resolver, _ Caller, r Resource, req AttestationReq) bool {
	need := req.MinSigners
	if need <= 0 {
		need = 1
	}
	seen := map[int64]bool{}
	for _, a := range r.Attestations {
		if a.Kind != req.Kind || a.Result != req.Result {
			continue
		}
		if r.OID != "" && a.OID != r.OID {
			continue
		}
		if a.SignerID == r.ChangeAuthor {
			continue
		}
		if !trustedSigner(res, a, req.Trusted) {
			continue
		}
		seen[a.SignerID] = true
	}
	return len(seen) >= need
}

func trustedSigner(res Resolver, a Attestation, t AllowEntry) bool {
	if len(t.Users) == 0 && len(t.Teams) == 0 && len(t.Roles) == 0 {
		return a.SignerRole >= RoleWrite
	}
	for _, u := range t.Users {
		if u == a.SignerID {
			return true
		}
	}
	for _, team := range t.Teams {
		if res.IsTeamMember(team, a.SignerID) {
			return true
		}
	}
	if len(t.Roles) > 0 {
		need := t.Roles[0]
		for _, role := range t.Roles[1:] {
			need = min(need, role)
		}
		return a.SignerRole >= need
	}
	return false
}
