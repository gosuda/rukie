package review

import (
	"errors"
	"testing"

	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
)

var approveReq = []Requirement{{Kind: "review/approve", Result: "approved", MinSigners: 1}}

func TestGapsEmptyHeadIsConflict(t *testing.T) {
	gaps := GapsForChange("", 7, approveReq, nil, 100)
	if len(gaps) != 1 || gaps[0].Reason != "conflict" {
		t.Fatalf("empty head gaps = %+v, want one conflict", gaps)
	}
}

func TestGapsNoFactsNeedsReview(t *testing.T) {
	gaps := GapsForChange("abc", 7, approveReq, nil, 100)
	if len(gaps) != 1 || gaps[0].Reason != "needs_review" {
		t.Fatalf("no-fact gaps = %+v, want one needs_review", gaps)
	}
	if gaps[0].NextOp != "change.review.create" {
		t.Fatalf("next op = %q, want change.review.create", gaps[0].NextOp)
	}
}

func TestGapsSatisfiedApproval(t *testing.T) {
	facts := []AttestationFact{{Kind: "review/approve", Result: "approved", OID: "abc", SignerID: 8, Live: true}}
	gaps := GapsForChange("abc", 7, approveReq, facts, 100)
	if len(gaps) != 0 {
		t.Fatalf("satisfied gaps = %+v, want none", gaps)
	}
}

func TestGapsAuthorApprovalIgnored(t *testing.T) {
	facts := []AttestationFact{{Kind: "review/approve", Result: "approved", OID: "abc", SignerID: 7, Live: true}}
	gaps := GapsForChange("abc", 7, approveReq, facts, 100)
	if len(gaps) != 1 || gaps[0].Reason != "needs_review" {
		t.Fatalf("author-only gaps = %+v, want needs_review", gaps)
	}
}

func TestGapsStaleAndSupersededFactsIgnored(t *testing.T) {
	facts := []AttestationFact{
		{Kind: "review/approve", Result: "approved", OID: "abc", SignerID: 8, Live: false},
		{Kind: "review/approve", Result: "approved", OID: "abc", SignerID: 9, Live: true, AgeSecs: 9999},
		{Kind: "review/approve", Result: "approved", OID: "zzz", SignerID: 9, Live: true},
	}
	reqs := []Requirement{{Kind: "review/approve", Result: "approved", MinSigners: 1, MaxAgeSecs: 60}}
	gaps := GapsForChange("abc", 7, reqs, facts, 100)
	if len(gaps) != 1 || gaps[0].Reason != "needs_review" {
		t.Fatalf("stale-fact gaps = %+v, want needs_review", gaps)
	}
}

func TestGapsQuorumCountsDistinctSigners(t *testing.T) {
	facts := []AttestationFact{
		{Kind: "review/approve", Result: "approved", OID: "abc", SignerID: 8, Live: true},
		{Kind: "review/approve", Result: "approved", OID: "abc", SignerID: 8, Live: true},
	}
	reqs := []Requirement{{Kind: "review/approve", Result: "approved", MinSigners: 2}}
	gaps := GapsForChange("abc", 7, reqs, facts, 100)
	if len(gaps) != 1 {
		t.Fatalf("quorum gaps = %+v, want one needs_review", gaps)
	}
}

func TestGapsCIKindYieldsNeedsCI(t *testing.T) {
	reqs := []Requirement{{Kind: "ci/*", Result: "pass", MinSigners: 1}}
	gaps := GapsForChange("abc", 7, reqs, nil, 100)
	if len(gaps) != 1 || gaps[0].Reason != "needs_ci" {
		t.Fatalf("ci gaps = %+v, want one needs_ci", gaps)
	}
}

func TestGapsLiveBlockFails(t *testing.T) {
	reqs := []Requirement{
		{Kind: "review/approve", Result: "approved", MinSigners: 1},
		{Kind: "review/block", Result: "absent", MinSigners: 1},
	}
	facts := []AttestationFact{
		{Kind: "review/approve", Result: "approved", OID: "abc", SignerID: 8, Live: true},
		{Kind: "review/block", Result: "block", OID: "abc", SignerID: 9, Live: true},
	}
	gaps := GapsForChange("abc", 7, reqs, facts, 100)
	if len(gaps) != 1 || gaps[0].Reason != "blocked_review" {
		t.Fatalf("blocked gaps = %+v, want one blocked_review", gaps)
	}
}

func TestGapsBlockOnOtherHeadIgnored(t *testing.T) {
	reqs := []Requirement{{Kind: "review/block", Result: "absent", MinSigners: 1}}
	facts := []AttestationFact{{Kind: "review/block", Result: "block", OID: "zzz", SignerID: 9, Live: true}}
	gaps := GapsForChange("abc", 7, reqs, facts, 100)
	if len(gaps) != 0 {
		t.Fatalf("other-head block gaps = %+v, want none", gaps)
	}
}

func TestGapsPairingErrorKeepsHostedCode(t *testing.T) {
	err := PairingError("compare")
	var rerr *ruk.Error
	if !errors.As(err, &rerr) {
		t.Fatalf("pairing error is %T, want *ruk.Error", err)
	}
	if rerr.Code != wire.CodeHosted {
		t.Fatalf("pairing code = %q, want E_HOSTED", string(rerr.Code))
	}
}
