package events

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

const (
	kindRefUpdated   = "ref.updated"
	kindPushReceived = "push.received"
	sourcePush       = "push"
)

type PushMeta struct {
	InvocationID string
	RepoID       int64
	ActorID      int64
}

func DeriveInvocationID(repoID, actorID int64, call wire.HookCall) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d\x00%d\x00%d\x00%d\x00", repoID, actorID, len(call.Commands), len(call.PushOptions))
	for _, cmd := range call.Commands {
		_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00", cmd.Name, cmd.Old, cmd.New)
	}
	for _, opt := range call.PushOptions {
		_, _ = fmt.Fprintf(h, "%s\x00", opt)
	}
	return "evt-" + hex.EncodeToString(h.Sum(nil)[:16])
}

type pushPayload struct {
	Invocation string      `json:"invocation"`
	Landed     []LandedRef `json:"landed"`
	Repo       int64       `json:"repo"`
	Actor      int64       `json:"actor"`
}

type scanPayload struct {
	Invocation string `json:"invocation"`
	Repo       int64  `json:"repo"`
}

type refUpdatedBody struct {
	Name       string `json:"name"`
	New        string `json:"new"`
	Old        string `json:"old"`
	Source     string `json:"source"`
	Invocation string `json:"invocation"`
	Repo       int64  `json:"repo"`
}

type pushReceivedBody struct {
	Source     string      `json:"source"`
	Invocation string      `json:"invocation"`
	Landed     []LandedRef `json:"landed"`
	Repo       int64       `json:"repo"`
	Actor      int64       `json:"actor"`
}

func acceptAll(call wire.HookCall) wire.HookReply {
	verdicts := make([]wire.HookRefVerdict, 0, len(call.Commands))
	for _, cmd := range call.Commands {
		verdicts = append(verdicts, wire.HookRefVerdict{
			Name:    cmd.Name,
			Verdict: wire.AcceptVerdict(),
		})
	}
	return wire.HookReply{Verdicts: verdicts}
}

func checkMeta(meta PushMeta) (PushMeta, error) {
	if meta.RepoID <= 0 {
		return PushMeta{}, fmt.Errorf("repo id must be positive")
	}
	if meta.ActorID < 0 {
		return PushMeta{}, fmt.Errorf("actor id must not be negative")
	}
	if len(meta.InvocationID) > 96 {
		return PushMeta{}, fmt.Errorf("invocation id must be at most 96 bytes")
	}
	return meta, nil
}

func HandlePostReceive(ctx context.Context, st *store.Store, sp *Spool, meta PushMeta, call wire.HookCall, now time.Time) wire.HookReply {
	if call.Phase != wire.HookPostReceive {
		return wire.RejectAllHookReply(fmt.Sprintf("post-receive handler got phase %q", string(call.Phase)))
	}
	checked, merr := checkMeta(meta)
	if merr != nil {
		return wire.RejectAllHookReply(merr.Error())
	}
	if len(call.Commands) == 0 {
		return wire.RejectAllHookReply("post-receive carried no commands")
	}
	if checked.InvocationID == "" {
		checked.InvocationID = DeriveInvocationID(checked.RepoID, checked.ActorID, call)
	}
	stamp := now.UnixMilli()
	if stamp <= 0 {
		return wire.RejectAllHookReply("post-receive time must be after the Unix epoch")
	}
	landed := make([]LandedRef, 0, len(call.Commands))
	for _, cmd := range call.Commands {
		if cmd.Name == "" || cmd.New == "" {
			return wire.RejectAllHookReply("post-receive command misses its ref name or new tip")
		}
		landed = append(landed, LandedRef{Name: cmd.Name, Old: cmd.Old, New: cmd.New})
	}
	rec := PushRecord{
		Landed:       landed,
		InvocationID: checked.InvocationID,
		RepoID:       checked.RepoID,
		ActorID:      checked.ActorID,
		AttemptedAt:  stamp,
	}
	if serr := sp.Store(rec); serr != nil {
		return wire.RejectAllHookReply("post-receive spool store failed")
	}
	invocation := checked.InvocationID
	werr := st.Write(ctx, func(tx *store.WriteTx) error {
		return EnqueuePush(tx, rec)
	})
	if werr != nil {
		return wire.RejectAllHookReply("post-receive callback failed; the spool drain retries it")
	}
	_ = sp.Remove(invocation)
	return acceptAll(call)
}

func EnqueuePush(tx *store.WriteTx, rec PushRecord) error {
	landedRaw, jerr := jsonv2.Marshal(rec.Landed)
	if jerr != nil {
		return jerr
	}
	inserted, perr := tx.PutPendingPush(store.PendingPush{
		Landed:       string(landedRaw),
		InvocationID: rec.InvocationID,
		RepoID:       rec.RepoID,
		ActorID:      rec.ActorID,
		AttemptedAt:  rec.AttemptedAt,
	})
	if perr != nil {
		return perr
	}
	if !inserted {
		return nil
	}
	payload, jerr := jsonv2.Marshal(pushPayload{
		Landed:     rec.Landed,
		Invocation: rec.InvocationID,
		Repo:       rec.RepoID,
		Actor:      rec.ActorID,
	})
	if jerr != nil {
		return jerr
	}
	at := time.UnixMilli(rec.AttemptedAt)
	jobs := []store.JobSpec{
		{Payload: string(payload), ID: "hook-delivery:" + rec.InvocationID, Kind: "hook_delivery", RepoID: rec.RepoID, At: at},
		{Payload: string(mustScanPayload(rec.InvocationID, rec.RepoID)), ID: "mirror-run:" + rec.InvocationID, Kind: "mirror_run", RepoID: rec.RepoID, At: at},
		{Payload: string(mustScanPayload(rec.InvocationID, rec.RepoID)), ID: "ci-plan:" + rec.InvocationID, Kind: "ci_plan", RepoID: rec.RepoID, At: at},
	}
	for _, spec := range jobs {
		if _, jerr = tx.EnqueueJob(spec); jerr != nil {
			return jerr
		}
	}
	received, rerr := jsonv2.Marshal(pushReceivedBody{
		Landed:     rec.Landed,
		Source:     sourcePush,
		Invocation: rec.InvocationID,
		Repo:       rec.RepoID,
		Actor:      rec.ActorID,
	})
	if rerr != nil {
		return rerr
	}
	if _, rerr = tx.Outbox().Emit(store.Event{
		OccurredAt: at,
		Kind:       kindPushReceived,
		Body:       jsontext.Value(received),
	}); rerr != nil {
		return rerr
	}
	for _, ref := range rec.Landed {
		body, berr := jsonv2.Marshal(refUpdatedBody{
			Name:       ref.Name,
			New:        ref.New,
			Old:        ref.Old,
			Source:     sourcePush,
			Invocation: rec.InvocationID,
			Repo:       rec.RepoID,
		})
		if berr != nil {
			return berr
		}
		if _, berr = tx.Outbox().Emit(store.Event{
			OccurredAt: at,
			Kind:       kindRefUpdated,
			Body:       jsontext.Value(body),
		}); berr != nil {
			return berr
		}
		if isZeroID(ref.New) {
			if derr := tx.DeleteRefTip(rec.RepoID, ref.Name); derr != nil {
				return derr
			}
			continue
		}
		if terr := tx.PutRefTip(rec.RepoID, ref.Name, ref.New, at); terr != nil {
			return terr
		}
	}
	return nil
}

func mustScanPayload(invocation string, repoID int64) []byte {
	raw, err := jsonv2.Marshal(scanPayload{Invocation: invocation, Repo: repoID})
	if err != nil {
		return []byte(`{}`)
	}
	return raw
}

func isZeroID(id string) bool {
	if id == "" {
		return false
	}
	for i := range id {
		if id[i] != '0' {
			return false
		}
	}
	return true
}
