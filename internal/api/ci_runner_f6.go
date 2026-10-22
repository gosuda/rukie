package api

import (
	"encoding/base64"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gosuda/rukie/internal/ci"
)

// CIRunnerV1 mounts the runner protocol outside the op table.
func (d Deps) CIRunnerV1(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /-/ci/v1/lease", d.f6Lease)
	mux.HandleFunc("POST /-/ci/v1/attempts/{id}/heartbeat", d.f6Heartbeat)
	mux.HandleFunc("POST /-/ci/v1/attempts/{id}/log.append", d.f6LogAppend)
	mux.HandleFunc("PUT /-/ci/v1/attempts/{id}/artifact.put/{digest}", d.f6ArtifactPut)
	mux.HandleFunc("POST /-/ci/v1/attempts/{id}/finish", d.f6Finish)
	mux.HandleFunc("GET /admin/runners", d.f6RunnersPage)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if runnerV1Route(r.URL.Path) {
			mux.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func runnerV1Route(path string) bool {
	if path == "/-/ci/v1/lease" || path == "/admin/runners" {
		return true
	}
	rest, ok := strings.CutPrefix(path, "/-/ci/v1/attempts/")
	if !ok {
		return false
	}
	id, tail, _ := strings.Cut(rest, "/")
	if id == "" {
		return false
	}
	if tail == "heartbeat" || tail == "log.append" || tail == "finish" {
		return true
	}
	digest, ok := strings.CutPrefix(tail, "artifact.put/")
	return ok && digest != ""
}

func (d Deps) f6Lease(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RunnerID string `json:"runner_id"`
		WaitMS   int    `json:"wait_ms"`
	}
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
			reply, serr := ctrl.Start(r.Context(), lease.Attempt.ID, lease.Token, lease.Attempt.Fence)
			if serr != nil {
				writeRunnerProblem(w, serr)
				return
			}
			_ = reply
			writeF6Grant(w, lease)
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

func writeF6Grant(w http.ResponseWriter, lease *ci.Lease) {
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
		"timers":  map[string]int64{"heartbeat_s": 10, "lease_ttl_s": 30, "self_fence_s": 300},
	})
}

func (d Deps) f6Heartbeat(w http.ResponseWriter, r *http.Request) {
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

func (d Deps) f6LogAppend(w http.ResponseWriter, r *http.Request) {
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

func (d Deps) f6ArtifactPut(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength < 0 {
		writeRunnerProblem(w, ciProblem("E_INVALID"))
		return
	}
	if r.ContentLength > 1<<30 {
		writeRunnerProblem(w, ciProblem("E_LIMIT"))
		return
	}
	fence := runnerFenceHeader(r)
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

func runnerFenceHeader(r *http.Request) int64 {
	for _, key := range []string{"Rukie-Fence", "X-Rukie-Fence"} {
		if raw := r.Header.Get(key); raw != "" {
			fence, err := strconv.ParseInt(raw, 10, 64)
			if err == nil {
				return fence
			}
		}
	}
	return 0
}

func (d Deps) f6Finish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Outcome   string        `json:"outcome"`
		Class     string        `json:"class"`
		Artifacts []ci.Artifact `json:"artifacts"`
		Log       struct {
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

func (d Deps) f6RunnersPage(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdmin(r.Context(), d.Service, d.ciPrincipal(r)); err != nil {
		writeRunnerProblem(w, err)
		return
	}
	ctrl, err := d.ciController()
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	runners, err := ctrl.Runners(r.Context())
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	runs, err := ctrl.Runs(r.Context(), 0)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	queued := 0
	for _, run := range runs {
		_, jobs, jerr := ctrl.Run(r.Context(), run.ID)
		if jerr != nil {
			writeRunnerProblem(w, jerr)
			return
		}
		for _, job := range jobs {
			if job.State == "queued" || job.State == "leased" || job.State == "running" {
				queued++
			}
		}
	}
	var b strings.Builder
	b.WriteString(heading("CI runners"))
	b.WriteString("<p>" + esc(strconv.Itoa(queued)) + " active jobs, " +
		esc(strconv.Itoa(len(runners))) + " runners. Runner tokens are issued through the ci.runner.create operation or rukie runner register.</p>\n")
	b.WriteString("<table>\n<tr><th>Runner</th><th>Scope</th><th>Labels</th><th>Capacity</th><th>Status</th></tr>\n")
	for _, runner := range runners {
		b.WriteString("<tr><td>" + esc(runner.ID) + "</td><td>" + esc(runner.Scope) + "</td>" +
			"<td>" + esc(strings.Join(runner.Labels, ", ")) + "</td>" +
			"<td>" + esc(strconv.Itoa(runner.Capacity)) + "</td><td>" + esc(runner.Status) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	setCIPageHeaders(w)
	_, _ = io.WriteString(w, pageShell("CI runners", b.String()))
}
