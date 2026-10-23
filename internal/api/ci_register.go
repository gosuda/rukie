package api

import (
	"fmt"
	"net/http"

	"github.com/gosuda/rukie/internal/op"
)

// RegisterCI registers the F5 CI control plane operations.
func RegisterCI(t *op.Table, deps Deps) error {
	if err := op.Register(t, op.Op[CIRunListIn, CIRunListOut]{
		Name: "ci.run.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/ci/runs",
		Scope: op.ScopeCIRead, Summary: "List CI runs",
		Description: "CI runs of one repository, newest first, with optional state, subject, and trigger filters.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out CIRunListOut) string {
			return fmt.Sprintf("\"ci-runs-%d-%s\"", len(out.Runs), out.Next)
		},
		Pagination: &op.Pagination[CIRunListOut]{Default: 100, Max: 1000, Next: func(out CIRunListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: ciRunListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[CIRunGetIn, CIRunGetOut]{
		Name: "ci.run.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/ci/runs/{run}",
		Scope: op.ScopeCIRead, Summary: "Show a CI run",
		Description: "One CI run with its jobs, attempts, and cache hits.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out CIRunGetOut) string {
			return out.ETag
		},
		Handler: ciRunGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[CIRunCreateIn, CIRunCreateOut]{
		Name: "ci.run.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/ci/runs",
		Scope: op.ScopeCITrigger, Summary: "Create a CI run",
		Description: "Start a manual CI run from an inline spec. Duplicate trigger keys return the existing run.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeValidation, op.CodeForbidden, op.CodeUnavailable},
		Location:    func(out CIRunCreateOut) string { return "/ci/runs/" + out.Run.ID },
		Handler:     ciRunCreateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[CIRunUpdateIn, CIRunUpdateOut]{
		Name: "ci.run.update", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/ci/runs/{run}/cancel",
		Scope: op.ScopeCITrigger, Summary: "Cancel a CI run",
		Description: "Move a run to cancelling through state cancelled. Terminal runs answer noop.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeValidation, op.CodeForbidden, op.CodeConflict},
		Location:    func(_ CIRunUpdateOut) string { return "" },
		Handler:     ciRunUpdateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[CIRunRetryIn, CIRunRetryOut]{
		Name: "ci.run.retry", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/ci/runs/{run}/retry",
		Scope: op.ScopeCITrigger, Summary: "Retry a CI run",
		Description: "Start a new run carrying passed jobs. Exclusive jobs need an explicit acknowledgement.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeValidation, op.CodeForbidden, op.CodeConflict},
		Location:    func(out CIRunRetryOut) string { return "/ci/runs/" + out.Run.ID },
		Handler:     ciRunRetryHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[CIJobListIn, CIJobListOut]{
		Name: "ci.job.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/ci/jobs",
		Scope: op.ScopeCIRead, Summary: "List CI jobs",
		Description: "CI jobs of one repository with state and wait-reason filters.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out CIJobListOut) string {
			return fmt.Sprintf("\"ci-jobs-%d-%s\"", len(out.Jobs), out.Next)
		},
		Pagination: &op.Pagination[CIJobListOut]{Default: 100, Max: 1000, Next: func(out CIJobListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: ciJobListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[CILogGetIn, CILogGetOut]{
		Name: "ci.log.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/ci/attempts/{attempt}/log",
		Scope: op.ScopeCIRead, Summary: "Read an attempt log",
		Description: "Offset-addressed log bytes with elision markers and the attempt state.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeValidation, op.CodeForbidden},
		ETag: func(out CILogGetOut) string {
			return fmt.Sprintf("\"ci-log-%d-%d-%s\"", out.Offset, out.NextOffset, out.AttemptState)
		},
		Handler: ciLogGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[CIArtifactListIn, CIArtifactListOut]{
		Name: "ci.artifact.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/ci/runs/{run}/artifacts",
		Scope: op.ScopeCIRead, Summary: "List run artifacts",
		Description: "Content-addressed artifacts linked by one run, oldest job first.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out CIArtifactListOut) string {
			return fmt.Sprintf("\"ci-artifacts-%d\"", len(out.Artifacts))
		},
		Handler: ciArtifactListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[CIArtifactGetIn, CIArtifactGetOut]{
		Name: "ci.artifact.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/ci/blobs/{digest}",
		Scope: op.ScopeCIRead, Summary: "Describe an artifact blob",
		Description: "Metadata for one content-addressed blob. Byte downloads use the download signal.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out CIArtifactGetOut) string {
			return "\"ci-blob-" + out.Digest + "\""
		},
		Handler: ciArtifactGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[CISpecVerifyIn, CISpecVerifyOut]{
		Name: "ci.spec.verify", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/ci/spec/validate",
		Scope: op.ScopeCITrigger, Summary: "Verify a CI spec",
		Description: "Validate an inline .rukie/ci.toml file against the schema and semantic rules.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeValidation, op.CodeForbidden, op.CodeUnavailable},
		Handler:     ciSpecVerifyHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[CISpecGetIn, CISpecGetOut]{
		Name: "ci.spec.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/ci/spec",
		Scope: op.ScopeCIRead, Summary: "Show the effective CI spec",
		Description: "Effective spec of one run with per-job digests and required proof kinds.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden, op.CodeUnavailable},
		ETag: func(out CISpecGetOut) string {
			return fmt.Sprintf("\"ci-spec-%d\"", len(out.Digests))
		},
		Handler: ciSpecGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[CIRunnerCreateIn, CIRunnerCreateOut]{
		Name: "ci.runner.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/-/ci/v1/runners",
		Scope: op.ScopeAdminWrite, Summary: "Register a CI runner",
		Description: "Register one runner scope with labels and capacity. The token shows once.",
		Problems:    []op.Code{op.CodeInvalid, op.CodeValidation, op.CodeForbidden},
		Handler:     ciRunnerCreateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[CIRunnerListIn, CIRunnerListOut]{
		Name: "ci.runner.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/-/ci/v1/runners",
		Scope: op.ScopeAdminWrite, Summary: "List CI runners",
		Description: "Registered runners without token material.",
		Problems:    []op.Code{op.CodeForbidden},
		ETag: func(out CIRunnerListOut) string {
			return fmt.Sprintf("\"ci-runners-%d\"", len(out.Runners))
		},
		Handler: ciRunnerListHandler(deps),
	}); err != nil {
		return err
	}
	return op.Register(t, op.Op[CIRunnerRevokeIn, CIRunnerRevokeOut]{
		Name: "ci.runner.revoke", Kind: op.KindMutation, Method: http.MethodPost, Path: "/-/ci/v1/runners/{id}/revoke",
		Scope: op.ScopeAdminWrite, Summary: "Revoke a CI runner",
		Description: "Revoke one runner. Active attempts go lost after the grace period.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		Handler:     ciRunnerRevokeHandler(deps),
	})
}
