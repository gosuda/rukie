package api

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ci"
	"github.com/gosuda/rukie/internal/op"
)

func ciRunnerStatus(code string) int {
	switch code {
	case "E_LEASE_LOST", "E_NOT_STARTED", "E_OFFSET_GAP", "E_SEQ_CONFLICT", "E_RESULT_CONFLICT", "E_LOG_INCOMPLETE", "E_BLOB_MISSING", "E_CONFLICT":
		return http.StatusConflict
	case "E_DIGEST_MISMATCH", "E_INVALID":
		return http.StatusUnprocessableEntity
	case "E_RECOVERING":
		return http.StatusServiceUnavailable
	case "E_LIMIT":
		return http.StatusRequestEntityTooLarge
	case "E_UNAUTHENTICATED":
		return http.StatusUnauthorized
	case "E_FORBIDDEN":
		return http.StatusForbidden
	case "E_NOT_FOUND":
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

func writeRunnerProblem(w http.ResponseWriter, err error) {
	code := ci.CodeOf(err)
	if code == "" {
		if oerr, ok := errors.AsType[*op.Error](err); ok {
			switch oerr.Code {
			case op.CodeNotFound:
				code = "E_NOT_FOUND"
			case op.CodeForbidden:
				code = "E_FORBIDDEN"
			case op.CodeUnauthorized:
				code = "E_UNAUTHENTICATED"
			case op.CodeInvalid, op.CodeValidation:
				code = "E_INVALID"
			case op.CodeConflict:
				code = "E_CONFLICT"
			case op.CodeLeaseConflict:
				code = "E_LEASE_LOST"
			case op.CodeUnavailable:
				code = "E_RECOVERING"
			default:
				code = "E_INTERNAL"
			}
		} else {
			code = "E_INTERNAL"
		}
	}
	status := ciRunnerStatus(code)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	raw, _ := jsonv2.Marshal(map[string]any{
		"type":   "urn:rukie:problem:" + code,
		"title":  "CI runner request failed",
		"status": status,
		"code":   code,
		"detail": err.Error(),
	})
	_, _ = w.Write(raw)
}

func readRunnerBody(w http.ResponseWriter, r *http.Request, out any) bool {
	if r.ContentLength > 1<<20 {
		writeRunnerProblem(w, ciProblem("E_LIMIT"))
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil {
		writeRunnerProblem(w, err)
		return false
	}
	if len(raw) > 1<<20 {
		writeRunnerProblem(w, ciProblem("E_LIMIT"))
		return false
	}
	if err := jsonv2.Unmarshal(raw, out); err != nil {
		writeRunnerProblem(w, ciProblem("E_INVALID"))
		return false
	}
	return true
}

func ciProblem(code string) error {
	return &ci.Problem{Code: code}
}

func runnerBearer(r *http.Request) string {
	scheme, value, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(value)
}

func writeRunnerJSON(w http.ResponseWriter, out any) {
	raw, err := jsonv2.Marshal(out)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

type runnerLeaseIn struct {
	RunnerID       string   `json:"runner_id"`
	Scope          string   `json:"scope"`
	Platform       string   `json:"platform"`
	SandboxProfile string   `json:"sandbox_profile"`
	Labels         []string `json:"labels"`
	FreeSlots      int      `json:"free_slots"`
	WaitMS         int      `json:"wait_ms"`
}

func (d Deps) runnerLease(w http.ResponseWriter, r *http.Request) {
	var in runnerLeaseIn
	if !readRunnerBody(w, r, &in) {
		return
	}
	ctrl, cerr := d.ciController()
	if cerr != nil {
		writeRunnerProblem(w, cerr)
		return
	}
	token := runnerBearer(r)
	deadline := time.Now().Add(time.Duration(min(max(in.WaitMS, 0), 25000)) * time.Millisecond)
	for {
		lease, lerr := ctrl.Lease(r.Context(), in.RunnerID, token)
		if lerr != nil {
			writeRunnerProblem(w, lerr)
			return
		}
		if lease != nil {
			writeRunnerLease(w, lease)
			return
		}
		if time.Now().After(deadline) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func writeRunnerLease(w http.ResponseWriter, lease *ci.Lease) {
	timeout, _ := time.ParseDuration(lease.Job.Spec.Timeout)
	steps := make([]map[string]string, 0, len(lease.Job.Spec.Steps))
	for _, step := range lease.Job.Spec.Steps {
		steps = append(steps, map[string]string{"name": step.Name, "run": step.Run})
	}
	env := map[string]string{"RUKIE_TREE": lease.Run.Tree}
	for name, value := range lease.Job.Spec.Env {
		env[name] = value
	}
	if !lease.Job.Spec.Pure {
		env["RUKIE_COMMIT"] = lease.Run.Subject
		env["RUKIE_RUN"] = lease.Run.ID
		env["RUKIE_ATTEMPT"] = lease.Attempt.ID
		env["RUKIE_FENCE"] = strconv.FormatInt(lease.Attempt.Fence, 10)
	}
	secrets := make([]map[string]any, 0, len(lease.Secrets))
	for _, sealed := range lease.Secrets {
		secrets = append(secrets, map[string]any{
			"name":    sealed.Name,
			"version": sealed.Version,
			"sealed": map[string]string{
				"epk":   base64.StdEncoding.EncodeToString(sealed.Ephemeral),
				"nonce": base64.StdEncoding.EncodeToString(sealed.Nonce),
				"ct":    base64.StdEncoding.EncodeToString(sealed.Ciphertext),
			},
		})
	}
	writeRunnerJSON(w, map[string]any{
		"attempt": map[string]any{
			"id": lease.Attempt.ID, "number": lease.Attempt.Number,
			"fence": lease.Attempt.Fence, "token": lease.Token,
		},
		"run": map[string]any{
			"id": lease.Run.ID, "trust": lease.Run.Trust, "spec_source": lease.Run.SpecFrom,
			"subject": lease.Run.Subject, "tree": lease.Run.Tree,
		},
		"job": map[string]any{
			"name": lease.Job.Spec.Name, "spec_digest": lease.Job.SpecDigest,
			"action_digest": lease.Job.ActionDigest, "pure": lease.Job.Spec.Pure,
			"effects": lease.Job.Spec.Effects, "network": lease.Job.Spec.Network,
			"interruptible": lease.Job.Spec.Interruptible, "timeout_s": int64(timeout.Seconds()),
			"image": map[string]string{"ref": lease.Job.Spec.Image, "manifest": lease.Job.Spec.Image},
			"steps": steps, "env": env, "artifacts": lease.Job.Spec.Artifacts,
		},
		"fetch": map[string]string{
			"url":  "/" + lease.Run.Owner + "/" + lease.Run.Repo + ".git",
			"want": lease.Run.Subject, "expect_tree": lease.Run.Tree,
		},
		"secrets": secrets,
		"timers":  map[string]int64{"heartbeat_s": 10, "lease_ttl_s": 30, "self_fence_s": 135},
	})
}

type runnerFenceIn struct {
	Fence int64 `json:"fence"`
}

func (d Deps) attemptToken(r *http.Request) (string, *ci.Controller, bool) {
	ctrl, cerr := d.ciController()
	if cerr != nil {
		return "", nil, false
	}
	return runnerBearer(r), ctrl, true
}

func (d Deps) runnerStart(w http.ResponseWriter, r *http.Request) {
	var in runnerFenceIn
	if !readRunnerBody(w, r, &in) {
		return
	}
	token, ctrl, ok := d.attemptToken(r)
	if !ok {
		writeRunnerProblem(w, ciProblem("E_RECOVERING"))
		return
	}
	reply, err := ctrl.Start(r.Context(), r.PathValue("id"), token, in.Fence)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	writeRunnerJSON(w, map[string]any{
		"state": reply.State, "cancel": nil,
		"heartbeat_persisted_at_ms": reply.HeartbeatPersistedAt,
	})
}

func (d Deps) runnerHeartbeat(w http.ResponseWriter, r *http.Request) {
	var in runnerFenceIn
	if !readRunnerBody(w, r, &in) {
		return
	}
	token, ctrl, ok := d.attemptToken(r)
	if !ok {
		writeRunnerProblem(w, ciProblem("E_RECOVERING"))
		return
	}
	reply, err := ctrl.Heartbeat(r.Context(), r.PathValue("id"), token, in.Fence)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	writeRunnerJSON(w, map[string]any{
		"state": reply.State, "cancel": nil, "lease_ttl_s": 30,
		"heartbeat_persisted_at_ms": reply.HeartbeatPersistedAt,
		"log_ack_offset":            reply.LogAckOffset,
	})
}

func (d Deps) runnerLogs(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SHA256 string        `json:"sha256"`
		Data   string        `json:"data_b64"`
		Events []ci.LogEvent `json:"events"`
		Fence  int64         `json:"fence"`
		Seq    int64         `json:"seq"`
		Offset int64         `json:"offset"`
	}
	if !readRunnerBody(w, r, &in) {
		return
	}
	token, ctrl, ok := d.attemptToken(r)
	if !ok {
		writeRunnerProblem(w, ciProblem("E_RECOVERING"))
		return
	}
	raw, derr := base64.StdEncoding.DecodeString(in.Data)
	if derr != nil {
		writeRunnerProblem(w, ciProblem("E_DIGEST_MISMATCH"))
		return
	}
	ack, err := ctrl.AppendLogs(r.Context(), r.PathValue("id"), token, ci.LogAppend{
		Fence: in.Fence, Seq: in.Seq, Offset: in.Offset,
		SHA256: in.SHA256, Data: raw, Events: in.Events,
	})
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	writeRunnerJSON(w, map[string]any{"ack_offset": ack, "next_seq": in.Seq + 1})
}

func (d Deps) runnerComplete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		FailedStep *int          `json:"failed_step"`
		Outcome    string        `json:"outcome"`
		Class      string        `json:"class"`
		Artifacts  []ci.Artifact `json:"artifacts"`
		Log        struct {
			SHA256    string `json:"sha256"`
			LastSeq   int64  `json:"last_seq"`
			EndOffset int64  `json:"end_offset"`
		} `json:"log"`
		Fence    int64 `json:"fence"`
		ExitCode int   `json:"exit_code"`
	}
	if !readRunnerBody(w, r, &in) {
		return
	}
	token, ctrl, ok := d.attemptToken(r)
	if !ok {
		writeRunnerProblem(w, ciProblem("E_RECOVERING"))
		return
	}
	req := ci.FinishRequest{
		Fence: in.Fence, Outcome: in.Outcome, Class: in.Class, ExitCode: in.ExitCode,
		LogSeq: in.Log.LastSeq, LogEnd: in.Log.EndOffset, LogDigest: in.Log.SHA256,
		Artifacts: in.Artifacts,
	}
	digest, derr := ci.FinishDigest(req)
	if derr != nil {
		writeRunnerProblem(w, derr)
		return
	}
	req.ResultDigest = digest
	reply, err := ctrl.Complete(r.Context(), r.PathValue("id"), token, req)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	writeRunnerJSON(w, map[string]any{
		"attempt": reply.Attempt, "job": reply.Job, "run": reply.Run, "replayed": reply.Replayed,
	})
}

func (d Deps) runnerRelease(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
		Fence  int64  `json:"fence"`
	}
	if !readRunnerBody(w, r, &in) {
		return
	}
	token, ctrl, ok := d.attemptToken(r)
	if !ok {
		writeRunnerProblem(w, ciProblem("E_RECOVERING"))
		return
	}
	if err := ctrl.Release(r.Context(), r.PathValue("id"), token, in.Fence, in.Reason); err != nil {
		writeRunnerProblem(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (d Deps) runnerBlob(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength <= 0 {
		writeRunnerProblem(w, ciProblem("E_INVALID"))
		return
	}
	if r.ContentLength > 1<<30 {
		writeRunnerProblem(w, ciProblem("E_LIMIT"))
		return
	}
	var fence int64
	if raw := r.Header.Get("X-Rukie-Fence"); raw != "" {
		parsed, perr := strconv.ParseInt(raw, 10, 64)
		if perr != nil {
			writeRunnerProblem(w, ciProblem("E_INVALID"))
			return
		}
		fence = parsed
	}
	raw, rerr := io.ReadAll(io.LimitReader(r.Body, (1<<30)+1))
	if rerr != nil {
		writeRunnerProblem(w, rerr)
		return
	}
	if int64(len(raw)) != r.ContentLength {
		writeRunnerProblem(w, ciProblem("E_INVALID"))
		return
	}
	token, ctrl, ok := d.attemptToken(r)
	if !ok {
		writeRunnerProblem(w, ciProblem("E_RECOVERING"))
		return
	}
	full := "sha256:" + strings.TrimPrefix(r.PathValue("digest"), "sha256:")
	if err := ctrl.PutBlob(r.Context(), r.PathValue("id"), token, fence, full, raw); err != nil {
		writeRunnerProblem(w, err)
		return
	}
	writeRunnerJSON(w, map[string]any{"digest": full, "size": len(raw)})
}

// CIRunnerHandler serves the runner-only protocol outside the op table.
func (d Deps) CIRunnerHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /_/ci/runner/v1/lease", d.runnerLease)
	mux.HandleFunc("POST /_/ci/runner/v1/attempts/{id}/start", d.runnerStart)
	mux.HandleFunc("POST /_/ci/runner/v1/attempts/{id}/heartbeat", d.runnerHeartbeat)
	mux.HandleFunc("POST /_/ci/runner/v1/attempts/{id}/logs", d.runnerLogs)
	mux.HandleFunc("POST /_/ci/runner/v1/attempts/{id}/complete", d.runnerComplete)
	mux.HandleFunc("POST /_/ci/runner/v1/attempts/{id}/release", d.runnerRelease)
	mux.HandleFunc("PUT /_/ci/runner/v1/attempts/{id}/blobs/{digest}", d.runnerBlob)
	return mux
}

func looksLikeCIBytes(path string) bool {
	trimmed := strings.TrimPrefix(path, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 5 || parts[2] != "ci" {
		return false
	}
	return parts[3] == "blobs"
}

func wantCIBytes(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if r.URL.Query().Has("download") {
		return true
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	if accept == "" || accept == "*/*" {
		return false
	}
	for media := range strings.SplitSeq(accept, ",") {
		kind, _, _ := strings.Cut(strings.TrimSpace(media), ";")
		if strings.TrimSpace(kind) == "application/octet-stream" {
			return true
		}
	}
	return r.URL.Query().Get("format") == "bytes"
}

// wantCIPage reports whether the request prefers the hand-authored CI page
// over the op mux rendering. JSON clients fall through to the op table so
// ci.run.get and the other dual-rendered ops stay reachable.
func wantCIPage(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	if accept == "" || accept == "*/*" {
		return true
	}
	for item := range strings.SplitSeq(accept, ",") {
		media, _, _ := strings.Cut(strings.TrimSpace(item), ";")
		if strings.TrimSpace(media) == "application/json" {
			return false
		}
	}
	return true
}

// CIRaw wraps the op mux to serve artifact bytes and the runner protocol.
func (d Deps) CIRaw(next http.Handler) http.Handler {
	runner := d.CIRunnerHandler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/_/ci/runner/v1/") {
			runner.ServeHTTP(w, r)
			return
		}
		if looksLikeCIBytes(r.URL.Path) && wantCIBytes(r) {
			d.serveCIBytes(w, r)
			return
		}
		if isCIArtifactDownload(r.URL.Path) {
			d.serveCIBytes(w, r)
			return
		}
		if wantCIPage(r) && d.serveCIPages(w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isCIArtifactDownload(path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	return len(parts) == 7 && parts[2] == "ci" && parts[3] == "runs" && parts[5] == "artifacts" && parts[6] != ""
}

func (d Deps) serveCIBytes(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		writeRunnerProblem(w, ciProblem("E_NOT_FOUND"))
		return
	}
	ctx := r.Context()
	principal := RawBytes{Deps: d}.principalFor(r)
	rq, rerr := d.requester(ctx, principal)
	if rerr != nil {
		writeRunnerProblem(w, rerr)
		return
	}
	rc, cerr := d.resolveRepo(ctx, rq, parts[0], parts[1], false)
	if cerr != nil {
		writeRunnerProblem(w, cerr)
		return
	}
	if derr := d.checkRepoAccess(rq, rc.row, "ci.view", ""); derr != nil {
		writeRunnerProblem(w, derr)
		return
	}
	ctrl, cerr := d.ciController()
	if cerr != nil {
		writeRunnerProblem(w, cerr)
		return
	}
	digest := ""
	name := ""
	switch {
	case parts[3] == "blobs" && len(parts) == 5:
		digest = "sha256:" + strings.TrimPrefix(parts[4], "sha256:")
		name = parts[4]
	case len(parts) == 7 && parts[3] == "runs" && parts[5] == "artifacts":
		run, jobs, rerr := ctrl.Run(ctx, parts[4])
		if rerr != nil || run.RepoID != rc.row.ID {
			writeRunnerProblem(w, op.Errorf(op.CodeNotFound, "CI run was not found"))
			return
		}
		for _, job := range jobs {
			artifacts, aerr := ctrl.Artifacts(ctx, job.ID)
			if aerr != nil {
				writeRunnerProblem(w, aerr)
				return
			}
			for _, artifact := range artifacts {
				if artifact.Name == parts[6] {
					digest = artifact.Digest
					name = artifact.Name
				}
			}
		}
		if digest == "" {
			writeRunnerProblem(w, op.Errorf(op.CodeNotFound, "CI artifact was not found"))
			return
		}
	default:
		writeRunnerProblem(w, op.Errorf(op.CodeNotFound, "CI blob was not found"))
		return
	}
	blob, ok := ctrl.RepoBlob(ctx, rc.row.ID, digest)
	if !ok {
		writeRunnerProblem(w, op.Errorf(op.CodeNotFound, "CI blob was not found"))
		return
	}
	w.Header().Set("Content-Type", sniffRawContentType(blob))
	w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
	if name != "" {
		w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(blob)
}
