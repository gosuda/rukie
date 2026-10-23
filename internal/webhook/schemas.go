package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

var frozenEventTypes = []string{
	"ref.updated",
	"change.opened",
	"change.updated",
	"change.landed",
	"change.abandoned",
	"review.submitted",
	"attestation.added",
	"task.created",
	"task.claimed",
	"task.closed",
	"ci.run.state_changed",
	"release.published",
	"mirror.sync_failed",
	"repo.created",
	"repo.deleted",
}

var catalogueFields = map[string][]string{
	"ref.updated":          {"ref", "ref_kind", "old_oid", "new_oid", "change_kind", "pusher", "compare_url"},
	"change.opened":        {"change_id", "version", "title", "branch", "base_ref", "author", "change_url", "branch_view_url"},
	"change.updated":       {"change_id", "version", "previous_version", "update_kind", "base_ref", "candidate_oid", "editor", "change_url"},
	"change.landed":        {"change_id", "version", "target_ref", "landed_oid", "base_oid_at_land", "landed_by", "change_url"},
	"change.abandoned":     {"change_id", "version", "abandoned_by", "change_url"},
	"review.submitted":     {"change_id", "version", "review_id", "verdict", "reviewer", "body_preview", "review_url"},
	"attestation.added":    {"subject_kind", "subject", "attest_kind", "result", "signer_fingerprint", "recorded_by", "attestation_url"},
	"task.created":         {"task_id", "task_number", "title", "state", "assignee", "created_by", "task_url"},
	"task.claimed":         {"task_id", "task_number", "holder", "lease_id", "lease_expires_at", "task_url"},
	"task.closed":          {"task_id", "task_number", "close_reason", "closed_by", "task_url"},
	"ci.run.state_changed": {"run_id", "attempt", "repo_ref", "commit_oid", "change_id", "pipeline", "state", "conclusion", "duration_ms", "run_url"},
	"release.published":    {"release_id", "tag", "name", "notes_preview", "assets", "published_by", "release_url"},
	"mirror.sync_failed":   {"remote_name", "remote_host", "mode", "refspec_summary", "trigger", "error", "consecutive_failures", "next_reconcile_at", "repo_url"},
	"repo.created":         {"repo_id", "visibility", "default_branch", "fork_of", "created_by", "repo_url"},
	"repo.deleted":         {"repo_id", "full_name", "visibility", "deleted_by", "last_default_tip"},
}

const ulidLen = 26

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// FreezeEventTypes returns the exact v1 event catalogue in pinned order.
func FreezeEventTypes() []string {
	out := make([]string, len(frozenEventTypes))
	copy(out, frozenEventTypes)
	return out
}

// Catalogue maps each event type to its required data fields.
func Catalogue() map[string][]string {
	out := make(map[string][]string, len(catalogueFields))
	for typ, fields := range catalogueFields {
		dup := make([]string, len(fields))
		copy(dup, fields)
		out[typ] = dup
	}
	return out
}

type envelope struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

// ValidateEnvelope checks id, type, timestamp, and required data fields.
func ValidateEnvelope(body []byte) error {
	var env envelope
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&env); err != nil {
		return fmt.Errorf("ValidateEnvelope: bad envelope: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("ValidateEnvelope: trailing data after envelope")
	}
	if !validMessageID(env.ID) {
		return fmt.Errorf("ValidateEnvelope: bad id %q", env.ID)
	}
	fields, ok := catalogueFields[env.Type]
	if !ok {
		return fmt.Errorf("ValidateEnvelope: unknown type %q", env.Type)
	}
	if _, err := time.Parse(time.RFC3339, env.Timestamp); err != nil {
		return fmt.Errorf("ValidateEnvelope: bad timestamp: %w", err)
	}
	if len(env.Data) == 0 {
		return fmt.Errorf("ValidateEnvelope: missing data")
	}
	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return fmt.Errorf("ValidateEnvelope: bad data: %w", err)
	}
	if data == nil {
		return fmt.Errorf("ValidateEnvelope: data must be an object")
	}
	for _, field := range fields {
		if _, ok := data[field]; !ok {
			return fmt.Errorf("ValidateEnvelope: data missing %q", field)
		}
	}
	return nil
}

// BuildEnvelope serializes the envelope canonically: HTML unescaped and
// without a trailing newline. The caller enforces the 256 KiB payload cap.
func BuildEnvelope(msgID, typ string, at time.Time, data map[string]any) ([]byte, error) {
	if data == nil {
		data = map[string]any{}
	}
	payload := map[string]any{
		"data":      data,
		"id":        msgID,
		"timestamp": at.UTC().Format(time.RFC3339Nano),
		"type":      typ,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, fmt.Errorf("BuildEnvelope: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func validMessageID(id string) bool {
	if !strings.HasPrefix(id, "msg_") || len(id) != len("msg_")+ulidLen {
		return false
	}
	for i := len("msg_"); i < len(id); i++ {
		if strings.IndexByte(crockford, id[i]) < 0 {
			return false
		}
	}
	return true
}
