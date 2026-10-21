package review

// DefaultRequired returns the default bookmark requirement set: one
// review/approve from a trusted signer who is not the change author.
func DefaultRequired() string {
	return `[{"kind":"review/approve","result":"approved","trusted":{},"min_signers":1}]`
}

// Verdict is one review verdict with its model marker carried as data.
type Verdict struct {
	Kind  string
	Model bool
}

// SatisfiesHumanApproval reports whether a verdict satisfies a human
// approval requirement. Model reviews never satisfy it.
func (v Verdict) SatisfiesHumanApproval() bool {
	return v.Kind == "review/approve" && !v.Model
}

// EligibleApprover reports whether a signer counts toward a human approval
// requirement on a change. The author never approves their own change.
func EligibleApprover(v Verdict, signerID, authorID int64) bool {
	if !v.SatisfiesHumanApproval() {
		return false
	}
	return signerID != authorID
}

// BypassAllowed reports whether a policy edit may enable admin bypass.
// Version one of the forge rejects bypass_admin outright.
func BypassAllowed() bool {
	return false
}
