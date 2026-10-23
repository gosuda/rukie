package store

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrTooLarge reports a webhook payload that exceeds the 256 KiB enqueue cap.
var ErrTooLarge = errors.New("store: payload too large")

// WebhookEventTypes is the v1 catalogue of webhook event type names.
var WebhookEventTypes = []string{
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

// Webhook failure-count thresholds shared by the store's relative failure
// recorder and the delivery worker's status derivation: 50 consecutive
// failures mark the endpoint "failing", 200 suspend it.
const (
	WebhookWarnAfterFailures    = 50
	WebhookSuspendAfterFailures = 200
)

var validWebhookEvent = func() map[string]bool {
	m := make(map[string]bool, len(WebhookEventTypes))
	for _, t := range WebhookEventTypes {
		m[t] = true
	}
	return m
}()

const maxWebhookPayload = 262144

const webhookEndpointColumns = `id, scope, repo_id, owner_id, owner_kind, url, url_hash, secret_enc,
prev_secret_enc, prev_secret_expires_at, event_types, ref_globs, active, concurrency,
timeout_seconds, note, created_by, last_status, consecutive_failures, disabled_at,
disable_reason, created_at, updated_at`

// WebhookEndpointRow is one webhook endpoint subscription. Times are Unix millis.
// OwnerKind ('user'|'org') disambiguates OwnerID across the independent user/org
// id namespaces; empty for repo scope.
type WebhookEndpointRow struct {
	ID                  string
	Scope               string
	OwnerKind           string
	URL                 string
	LastStatus          string
	Note                string
	DisableReason       string
	URLHash             []byte
	SecretEnc           []byte
	PrevSecretEnc       []byte
	EventTypes          []string
	RefGlobs            []string
	RepoID              int64
	OwnerID             int64
	PrevSecretExpiresAt int64
	CreatedAt           int64
	UpdatedAt           int64
	DisabledAt          int64
	Concurrency         int64
	TimeoutSeconds      int64
	CreatedBy           int64
	ConsecutiveFailures int64
	Active              bool
}

// WebhookEndpointInsert carries one new endpoint's configuration. A non-empty
// ID is used verbatim (caller-sealed secrets bind the final id as AAD); empty
// mints wh_<ulid>.
type WebhookEndpointInsert struct {
	ID             string
	Scope          string
	OwnerKind      string
	URL            string
	Note           string
	SecretEnc      []byte
	EventTypes     []string
	RefGlobs       []string
	RepoID         int64
	OwnerID        int64
	Concurrency    int64
	TimeoutSeconds int64
	CreatedBy      int64
	Active         bool
}

// WebhookEndpointUpdate carries a partial endpoint update. Nil leaves the column unchanged.
type WebhookEndpointUpdate struct {
	URL                 *string
	EventTypes          *[]string
	RefGlobs            *[]string
	Active              *bool
	Concurrency         *int64
	TimeoutSeconds      *int64
	ConsecutiveFailures *int64
	LastStatus          *string
	Note                *string
	DisableReason       *string
	DisabledAt          *int64
}

// WebhookMessageRow is one fanned-out delivery. Times are Unix millis.
type WebhookMessageRow struct {
	ID             string
	EndpointID     string
	RedeliveryOf   string
	EventType      string
	State          string
	LastError      string
	LeaseOwner     string
	Payload        []byte
	OutboxSeq      int64
	EventTime      int64
	AttemptCount   int64
	NextAttemptAt  int64
	LastStatusCode int64
	DeliveredAt    int64
	LeaseExpiresAt int64
	UpdatedAt      int64
}

// WebhookMessageInsert carries one fanned-out delivery.
type WebhookMessageInsert struct {
	ID            string
	EndpointID    string
	EventType     string
	Payload       []byte
	OutboxSeq     int64
	EventTime     int64
	NextAttemptAt int64
}

// WebhookAttemptInsert carries one delivery attempt record. ReqHeaders holds
// the redacted request-headers JSON (signatures as presence flags only).
type WebhookAttemptInsert struct {
	MessageID        string
	Error            string
	ReqHeaders       []byte
	RespHeaders      []byte
	RespBody         []byte
	AttemptNo        int64
	StartedAt        int64
	FinishedAt       int64
	AttemptTimestamp int64
	HTTPStatus       int64
	DurationMs       int64
}

// WebhookAttemptRow is one recorded delivery attempt.
type WebhookAttemptRow struct {
	Error            string
	MessageID        string
	ReqHeaders       []byte
	RespHeaders      []byte
	RespBody         []byte
	ID               int64
	AttemptNo        int64
	StartedAt        int64
	FinishedAt       int64
	AttemptTimestamp int64
	HTTPStatus       int64
	DurationMs       int64
}

func encodeWebhookStrings(vs []string) string {
	if len(vs) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(vs)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func decodeWebhookStrings(raw string) ([]string, error) {
	if raw == "" {
		return []string{}, nil
	}
	var vs []string
	if err := json.Unmarshal([]byte(raw), &vs); err != nil {
		return nil, err
	}
	if vs == nil {
		return []string{}, nil
	}
	return vs, nil
}

func scanWebhookEndpoint(scan func(...any) error) (WebhookEndpointRow, error) {
	var r WebhookEndpointRow
	var repoID, ownerID, prevExp, disabledAt sql.NullInt64
	var prevEnc []byte
	var eventTypes, refGlobs string
	var active int64
	err := scan(&r.ID, &r.Scope, &repoID, &ownerID, &r.OwnerKind, &r.URL, &r.URLHash, &r.SecretEnc,
		&prevEnc, &prevExp, &eventTypes, &refGlobs, &active, &r.Concurrency,
		&r.TimeoutSeconds, &r.Note, &r.CreatedBy, &r.LastStatus, &r.ConsecutiveFailures,
		&disabledAt, &r.DisableReason, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r, fmt.Errorf("scanWebhookEndpoint: %w", ErrNotFound)
		}
		return r, err
	}
	if repoID.Valid {
		r.RepoID = repoID.Int64
	}
	if ownerID.Valid {
		r.OwnerID = ownerID.Int64
	}
	if prevExp.Valid {
		r.PrevSecretExpiresAt = prevExp.Int64
	}
	if disabledAt.Valid {
		r.DisabledAt = disabledAt.Int64
	}
	r.PrevSecretEnc = prevEnc
	r.Active = active == 1
	var derr error
	if r.EventTypes, derr = decodeWebhookStrings(eventTypes); derr != nil {
		return r, fmt.Errorf("scanWebhookEndpoint: %w", derr)
	}
	if r.RefGlobs, derr = decodeWebhookStrings(refGlobs); derr != nil {
		return r, fmt.Errorf("scanWebhookEndpoint: %w", derr)
	}
	return r, nil
}

func scanWebhookMessage(scan func(...any) error) (WebhookMessageRow, error) {
	var r WebhookMessageRow
	var redelivery sql.NullString
	var lastStatus sql.NullInt64
	var deliveredAt sql.NullInt64
	err := scan(&r.ID, &r.EndpointID, &r.OutboxSeq, &r.EventType, &r.EventTime,
		&r.Payload, &r.State, &r.AttemptCount, &r.NextAttemptAt, &r.LeaseExpiresAt,
		&r.LeaseOwner, &r.LastError, &lastStatus, &deliveredAt, &redelivery, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r, fmt.Errorf("scanWebhookMessage: %w", ErrNotFound)
		}
		return r, err
	}
	if redelivery.Valid {
		r.RedeliveryOf = redelivery.String
	}
	if lastStatus.Valid {
		r.LastStatusCode = lastStatus.Int64
	}
	if deliveredAt.Valid {
		r.DeliveredAt = deliveredAt.Int64
	}
	return r, nil
}

func validateWebhookEndpointInsert(in WebhookEndpointInsert) error {
	if in.Scope != "repo" && in.Scope != "owner" {
		return fmt.Errorf("webhook endpoint: %w: scope must be repo or owner", ErrInvalid)
	}
	if (in.RepoID != 0) == (in.OwnerID != 0) {
		return fmt.Errorf("webhook endpoint: %w: exactly one of repo_id or owner_id must be set", ErrInvalid)
	}
	if in.Scope == "repo" && in.RepoID == 0 {
		return fmt.Errorf("webhook endpoint: %w: repo scope needs repo_id", ErrInvalid)
	}
	if in.Scope == "owner" && in.OwnerID == 0 {
		return fmt.Errorf("webhook endpoint: %w: owner scope needs owner_id", ErrInvalid)
	}
	if in.Scope == "repo" && in.OwnerKind != "" {
		return fmt.Errorf("webhook endpoint: %w: repo scope takes no owner_kind", ErrInvalid)
	}
	if in.Scope == "owner" && in.OwnerKind != "user" && in.OwnerKind != "org" {
		return fmt.Errorf("webhook endpoint: %w: owner scope needs owner_kind user or org", ErrInvalid)
	}
	if in.ID != "" && len(in.ID) > 64 {
		return fmt.Errorf("webhook endpoint: %w: id must fit 64 bytes", ErrInvalid)
	}
	if len(in.URL) == 0 || len(in.URL) > 2048 {
		return fmt.Errorf("webhook endpoint: %w: url must be 1 to 2048 bytes", ErrInvalid)
	}
	if len(in.SecretEnc) == 0 {
		return fmt.Errorf("webhook endpoint: %w: secret must be sealed before insert", ErrInvalid)
	}
	if err := validateWebhookEventTypes(in.EventTypes, in.RefGlobs); err != nil {
		return err
	}
	if in.Concurrency != 0 && (in.Concurrency < 1 || in.Concurrency > 16) {
		return fmt.Errorf("webhook endpoint: %w: concurrency must be 1 to 16", ErrInvalid)
	}
	if in.TimeoutSeconds != 0 && (in.TimeoutSeconds < 5 || in.TimeoutSeconds > 60) {
		return fmt.Errorf("webhook endpoint: %w: timeout_seconds must be 5 to 60", ErrInvalid)
	}
	if len(in.Note) > 256 {
		return fmt.Errorf("webhook endpoint: %w: note must fit 256 bytes", ErrInvalid)
	}
	if in.CreatedBy <= 0 {
		return fmt.Errorf("webhook endpoint: %w: created_by must be positive", ErrInvalid)
	}
	return nil
}

func validateWebhookEventTypes(types, globs []string) error {
	if len(types) == 0 {
		return fmt.Errorf("webhook endpoint: %w: event_types must not be empty", ErrInvalid)
	}
	hasRef := false
	for _, t := range types {
		if !validWebhookEvent[t] {
			return fmt.Errorf("webhook endpoint: %w: unknown event type %q", ErrInvalid, t)
		}
		if t == "ref.updated" {
			hasRef = true
		}
	}
	if len(globs) > 32 {
		return fmt.Errorf("webhook endpoint: %w: ref_globs must hold at most 32 entries", ErrInvalid)
	}
	if len(globs) > 0 && !hasRef {
		return fmt.Errorf("webhook endpoint: %w: ref_globs require ref.updated in event_types", ErrInvalid)
	}
	return nil
}

func webhookConcurrency(in int64) int64 {
	if in == 0 {
		return 4
	}
	return in
}

func webhookTimeout(in int64) int64 {
	if in == 0 {
		return 20
	}
	return in
}

func webhookURLHash(url string) []byte {
	sum := sha256.Sum256([]byte(url))
	return sum[:]
}

// InsertWebhookEndpoint creates one endpoint subscription.
func (tx *WriteTx) InsertWebhookEndpoint(now time.Time, in WebhookEndpointInsert) (WebhookEndpointRow, error) {
	if err := validateWebhookEndpointInsert(in); err != nil {
		return WebhookEndpointRow{}, err
	}
	inner, err := tx.live()
	if err != nil {
		return WebhookEndpointRow{}, err
	}
	id := in.ID
	if id == "" {
		id = "wh_" + string(NewID(now))
	}
	at := msOf(now)
	_, ierr := inner.ExecContext(tx.ctx, `INSERT INTO webhook_endpoints
(id, scope, repo_id, owner_id, owner_kind, url, url_hash, secret_enc, prev_secret_enc, prev_secret_expires_at,
 event_types, ref_globs, active, concurrency, timeout_seconds, note, created_by,
 last_status, consecutive_failures, disabled_at, disable_reason, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?, ?, ?, ?, ?, 'never', 0, NULL, '', ?, ?)`,
		id, in.Scope, nullInt(sql.NullInt64{Int64: in.RepoID, Valid: in.RepoID != 0}),
		nullInt(sql.NullInt64{Int64: in.OwnerID, Valid: in.OwnerID != 0}), in.OwnerKind,
		in.URL, webhookURLHash(in.URL), in.SecretEnc,
		encodeWebhookStrings(in.EventTypes), encodeWebhookStrings(in.RefGlobs),
		boolInt(in.Active), webhookConcurrency(in.Concurrency), webhookTimeout(in.TimeoutSeconds),
		in.Note, in.CreatedBy, at, at)
	if ierr != nil {
		return WebhookEndpointRow{}, fmt.Errorf("InsertWebhookEndpoint: %w", mapErr(ierr))
	}
	return tx.GetWebhookEndpoint(id)
}

// GetWebhookEndpoint loads one endpoint by id.
func (tx *ReadTx) GetWebhookEndpoint(id string) (WebhookEndpointRow, error) {
	inner, err := tx.live()
	if err != nil {
		return WebhookEndpointRow{}, err
	}
	return scanWebhookEndpoint(inner.QueryRowContext(tx.ctx,
		`SELECT `+webhookEndpointColumns+` FROM webhook_endpoints WHERE id = ?`, id).Scan)
}

// GetWebhookEndpoint loads one endpoint by id in a write transaction.
func (tx *WriteTx) GetWebhookEndpoint(id string) (WebhookEndpointRow, error) {
	inner, err := tx.live()
	if err != nil {
		return WebhookEndpointRow{}, err
	}
	return scanWebhookEndpoint(inner.QueryRowContext(tx.ctx,
		`SELECT `+webhookEndpointColumns+` FROM webhook_endpoints WHERE id = ?`, id).Scan)
}

func encodeEndpointCursor(createdAt, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "\x1f" + id))
}

func parseEndpointCursor(raw string) (int64, string, error) {
	decoded, derr := base64.RawURLEncoding.DecodeString(raw)
	if derr != nil {
		return 0, "", fmt.Errorf("cursor: %w: malformed endpoint cursor", ErrInvalid)
	}
	value, id, ok := strings.Cut(string(decoded), "\x1f")
	if !ok || id == "" {
		return 0, "", fmt.Errorf("cursor: %w: malformed endpoint cursor", ErrInvalid)
	}
	var created int64
	for i := range len(value) {
		c := value[i]
		if c < '0' || c > '9' {
			return 0, "", fmt.Errorf("cursor: %w: malformed endpoint cursor", ErrInvalid)
		}
		created = created*10 + int64(c-'0')
	}
	if created <= 0 {
		return 0, "", fmt.Errorf("cursor: %w: malformed endpoint cursor", ErrInvalid)
	}
	return created, id, nil
}

func clampWebhookLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func listWebhookEndpoints(tx *txCore, query, cursor string, limit int, args ...any) ([]WebhookEndpointRow, string, error) {
	limit = clampWebhookLimit(limit)
	inner, err := tx.live()
	if err != nil {
		return nil, "", err
	}
	if cursor != "" {
		created, id, cerr := parseEndpointCursor(cursor)
		if cerr != nil {
			return nil, "", cerr
		}
		query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, created, created, id)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := inner.QueryContext(tx.ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("ListWebhookEndpoints: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []WebhookEndpointRow
	for rows.Next() {
		r, serr := scanWebhookEndpoint(rows.Scan)
		if serr != nil {
			return nil, "", serr
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		return nil, "", fmt.Errorf("ListWebhookEndpoints: %w", err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeEndpointCursor(fmt.Sprintf("%d", last.CreatedAt), last.ID)
	}
	return out, next, nil
}

// ListWebhookEndpointsByRepo lists one repo's endpoints, newest first.
func (tx *ReadTx) ListWebhookEndpointsByRepo(repoID int64, cursor string, limit int) ([]WebhookEndpointRow, string, error) {
	return listWebhookEndpoints(&tx.txCore,
		`SELECT `+webhookEndpointColumns+` FROM webhook_endpoints WHERE repo_id = ?`, cursor, limit, repoID)
}

// ListWebhookEndpointsByOwner lists one owner's endpoints, newest first.
func (tx *ReadTx) ListWebhookEndpointsByOwner(ownerID int64, cursor string, limit int) ([]WebhookEndpointRow, string, error) {
	return listWebhookEndpoints(&tx.txCore,
		`SELECT `+webhookEndpointColumns+` FROM webhook_endpoints WHERE owner_id = ?`, cursor, limit, ownerID)
}

// UpdateWebhookEndpoint applies a partial endpoint update.
func (tx *WriteTx) UpdateWebhookEndpoint(id string, now time.Time, up WebhookEndpointUpdate) (WebhookEndpointRow, error) {
	current, err := tx.GetWebhookEndpoint(id)
	if err != nil {
		return WebhookEndpointRow{}, err
	}
	merged := WebhookEndpointInsert{
		Scope: current.Scope, RepoID: current.RepoID, OwnerID: current.OwnerID,
		OwnerKind: current.OwnerKind,
		URL:       current.URL, SecretEnc: current.SecretEnc,
		EventTypes: current.EventTypes, RefGlobs: current.RefGlobs,
		Active: current.Active, Concurrency: current.Concurrency,
		TimeoutSeconds: current.TimeoutSeconds, Note: current.Note, CreatedBy: current.CreatedBy,
	}
	if up.URL != nil {
		merged.URL = *up.URL
	}
	if up.EventTypes != nil {
		merged.EventTypes = *up.EventTypes
	}
	if up.RefGlobs != nil {
		merged.RefGlobs = *up.RefGlobs
	}
	if up.Active != nil {
		merged.Active = *up.Active
	}
	if up.Concurrency != nil {
		merged.Concurrency = *up.Concurrency
	}
	if up.TimeoutSeconds != nil {
		merged.TimeoutSeconds = *up.TimeoutSeconds
	}
	if up.Note != nil {
		merged.Note = *up.Note
	}
	if verr := validateWebhookEndpointInsert(merged); verr != nil {
		return WebhookEndpointRow{}, verr
	}
	lastStatus := current.LastStatus
	if up.LastStatus != nil {
		switch *up.LastStatus {
		case "never", "ok", "failing", "disabled":
			lastStatus = *up.LastStatus
		default:
			return WebhookEndpointRow{}, fmt.Errorf("UpdateWebhookEndpoint: %w: bad last_status", ErrInvalid)
		}
	}
	failures := current.ConsecutiveFailures
	if up.ConsecutiveFailures != nil {
		if *up.ConsecutiveFailures < 0 {
			return WebhookEndpointRow{}, fmt.Errorf("UpdateWebhookEndpoint: %w: consecutive_failures", ErrInvalid)
		}
		failures = *up.ConsecutiveFailures
	}
	disabledAt := sql.NullInt64{Int64: current.DisabledAt, Valid: current.DisabledAt != 0}
	if up.DisabledAt != nil {
		if *up.DisabledAt < 0 {
			return WebhookEndpointRow{}, fmt.Errorf("UpdateWebhookEndpoint: %w: disabled_at", ErrInvalid)
		}
		disabledAt = sql.NullInt64{Int64: *up.DisabledAt, Valid: *up.DisabledAt != 0}
	}
	disableReason := current.DisableReason
	if up.DisableReason != nil {
		disableReason = *up.DisableReason
	}
	inner, err := tx.live()
	if err != nil {
		return WebhookEndpointRow{}, err
	}
	_, uerr := inner.ExecContext(tx.ctx, `UPDATE webhook_endpoints SET
url = ?, url_hash = ?, event_types = ?, ref_globs = ?, active = ?, concurrency = ?,
timeout_seconds = ?, note = ?, last_status = ?, consecutive_failures = ?,
disabled_at = ?, disable_reason = ?, updated_at = ? WHERE id = ?`,
		merged.URL, webhookURLHash(merged.URL),
		encodeWebhookStrings(merged.EventTypes), encodeWebhookStrings(merged.RefGlobs),
		boolInt(merged.Active), webhookConcurrency(merged.Concurrency),
		webhookTimeout(merged.TimeoutSeconds), merged.Note, lastStatus, failures,
		nullInt(disabledAt), disableReason, msOf(now), id)
	if uerr != nil {
		return WebhookEndpointRow{}, fmt.Errorf("UpdateWebhookEndpoint: %w", mapErr(uerr))
	}
	return tx.GetWebhookEndpoint(id)
}

// NoteWebhookFailure records one failed delivery with a relative increment:
// the counter advances from its in-transaction value and the
// warning/suspension status derives from that value in the same write
// transaction, so concurrent deliveries converge on the correct count and a
// concurrent success reset is never clobbered by a stale snapshot write.
// Suspension derives from WebhookSuspendAfterFailures ("undeliverable") and
// the warn threshold marks "failing". A missing endpoint reports ErrNotFound.
func (tx *WriteTx) NoteWebhookFailure(id string, nowMillis int64) (WebhookEndpointRow, error) {
	return tx.noteWebhookFailure(id, nowMillis, false, "")
}

// NoteWebhookGone records one failed delivery to a permanently gone endpoint
// (410-gone): the counter still increments relatively, and the endpoint
// suspends regardless of the count with the caller's reason. A missing
// endpoint reports ErrNotFound.
func (tx *WriteTx) NoteWebhookGone(id string, nowMillis int64, reason string) (WebhookEndpointRow, error) {
	return tx.noteWebhookFailure(id, nowMillis, true, reason)
}

func (tx *WriteTx) noteWebhookFailure(id string, nowMillis int64, forceDisable bool, disableReason string) (WebhookEndpointRow, error) {
	inner, err := tx.live()
	if err != nil {
		return WebhookEndpointRow{}, err
	}
	n, uerr := execAffected(tx.ctx, inner, `UPDATE webhook_endpoints SET
consecutive_failures = consecutive_failures + 1, updated_at = ? WHERE id = ?`,
		nowMillis, id)
	if uerr != nil {
		return WebhookEndpointRow{}, fmt.Errorf("noteWebhookFailure: %w", mapErr(uerr))
	}
	if n == 0 {
		return WebhookEndpointRow{}, fmt.Errorf("noteWebhookFailure: %w: %s", ErrNotFound, id)
	}
	row, gerr := tx.GetWebhookEndpoint(id)
	if gerr != nil {
		return WebhookEndpointRow{}, gerr
	}
	status := row.LastStatus
	active := row.Active
	reason := row.DisableReason
	disabledAt := row.DisabledAt
	switch {
	case forceDisable:
		status, active, reason, disabledAt = "disabled", false, disableReason, nowMillis
	case row.ConsecutiveFailures >= WebhookSuspendAfterFailures:
		status, active, reason, disabledAt = "disabled", false, "undeliverable", nowMillis
	case row.ConsecutiveFailures >= WebhookWarnAfterFailures:
		if status != "disabled" {
			status = "failing"
		}
	}
	if _, uerr := inner.ExecContext(tx.ctx, `UPDATE webhook_endpoints SET
last_status = ?, active = ?, disable_reason = ?, disabled_at = ?, updated_at = ?
WHERE id = ?`, status, boolInt(active), reason, nullInt(sql.NullInt64{Int64: disabledAt, Valid: disabledAt != 0}), nowMillis, id); uerr != nil {
		return WebhookEndpointRow{}, fmt.Errorf("noteWebhookFailure: %w", mapErr(uerr))
	}
	return tx.GetWebhookEndpoint(id)
}

// DeleteWebhookEndpoint removes one endpoint; its messages and attempts cascade.
func (tx *WriteTx) DeleteWebhookEndpoint(id string) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	n, derr := execAffected(tx.ctx, inner, `DELETE FROM webhook_endpoints WHERE id = ?`, id)
	if derr != nil {
		return fmt.Errorf("DeleteWebhookEndpoint: %w", derr)
	}
	if n == 0 {
		return fmt.Errorf("DeleteWebhookEndpoint: %w", ErrNotFound)
	}
	return nil
}

// RotateWebhookEndpointSecret moves the current secret to the previous slot with a
// 24 h expiry and installs newEnc as current.
func (tx *WriteTx) RotateWebhookEndpointSecret(id string, now time.Time, newEnc []byte) (WebhookEndpointRow, error) {
	if len(newEnc) == 0 {
		return WebhookEndpointRow{}, fmt.Errorf("RotateWebhookEndpointSecret: %w: new secret must not be empty", ErrInvalid)
	}
	current, err := tx.GetWebhookEndpoint(id)
	if err != nil {
		return WebhookEndpointRow{}, err
	}
	if len(current.SecretEnc) == 0 {
		return WebhookEndpointRow{}, fmt.Errorf("RotateWebhookEndpointSecret: %w: endpoint has no secret", ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return WebhookEndpointRow{}, err
	}
	at := msOf(now)
	_, uerr := inner.ExecContext(tx.ctx, `UPDATE webhook_endpoints SET
secret_enc = ?, prev_secret_enc = ?, prev_secret_expires_at = ?, updated_at = ? WHERE id = ?`,
		newEnc, current.SecretEnc, at+24*60*60*1000, at, id)
	if uerr != nil {
		return WebhookEndpointRow{}, fmt.Errorf("RotateWebhookEndpointSecret: %w", mapErr(uerr))
	}
	return tx.GetWebhookEndpoint(id)
}

// CountWebhookEndpointsByRepo counts one repo's endpoints.
func (tx *ReadTx) CountWebhookEndpointsByRepo(repoID int64) (int, error) {
	return countWebhookEndpoints(&tx.txCore, `repo_id = ?`, repoID)
}

// CountWebhookEndpointsByOwner counts one owner's endpoints.
func (tx *ReadTx) CountWebhookEndpointsByOwner(ownerID int64) (int, error) {
	return countWebhookEndpoints(&tx.txCore, `owner_id = ?`, ownerID)
}

func countWebhookEndpoints(core *txCore, where string, args ...any) (int, error) {
	inner, err := core.live()
	if err != nil {
		return 0, err
	}
	var n int
	if serr := inner.QueryRowContext(core.ctx,
		`SELECT count(*) FROM webhook_endpoints WHERE `+where, args...).Scan(&n); serr != nil {
		return 0, fmt.Errorf("CountWebhookEndpoints: %w", serr)
	}
	return n, nil
}

// InsertWebhookMessages enqueues fanned-out deliveries. Retried fan-out of the same
// outbox row is a no-op via INSERT OR IGNORE on (outbox_seq, endpoint_id).
func (tx *WriteTx) InsertWebhookMessages(msgs []WebhookMessageInsert) error {
	if len(msgs) == 0 {
		return nil
	}
	inner, err := tx.live()
	if err != nil {
		return err
	}
	for i := range msgs {
		m := msgs[i]
		if m.ID == "" || m.EndpointID == "" || m.EventType == "" {
			return fmt.Errorf("InsertWebhookMessages: %w: id, endpoint_id and event_type are required", ErrInvalid)
		}
		if !validWebhookEvent[m.EventType] {
			return fmt.Errorf("InsertWebhookMessages: %w: unknown event type %q", ErrInvalid, m.EventType)
		}
		if len(m.Payload) > maxWebhookPayload {
			return fmt.Errorf("InsertWebhookMessages: %w: %d bytes", ErrTooLarge, len(m.Payload))
		}
		if m.EventTime <= 0 || m.NextAttemptAt <= 0 {
			return fmt.Errorf("InsertWebhookMessages: %w: event_time and next_attempt_at must be positive", ErrInvalid)
		}
		_, ierr := inner.ExecContext(tx.ctx, `INSERT OR IGNORE INTO webhook_messages
(id, endpoint_id, outbox_seq, event_type, event_time, payload, state,
 attempt_count, next_attempt_at, lease_expires_at, last_error, last_status_code,
 delivered_at, redelivery_of, updated_at)
VALUES (?, ?, ?, ?, ?, ?, 'pending', 0, ?, 0, '', NULL, NULL, NULL, ?)`,
			m.ID, m.EndpointID, m.OutboxSeq, m.EventType, m.EventTime,
			m.Payload, m.NextAttemptAt, m.EventTime)
		if ierr != nil {
			return fmt.Errorf("InsertWebhookMessages: %w", mapErr(ierr))
		}
	}
	return nil
}

const webhookMessageColumns = `id, endpoint_id, outbox_seq, event_type, event_time,
payload, state, attempt_count, next_attempt_at, lease_expires_at, lease_owner, last_error,
last_status_code, delivered_at, redelivery_of, updated_at`

const webhookLeaseMillis = 5 * 60 * 1000

// ClaimWebhookMessages moves due pending rows to inflight with a 5-minute lease
// owned by leaseOwner. An empty leaseOwner is rejected. Per-endpoint concurrency
// is enforced: at most perEndpointCap rows per endpoint are
// inflight-or-newly-claimed (perEndpointCap <= 0 falls back to each endpoint's own
// concurrency). Inactive endpoints are excluded from candidate selection in SQL
// (not after a LIMIT) so a disabled endpoint with many due pendings cannot
// occupy every batch, and endpoints already at cap are skipped during selection
// so one capped endpoint cannot monopolize candidates either. Every pick takes
// the globally earliest claimable row, preserving next-attempt ordering. The
// loop ends after batch claims, when no claimable row remains, or after a
// bounded number of contested picks (a concurrent claimer stole the row;
// claiming is best-effort per drive).
func (tx *WriteTx) ClaimWebhookMessages(nowMillis int64, batch int, perEndpointCap int, leaseOwner string) ([]WebhookMessageRow, error) {
	if leaseOwner == "" {
		return nil, fmt.Errorf("ClaimWebhookMessages: %w: lease owner is required", ErrInvalid)
	}
	if batch <= 0 {
		batch = 64
	}
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	inflight := map[string]int64{}
	claimed := map[string]int64{}
	exhausted := map[string]bool{}
	var out []WebhookMessageRow
	stalls := 0
	for len(out) < batch {
		query := `SELECT m.id, m.endpoint_id, m.outbox_seq, m.event_type, m.event_time,
m.payload, m.state, m.attempt_count, m.next_attempt_at, m.lease_expires_at, m.lease_owner, m.last_error,
m.last_status_code, m.delivered_at, m.redelivery_of, m.updated_at FROM webhook_messages m
JOIN webhook_endpoints e ON e.id = m.endpoint_id
WHERE m.state = 'pending' AND m.next_attempt_at <= ? AND e.active = 1`
		args := []any{nowMillis}
		if len(exhausted) > 0 {
			query += ` AND m.endpoint_id NOT IN (`
			first := true
			for id := range exhausted {
				if !first {
					query += `,`
				}
				query += `?`
				args = append(args, id)
				first = false
			}
			query += `)`
		}
		query += ` ORDER BY m.next_attempt_at ASC, m.id ASC LIMIT 1`
		c, serr := scanWebhookMessage(inner.QueryRowContext(tx.ctx, query, args...).Scan)
		if serr != nil {
			if errors.Is(serr, sql.ErrNoRows) || errors.Is(serr, ErrNotFound) {
				break
			}
			return nil, fmt.Errorf("ClaimWebhookMessages: %w", serr)
		}
		ep, gerr := tx.GetWebhookEndpoint(c.EndpointID)
		if gerr != nil || !ep.Active {
			exhausted[c.EndpointID] = true
			continue
		}
		want := int64(perEndpointCap)
		if want <= 0 {
			want = ep.Concurrency
			if want <= 0 {
				want = 4
			}
		}
		if _, ok := inflight[c.EndpointID]; !ok {
			var n int64
			if qerr := inner.QueryRowContext(tx.ctx, `SELECT count(*) FROM webhook_messages
WHERE endpoint_id = ? AND state = 'inflight'`, c.EndpointID).Scan(&n); qerr != nil {
				return nil, fmt.Errorf("ClaimWebhookMessages: %w", qerr)
			}
			inflight[c.EndpointID] = n
		}
		if inflight[c.EndpointID]+claimed[c.EndpointID] >= want {
			exhausted[c.EndpointID] = true
			continue
		}
		n, uerr := execAffected(tx.ctx, inner, `UPDATE webhook_messages SET
state = 'inflight', lease_owner = ?, lease_expires_at = ?, updated_at = ? WHERE id = ? AND state = 'pending'`,
			leaseOwner, nowMillis+webhookLeaseMillis, nowMillis, c.ID)
		if uerr != nil {
			return nil, fmt.Errorf("ClaimWebhookMessages: %w", uerr)
		}
		if n == 0 {
			stalls++
			if stalls > 64 {
				break
			}
			continue
		}
		claimed[c.EndpointID]++
		updated, gerr := tx.GetWebhookMessage(c.ID)
		if gerr != nil {
			return nil, gerr
		}
		out = append(out, updated)
	}
	return out, nil
}

// SweepWebhookLeases transitions expired-lease inflight rows in this transaction.
// Rows whose next failure would exhaust the attempt budget go dead with
// last_error set; the rest return to pending with the attempt count incremented
// (the crashed attempt counts as spent). Lease fields are cleared in both
// cases. It returns the total rows transitioned.
func (tx *WriteTx) SweepWebhookLeases(nowMillis int64, maxAttempts int) (int64, error) {
	if maxAttempts <= 0 {
		return 0, fmt.Errorf("SweepWebhookLeases: %w: max attempts must be positive", ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return 0, err
	}
	dead, derr := execAffected(tx.ctx, inner, `UPDATE webhook_messages SET
state = 'dead', last_error = 'delivery lease expired repeatedly',
lease_expires_at = 0, lease_owner = '', updated_at = ?
WHERE state = 'inflight' AND lease_expires_at <= ? AND attempt_count + 1 >= ?`,
		nowMillis, nowMillis, maxAttempts)
	if derr != nil {
		return 0, fmt.Errorf("SweepWebhookLeases: %w", derr)
	}
	pending, perr := execAffected(tx.ctx, inner, `UPDATE webhook_messages SET
state = 'pending', attempt_count = attempt_count + 1,
lease_expires_at = 0, lease_owner = '', updated_at = ?
WHERE state = 'inflight' AND lease_expires_at <= ?`, nowMillis, nowMillis)
	if perr != nil {
		return 0, fmt.Errorf("SweepWebhookLeases: %w", perr)
	}
	return dead + pending, nil
}

// ClearExpiredEndpointSecrets NULLs previous endpoint secrets whose rotation
// overlap expired. It returns the cleared endpoint count.
func (tx *WriteTx) ClearExpiredEndpointSecrets(nowMillis int64) (int64, error) {
	inner, err := tx.live()
	if err != nil {
		return 0, err
	}
	n, uerr := execAffected(tx.ctx, inner, `UPDATE webhook_endpoints SET
prev_secret_enc = NULL, prev_secret_expires_at = NULL
WHERE prev_secret_expires_at IS NOT NULL AND prev_secret_expires_at <= ?`, nowMillis)
	if uerr != nil {
		return 0, fmt.Errorf("ClearExpiredEndpointSecrets: %w", mapErr(uerr))
	}
	return n, nil
}

// GetWebhookMessage loads one delivery by id.
func (tx *ReadTx) GetWebhookMessage(id string) (WebhookMessageRow, error) {
	inner, err := tx.live()
	if err != nil {
		return WebhookMessageRow{}, err
	}
	return scanWebhookMessage(inner.QueryRowContext(tx.ctx,
		`SELECT `+webhookMessageColumns+` FROM webhook_messages WHERE id = ?`, id).Scan)
}

// GetWebhookMessage loads one delivery by id in a write transaction.
func (tx *WriteTx) GetWebhookMessage(id string) (WebhookMessageRow, error) {
	inner, err := tx.live()
	if err != nil {
		return WebhookMessageRow{}, err
	}
	return scanWebhookMessage(inner.QueryRowContext(tx.ctx,
		`SELECT `+webhookMessageColumns+` FROM webhook_messages WHERE id = ?`, id).Scan)
}

func encodeMessageCursor(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id))
}

func parseMessageCursor(raw string) (string, error) {
	decoded, derr := base64.RawURLEncoding.DecodeString(raw)
	if derr != nil || len(decoded) == 0 {
		return "", fmt.Errorf("cursor: %w: malformed message cursor", ErrInvalid)
	}
	return string(decoded), nil
}

// ListWebhookMessages lists one endpoint's deliveries, newest first.
func (tx *ReadTx) ListWebhookMessages(endpointID, cursor string, limit int) ([]WebhookMessageRow, string, error) {
	limit = clampWebhookLimit(limit)
	inner, err := tx.live()
	if err != nil {
		return nil, "", err
	}
	query := `SELECT ` + webhookMessageColumns + ` FROM webhook_messages WHERE endpoint_id = ?`
	args := []any{endpointID}
	if cursor != "" {
		id, cerr := parseMessageCursor(cursor)
		if cerr != nil {
			return nil, "", cerr
		}
		query += ` AND id < ?`
		args = append(args, id)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := inner.QueryContext(tx.ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("ListWebhookMessages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []WebhookMessageRow
	for rows.Next() {
		r, serr := scanWebhookMessage(rows.Scan)
		if serr != nil {
			return nil, "", serr
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		return nil, "", fmt.Errorf("ListWebhookMessages: %w", err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = encodeMessageCursor(out[len(out)-1].ID)
	}
	return out, next, nil
}

func truncateError(s string) string {
	const limit = 2048
	if len(s) > limit {
		return s[:limit]
	}
	return s
}

// RequeueWebhookMessage records one failed attempt's outcome and schedules the next
// try. Only the lease owner may requeue its inflight message; the attempt count
// increments and lease fields clear on success. Zero matched rows report ErrStale.
func (tx *WriteTx) RequeueWebhookMessage(id string, errText string, statusCode int64, nextAttemptAt int64, nowMillis int64, leaseOwner string) (WebhookMessageRow, error) {
	if nextAttemptAt <= 0 {
		return WebhookMessageRow{}, fmt.Errorf("RequeueWebhookMessage: %w: next_attempt_at must be positive", ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return WebhookMessageRow{}, err
	}
	n, uerr := execAffected(tx.ctx, inner, `UPDATE webhook_messages SET
state = 'pending', attempt_count = attempt_count + 1, last_error = ?,
last_status_code = ?, next_attempt_at = ?, lease_expires_at = 0, lease_owner = '',
updated_at = ? WHERE id = ? AND state = 'inflight' AND lease_owner = ?`,
		truncateError(errText), nullInt(sql.NullInt64{Int64: statusCode, Valid: true}),
		nextAttemptAt, nowMillis, id, leaseOwner)
	if uerr != nil {
		return WebhookMessageRow{}, fmt.Errorf("RequeueWebhookMessage: %w", mapErr(uerr))
	}
	if n == 0 {
		return WebhookMessageRow{}, fmt.Errorf("RequeueWebhookMessage: %w: message is not owned by this lease", ErrStale)
	}
	return tx.GetWebhookMessage(id)
}

// MarkWebhookDelivered records a 2xx outcome. Only the lease owner may complete
// its inflight message; lease fields clear on success. Zero matched rows report
// ErrStale.
func (tx *WriteTx) MarkWebhookDelivered(id string, nowMillis int64, statusCode int64, leaseOwner string) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	n, uerr := execAffected(tx.ctx, inner, `UPDATE webhook_messages SET
state = 'delivered', delivered_at = ?, last_status_code = ?, last_error = '',
lease_expires_at = 0, lease_owner = '', updated_at = ?
WHERE id = ? AND state = 'inflight' AND lease_owner = ?`,
		nowMillis, nullInt(sql.NullInt64{Int64: statusCode, Valid: true}), nowMillis, id, leaseOwner)
	if uerr != nil {
		return fmt.Errorf("MarkWebhookDelivered: %w", mapErr(uerr))
	}
	if n == 0 {
		return fmt.Errorf("MarkWebhookDelivered: %w", ErrStale)
	}
	return nil
}

// MarkWebhookDead records a terminal failure. Only the lease owner may complete
// its inflight message; lease fields clear on success. Zero matched rows report
// ErrStale.
func (tx *WriteTx) MarkWebhookDead(id, errText string, nowMillis int64, statusCode int64, leaseOwner string) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	n, uerr := execAffected(tx.ctx, inner, `UPDATE webhook_messages SET
state = 'dead', last_error = ?, last_status_code = ?, lease_expires_at = 0,
lease_owner = '', updated_at = ? WHERE id = ? AND state = 'inflight' AND lease_owner = ?`,
		truncateError(errText),
		nullInt(sql.NullInt64{Int64: statusCode, Valid: true}), nowMillis, id, leaseOwner)
	if uerr != nil {
		return fmt.Errorf("MarkWebhookDead: %w", mapErr(uerr))
	}
	if n == 0 {
		return fmt.Errorf("MarkWebhookDead: %w", ErrStale)
	}
	return nil
}

// RedeliverWebhookMessage resets a dead or delivered message to pending with the
// SAME id and payload. The attempt count resets. RedeliveryOf is set to the message
// id itself only when re-driving a delivered message (debugging), else NULL.
func (tx *WriteTx) RedeliverWebhookMessage(id string, nowMillis int64) (WebhookMessageRow, error) {
	current, err := tx.GetWebhookMessage(id)
	if err != nil {
		return WebhookMessageRow{}, err
	}
	if current.State != "dead" && current.State != "delivered" {
		return WebhookMessageRow{}, fmt.Errorf("RedeliverWebhookMessage: %w: only dead or delivered messages can be redelivered", ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return WebhookMessageRow{}, err
	}
	redelivery := sql.NullString{}
	if current.State == "delivered" {
		redelivery = sql.NullString{String: id, Valid: true}
	}
	_, uerr := inner.ExecContext(tx.ctx, `UPDATE webhook_messages SET
state = 'pending', attempt_count = 0, next_attempt_at = ?, lease_expires_at = 0,
lease_owner = '', last_error = '', last_status_code = NULL, delivered_at = NULL,
redelivery_of = ?, updated_at = ? WHERE id = ?`, nowMillis, nullStr(redelivery), nowMillis, id)
	if uerr != nil {
		return WebhookMessageRow{}, fmt.Errorf("RedeliverWebhookMessage: %w", mapErr(uerr))
	}
	return tx.GetWebhookMessage(id)
}

// RecordWebhookAttempt appends one attempt and prunes history to the latest 10
// attempts per message.
func (tx *WriteTx) RecordWebhookAttempt(at WebhookAttemptInsert) error {
	if at.MessageID == "" || at.AttemptNo <= 0 {
		return fmt.Errorf("RecordWebhookAttempt: %w: message_id and positive attempt_no are required", ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return err
	}
	_, ierr := inner.ExecContext(tx.ctx, `INSERT INTO webhook_attempts
(message_id, attempt_no, started_at, finished_at, attempt_timestamp, http_status, error,
 req_headers, resp_headers, resp_body, duration_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		at.MessageID, at.AttemptNo, at.StartedAt, at.FinishedAt, at.AttemptTimestamp,
		nullInt(sql.NullInt64{Int64: at.HTTPStatus, Valid: at.HTTPStatus != 0}),
		at.Error, string(at.ReqHeaders), at.RespHeaders, at.RespBody, at.DurationMs)
	if ierr != nil {
		return fmt.Errorf("RecordWebhookAttempt: %w", mapErr(ierr))
	}
	_, derr := inner.ExecContext(tx.ctx, `DELETE FROM webhook_attempts
WHERE message_id = ? AND id NOT IN (
 SELECT id FROM webhook_attempts WHERE message_id = ? ORDER BY id DESC LIMIT 10)`,
		at.MessageID, at.MessageID)
	if derr != nil {
		return fmt.Errorf("RecordWebhookAttempt: %w", derr)
	}
	return nil
}

// ListWebhookAttempts lists one message's attempts, newest first.
func (tx *ReadTx) ListWebhookAttempts(messageID string, limit int) ([]WebhookAttemptRow, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	rows, err := inner.QueryContext(tx.ctx, `SELECT id, message_id, attempt_no,
started_at, finished_at, attempt_timestamp, http_status, error, req_headers,
resp_headers, resp_body, duration_ms
FROM webhook_attempts WHERE message_id = ? ORDER BY attempt_no DESC, id DESC LIMIT ?`,
		messageID, limit)
	if err != nil {
		return nil, fmt.Errorf("ListWebhookAttempts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []WebhookAttemptRow
	for rows.Next() {
		var r WebhookAttemptRow
		var status sql.NullInt64
		if serr := rows.Scan(&r.ID, &r.MessageID, &r.AttemptNo, &r.StartedAt,
			&r.FinishedAt, &r.AttemptTimestamp, &status, &r.Error, &r.ReqHeaders,
			&r.RespHeaders, &r.RespBody, &r.DurationMs); serr != nil {
			return nil, fmt.Errorf("ListWebhookAttempts: %w", serr)
		}
		if status.Valid {
			r.HTTPStatus = status.Int64
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("ListWebhookAttempts: %w", err)
	}
	return out, nil
}

// PruneWebhookHistory hard-deletes delivered/dead messages completed before
// beforeMillis. Attempts cascade. Returns the deleted message count.
func (tx *WriteTx) PruneWebhookHistory(beforeMillis int64) (int64, error) {
	inner, err := tx.live()
	if err != nil {
		return 0, err
	}
	n, derr := execAffected(tx.ctx, inner, `DELETE FROM webhook_messages
WHERE state IN ('delivered','dead') AND updated_at < ?`, beforeMillis)
	if derr != nil {
		return 0, fmt.Errorf("PruneWebhookHistory: %w", derr)
	}
	return n, nil
}

func matchRefGlob(glob, ref string) bool {
	if glob == "" {
		return false
	}
	if glob == "*" {
		return true
	}
	if strings.HasPrefix(glob, "*") && strings.HasSuffix(glob, "*") && len(glob) > 1 {
		return strings.Contains(ref, glob[1:len(glob)-1])
	}
	if strings.HasPrefix(glob, "*") {
		return strings.HasSuffix(ref, glob[1:])
	}
	if strings.HasSuffix(glob, "*") {
		return strings.HasPrefix(ref, glob[:len(glob)-1])
	}
	return ref == glob
}

// fanoutEvent materializes webhook deliveries and inbox rows for one committed
// outbox row. Unknown event kinds return nil. Any error rolls back the caller's
// mutation; fan-out rows never outlive the mutation they describe.
func fanoutEvent(core *txCore, row OutboxRow) error {
	if err := fanoutWebhooks(core, row); err != nil {
		return err
	}
	return fanoutInbox(core, row)
}

type fanoutEventData struct {
	body    map[string]any
	ref     string
	title   string
	snippet string
	repoID  int64
	known   bool
}

func parseFanoutBody(row OutboxRow) (fanoutEventData, error) {
	var out fanoutEventData
	// Unknown kinds fan out nowhere: skip the parse entirely so the hot
	// probe/queue emit paths pay only a map lookup. (Emit already rejects
	// non-object bodies, so the unmarshal below cannot fail.)
	switch row.Kind {
	case "task.created", "task.claimed", "task.closed",
		"ref.updated", "repo.created", "repo.deleted":
		out.known = true
	default:
		return out, nil
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(row.Body), &body); err != nil {
		return out, fmt.Errorf("fanout: %w: event body is not a JSON object", ErrInvalid)
	}
	out.body = body
	num := func(key string) int64 {
		v, ok := body[key]
		if !ok {
			return 0
		}
		switch n := v.(type) {
		case float64:
			return int64(n)
		case json.Number:
			i, _ := n.Int64()
			return i
		}
		return 0
	}
	str := func(key string) string {
		v, ok := body[key]
		if !ok {
			return ""
		}
		s, _ := v.(string)
		return s
	}
	switch row.Kind {
	case "task.created", "task.claimed", "task.closed":
		intentID := str("intent_id")
		if num("repo_id") == 0 || intentID == "" {
			return out, fmt.Errorf("fanout: %w: task event needs repo_id and intent_id", ErrInvalid)
		}
		out.known = true
		out.repoID = num("repo_id")
	case "ref.updated":
		if num("repo") == 0 || str("name") == "" {
			return out, fmt.Errorf("fanout: %w: ref.updated needs repo and name", ErrInvalid)
		}
		out.known = true
		out.repoID = num("repo")
		out.ref = str("name")
	case "repo.created", "repo.deleted":
		if num("repo_id") == 0 {
			return out, fmt.Errorf("fanout: %w: needs repo_id", ErrInvalid)
		}
		out.known = true
		out.repoID = num("repo_id")
	}
	return out, nil
}

// buildWebhookPayload serializes the canonical envelope ONCE. The stored bytes are
// signed and transmitted verbatim; never re-serialize before signing.
func buildWebhookPayload(msgID, eventType string, eventTime int64, data map[string]any) ([]byte, error) {
	env := map[string]any{
		"id":        msgID,
		"type":      eventType,
		"timestamp": time.UnixMilli(eventTime).UTC().Format(time.RFC3339Nano),
		"data":      data,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(env); err != nil {
		return nil, fmt.Errorf("fanout: %w", err)
	}
	raw := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if len(raw) > maxWebhookPayload {
		return nil, fmt.Errorf("fanout: %w: %d bytes", ErrTooLarge, len(raw))
	}
	return raw, nil
}

// webhookDataSchema freezes the whitelisted envelope data keys per event type.
// It is the store-side twin of internal/webhook.Catalogue (which the store
// cannot import: webhook imports store). Fan-out builds data objects with
// exactly these keys; internal body keys (purge_after, invocation ids, ...)
// never cross the boundary.
var webhookDataSchema = map[string][]string{
	"task.created": {"task_id", "task_number", "title", "state", "assignee", "created_by", "repo", "task_url", "event_id"},
	"task.claimed": {"task_id", "task_number", "title", "state", "assignee", "created_by", "repo", "task_url", "event_id"},
	"task.closed":  {"task_id", "task_number", "title", "state", "assignee", "created_by", "repo", "task_url", "event_id"},
	"ref.updated":  {"ref", "ref_kind", "old_oid", "new_oid", "change_kind", "pusher", "repo", "event_id"},
	"repo.created": {"repo_id", "full_name", "visibility", "default_branch", "fork_of", "created_by", "repo_url", "event_id"},
	"repo.deleted": {"repo_id", "full_name", "visibility", "default_branch", "fork_of", "deleted_by", "last_default_tip", "repo_url", "event_id"},
}

// userLink is a small id+name identity embedded in webhook data.
type userLink struct {
	Name string `json:"name"`
	ID   int64  `json:"id"`
}

type repoLink struct {
	FullName string `json:"full_name"`
	URL      string `json:"url"`
	ID       int64  `json:"id"`
}

func fanoutUserName(core *txCore, tx *sql.Tx, userID int64) (string, error) {
	var name string
	if serr := tx.QueryRowContext(core.ctx,
		`SELECT name FROM users WHERE id = ?`, userID).Scan(&name); serr != nil {
		return "", fmt.Errorf("fanoutWebhooks: %w", serr)
	}
	return name, nil
}

func fanoutRepoLink(core *txCore, tx *sql.Tx, repoID int64) (repoLink, int64, int64, string, error) {
	var out repoLink
	var ownerNull, orgNull sql.NullInt64
	var name, visibility, defaultBranch string
	var forkNull sql.NullInt64
	if serr := tx.QueryRowContext(core.ctx, `SELECT owner_user_id, org_id, name,
visibility, default_branch, fork_of_repo_id FROM repos WHERE id = ?`, repoID).Scan(
		&ownerNull, &orgNull, &name, &visibility, &defaultBranch, &forkNull); serr != nil {
		return out, 0, 0, "", serr
	}
	var ownerName string
	var ownerUser, orgID int64
	if ownerNull.Valid {
		ownerUser = ownerNull.Int64
		uname, uerr := fanoutUserName(core, tx, ownerUser)
		if uerr != nil {
			return out, 0, 0, "", uerr
		}
		ownerName = uname
	} else {
		orgID = orgNull.Int64
		var oname string
		if oerr := tx.QueryRowContext(core.ctx,
			`SELECT name FROM orgs WHERE id = ?`, orgID).Scan(&oname); oerr != nil {
			return out, 0, 0, "", fmt.Errorf("fanoutWebhooks: %w", oerr)
		}
		ownerName = oname
	}
	out = repoLink{ID: repoID, FullName: ownerName + "/" + name, URL: "/" + ownerName + "/" + name}
	return out, ownerUser, orgID, visibility, nil
}

func zeroOID(s string) bool {
	if s == "" {
		return true
	}
	for i := range len(s) {
		if s[i] != '0' {
			return false
		}
	}
	return true
}

func refChangeKind(oldOID, newOID string) string {
	switch {
	case zeroOID(oldOID) && !zeroOID(newOID):
		return "created"
	case !zeroOID(oldOID) && zeroOID(newOID):
		return "deleted"
	default:
		// Force-updates surface as updated: the body carries no ancestry flag.
		return "updated"
	}
}

func refKindOf(ref string) string {
	switch {
	case strings.HasPrefix(ref, "refs/heads/"):
		return "branch"
	case strings.HasPrefix(ref, "refs/tags/"):
		return "tag"
	default:
		return "other"
	}
}

// fanoutWebhookData builds the whitelisted envelope data object for one outbox
// row from in-transaction store reads. Internal body keys never pass through.
//
// URL deviation: the store transaction has no instance absolute base, so every
// URL is a RELATIVE path (/owner/repo, /owner/repo/tasks/<public-id>); the
// webhook worker (or API) prefixes the instance base before delivery docs.
func fanoutWebhookData(core *txCore, tx *sql.Tx, row OutboxRow, parsed fanoutEventData) (map[string]any, error) {
	repo, ownerUser, _, _, derr := fanoutRepoLink(core, tx, parsed.repoID)
	if derr != nil {
		if errors.Is(derr, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, derr
	}
	_ = ownerUser
	eventID := string(row.ID)
	switch row.Kind {
	case "task.created", "task.claimed", "task.closed":
		var number int64
		var title, state string
		var reporter, assignee sql.NullInt64
		serr := tx.QueryRowContext(core.ctx, `SELECT number, title, state,
reporter_id, assignee_id FROM intents WHERE repo_id = ? AND public_id = ?`,
			parsed.repoID, parsed.body["intent_id"]).Scan(&number, &title, &state, &reporter, &assignee)
		if serr != nil {
			if errors.Is(serr, sql.ErrNoRows) {
				return nil, fmt.Errorf("fanoutWebhooks: %w: intent is gone", ErrInvalid)
			}
			return nil, fmt.Errorf("fanoutWebhooks: %w", serr)
		}
		creatorName := ""
		var creatorID int64
		if reporter.Valid {
			creatorID = reporter.Int64
			cname, cerr := fanoutUserName(core, tx, creatorID)
			if cerr != nil {
				return nil, cerr
			}
			creatorName = cname
		}
		var assigneeLink any
		if assignee.Valid {
			aname, cerr := fanoutUserName(core, tx, assignee.Int64)
			if cerr != nil {
				return nil, cerr
			}
			assigneeLink = userLink{ID: assignee.Int64, Name: aname}
		}
		intentID, _ := parsed.body["intent_id"].(string)
		return map[string]any{
			"task_id": intentID, "task_number": number, "title": title, "state": state,
			"assignee":   assigneeLink,
			"created_by": userLink{ID: creatorID, Name: creatorName},
			"repo":       repo, "task_url": repo.URL + "/tasks/" + intentID, "event_id": eventID,
		}, nil
	case "ref.updated":
		oldOID, _ := parsed.body["old"].(string)
		newOID, _ := parsed.body["new"].(string)
		var pusherLink any
		if name, ok := parsed.body["pusher"].(string); ok && name != "" {
			var pid int64
			var canonical string
			if perr := tx.QueryRowContext(core.ctx,
				`SELECT id, name FROM users WHERE lower_name = ?`, strings.ToLower(name)).Scan(&pid, &canonical); perr == nil {
				pusherLink = userLink{ID: pid, Name: canonical}
			} else if !errors.Is(perr, sql.ErrNoRows) {
				return nil, fmt.Errorf("fanoutWebhooks: %w", perr)
			}
		}
		return map[string]any{
			"ref": parsed.ref, "ref_kind": refKindOf(parsed.ref),
			"old_oid": oldOID, "new_oid": newOID, "change_kind": refChangeKind(oldOID, newOID),
			"pusher": pusherLink, "repo": repo, "event_id": eventID,
		}, nil
	case "repo.created", "repo.deleted":
		var defaultBranch, visibility string
		var forkNull sql.NullInt64
		serr := tx.QueryRowContext(core.ctx,
			`SELECT visibility, default_branch, fork_of_repo_id FROM repos WHERE id = ?`,
			parsed.repoID).Scan(&visibility, &defaultBranch, &forkNull)
		if serr != nil {
			if errors.Is(serr, sql.ErrNoRows) {
				return nil, nil
			}
			return nil, fmt.Errorf("fanoutWebhooks: %w", serr)
		}
		var forkOf any
		if forkNull.Valid {
			parent, _, _, _, perr := fanoutRepoLink(core, tx, forkNull.Int64)
			if perr != nil {
				if !errors.Is(perr, sql.ErrNoRows) {
					return nil, perr
				}
			} else {
				forkOf = parent.FullName
			}
		}
		byKey := "created_by"
		if row.Kind == "repo.deleted" {
			byKey = "deleted_by"
		}
		byName := ""
		byID := int64(0)
		// The tombstone body names no actor; attribute to the owning identity.
		if ownerUser != 0 {
			byID = ownerUser
			uname, uerr := fanoutUserName(core, tx, ownerUser)
			if uerr != nil {
				return nil, uerr
			}
			byName = uname
		}
		data := map[string]any{
			"repo_id": parsed.repoID, "full_name": repo.FullName, "visibility": visibility,
			"default_branch": defaultBranch, "fork_of": forkOf,
			byKey:      userLink{ID: byID, Name: byName},
			"repo_url": repo.URL, "event_id": eventID,
		}
		if row.Kind == "repo.deleted" {
			tip := ""
			if terr := tx.QueryRowContext(core.ctx, `SELECT target FROM ref_tips
WHERE repo_id = ? AND name = ?`, parsed.repoID, "refs/heads/"+defaultBranch).Scan(&tip); terr == nil {
				data["last_default_tip"] = tip
			} else if !errors.Is(terr, sql.ErrNoRows) {
				return nil, fmt.Errorf("fanoutWebhooks: %w", terr)
			} else {
				data["last_default_tip"] = nil
			}
		}
		return data, nil
	}
	return nil, nil
}

func fanoutWebhooks(core *txCore, row OutboxRow) error {
	parsed, err := parseFanoutBody(row)
	if err != nil {
		return err
	}
	if !parsed.known {
		return nil
	}
	tx, err := core.live()
	if err != nil {
		return err
	}
	repo, ownerUser, orgID, _, derr := fanoutRepoLink(core, tx, parsed.repoID)
	if derr != nil {
		if errors.Is(derr, sql.ErrNoRows) {
			return nil
		}
		return derr
	}
	_ = repo
	data, derr := fanoutWebhookData(core, tx, row, parsed)
	if derr != nil {
		return derr
	}
	if data == nil {
		return nil
	}
	if want, ok := webhookDataSchema[row.Kind]; ok {
		for _, key := range want {
			if _, present := data[key]; !present {
				return fmt.Errorf("fanoutWebhooks: %w: data missing %q", ErrInvalid, key)
			}
		}
	}
	rows, err := tx.QueryContext(core.ctx, `SELECT `+webhookEndpointColumns+` FROM webhook_endpoints
WHERE active = 1 AND ((scope = 'repo' AND repo_id = ?)
OR (scope = 'owner' AND owner_kind = 'user' AND owner_id = ?)
OR (scope = 'owner' AND owner_kind = 'org' AND owner_id = ?))`,
		parsed.repoID, ownerUser, orgID)
	if err != nil {
		return fmt.Errorf("fanoutWebhooks: %w", err)
	}
	var endpoints []WebhookEndpointRow
	for rows.Next() {
		ep, serr := scanWebhookEndpoint(rows.Scan)
		if serr != nil {
			_ = rows.Close()
			return serr
		}
		endpoints = append(endpoints, ep)
	}
	if cerr := rows.Err(); cerr != nil {
		_ = rows.Close()
		return fmt.Errorf("fanoutWebhooks: %w", cerr)
	}
	_ = rows.Close()
	eventMillis := msOf(row.OccurredAt)
	msgs := make([]WebhookMessageInsert, 0, len(endpoints))
	for _, ep := range endpoints {
		matched := false
		for _, t := range ep.EventTypes {
			if t == row.Kind {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if row.Kind == "ref.updated" && len(ep.RefGlobs) > 0 {
			ok := false
			for _, g := range ep.RefGlobs {
				if matchRefGlob(g, parsed.ref) {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		// Each delivery gets its own message id (the primary key) stamped in
		// both the row and its envelope; payloads are otherwise identical.
		msgID := "msg_" + string(NewID(row.OccurredAt))
		perPayload, berr := buildWebhookPayload(msgID, row.Kind, eventMillis, data)
		if berr != nil {
			return berr
		}
		msgs = append(msgs, WebhookMessageInsert{
			ID: msgID, EndpointID: ep.ID, EventType: row.Kind,
			OutboxSeq: row.Seq, EventTime: eventMillis, NextAttemptAt: eventMillis,
			Payload: perPayload,
		})
	}
	if len(msgs) == 0 {
		return nil
	}
	inner := WriteTx{txCore: *core}
	return inner.InsertWebhookMessages(msgs)
}
