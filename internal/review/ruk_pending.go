package review

import (
	"context"

	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
)

// RukCaller is the minimal typed-call surface the pending F2 methods need.
// The pairing lane fills each stub body with the matching live call.
type RukCaller interface {
	CallRuk(ctx context.Context, repo ruk.Repo, method wire.Method, args map[string]any, out any) error
}

// PairingError reports that a Ruk method activates on the F2 pin. It keeps
// the E_HOSTED source error so handlers map it through the standard path.
func PairingError(method string) error {
	return &ruk.Error{Code: wire.CodeHosted, Op: method, Message: "needs-ruk-F2"}
}

// PendingCompare is the code.compare.get call site. It serves Ruk compare
// once the F2 pin lands.
func PendingCompare(ctx context.Context, c RukCaller, repo ruk.Repo, args map[string]any, out any) error {
	_ = ctx
	_ = c
	_ = repo
	_ = args
	_ = out
	return PairingError(string(wire.MethodCompare))
}

// PendingOpLogTail is the op.log ascending-cursor call site for lane
// mutations. It serves Ruk op.log once the F2 pin lands.
func PendingOpLogTail(ctx context.Context, c RukCaller, repo ruk.Repo, args map[string]any, out any) error {
	_ = ctx
	_ = c
	_ = repo
	_ = args
	_ = out
	return PairingError(string(wire.MethodOpLog))
}

// PendingLandPrepare is the land.plan.create call site. It serves Ruk
// land.prepare with requirements and policy generation once the F2 pin lands.
func PendingLandPrepare(ctx context.Context, c RukCaller, repo ruk.Repo, args map[string]any, out any) error {
	_ = ctx
	_ = c
	_ = repo
	_ = args
	_ = out
	return PairingError(string(wire.MethodLandPrepare))
}

// PendingLandExecute is the land.run.create call site. It serves Ruk
// land.execute once the F2 pin lands.
func PendingLandExecute(ctx context.Context, c RukCaller, repo ruk.Repo, args map[string]any, out any) error {
	_ = ctx
	_ = c
	_ = repo
	_ = args
	_ = out
	return PairingError(string(wire.MethodLandExecute))
}

// PendingAttestAdd is the review attestation call site. It serves Ruk
// attest.add under the review capability grant once the F2 pin lands.
func PendingAttestAdd(ctx context.Context, c RukCaller, repo ruk.Repo, args map[string]any, out any) error {
	_ = ctx
	_ = c
	_ = repo
	_ = args
	_ = out
	return PairingError(string(wire.MethodAttestAdd))
}

// PendingPolicySet is the repo.policy.set call site. It serves Ruk
// policy.set and bumps the policy generation once the F2 pin lands.
func PendingPolicySet(ctx context.Context, c RukCaller, repo ruk.Repo, args map[string]any, out any) error {
	_ = ctx
	_ = c
	_ = repo
	_ = args
	_ = out
	return PairingError(string(wire.MethodPolicySet))
}
