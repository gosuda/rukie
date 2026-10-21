package review

import (
	"strconv"
	"strings"
	"time"
)

// Requirement is one land requirement pattern over live attestations.
type Requirement struct {
	Kind       string `json:"kind"`
	Result     string `json:"result"`
	MinSigners int    `json:"min_signers"`
	MaxAgeSecs int64  `json:"max_age_secs"`
}

// AttestationFact is one live attestation read from Ruk at evaluation time.
type AttestationFact struct {
	Kind     string
	Result   string
	OID      string
	SignerID int64
	AgeSecs  int64
	Live     bool
}

// Gap names one machine-checkable reason a change cannot land yet.
// Kind carries the requirement pattern that produced the gap.
type Gap struct {
	Kind   string
	Reason string
	Detail string
	NextOp string
}

// GapsForChange computes gap reasons from live facts, never from cached
// verdicts. The first gap is the primary display reason.
func GapsForChange(head string, authorID int64, reqs []Requirement, facts []AttestationFact, now int64) []Gap {
	var out []Gap
	if head == "" {
		return append(out, Gap{Reason: "conflict", Detail: "change head is conflicted", NextOp: "change.get"})
	}
	for _, req := range reqs {
		need := req.MinSigners
		if need <= 0 {
			need = 1
		}
		if req.Kind == "review/block" {
			for _, fact := range facts {
				if !fact.Live || fact.OID != head || fact.Kind != "review/block" {
					continue
				}
				out = append(out, Gap{Kind: req.Kind, Reason: "blocked_review", Detail: "review/block is live", NextOp: "change.review.create"})
				break
			}
			continue
		}
		seen := map[int64]bool{}
		for _, fact := range facts {
			if !fact.Live || fact.OID != head {
				continue
			}
			if req.MaxAgeSecs > 0 && fact.AgeSecs > req.MaxAgeSecs {
				continue
			}
			if !matchKind(req.Kind, fact.Kind) || req.Result != fact.Result {
				continue
			}
			if fact.SignerID == authorID {
				continue
			}
			seen[fact.SignerID] = true
		}
		if len(seen) < need {
			reason := "needs_review"
			if strings.HasPrefix(req.Kind, "ci/") {
				reason = "needs_ci"
			}
			out = append(out, Gap{
				Kind:   req.Kind,
				Reason: reason,
				Detail: req.Kind + " wants " + itoa(need-len(seen)) + " more",
				NextOp: "change.review.create",
			})
		}
	}
	return out
}

func matchKind(pattern, kind string) bool {
	if before, _, found := strings.Cut(pattern, "*"); found {
		return strings.HasPrefix(kind, before)
	}
	return pattern == kind
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

// FreshNow returns evaluation time in Unix seconds.
func FreshNow() int64 {
	return time.Now().Unix()
}
