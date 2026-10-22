package api

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/gosuda/rukie/internal/mirror"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

// MirrorFlagsJSON carries mirror toggles.
type MirrorFlagsJSON struct {
	IncludeNotes         bool `json:"include_notes"`
	ProtectDefaultBranch bool `json:"protect_default_branch"`
	Enabled              bool `json:"enabled"`
	SyncOnRefUpdate      bool `json:"sync_on_ref_update"`
	CreateIfMissing      bool `json:"create_if_missing"`
	PolicyAck            bool `json:"policy_ack"`
	AdoptExistingRefs    bool `json:"adopt_existing_refs"`
	Adopted              bool `json:"adopted"`
}

// MirrorErrorJSON carries mirror error detail without secret material.
type MirrorErrorJSON struct {
	Class      string `json:"class"`
	Message    string `json:"message"`
	HTTPStatus int64  `json:"http_status"`
}

// MirrorCredentialJSON carries non-secret credential metadata.
type MirrorCredentialJSON struct {
	Last4      string `json:"last4"`
	KeyVersion int64  `json:"key_version"`
	CreatedAt  int64  `json:"created_at"`
	RotatedAt  int64  `json:"rotated_at"`
	LastUsedAt int64  `json:"last_used_at"`
}

// MirrorJSON is one push mirror without secret material.
type MirrorJSON struct {
	ID                  string                `json:"id"`
	Repo                int64                 `json:"repo"`
	Name                string                `json:"name"`
	TargetURL           string                `json:"target_url"`
	HostKind            string                `json:"host_kind"`
	Username            string                `json:"username"`
	Mode                string                `json:"mode"`
	RefSelection        string                `json:"ref_selection"`
	Globs               []string              `json:"globs"`
	Flags               MirrorFlagsJSON       `json:"flags"`
	State               string                `json:"state"`
	ConsecutiveFailures int64                 `json:"consecutive_failures"`
	LastError           MirrorErrorJSON       `json:"last_error"`
	LastSuccessAt       int64                 `json:"last_success_at"`
	LastAttemptAt       int64                 `json:"last_attempt_at"`
	NextRunAt           int64                 `json:"next_run_at"`
	Credential          *MirrorCredentialJSON `json:"credential,omitempty"`
}

// RefStateJSON is one mirror ref state.
type RefStateJSON struct {
	Ref                 string `json:"ref"`
	LocalOid            string `json:"local_oid"`
	RemoteOid           string `json:"remote_oid"`
	Status              string `json:"status"`
	Reason              string `json:"reason"`
	ReasonClass         string `json:"reason_class"`
	FirstFailedAt       int64  `json:"first_failed_at"`
	LastAttemptAt       int64  `json:"last_attempt_at"`
	LastSuccessAt       int64  `json:"last_success_at"`
	ConsecutiveFailures int64  `json:"consecutive_failures"`
}

// RunOutcomeJSON carries run outcome detail.
type RunOutcomeJSON struct {
	Class   string `json:"class"`
	Summary string `json:"summary"`
	Counts  string `json:"counts"`
}

// RunJSON is one mirror sync run.
type RunJSON struct {
	ID         string         `json:"id"`
	MirrorID   int64          `json:"mirror_id"`
	Kind       string         `json:"kind"`
	State      string         `json:"state"`
	Trigger    string         `json:"trigger"`
	AttemptNo  int64          `json:"attempt_no"`
	PlanDigest string         `json:"plan_digest"`
	Outcome    RunOutcomeJSON `json:"outcome"`
	StartedAt  int64          `json:"started_at"`
	FinishedAt int64          `json:"finished_at"`
}

// MirrorPlanJSON is one planned ref change.
type MirrorPlanJSON struct {
	Ref    string `json:"ref"`
	Old    string `json:"old"`
	New    string `json:"new"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// MirrorPlanOut is one computed sync plan.
type MirrorPlanOut struct {
	Updates        []MirrorPlanJSON `json:"updates"`
	Deletes        []MirrorPlanJSON `json:"deletes"`
	Diverged       []MirrorPlanJSON `json:"diverged"`
	Ignored        []MirrorPlanJSON `json:"ignored"`
	Digest         string           `json:"digest"`
	TargetNonEmpty bool             `json:"target_non_empty"`
}

// MirrorProbeJSON records one remote probe.
type MirrorProbeJSON struct {
	Ok    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func parseMirrorGlobs(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var globs []string
	if uerr := unmarshalObject([]byte(trimmed), &globs); uerr == nil {
		return globs, nil
	}
	return nil, op.Wrap(op.CodeInternal, errors.New("mirror globs are corrupt"), "mirror globs are corrupt")
}

func mirrorRowConfig(row store.MirrorRow) (mirror.Mirror, error) {
	globs, gerr := parseMirrorGlobs(row.Globs)
	if gerr != nil {
		return mirror.Mirror{}, gerr
	}
	return mirror.Mirror{
		Name:                     row.Name,
		TargetURL:                row.TargetURL,
		HostKind:                 mirror.HostKind(row.HostKind),
		Username:                 row.Username,
		Mode:                     mirror.Mode(row.Mode),
		RefSelection:             mirror.RefSelection(row.RefSelection),
		Globs:                    globs,
		IncludeNotes:             row.IncludeNotes,
		ProtectDefaultBranch:     row.ProtectDefaultBranch,
		Enabled:                  row.Enabled,
		SyncOnRefUpdate:          row.SyncOnRefUpdate,
		DebounceSeconds:          row.DebounceSeconds,
		MaxDelaySeconds:          row.MaxDelaySeconds,
		ReconcileIntervalSeconds: row.ReconcileIntervalSeconds,
		CreateIfMissing:          row.CreateIfMissing,
		PolicyAck:                row.PolicyAck,
		AdoptExistingRefs:        row.AdoptExistingRefs,
	}, nil
}

func mirrorJSON(row store.MirrorRow, cred *store.CredentialRow) MirrorJSON {
	globs, _ := parseMirrorGlobs(row.Globs)
	if globs == nil {
		globs = []string{}
	}
	out := MirrorJSON{
		ID: row.PublicID, Repo: row.RepoID, Name: row.Name,
		TargetURL: row.TargetURL, HostKind: row.HostKind, Username: row.Username,
		Mode: row.Mode, RefSelection: row.RefSelection, Globs: globs,
		Flags: MirrorFlagsJSON{
			IncludeNotes: row.IncludeNotes, ProtectDefaultBranch: row.ProtectDefaultBranch,
			Enabled: row.Enabled, SyncOnRefUpdate: row.SyncOnRefUpdate,
			CreateIfMissing: row.CreateIfMissing, PolicyAck: row.PolicyAck,
			AdoptExistingRefs: row.AdoptExistingRefs, Adopted: row.Adopted,
		},
		State: row.State, ConsecutiveFailures: row.ConsecutiveFailures,
		LastError: MirrorErrorJSON{
			Class: row.LastErrorClass, Message: row.LastError, HTTPStatus: row.LastHTTPStatus,
		},
		LastSuccessAt: row.LastSuccessAt, LastAttemptAt: row.LastAttemptAt, NextRunAt: row.NextRunAt,
	}
	if cred != nil {
		out.Credential = &MirrorCredentialJSON{
			Last4: cred.Last4, KeyVersion: cred.KeyVersion,
			CreatedAt: cred.CreatedAt, RotatedAt: cred.RotatedAt, LastUsedAt: cred.LastUsedAt,
		}
	}
	return out
}

func refStateJSON(row store.RefStateRow) RefStateJSON {
	return RefStateJSON{
		Ref: row.Ref, LocalOid: row.LocalOid, RemoteOid: row.RemoteOid,
		Status: row.Status, Reason: row.Reason, ReasonClass: row.ReasonClass,
		FirstFailedAt: row.FirstFailedAt, LastAttemptAt: row.LastAttemptAt,
		LastSuccessAt: row.LastSuccessAt, ConsecutiveFailures: row.ConsecutiveFailures,
	}
}

func runJSON(row store.MirrorRunRow) RunJSON {
	return RunJSON{
		ID: row.PublicID, MirrorID: row.MirrorID, Kind: row.Kind, State: row.State,
		Trigger: row.Trigger, AttemptNo: row.AttemptNo, PlanDigest: row.PlanDigest,
		Outcome:   RunOutcomeJSON{Class: row.OutcomeClass, Summary: row.OutcomeSummary, Counts: row.Counts},
		StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
	}
}

func mirrorPlanOut(plan mirror.Plan) MirrorPlanOut {
	out := MirrorPlanOut{
		Updates: []MirrorPlanJSON{}, Deletes: []MirrorPlanJSON{},
		Diverged: []MirrorPlanJSON{}, Ignored: []MirrorPlanJSON{},
		Digest: plan.Digest, TargetNonEmpty: plan.TargetNonEmpty,
	}
	for _, entry := range plan.Updates {
		out.Updates = append(out.Updates, MirrorPlanJSON{
			Ref: entry.Ref, Old: entry.Old, New: entry.New,
			Action: string(entry.Action), Reason: entry.Reason,
		})
	}
	for _, entry := range plan.Deletes {
		out.Deletes = append(out.Deletes, MirrorPlanJSON{
			Ref: entry.Ref, Old: entry.Old, New: entry.New,
			Action: string(entry.Action), Reason: entry.Reason,
		})
	}
	for _, entry := range plan.Diverged {
		out.Diverged = append(out.Diverged, MirrorPlanJSON{
			Ref: entry.Ref, Old: entry.Old, New: entry.New,
			Action: string(entry.Action), Reason: entry.Reason,
		})
	}
	for _, entry := range plan.Ignored {
		out.Ignored = append(out.Ignored, MirrorPlanJSON{
			Ref: entry.Ref, Old: entry.Old, New: entry.New,
			Action: string(entry.Action), Reason: entry.Reason,
		})
	}
	return out
}

func loadMirrorRow(ctx context.Context, d Deps, rc repoContext, publicID string) (store.MirrorRow, error) {
	var row store.MirrorRow
	serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		var gerr error
		row, gerr = tx.GetMirrorByPublicID(publicID)
		return gerr
	})
	if serr != nil {
		return store.MirrorRow{}, storeToOp(serr, "mirror was not found")
	}
	if row.RepoID != rc.row.ID {
		return store.MirrorRow{}, op.Errorf(op.CodeNotFound, "mirror was not found")
	}
	return row, nil
}

func loadMirrorCredential(ctx context.Context, d Deps, mirrorID int64) *store.CredentialRow {
	var cred store.CredentialRow
	serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		var gerr error
		cred, gerr = tx.GetCredential(mirrorID)
		return gerr
	})
	if serr != nil {
		return nil
	}
	return &cred
}

func mirrorNameTaken(ctx context.Context, d Deps, repoID int64, name string) (bool, error) {
	taken := false
	rerr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		_, gerr := tx.GetMirrorByRepoName(repoID, name)
		if gerr == nil {
			taken = true
			return nil
		}
		if errors.Is(gerr, store.ErrNotFound) {
			return nil
		}
		return gerr
	})
	return taken, rerr
}

func enqueueMirrorRun(ctx context.Context, d Deps, repoID int64, runPub string, mirrorID int64) error {
	if d.Queue == nil {
		return op.Errorf(op.CodeUnavailable, "job queue is not configured")
	}
	payload, merr := marshalJSON(map[string]any{"run": runPub, "mirror": mirrorID})
	if merr != nil {
		return op.Wrap(op.CodeInternal, merr, "run payload encoding failed")
	}
	_, eerr := d.Queue.Enqueue(ctx, queue.EnqueueSpec{
		Payload: string(payload), ID: "mirror-run:" + runPub,
		Kind: queue.KindMirrorRun, RepoID: repoID,
	})
	if eerr != nil {
		return op.Wrap(op.CodeInternal, eerr, "run enqueue failed")
	}
	return nil
}

func (d Deps) collectMirrorLocal(ctx context.Context, daemon ruk.Repo) (map[string]string, error) {
	if d.Ruk == nil {
		return nil, op.Errorf(op.CodeUnavailable, "ruk backend is not configured")
	}
	out := map[string]string{}
	var after *string
	for {
		reply, rerr := ruk.RefsList(ctx, d.Ruk, daemon, ruk.RefsListArgs{After: after, Limit: ruk.MaxRefsLimit})
		if rerr != nil {
			return nil, mapRukError(rerr)
		}
		for _, entry := range reply.Refs.Items {
			if entry.Target.Direct == nil {
				continue
			}
			out[entry.Name] = *entry.Target.Direct
		}
		if reply.Refs.NextAfter == nil || *reply.Refs.NextAfter == "" {
			return out, nil
		}
		after = reply.Refs.NextAfter
	}
}

func (d Deps) collectMirrorRemote(ctx context.Context, daemon ruk.Repo, cfg mirror.Mirror, token string) (map[string]string, string, error) {
	if d.Ruk == nil {
		return nil, "", op.Errorf(op.CodeUnavailable, "ruk backend is not configured")
	}
	auth, aerr := wire.AuthForMirror(cfg.Username, token, cfg.TargetURL)
	if aerr != nil {
		return nil, "", op.Wrap(op.CodeValidation, aerr, "remote credentials are not valid")
	}
	reply, rerr := ruk.RemoteRefs(ctx, d.Ruk, daemon, ruk.RemoteRefsArgs{URL: cfg.TargetURL}, auth)
	if rerr != nil {
		return nil, "", mapRukError(rerr)
	}
	refs := make(map[string]string, len(reply.Refs))
	for _, entry := range reply.Refs {
		if entry.Target.Direct == nil {
			continue
		}
		refs[entry.Name] = *entry.Target.Direct
	}
	def := "main"
	for _, entry := range reply.Refs {
		if entry.Name == "HEAD" && entry.Target.Symbolic != nil {
			if branch, ok := strings.CutPrefix(*entry.Target.Symbolic, "refs/heads/"); ok && branch != "" {
				def = branch
			}
		}
	}
	return refs, def, nil
}

func scrubToken(text, token string) string {
	if token == "" {
		return firstLine(text)
	}
	return firstLine(strings.ReplaceAll(text, token, "[redacted]"))
}

// MirrorListIn carries one mirror list request.
type MirrorListIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

// MirrorListOut carries one repository mirror page.
type MirrorListOut struct {
	Next    string       `json:"-"`
	Mirrors []MirrorJSON `json:"mirrors"`
}

func mirrorListHandler(d Deps) func(context.Context, op.Principal, MirrorListIn) (MirrorListOut, error) {
	return func(ctx context.Context, p op.Principal, in MirrorListIn) (MirrorListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return MirrorListOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return MirrorListOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "mirror.view", ""); derr != nil {
			return MirrorListOut{}, derr
		}
		out := MirrorListOut{Mirrors: []MirrorJSON{}}
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			rows, next, lerr := tx.ListMirrors(rc.row.ID, in.Cursor, in.Limit)
			if lerr != nil {
				return lerr
			}
			for _, row := range rows {
				cred, _ := func() (*store.CredentialRow, error) {
					got, gerr := tx.GetCredential(row.ID)
					if gerr != nil {
						return nil, gerr
					}
					return &got, nil
				}()
				out.Mirrors = append(out.Mirrors, mirrorJSON(row, cred))
			}
			out.Next = next
			return nil
		})
		if serr != nil {
			return MirrorListOut{}, storeToOp(serr, "mirror list failed")
		}
		return out, nil
	}
}

// MirrorCreateBody carries one mirror create request.
type MirrorCreateBody struct {
	Name                     string   `json:"name" minLength:"1" maxLength:"100"`
	TargetURL                string   `json:"target_url" minLength:"1" maxLength:"2048"`
	HostKind                 string   `json:"host_kind" minLength:"1" maxLength:"32"`
	Username                 string   `json:"username,omitempty" maxLength:"256"`
	Mode                     string   `json:"mode,omitempty" maxLength:"32"`
	RefSelection             string   `json:"ref_selection,omitempty" maxLength:"32"`
	Globs                    []string `json:"globs,omitempty"`
	IncludeNotes             bool     `json:"include_notes,omitzero"`
	ProtectDefaultBranch     bool     `json:"protect_default_branch,omitzero"`
	Enabled                  *bool    `json:"enabled,omitzero"`
	SyncOnRefUpdate          *bool    `json:"sync_on_ref_update,omitzero"`
	DebounceSeconds          uint64   `json:"debounce_seconds,omitzero"`
	MaxDelaySeconds          uint64   `json:"max_delay_seconds,omitzero"`
	ReconcileIntervalSeconds uint64   `json:"reconcile_interval_seconds,omitzero"`
	CreateIfMissing          bool     `json:"create_if_missing,omitzero"`
	PolicyAck                bool     `json:"policy_ack,omitzero"`
	AdoptExistingRefs        bool     `json:"adopt_existing_refs,omitzero"`
	Token                    string   `json:"token" minLength:"1" maxLength:"4096"`
}

// MirrorCreateIn carries one mirror create request with its idempotency key.
type MirrorCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Body           MirrorCreateBody
}

// MirrorCreateOut carries the created mirror with its probe record.
type MirrorCreateOut struct {
	Mirror MirrorJSON      `json:"mirror"`
	Probe  MirrorProbeJSON `json:"probe"`
}

func mirrorCreateHandler(d Deps) func(context.Context, op.Principal, MirrorCreateIn) (MirrorCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in MirrorCreateIn) (MirrorCreateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return MirrorCreateOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return MirrorCreateOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "mirror.manage", ""); derr != nil {
			return MirrorCreateOut{}, derr
		}
		if in.Body.Token == "" {
			return MirrorCreateOut{}, op.Errorf(op.CodeInvalid, "mirror token is required")
		}
		mode := cmp.Or(in.Body.Mode, "fast-forward-only")
		selection := cmp.Or(in.Body.RefSelection, "all")
		enabled := in.Body.Enabled == nil || *in.Body.Enabled
		syncOnRef := in.Body.SyncOnRefUpdate == nil || *in.Body.SyncOnRefUpdate
		debounce := in.Body.DebounceSeconds
		if syncOnRef && debounce == 0 {
			debounce = 10
		}
		cfg := mirror.Mirror{
			Name: in.Body.Name, TargetURL: in.Body.TargetURL,
			HostKind: mirror.HostKind(in.Body.HostKind), Username: in.Body.Username,
			Mode: mirror.Mode(mode), RefSelection: mirror.RefSelection(selection),
			Globs: in.Body.Globs, IncludeNotes: in.Body.IncludeNotes,
			ProtectDefaultBranch: in.Body.ProtectDefaultBranch, Enabled: enabled,
			SyncOnRefUpdate: syncOnRef, DebounceSeconds: debounce,
			MaxDelaySeconds:          in.Body.MaxDelaySeconds,
			ReconcileIntervalSeconds: in.Body.ReconcileIntervalSeconds,
			CreateIfMissing:          in.Body.CreateIfMissing, PolicyAck: in.Body.PolicyAck,
			AdoptExistingRefs: in.Body.AdoptExistingRefs,
		}
		if verr := cfg.Validate(d.now()); verr != nil {
			return MirrorCreateOut{}, op.Wrap(op.CodeValidation, verr, "mirror configuration is not valid")
		}
		globsRaw, merr := marshalJSON(cfg.Globs)
		if merr != nil {
			return MirrorCreateOut{}, op.Wrap(op.CodeInternal, merr, "mirror globs encoding failed")
		}
		if len(cfg.Globs) == 0 {
			globsRaw = []byte("[]")
		}
		probe := MirrorProbeJSON{}
		remote := map[string]string{}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			probe = MirrorProbeJSON{Ok: false, Error: scrubToken(oerr.Error(), in.Body.Token)}
		} else {
			refs, _, rerr := d.collectMirrorRemote(ctx, daemon, cfg, in.Body.Token)
			if rerr != nil {
				probe = MirrorProbeJSON{Ok: false, Error: scrubToken(rerr.Error(), in.Body.Token)}
			} else {
				probe = MirrorProbeJSON{Ok: true}
				remote = refs
			}
		}
		if cfg.Mode == mirror.ModeExact && !cfg.AdoptExistingRefs && (len(remote) > 0 || !probe.Ok) {
			if len(remote) > 0 {
				return MirrorCreateOut{}, op.Errorf(op.CodeInvalid, "target is not empty; set adopt_existing_refs to confirm")
			}
			return MirrorCreateOut{}, op.Errorf(op.CodeInvalid, "target could not be probed; set adopt_existing_refs to confirm")
		}
		if len(d.SealKey) == 0 {
			return MirrorCreateOut{}, op.Errorf(op.CodeInternal, "mirror seal key is not configured")
		}
		taken, terr := mirrorNameTaken(ctx, d, rc.row.ID, cfg.Name)
		if terr != nil {
			return MirrorCreateOut{}, op.Wrap(op.CodeInternal, terr, "mirror lookup failed")
		}
		if taken {
			return MirrorCreateOut{}, op.Errorf(op.CodeConflict, "mirror name is taken")
		}
		var row store.MirrorRow
		var cred store.CredentialRow
		var runPub string
		now := d.now()
		serr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			inserted, ierr := tx.InsertMirror(now, store.MirrorInsert{
				RepoID: rc.row.ID, Name: cfg.Name, TargetURL: cfg.TargetURL,
				HostKind: string(cfg.HostKind), Username: cfg.Username,
				Mode: string(cfg.Mode), RefSelection: string(cfg.RefSelection),
				Globs: string(globsRaw), IncludeNotes: cfg.IncludeNotes,
				ProtectDefaultBranch: cfg.ProtectDefaultBranch, Enabled: cfg.Enabled,
				SyncOnRefUpdate: cfg.SyncOnRefUpdate, CreateIfMissing: cfg.CreateIfMissing,
				PolicyAck: cfg.PolicyAck, AdoptExistingRefs: cfg.AdoptExistingRefs,
				DebounceSeconds: cfg.DebounceSeconds, MaxDelaySeconds: cfg.MaxDelaySeconds,
				ReconcileIntervalSeconds: cfg.ReconcileIntervalSeconds, State: "idle",
			})
			if ierr != nil {
				return ierr
			}
			tokenBytes := []byte(in.Body.Token)
			sealed, serr := mirror.Seal(d.SealKey, tokenBytes, "mirror:"+inserted.PublicID, 1, mirror.Last4(in.Body.Token))
			mirror.Zero(tokenBytes)
			if serr != nil {
				return op.Wrap(op.CodeInternal, serr, "mirror credential seal failed")
			}
			put := store.CredentialRow{
				MirrorID: inserted.ID, KeyVersion: int64(sealed.KeyVersion),
				Nonce: sealed.Nonce, Ciphertext: sealed.Ciphertext, Last4: sealed.Last4,
				CreatedAt: now.UnixMilli(),
			}
			if perr := tx.PutCredential(put); perr != nil {
				return perr
			}
			run, rerr := tx.InsertRun(now, store.MirrorRunRow{
				MirrorID: inserted.ID, Kind: "sync", State: "queued", Trigger: "manual",
			})
			if rerr != nil {
				return rerr
			}
			row, cred, runPub = inserted, put, run.PublicID
			return nil
		})
		if serr != nil {
			if oerr, ok := errors.AsType[*op.Error](serr); ok {
				return MirrorCreateOut{}, oerr
			}
			if errors.Is(serr, store.ErrInvalid) {
				taken, terr := mirrorNameTaken(ctx, d, rc.row.ID, cfg.Name)
				if terr != nil {
					return MirrorCreateOut{}, op.Wrap(op.CodeInternal, terr, "mirror lookup failed")
				}
				if taken {
					return MirrorCreateOut{}, op.Errorf(op.CodeConflict, "mirror name is taken")
				}
				return MirrorCreateOut{}, op.Errorf(op.CodeInvalid, "mirror configuration is not valid")
			}
			return MirrorCreateOut{}, op.Wrap(op.CodeInternal, serr, "mirror store failed")
		}
		if eerr := enqueueMirrorRun(ctx, d, rc.row.ID, runPub, row.ID); eerr != nil {
			return MirrorCreateOut{}, eerr
		}
		return MirrorCreateOut{Mirror: mirrorJSON(row, &cred), Probe: probe}, nil
	}
}

// MirrorGetIn carries one mirror read.
type MirrorGetIn struct {
	Owner       string `path:"owner"`
	Repo        string `path:"repo"`
	ID          string `path:"id"`
	IncludeRefs bool   `query:"include_refs"`
	Cursor      string `query:"cursor"`
	Limit       int    `query:"limit"`
}

// MirrorGetOut carries one mirror with an optional ref page.
type MirrorGetOut struct {
	Next   string         `json:"-"`
	Mirror MirrorJSON     `json:"mirror"`
	Refs   []RefStateJSON `json:"refs"`
}

func mirrorGetHandler(d Deps) func(context.Context, op.Principal, MirrorGetIn) (MirrorGetOut, error) {
	return func(ctx context.Context, p op.Principal, in MirrorGetIn) (MirrorGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return MirrorGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return MirrorGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "mirror.view", ""); derr != nil {
			return MirrorGetOut{}, derr
		}
		row, lerr := loadMirrorRow(ctx, d, rc, in.ID)
		if lerr != nil {
			return MirrorGetOut{}, lerr
		}
		out := MirrorGetOut{Mirror: mirrorJSON(row, loadMirrorCredential(ctx, d, row.ID)), Refs: []RefStateJSON{}}
		if !in.IncludeRefs {
			return out, nil
		}
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			rows, next, lerr := tx.ListRefStates(row.ID, in.Cursor, in.Limit)
			if lerr != nil {
				return lerr
			}
			for _, state := range rows {
				out.Refs = append(out.Refs, refStateJSON(state))
			}
			out.Next = next
			return nil
		})
		if serr != nil {
			return MirrorGetOut{}, storeToOp(serr, "mirror refs failed")
		}
		return out, nil
	}
}

// MirrorUpdateBody carries one partial mirror update.
type MirrorUpdateBody struct {
	Name                     *string   `json:"name,omitempty" maxLength:"100"`
	TargetURL                *string   `json:"target_url,omitempty" maxLength:"2048"`
	HostKind                 *string   `json:"host_kind,omitempty" maxLength:"32"`
	Username                 *string   `json:"username,omitempty" maxLength:"256"`
	Mode                     *string   `json:"mode,omitempty" maxLength:"32"`
	RefSelection             *string   `json:"ref_selection,omitempty" maxLength:"32"`
	Globs                    *[]string `json:"globs,omitempty"`
	IncludeNotes             *bool     `json:"include_notes,omitzero"`
	ProtectDefaultBranch     *bool     `json:"protect_default_branch,omitzero"`
	Enabled                  *bool     `json:"enabled,omitzero"`
	SyncOnRefUpdate          *bool     `json:"sync_on_ref_update,omitzero"`
	DebounceSeconds          *uint64   `json:"debounce_seconds,omitzero"`
	MaxDelaySeconds          *uint64   `json:"max_delay_seconds,omitzero"`
	ReconcileIntervalSeconds *uint64   `json:"reconcile_interval_seconds,omitzero"`
	CreateIfMissing          *bool     `json:"create_if_missing,omitzero"`
	PolicyAck                *bool     `json:"policy_ack,omitzero"`
	AdoptExistingRefs        *bool     `json:"adopt_existing_refs,omitzero"`
	Token                    *string   `json:"token,omitempty" maxLength:"4096"`
}

// MirrorUpdateIn carries one mirror update with its idempotency key.
type MirrorUpdateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	ID             string `path:"id"`
	Body           MirrorUpdateBody
}

// MirrorUpdateOut carries the updated mirror.
type MirrorUpdateOut struct {
	Mirror MirrorJSON `json:"mirror"`
}

func applyMirrorUpdate(row store.MirrorRow, body MirrorUpdateBody) (mirror.Mirror, store.MirrorPatch, bool, error) {
	cfg, cerr := mirrorRowConfig(row)
	if cerr != nil {
		return mirror.Mirror{}, store.MirrorPatch{}, false, cerr
	}
	var patch store.MirrorPatch
	changed := false
	if body.Name != nil {
		if *body.Name == "" {
			return mirror.Mirror{}, store.MirrorPatch{}, false, op.Errorf(op.CodeValidation, "mirror name must not be empty")
		}
		if *body.Name != cfg.Name {
			cfg.Name, changed = *body.Name, true
		}
		patch.Name = body.Name
	}
	if body.TargetURL != nil {
		if *body.TargetURL != cfg.TargetURL {
			cfg.TargetURL, changed = *body.TargetURL, true
		}
		patch.TargetURL = body.TargetURL
	}
	if body.HostKind != nil {
		if mirror.HostKind(*body.HostKind) != cfg.HostKind {
			cfg.HostKind, changed = mirror.HostKind(*body.HostKind), true
		}
		patch.HostKind = body.HostKind
	}
	if body.Username != nil {
		if *body.Username != cfg.Username {
			cfg.Username, changed = *body.Username, true
		}
		patch.Username = body.Username
	}
	if body.Mode != nil {
		if mirror.Mode(*body.Mode) != cfg.Mode {
			cfg.Mode, changed = mirror.Mode(*body.Mode), true
		}
		patch.Mode = body.Mode
	}
	if body.RefSelection != nil {
		if mirror.RefSelection(*body.RefSelection) != cfg.RefSelection {
			cfg.RefSelection, changed = mirror.RefSelection(*body.RefSelection), true
		}
		patch.RefSelection = body.RefSelection
	}
	if body.Globs != nil {
		if !slices.Equal(*body.Globs, cfg.Globs) {
			cfg.Globs, changed = *body.Globs, true
		}
		raw, merr := marshalJSON(*body.Globs)
		if merr != nil {
			return mirror.Mirror{}, store.MirrorPatch{}, false, op.Wrap(op.CodeInternal, merr, "mirror globs encoding failed")
		}
		text := string(raw)
		if len(*body.Globs) == 0 {
			text = "[]"
		}
		patch.Globs = &text
	}
	if body.IncludeNotes != nil {
		if *body.IncludeNotes != cfg.IncludeNotes {
			cfg.IncludeNotes, changed = *body.IncludeNotes, true
		}
		patch.IncludeNotes = body.IncludeNotes
	}
	if body.ProtectDefaultBranch != nil {
		if *body.ProtectDefaultBranch != cfg.ProtectDefaultBranch {
			cfg.ProtectDefaultBranch, changed = *body.ProtectDefaultBranch, true
		}
		patch.ProtectDefaultBranch = body.ProtectDefaultBranch
	}
	if body.Enabled != nil {
		if *body.Enabled != cfg.Enabled {
			cfg.Enabled, changed = *body.Enabled, true
		}
		patch.Enabled = body.Enabled
	}
	if body.SyncOnRefUpdate != nil {
		if *body.SyncOnRefUpdate != cfg.SyncOnRefUpdate {
			cfg.SyncOnRefUpdate, changed = *body.SyncOnRefUpdate, true
		}
		patch.SyncOnRefUpdate = body.SyncOnRefUpdate
	}
	if body.DebounceSeconds != nil {
		if *body.DebounceSeconds != cfg.DebounceSeconds {
			cfg.DebounceSeconds, changed = *body.DebounceSeconds, true
		}
		patch.DebounceSeconds = body.DebounceSeconds
	}
	if body.MaxDelaySeconds != nil {
		if *body.MaxDelaySeconds != cfg.MaxDelaySeconds {
			cfg.MaxDelaySeconds, changed = *body.MaxDelaySeconds, true
		}
		patch.MaxDelaySeconds = body.MaxDelaySeconds
	}
	if body.ReconcileIntervalSeconds != nil {
		if *body.ReconcileIntervalSeconds != cfg.ReconcileIntervalSeconds {
			cfg.ReconcileIntervalSeconds, changed = *body.ReconcileIntervalSeconds, true
		}
		patch.ReconcileIntervalSeconds = body.ReconcileIntervalSeconds
	}
	if body.CreateIfMissing != nil {
		if *body.CreateIfMissing != cfg.CreateIfMissing {
			cfg.CreateIfMissing, changed = *body.CreateIfMissing, true
		}
		patch.CreateIfMissing = body.CreateIfMissing
	}
	if body.PolicyAck != nil {
		if *body.PolicyAck != cfg.PolicyAck {
			cfg.PolicyAck, changed = *body.PolicyAck, true
		}
		patch.PolicyAck = body.PolicyAck
	}
	if body.AdoptExistingRefs != nil {
		if *body.AdoptExistingRefs != cfg.AdoptExistingRefs {
			cfg.AdoptExistingRefs, changed = *body.AdoptExistingRefs, true
		}
		patch.AdoptExistingRefs = body.AdoptExistingRefs
	}
	return cfg, patch, changed, nil
}

func mirrorUpdateHandler(d Deps) func(context.Context, op.Principal, MirrorUpdateIn) (MirrorUpdateOut, error) {
	return func(ctx context.Context, p op.Principal, in MirrorUpdateIn) (MirrorUpdateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return MirrorUpdateOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return MirrorUpdateOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "mirror.manage", ""); derr != nil {
			return MirrorUpdateOut{}, derr
		}
		row, lerr := loadMirrorRow(ctx, d, rc, in.ID)
		if lerr != nil {
			return MirrorUpdateOut{}, lerr
		}
		cfg, patch, changed, aerr := applyMirrorUpdate(row, in.Body)
		if aerr != nil {
			return MirrorUpdateOut{}, aerr
		}
		if verr := cfg.Validate(d.now()); verr != nil {
			return MirrorUpdateOut{}, op.Wrap(op.CodeValidation, verr, "mirror configuration is not valid")
		}
		rotate := in.Body.Token != nil && *in.Body.Token != ""
		if rotate && len(d.SealKey) == 0 {
			return MirrorUpdateOut{}, op.Errorf(op.CodeInternal, "mirror seal key is not configured")
		}
		var oldCred *store.CredentialRow
		if rotate {
			if existing := loadMirrorCredential(ctx, d, row.ID); existing != nil {
				oldCred = existing
			}
		}
		var updated store.MirrorRow
		var cred store.CredentialRow
		var runPub string
		now := d.now()
		serr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			next, uerr := tx.UpdateMirror(now, row.ID, patch)
			if uerr != nil {
				return uerr
			}
			updated = next
			if rotate {
				version := int64(1)
				created, lastUsed := now.UnixMilli(), int64(0)
				if oldCred != nil {
					version, created, lastUsed = oldCred.KeyVersion+1, oldCred.CreatedAt, oldCred.LastUsedAt
				}
				tokenBytes := []byte(*in.Body.Token)
				sealed, serr := mirror.Seal(d.SealKey, tokenBytes, "mirror:"+row.PublicID, uint32(version), mirror.Last4(*in.Body.Token))
				mirror.Zero(tokenBytes)
				if serr != nil {
					return op.Wrap(op.CodeInternal, serr, "mirror credential seal failed")
				}
				cred = store.CredentialRow{
					MirrorID: row.ID, KeyVersion: int64(sealed.KeyVersion),
					Nonce: sealed.Nonce, Ciphertext: sealed.Ciphertext, Last4: sealed.Last4,
					CreatedAt: created, RotatedAt: now.UnixMilli(), LastUsedAt: lastUsed,
				}
				if perr := tx.PutCredential(cred); perr != nil {
					return perr
				}
			}
			if changed {
				run, rerr := tx.InsertRun(now, store.MirrorRunRow{
					MirrorID: row.ID, Kind: "reconcile", State: "queued", Trigger: "manual",
				})
				if rerr != nil {
					return rerr
				}
				runPub = run.PublicID
			}
			return nil
		})
		if serr != nil {
			if oerr, ok := errors.AsType[*op.Error](serr); ok {
				return MirrorUpdateOut{}, oerr
			}
			if errors.Is(serr, store.ErrInvalid) || errors.Is(serr, store.ErrConflict) {
				var other store.MirrorRow
				rerr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
					var gerr error
					other, gerr = tx.GetMirrorByRepoName(rc.row.ID, cfg.Name)
					return gerr
				})
				if rerr == nil && other.ID != row.ID {
					return MirrorUpdateOut{}, op.Errorf(op.CodeConflict, "mirror name is taken")
				}
				if rerr != nil && !errors.Is(rerr, store.ErrNotFound) {
					return MirrorUpdateOut{}, op.Wrap(op.CodeInternal, rerr, "mirror lookup failed")
				}
				return MirrorUpdateOut{}, op.Errorf(op.CodeInvalid, "mirror configuration is not valid")
			}
			return MirrorUpdateOut{}, op.Wrap(op.CodeInternal, serr, "mirror store failed")
		}
		if runPub != "" {
			if eerr := enqueueMirrorRun(ctx, d, rc.row.ID, runPub, row.ID); eerr != nil {
				return MirrorUpdateOut{}, eerr
			}
		}
		if cred.MirrorID == 0 {
			if existing := loadMirrorCredential(ctx, d, row.ID); existing != nil {
				cred = *existing
			}
		}
		var credPtr *store.CredentialRow
		if cred.MirrorID != 0 {
			credPtr = &cred
		}
		return MirrorUpdateOut{Mirror: mirrorJSON(updated, credPtr)}, nil
	}
}

// MirrorDeleteIn carries one mirror delete with its idempotency key.
type MirrorDeleteIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	ID             string `path:"id"`
}

// MirrorDeleteOut carries the delete receipt.
type MirrorDeleteOut struct {
	ID string `json:"id"`
}

func mirrorDeleteHandler(d Deps) func(context.Context, op.Principal, MirrorDeleteIn) (MirrorDeleteOut, error) {
	return func(ctx context.Context, p op.Principal, in MirrorDeleteIn) (MirrorDeleteOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return MirrorDeleteOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return MirrorDeleteOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "mirror.manage", ""); derr != nil {
			return MirrorDeleteOut{}, derr
		}
		row, lerr := loadMirrorRow(ctx, d, rc, in.ID)
		if lerr != nil {
			return MirrorDeleteOut{}, lerr
		}
		serr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			return tx.DeleteMirror(row.ID)
		})
		if serr != nil {
			return MirrorDeleteOut{}, storeToOp(serr, "mirror was not found")
		}
		return MirrorDeleteOut{ID: row.PublicID}, nil
	}
}

// MirrorSyncBody carries one manual sync request.
type MirrorSyncBody struct {
	DryRun        bool   `json:"dry_run,omitzero"`
	ConfirmDigest string `json:"confirm_digest,omitempty" maxLength:"128"`
	Mode          string `json:"mode,omitempty" maxLength:"32"`
}

// MirrorSyncIn carries one mirror sync with its idempotency key.
type MirrorSyncIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	ID             string `path:"id"`
	Body           MirrorSyncBody
}

// MirrorSyncOut carries the plan preview or the queued run.
type MirrorSyncOut struct {
	Plan            *MirrorPlanOut `json:"plan,omitempty"`
	Digest          string         `json:"digest,omitempty"`
	AdoptedRequired bool           `json:"adopted_required"`
	Run             *RunJSON       `json:"run,omitempty"`
}

const manualSyncThrottleMs = int64(60000)

func mirrorSyncHandler(d Deps) func(context.Context, op.Principal, MirrorSyncIn) (MirrorSyncOut, error) {
	return func(ctx context.Context, p op.Principal, in MirrorSyncIn) (MirrorSyncOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return MirrorSyncOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return MirrorSyncOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "mirror.manage", ""); derr != nil {
			return MirrorSyncOut{}, derr
		}
		row, lerr := loadMirrorRow(ctx, d, rc, in.ID)
		if lerr != nil {
			return MirrorSyncOut{}, lerr
		}
		nowMs := d.now().UnixMilli()
		if row.LastManualAt != 0 && nowMs-row.LastManualAt < manualSyncThrottleMs {
			return MirrorSyncOut{}, op.Errorf(op.CodeConflict, "manual sync was throttled")
		}
		if !row.Enabled {
			return MirrorSyncOut{}, op.Errorf(op.CodeConflict, "mirror is disabled")
		}
		cfg, gerr := mirrorRowConfig(row)
		if gerr != nil {
			return MirrorSyncOut{}, gerr
		}
		if verr := cfg.Validate(d.now()); verr != nil {
			return MirrorSyncOut{}, op.Wrap(op.CodeValidation, verr, "mirror configuration is not valid")
		}
		mode := in.Body.Mode
		if mode == "" {
			mode = "now"
		}
		if mode != "now" && mode != "reconcile" {
			return MirrorSyncOut{}, op.Errorf(op.CodeValidation, "sync mode must be now or reconcile")
		}
		var cred store.CredentialRow
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			var gerr error
			cred, gerr = tx.GetCredential(row.ID)
			return gerr
		})
		if serr != nil {
			return MirrorSyncOut{}, op.Errorf(op.CodeConflict, "mirror has no credential")
		}
		if len(d.SealKey) == 0 {
			return MirrorSyncOut{}, op.Errorf(op.CodeInternal, "mirror seal key is not configured")
		}
		token, oerr := mirror.Open(d.SealKey, mirror.SealedCredential{
			KeyVersion: uint32(cred.KeyVersion), Nonce: cred.Nonce, Ciphertext: cred.Ciphertext,
		}, "mirror:"+row.PublicID)
		if oerr != nil {
			return MirrorSyncOut{}, op.Errorf(op.CodeConflict, "mirror credential cannot be decrypted")
		}
		defer mirror.Zero(token)
		daemon, derr := d.openDaemonRepo(ctx, rc.row)
		if derr != nil {
			return MirrorSyncOut{}, derr
		}
		local, lerr := d.collectMirrorLocal(ctx, daemon)
		if lerr != nil {
			return MirrorSyncOut{}, lerr
		}
		remote, defBranch, rerr := d.collectMirrorRemote(ctx, daemon, cfg, string(token))
		if rerr != nil {
			return MirrorSyncOut{}, rerr
		}
		plan, perr := mirror.ComputePlan(local, remote, cfg, defBranch, in.Body.ConfirmDigest)
		needed := errors.Is(perr, mirror.ErrNeedsConfirmation)
		if perr != nil && !needed {
			return MirrorSyncOut{}, op.Wrap(op.CodeInternal, perr, "mirror plan failed")
		}
		planned := mirrorPlanOut(plan)
		if in.Body.DryRun {
			return MirrorSyncOut{Plan: &planned, Digest: plan.Digest, AdoptedRequired: needed}, nil
		}
		kind := "sync"
		if mode == "reconcile" {
			kind = "reconcile"
		}
		var inserted store.MirrorRunRow
		now := d.now()
		serr = d.Store.Write(ctx, func(tx *store.WriteTx) error {
			run, rerr := tx.InsertRun(now, store.MirrorRunRow{
				MirrorID: row.ID, Kind: kind, State: "queued", Trigger: "manual",
				ConfirmedDigest: in.Body.ConfirmDigest,
			})
			if rerr != nil {
				return rerr
			}
			if merr := tx.SetMirrorManual(now, row.ID); merr != nil {
				return merr
			}
			inserted = run
			return nil
		})
		if serr != nil {
			return MirrorSyncOut{}, storeToOp(serr, "mirror was not found")
		}
		if eerr := enqueueMirrorRun(ctx, d, rc.row.ID, inserted.PublicID, row.ID); eerr != nil {
			return MirrorSyncOut{}, eerr
		}
		out := runJSON(inserted)
		return MirrorSyncOut{Run: &out}, nil
	}
}

// MirrorRunListIn carries one mirror run list request.
type MirrorRunListIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	ID     string `path:"id"`
	State  string `query:"state"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

// MirrorRunListOut carries one mirror run page.
type MirrorRunListOut struct {
	Next string    `json:"-"`
	Runs []RunJSON `json:"runs"`
}

func mirrorRunListHandler(d Deps) func(context.Context, op.Principal, MirrorRunListIn) (MirrorRunListOut, error) {
	return func(ctx context.Context, p op.Principal, in MirrorRunListIn) (MirrorRunListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return MirrorRunListOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return MirrorRunListOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "mirror.view", ""); derr != nil {
			return MirrorRunListOut{}, derr
		}
		row, lerr := loadMirrorRow(ctx, d, rc, in.ID)
		if lerr != nil {
			return MirrorRunListOut{}, lerr
		}
		out := MirrorRunListOut{Runs: []RunJSON{}}
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			rows, next, lerr := tx.ListRuns(row.ID, in.State, in.Cursor, in.Limit)
			if lerr != nil {
				return lerr
			}
			for _, run := range rows {
				out.Runs = append(out.Runs, runJSON(run))
			}
			out.Next = next
			return nil
		})
		if serr != nil {
			return MirrorRunListOut{}, storeToOp(serr, "mirror run list failed")
		}
		return out, nil
	}
}

// MirrorRunGetIn carries one mirror run read.
type MirrorRunGetIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	ID    string `path:"id"`
	Run   string `path:"run"`
}

// MirrorRunGetOut carries one mirror run.
type MirrorRunGetOut struct {
	Run RunJSON `json:"run"`
}

func mirrorRunGetHandler(d Deps) func(context.Context, op.Principal, MirrorRunGetIn) (MirrorRunGetOut, error) {
	return func(ctx context.Context, p op.Principal, in MirrorRunGetIn) (MirrorRunGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return MirrorRunGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return MirrorRunGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "mirror.view", ""); derr != nil {
			return MirrorRunGetOut{}, derr
		}
		row, lerr := loadMirrorRow(ctx, d, rc, in.ID)
		if lerr != nil {
			return MirrorRunGetOut{}, lerr
		}
		var run store.MirrorRunRow
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			var gerr error
			run, gerr = tx.GetRunByPublicID(in.Run)
			return gerr
		})
		if serr != nil {
			return MirrorRunGetOut{}, storeToOp(serr, "mirror run was not found")
		}
		if run.MirrorID != row.ID {
			return MirrorRunGetOut{}, op.Errorf(op.CodeNotFound, "mirror run was not found")
		}
		return MirrorRunGetOut{Run: runJSON(run)}, nil
	}
}
