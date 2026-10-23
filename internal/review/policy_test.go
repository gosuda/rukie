package review

import (
	"testing"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
)

func TestDefaultRequiredIsOneHumanApproval(t *testing.T) {
	var reqs []Requirement
	dec := jsonv2.Unmarshal([]byte(DefaultRequired()), &reqs, jsonv1.DefaultOptionsV1())
	if dec != nil {
		t.Fatalf("default required is not JSON: %v", dec)
	}
	if len(reqs) != 1 {
		t.Fatalf("default required holds %d requirements, want 1", len(reqs))
	}
	req := reqs[0]
	if req.Kind != "review/approve" || req.Result != "approved" || req.MinSigners != 1 {
		t.Fatalf("default requirement = %+v, want review/approve approved x1", req)
	}
}

func TestVerdictSatisfiesHumanApproval(t *testing.T) {
	human := Verdict{Kind: "review/approve"}
	if !human.SatisfiesHumanApproval() {
		t.Fatal("human approve verdict does not satisfy")
	}
	modelApprove := Verdict{Kind: "review/approve", Model: true}
	if modelApprove.SatisfiesHumanApproval() {
		t.Fatal("model review satisfies human approval")
	}
	block := Verdict{Kind: "review/block"}
	if block.SatisfiesHumanApproval() {
		t.Fatal("block verdict satisfies human approval")
	}
	modelKind := Verdict{Kind: "model/review"}
	if modelKind.SatisfiesHumanApproval() {
		t.Fatal("model kind satisfies human approval")
	}
}

func TestEligibleApproverRejectsSelfApproval(t *testing.T) {
	v := Verdict{Kind: "review/approve"}
	if EligibleApprover(v, 7, 7) {
		t.Fatal("author approves their own change")
	}
	if !EligibleApprover(v, 8, 7) {
		t.Fatal("unrelated signer is not eligible")
	}
	if EligibleApprover(Verdict{Kind: "review/approve", Model: true}, 8, 7) {
		t.Fatal("model review from another signer is eligible")
	}
}

func TestBypassAdminRejectedInV1(t *testing.T) {
	if BypassAllowed() {
		t.Fatal("admin bypass is allowed in v1")
	}
}
