package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	jsonv1 "encoding/json"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/webhook"
)

func nonemptyStrings(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	return slices.Clone(in)
}

// Webhook endpoint and delivery ops (F8). Repo-family paths carry the owner
// and repo segments; endpoint authorization always follows the ENDPOINT's own
// scope, never the path segments:
//
//   - repo-scope endpoints: repo admin of the endpoint's repo.
//   - owner-scope endpoints: owner admin of the endpoint's owner (the owning
//     user, or the owners team / site admin for organizations).
//
// A path repo that disagrees with a repo-scope endpoint's repo, or any path
// repo on an owner-scope endpoint, is ignored after the endpoint loads. A
// literal "-" repo segment routes the same op and is the conventional
// placeholder when no repository is in context (owner hooks page). Owner
// scope creation reads the owner from the path (In.owner may repeat it or be
// absent); any other In.owner is rejected.

const (
	webhookDefaultConcurrency int64 = 4
	webhookMinConcurrency     int64 = 1
	webhookMaxConcurrency     int64 = 16
	webhookDefaultTimeout     int64 = 20
	webhookMinTimeout         int64 = 5
	webhookMaxTimeout         int64 = 60
	webhookMaxNoteLen         int   = 256
	webhookMaxRefGlobs        int   = 32
	webhookMaxEndpointsRepo   int   = 32
	webhookMaxEndpointsOwner  int   = 64
	webhookListDefault        int   = 30
	webhookListMax            int   = 100
	webhookAttemptListMax     int   = 100
)

// WebhookEndpointJSON is one endpoint. Secret is present only in the create
// and rotate-secret responses that mint it; it is never stored or returned
// again.
type WebhookEndpointJSON struct {
	Secret              string   `json:"secret,omitempty"`
	ID                  string   `json:"id"`
	Scope               string   `json:"scope"`
	Repo                string   `json:"repo,omitempty"`
	Owner               string   `json:"owner,omitempty"`
	URL                 string   `json:"url"`
	Note                string   `json:"note,omitempty"`
	LastStatus          string   `json:"last_status"`
	DisableReason       string   `json:"disable_reason,omitempty"`
	EventTypes          []string `json:"event_types"`
	RefGlobs            []string `json:"ref_globs"`
	Concurrency         int64    `json:"concurrency"`
	TimeoutSeconds      int64    `json:"timeout_seconds"`
	ConsecutiveFailures int64    `json:"consecutive_failures"`
	DisabledAt          int64    `json:"disabled_at,omitempty"`
	CreatedAt           int64    `json:"created_at"`
	UpdatedAt           int64    `json:"updated_at"`
	CreatedBy           int64    `json:"created_by"`
	Active              bool     `json:"active"`
}

// WebhookMessageJSON is one outbox/delivery-log row. Payload bytes travel
// only on the single-message read; the list carries metadata.
type WebhookMessageJSON struct {
	ID             string            `json:"id"`
	EndpointID     string            `json:"endpoint_id"`
	EventType      string            `json:"event_type"`
	State          string            `json:"state"`
	LastError      string            `json:"last_error,omitempty"`
	RedeliveryOf   string            `json:"redelivery_of,omitempty"`
	Payload        jsonv1.RawMessage `json:"payload,omitempty"`
	EventTime      int64             `json:"event_time"`
	AttemptCount   int64             `json:"attempt_count"`
	NextAttemptAt  int64             `json:"next_attempt_at"`
	LastStatusCode int64             `json:"last_status_code,omitempty"`
	DeliveredAt    int64             `json:"delivered_at,omitempty"`
}

// WebhookAttemptJSON is one recorded delivery attempt. Request headers carry
// the signature presence marker instead of the value.
type WebhookAttemptJSON struct {
	RespBody         string            `json:"resp_body,omitempty"`
	Error            string            `json:"error,omitempty"`
	MessageID        string            `json:"message_id"`
	RespHeaders      jsonv1.RawMessage `json:"resp_headers,omitempty"`
	RequestHeaders   jsonv1.RawMessage `json:"request_headers,omitempty"`
	ID               int64             `json:"id"`
	AttemptNo        int64             `json:"attempt_no"`
	StartedAt        int64             `json:"started_at"`
	FinishedAt       int64             `json:"finished_at"`
	AttemptTimestamp int64             `json:"attempt_timestamp,omitzero"`
	HTTPStatus       int64             `json:"http_status,omitempty"`
	DurationMs       int64             `json:"duration_ms"`
}

// WebhookEndpointCreateBody carries one endpoint create. OwnerName repeats
// the path owner for owner scope (equal-or-absent); the path segment keeps
// the bare "owner" argument name.
type WebhookEndpointCreateBody struct {
	Active         *bool    `json:"active,omitzero"`
	Scope          string   `json:"scope,omitempty"`
	Owner          string   `json:"owner_name,omitempty"`
	URL            string   `json:"url"`
	Note           string   `json:"note,omitempty"`
	EventTypes     []string `json:"event_types"`
	RefGlobs       []string `json:"ref_globs,omitempty"`
	Concurrency    int64    `json:"concurrency,omitzero"`
	TimeoutSeconds int64    `json:"timeout_seconds,omitzero"`
}

// WebhookEndpointCreateIn carries one endpoint create with its idempotency key.
type WebhookEndpointCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Body           WebhookEndpointCreateBody
}

// WebhookEndpointCreateOut carries the created endpoint with its secret shown once.
type WebhookEndpointCreateOut struct {
	Endpoint WebhookEndpointJSON `json:"endpoint"`
}

// WebhookEndpointListIn carries one endpoint list.
type WebhookEndpointListIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Scope  string `query:"scope"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

// WebhookEndpointListOut carries one endpoint page without secrets.
type WebhookEndpointListOut struct {
	Next      string                `json:"-"`
	Endpoints []WebhookEndpointJSON `json:"endpoints"`
}

// WebhookEndpointGetIn carries one endpoint read.
type WebhookEndpointGetIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	ID    string `path:"id"`
}

// WebhookEndpointGetOut carries one endpoint without its secret.
type WebhookEndpointGetOut struct {
	Endpoint WebhookEndpointJSON `json:"endpoint"`
}

// WebhookEndpointUpdateBody carries one partial endpoint update.
type WebhookEndpointUpdateBody struct {
	URL            *string   `json:"url,omitempty"`
	EventTypes     *[]string `json:"event_types,omitempty"`
	RefGlobs       *[]string `json:"ref_globs,omitempty"`
	Active         *bool     `json:"active,omitzero"`
	Note           *string   `json:"note,omitempty"`
	Concurrency    *int64    `json:"concurrency,omitzero"`
	TimeoutSeconds *int64    `json:"timeout_seconds,omitzero"`
}

// WebhookEndpointUpdateIn carries one endpoint update with its idempotency key.
type WebhookEndpointUpdateIn struct {
	Body           WebhookEndpointUpdateBody
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	ID             string `path:"id"`
}

// WebhookEndpointUpdateOut carries the updated endpoint without its secret.
type WebhookEndpointUpdateOut struct {
	Endpoint WebhookEndpointJSON `json:"endpoint"`
}

// WebhookEndpointDeleteIn carries one endpoint delete with its idempotency key.
type WebhookEndpointDeleteIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	ID             string `path:"id"`
}

// WebhookEndpointDeleteOut carries the delete receipt.
type WebhookEndpointDeleteOut struct {
	Deleted bool `json:"deleted"`
}

// WebhookEndpointRotateIn carries one secret rotation with its idempotency key.
type WebhookEndpointRotateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	ID             string `path:"id"`
}

// WebhookEndpointRotateOut carries the endpoint with the new secret shown once.
type WebhookEndpointRotateOut struct {
	Endpoint WebhookEndpointJSON `json:"endpoint"`
}

// WebhookEndpointTestIn carries one endpoint test with its idempotency key.
type WebhookEndpointTestIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	ID             string `path:"id"`
}

// WebhookEndpointTestOut carries the synchronous ping result. The ping never
// touches the outbox or message tables.
type WebhookEndpointTestOut struct {
	Error      string `json:"error,omitempty"`
	Ok         bool   `json:"ok"`
	HTTPStatus int64  `json:"http_status"`
	DurationMs int64  `json:"duration_ms"`
}

// WebhookMessageListIn carries one message list.
type WebhookMessageListIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	ID     string `path:"id"`
	State  string `query:"state"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

// WebhookMessageListOut carries one message page without payload bytes.
type WebhookMessageListOut struct {
	Next     string               `json:"-"`
	Messages []WebhookMessageJSON `json:"messages"`
}

// WebhookMessageGetIn carries one message read.
type WebhookMessageGetIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	ID    string `path:"id"`
	MID   string `path:"mid"`
}

// WebhookMessageGetOut carries one message with its payload bytes.
type WebhookMessageGetOut struct {
	Message WebhookMessageJSON `json:"message"`
}

// WebhookMessageRedeliverIn carries one redelivery with its idempotency key.
type WebhookMessageRedeliverIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	ID             string `path:"id"`
	MID            string `path:"mid"`
}

// WebhookMessageRedeliverOut carries the re-queued message.
type WebhookMessageRedeliverOut struct {
	Message WebhookMessageJSON `json:"message"`
}

// WebhookAttemptListIn carries one attempt list.
type WebhookAttemptListIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	ID    string `path:"id"`
	MID   string `path:"mid"`
	Limit int    `query:"limit"`
}

// WebhookAttemptListOut carries the retained attempts of one message.
type WebhookAttemptListOut struct {
	Attempts []WebhookAttemptJSON `json:"attempts"`
}

func mapWebhookStore(err error, notFound string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return op.Errorf(op.CodeNotFound, "%s", notFound)
	}
	if oerr, ok := errors.AsType[*op.Error](err); ok {
		return oerr
	}
	if errors.Is(err, store.ErrInvalid) {
		return op.Errorf(op.CodeValidation, "webhook input is not valid")
	}
	if errors.Is(err, store.ErrConflict) {
		return op.Errorf(op.CodeConflict, "webhook endpoint already exists")
	}
	return op.Wrap(op.CodeInternal, err, "webhook store failed")
}

func webhookLimit(limit, def, upper int) int {
	if limit <= 0 {
		return def
	}
	if limit > upper {
		return upper
	}
	return limit
}

func (d Deps) requireRepoAdmin(rq requester, row store.RepoRow) error {
	if !rq.caller.Active {
		return op.Errorf(op.CodeForbidden, "repository access was refused")
	}
	if !rq.tokenAllows(row.ID) {
		return op.Errorf(op.CodeNotFound, "repository was not found")
	}
	resolver := d.Store.Resolver()
	if rq.userID != 0 && resolver.IsSiteAdmin(rq.userID) {
		return nil
	}
	var grant access.Role
	if role, ok := resolver.DirectRole(row.ID, rq.userID); ok && row.OrgID == 0 && role > grant {
		grant = role
	}
	if role, ok := resolver.TeamRole(row.ID, rq.userID); ok && role > grant {
		grant = role
	}
	if rq.userID != 0 {
		if row.OrgID == 0 {
			if row.OwnerUserID.Valid && row.OwnerUserID.Int64 == rq.userID {
				grant = access.RoleAdmin
			}
		} else if resolver.IsOrgOwner(row.OrgID, rq.userID) {
			grant = access.RoleAdmin
		}
	}
	if grant < access.RoleAdmin {
		return op.Errorf(op.CodeForbidden, "repository access was refused")
	}
	return nil
}

// webhookOwnerKind resolves an owner name to its user or org id. Either a
// user or an org may own the name, never both.
type webhookOwnerKind struct {
	name  string
	id    int64
	isOrg bool
}

func (d Deps) resolveWebhookOwner(ctx context.Context, owner string) (webhookOwnerKind, error) {
	if err := checkOwnerName(owner); err != nil {
		return webhookOwnerKind{}, err
	}
	umeta, uerr := d.Store.LookupUserByName(ctx, strings.ToLower(owner))
	if uerr == nil {
		return webhookOwnerKind{name: umeta.Name, id: umeta.ID}, nil
	}
	if !errors.Is(uerr, store.ErrNotFound) {
		return webhookOwnerKind{}, op.Wrap(op.CodeInternal, uerr, "webhook owner lookup failed")
	}
	var org store.OrgRow
	serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		var gerr error
		org, gerr = tx.FindOrgByName(owner)
		return gerr
	})
	if serr != nil {
		if errors.Is(serr, store.ErrNotFound) {
			return webhookOwnerKind{}, op.Errorf(op.CodeNotFound, "webhook owner was not found")
		}
		return webhookOwnerKind{}, op.Wrap(op.CodeInternal, serr, "webhook owner lookup failed")
	}
	return webhookOwnerKind{name: org.Name, id: org.ID, isOrg: true}, nil
}

func (d Deps) requireOwnerAdmin(rq requester, owner webhookOwnerKind) error {
	if !rq.caller.Active {
		return op.Errorf(op.CodeForbidden, "webhook owner access was denied")
	}
	if rq.userID == 0 {
		return op.Errorf(op.CodeForbidden, "webhook owner access was denied")
	}
	if rq.isToken && len(rq.tokenRepos) > 0 {
		return op.Errorf(op.CodeNotFound, "webhook owner was not found")
	}
	resolver := d.Store.Resolver()
	if resolver.IsSiteAdmin(rq.userID) {
		return nil
	}
	if owner.isOrg {
		if resolver.IsOrgOwner(owner.id, rq.userID) {
			return nil
		}
		return op.Errorf(op.CodeForbidden, "webhook owner access was denied")
	}
	if rq.userID == owner.id {
		return nil
	}
	return op.Errorf(op.CodeForbidden, "webhook owner access was denied")
}

// webhookOwnerByID resolves an owner id stored on an endpoint row. The row's
// owner kind selects the namespace. An empty kind on an owner row is corrupt
// (the store requires user|org); it fails closed as not-found so no caller is
// authorized against the wrong namespace.
func (d Deps) webhookOwnerByID(ctx context.Context, ownerID int64, kind string) (webhookOwnerKind, error) {
	switch kind {
	case "org":
		org, oerr := d.webhookOrgByID(ctx, ownerID)
		if oerr != nil {
			return webhookOwnerKind{}, oerr
		}
		return webhookOwnerKind{name: org.Name, id: org.ID, isOrg: true}, nil
	case "user":
		umeta, uerr := d.Store.LookupUserByID(ctx, ownerID)
		if uerr != nil {
			return webhookOwnerKind{}, mapWebhookStore(uerr, "webhook endpoint was not found")
		}
		return webhookOwnerKind{name: umeta.Name, id: umeta.ID}, nil
	default:
		return webhookOwnerKind{}, op.Errorf(op.CodeNotFound, "webhook endpoint was not found")
	}
}

func (d Deps) webhookOrgByID(ctx context.Context, orgID int64) (store.OrgRow, error) {
	var org store.OrgRow
	serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		var gerr error
		org, gerr = tx.GetOrg(orgID)
		return gerr
	})
	if serr != nil {
		if errors.Is(serr, store.ErrNotFound) {
			return store.OrgRow{}, op.Errorf(op.CodeNotFound, "webhook endpoint was not found")
		}
		return store.OrgRow{}, op.Wrap(op.CodeInternal, serr, "webhook owner lookup failed")
	}
	return org, nil
}

// webhookEndpointAccess is an endpoint row plus whichever scope principal was
// authorized against it.
type webhookEndpointAccess struct {
	owner     webhookOwnerKind
	row       store.WebhookEndpointRow
	repo      store.RepoRow
	repoValid bool
}

// loadWebhookEndpoint loads an endpoint by id and authorizes the caller
// against the endpoint's own scope. Path segments are already spent: a
// disagreeing path repo never overrides the endpoint's repo.
func (d Deps) loadWebhookEndpoint(ctx context.Context, rq requester, id string) (webhookEndpointAccess, error) {
	var out webhookEndpointAccess
	if strings.TrimSpace(id) == "" {
		return out, op.Errorf(op.CodeInvalid, "webhook endpoint id is required")
	}
	var row store.WebhookEndpointRow
	serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		var gerr error
		row, gerr = tx.GetWebhookEndpoint(id)
		return gerr
	})
	if serr != nil {
		return out, mapWebhookStore(serr, "webhook endpoint was not found")
	}
	out.row = row
	if row.Scope == "repo" {
		var repo store.RepoRow
		rerr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			var gerr error
			repo, gerr = tx.GetRepo(row.RepoID)
			return gerr
		})
		if rerr != nil {
			return out, mapWebhookStore(rerr, "webhook endpoint was not found")
		}
		if derr := d.requireRepoAdmin(rq, repo); derr != nil {
			return out, derr
		}
		out.repo, out.repoValid = repo, true
		return out, nil
	}
	owner, oerr := d.webhookOwnerByID(ctx, row.OwnerID, row.OwnerKind)
	if oerr != nil {
		return out, oerr
	}
	if derr := d.requireOwnerAdmin(rq, owner); derr != nil {
		return out, derr
	}
	out.owner = owner
	return out, nil
}

// webhookSealAAD binds an endpoint secret to its endpoint id.
func webhookSealAAD(endpointID string) string {
	return "webhook-endpoint-secret:" + endpointID
}

// sealWebhookSecret seals raw secret bytes under aad. Every endpoint secret is
// sealed under its endpoint id; the one throwaway provisioning seal (replaced
// by rotation before create returns) uses the provisioning aad.
func (d Deps) sealWebhookSecret(aad string, raw []byte) ([]byte, error) {
	if len(d.SealKey) == 0 {
		return nil, op.Errorf(op.CodeInternal, "webhook seal key is not configured")
	}
	sealed, serr := webhook.SealSecret(d.SealKey, raw, aad)
	if serr != nil {
		return nil, op.Wrap(op.CodeInternal, serr, "webhook secret seal failed")
	}
	return sealed, nil
}

func (d Deps) openWebhookSecret(row store.WebhookEndpointRow) ([]byte, error) {
	if len(d.SealKey) == 0 {
		return nil, op.Errorf(op.CodeInternal, "webhook seal key is not configured")
	}
	raw, oerr := webhook.OpenSecret(string(d.SealKey), base64.StdEncoding.EncodeToString(row.SecretEnc), webhookSealAAD(row.ID))
	if oerr != nil {
		return nil, op.Wrap(op.CodeInternal, oerr, "webhook secret open failed")
	}
	return raw, nil
}

func webhookRepoFullName(ctx context.Context, d Deps, repo store.RepoRow) (string, error) {
	if repo.OrgID != 0 {
		var org store.OrgRow
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			var gerr error
			org, gerr = tx.GetOrg(repo.OrgID)
			return gerr
		})
		if serr != nil {
			return "", mapWebhookStore(serr, "webhook endpoint was not found")
		}
		return org.Name + "/" + repo.Name, nil
	}
	if !repo.OwnerUserID.Valid {
		return "", op.Errorf(op.CodeNotFound, "webhook endpoint was not found")
	}
	umeta, uerr := d.Store.LookupUserByID(ctx, repo.OwnerUserID.Int64)
	if uerr != nil {
		return "", mapWebhookStore(uerr, "webhook endpoint was not found")
	}
	return umeta.Name + "/" + repo.Name, nil
}

func (d Deps) webhookEndpointJSON(ctx context.Context, row store.WebhookEndpointRow, secret string) (WebhookEndpointJSON, error) {
	out := WebhookEndpointJSON{
		ID: row.ID, Scope: row.Scope, URL: row.URL,
		EventTypes: nonemptyStrings(row.EventTypes),
		RefGlobs:   nonemptyStrings(row.RefGlobs),
		Active:     row.Active, Concurrency: row.Concurrency,
		TimeoutSeconds: row.TimeoutSeconds, Note: row.Note,
		LastStatus: row.LastStatus, ConsecutiveFailures: row.ConsecutiveFailures,
		DisabledAt: row.DisabledAt, DisableReason: row.DisableReason,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CreatedBy: row.CreatedBy,
	}
	if secret != "" {
		out.Secret = secret
	}
	if row.Scope == "repo" {
		var repo store.RepoRow
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			var gerr error
			repo, gerr = tx.GetRepo(row.RepoID)
			return gerr
		})
		if serr != nil {
			return WebhookEndpointJSON{}, mapWebhookStore(serr, "webhook endpoint was not found")
		}
		full, ferr := webhookRepoFullName(ctx, d, repo)
		if ferr != nil {
			return WebhookEndpointJSON{}, ferr
		}
		out.Repo = full
		return out, nil
	}
	owner, oerr := d.webhookOwnerByID(ctx, row.OwnerID, row.OwnerKind)
	if oerr != nil {
		return WebhookEndpointJSON{}, oerr
	}
	out.Owner = owner.name
	return out, nil
}

func webhookMessageJSON(row store.WebhookMessageRow) WebhookMessageJSON {
	return WebhookMessageJSON{
		ID: row.ID, EndpointID: row.EndpointID, EventType: row.EventType,
		State: row.State, LastError: row.LastError, RedeliveryOf: row.RedeliveryOf,
		EventTime: row.EventTime, AttemptCount: row.AttemptCount,
		NextAttemptAt: row.NextAttemptAt, LastStatusCode: row.LastStatusCode,
		DeliveredAt: row.DeliveredAt,
	}
}

func webhookMessageJSONWithPayload(row store.WebhookMessageRow) WebhookMessageJSON {
	out := webhookMessageJSON(row)
	if len(row.Payload) > 0 {
		out.Payload = slices.Clone(row.Payload)
	}
	return out
}

func webhookAttemptJSON(row store.WebhookAttemptRow) WebhookAttemptJSON {
	out := WebhookAttemptJSON{
		MessageID: row.MessageID, ID: row.ID, AttemptNo: row.AttemptNo,
		StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
		AttemptTimestamp: row.AttemptTimestamp,
		HTTPStatus:       row.HTTPStatus, Error: row.Error, DurationMs: row.DurationMs,
	}
	// Request headers arrive redacted upstream (signature presence marker);
	// empty bytes stay omitted.
	if len(row.ReqHeaders) > 0 {
		var headers map[string]any
		if uerr := unmarshalObject(row.ReqHeaders, &headers); uerr == nil {
			encoded, merr := marshalJSON(headers)
			if merr == nil {
				out.RequestHeaders = encoded
			}
		}
	}
	if len(row.RespHeaders) > 0 {
		out.RespHeaders = slices.Clone(row.RespHeaders)
	}
	if len(row.RespBody) > 0 {
		out.RespBody = string(row.RespBody)
	}
	return out
}

func validateWebhookCreate(body WebhookEndpointCreateBody) (scope string, concurrency, timeout int64, eventTypes []string, err error) {
	scope = strings.TrimSpace(body.Scope)
	if scope == "" {
		scope = "repo"
	}
	if scope != "repo" && scope != "owner" {
		return "", 0, 0, nil, op.Errorf(op.CodeInvalid, "webhook scope must be repo or owner")
	}
	if strings.TrimSpace(body.URL) == "" {
		return "", 0, 0, nil, op.Errorf(op.CodeInvalid, "webhook url is required")
	}
	known := map[string]bool{}
	for _, name := range webhook.FreezeEventTypes() {
		known[name] = true
	}
	seen := map[string]bool{}
	frozen := make([]string, 0, len(body.EventTypes))
	for _, name := range body.EventTypes {
		if !known[name] {
			return "", 0, 0, nil, op.Errorf(op.CodeInvalid, "unknown webhook event type %q", name)
		}
		if !seen[name] {
			seen[name] = true
			frozen = append(frozen, name)
		}
	}
	if len(frozen) == 0 {
		return "", 0, 0, nil, op.Errorf(op.CodeInvalid, "webhook event types must not be empty")
	}
	slices.Sort(frozen)
	hasRefUpdated := false
	for _, name := range frozen {
		if name == "ref.updated" {
			hasRefUpdated = true
			break
		}
	}
	if len(body.RefGlobs) > 0 && !hasRefUpdated {
		return "", 0, 0, nil, op.Errorf(op.CodeInvalid, "ref globs require the ref.updated event type")
	}
	if len(body.RefGlobs) > webhookMaxRefGlobs {
		return "", 0, 0, nil, op.Errorf(op.CodeInvalid, "too many ref globs")
	}
	if len(body.Note) > webhookMaxNoteLen {
		return "", 0, 0, nil, op.Errorf(op.CodeInvalid, "webhook note is too long")
	}
	concurrency = body.Concurrency
	if concurrency == 0 {
		concurrency = webhookDefaultConcurrency
	}
	if concurrency < webhookMinConcurrency {
		concurrency = webhookMinConcurrency
	}
	if concurrency > webhookMaxConcurrency {
		concurrency = webhookMaxConcurrency
	}
	timeout = body.TimeoutSeconds
	if timeout == 0 {
		timeout = webhookDefaultTimeout
	}
	if timeout < webhookMinTimeout {
		timeout = webhookMinTimeout
	}
	if timeout > webhookMaxTimeout {
		timeout = webhookMaxTimeout
	}
	return scope, concurrency, timeout, frozen, nil
}

func webhookEndpointCreateHandler(d Deps) func(context.Context, op.Principal, WebhookEndpointCreateIn) (WebhookEndpointCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in WebhookEndpointCreateIn) (WebhookEndpointCreateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return WebhookEndpointCreateOut{}, rerr
		}
		scope, concurrency, timeout, eventTypes, verr := validateWebhookCreate(in.Body)
		if verr != nil {
			return WebhookEndpointCreateOut{}, verr
		}
		if _, perr := d.Webhooks.CheckURL(in.Body.URL); perr != nil {
			return WebhookEndpointCreateOut{}, op.Wrap(op.CodeInvalid, perr, "webhook url is not allowed")
		}
		var repoID, ownerID int64
		ownerKind := ""
		active := in.Body.Active == nil || *in.Body.Active
		if scope == "repo" {
			rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
			if cerr != nil {
				return WebhookEndpointCreateOut{}, cerr
			}
			if derr := d.requireRepoAdmin(rq, rc.row); derr != nil {
				return WebhookEndpointCreateOut{}, derr
			}
			repoID = rc.row.ID
			count, cerr := webhookCountForRepo(ctx, d, repoID)
			if cerr != nil {
				return WebhookEndpointCreateOut{}, cerr
			}
			if count >= webhookMaxEndpointsRepo {
				return WebhookEndpointCreateOut{}, op.Errorf(op.CodeLimitExceeded, "too many webhook endpoints for this repository")
			}
		} else {
			owner, oerr := d.resolveWebhookOwner(ctx, in.Owner)
			if oerr != nil {
				return WebhookEndpointCreateOut{}, oerr
			}
			if derr := d.requireOwnerAdmin(rq, owner); derr != nil {
				return WebhookEndpointCreateOut{}, derr
			}
			if trimmed := strings.TrimSpace(in.Body.Owner); trimmed != "" &&
				!strings.EqualFold(trimmed, in.Owner) && !strings.EqualFold(trimmed, owner.name) {
				return WebhookEndpointCreateOut{}, op.Errorf(op.CodeInvalid, "webhook owner must match the path owner")
			}
			ownerID = owner.id
			if owner.isOrg {
				ownerKind = "org"
			} else {
				ownerKind = "user"
			}
			count, cerr := webhookCountForOwner(ctx, d, ownerID)
			if cerr != nil {
				return WebhookEndpointCreateOut{}, cerr
			}
			if count >= webhookMaxEndpointsOwner {
				return WebhookEndpointCreateOut{}, op.Errorf(op.CodeLimitExceeded, "too many webhook endpoints for this owner")
			}
		}
		display, raw, gerr := webhook.GenerateSecret()
		if gerr != nil {
			return WebhookEndpointCreateOut{}, op.Wrap(op.CodeInternal, gerr, "webhook secret generation failed")
		}
		if !webhook.IsValidSecretDisplay(display) {
			return WebhookEndpointCreateOut{}, op.Errorf(op.CodeInternal, "webhook secret generation failed")
		}
		// The endpoint id is handler-generated so the secret seals under the
		// final id aad in a single insert; fresh endpoints carry no previous
		// secret.
		now := d.now()
		endpointID := "wh_" + string(store.NewID(now))
		sealed, serr := d.sealWebhookSecret(webhookSealAAD(endpointID), raw)
		if serr != nil {
			return WebhookEndpointCreateOut{}, serr
		}
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			row, ierr := tx.InsertWebhookEndpoint(now, store.WebhookEndpointInsert{
				ID: endpointID, Scope: scope, RepoID: repoID, OwnerID: ownerID, OwnerKind: ownerKind, URL: in.Body.URL,
				SecretEnc: sealed, EventTypes: eventTypes, RefGlobs: in.Body.RefGlobs,
				Active: active, Concurrency: concurrency, TimeoutSeconds: timeout,
				Note: in.Body.Note, CreatedBy: rq.userID,
			})
			if ierr != nil {
				return ierr
			}
			endpointID = row.ID
			return nil
		})
		if werr != nil {
			return WebhookEndpointCreateOut{}, mapWebhookStore(werr, "webhook endpoint was not found")
		}
		var row store.WebhookEndpointRow
		serr = d.Store.Read(ctx, func(tx *store.ReadTx) error {
			var gerr error
			row, gerr = tx.GetWebhookEndpoint(endpointID)
			return gerr
		})
		if serr != nil {
			return WebhookEndpointCreateOut{}, mapWebhookStore(serr, "webhook endpoint was not found")
		}
		rendered, rerr := d.webhookEndpointJSON(ctx, row, display)
		if rerr != nil {
			return WebhookEndpointCreateOut{}, rerr
		}
		return WebhookEndpointCreateOut{Endpoint: rendered}, nil
	}
}

func webhookCountForRepo(ctx context.Context, d Deps, repoID int64) (int, error) {
	var count int
	serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		var gerr error
		count, gerr = tx.CountWebhookEndpointsByRepo(repoID)
		return gerr
	})
	if serr != nil {
		return 0, op.Wrap(op.CodeInternal, serr, "webhook endpoint count failed")
	}
	return count, nil
}

func webhookCountForOwner(ctx context.Context, d Deps, ownerID int64) (int, error) {
	var count int
	serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		var gerr error
		count, gerr = tx.CountWebhookEndpointsByOwner(ownerID)
		return gerr
	})
	if serr != nil {
		return 0, op.Wrap(op.CodeInternal, serr, "webhook endpoint count failed")
	}
	return count, nil
}

func webhookEndpointListHandler(d Deps) func(context.Context, op.Principal, WebhookEndpointListIn) (WebhookEndpointListOut, error) {
	return func(ctx context.Context, p op.Principal, in WebhookEndpointListIn) (WebhookEndpointListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return WebhookEndpointListOut{}, rerr
		}
		scope := strings.TrimSpace(in.Scope)
		if scope == "" {
			scope = "repo"
		}
		if scope != "repo" && scope != "owner" {
			return WebhookEndpointListOut{}, op.Errorf(op.CodeInvalid, "webhook scope must be repo or owner")
		}
		limit := webhookLimit(in.Limit, webhookListDefault, webhookListMax)
		out := WebhookEndpointListOut{Endpoints: []WebhookEndpointJSON{}}
		var rows []store.WebhookEndpointRow
		var next string
		if scope == "repo" {
			rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
			if cerr != nil {
				return WebhookEndpointListOut{}, cerr
			}
			if derr := d.requireRepoAdmin(rq, rc.row); derr != nil {
				return WebhookEndpointListOut{}, derr
			}
			serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
				got, cursor, lerr := tx.ListWebhookEndpointsByRepo(rc.row.ID, in.Cursor, limit)
				if lerr != nil {
					return lerr
				}
				rows, next = got, cursor
				return nil
			})
			if serr != nil {
				return WebhookEndpointListOut{}, mapWebhookStore(serr, "webhook endpoint list failed")
			}
		} else {
			owner, oerr := d.resolveWebhookOwner(ctx, in.Owner)
			if oerr != nil {
				return WebhookEndpointListOut{}, oerr
			}
			if derr := d.requireOwnerAdmin(rq, owner); derr != nil {
				return WebhookEndpointListOut{}, derr
			}
			serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
				got, cursor, lerr := tx.ListWebhookEndpointsByOwner(owner.id, in.Cursor, limit)
				if lerr != nil {
					return lerr
				}
				rows, next = got, cursor
				return nil
			})
			if serr != nil {
				return WebhookEndpointListOut{}, mapWebhookStore(serr, "webhook endpoint list failed")
			}
		}
		for _, row := range rows {
			rendered, rerr := d.webhookEndpointJSON(ctx, row, "")
			if rerr != nil {
				return WebhookEndpointListOut{}, rerr
			}
			out.Endpoints = append(out.Endpoints, rendered)
		}
		out.Next = next
		return out, nil
	}
}

func webhookEndpointGetHandler(d Deps) func(context.Context, op.Principal, WebhookEndpointGetIn) (WebhookEndpointGetOut, error) {
	return func(ctx context.Context, p op.Principal, in WebhookEndpointGetIn) (WebhookEndpointGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return WebhookEndpointGetOut{}, rerr
		}
		access, lerr := d.loadWebhookEndpoint(ctx, rq, in.ID)
		if lerr != nil {
			return WebhookEndpointGetOut{}, lerr
		}
		rendered, rerr := d.webhookEndpointJSON(ctx, access.row, "")
		if rerr != nil {
			return WebhookEndpointGetOut{}, rerr
		}
		return WebhookEndpointGetOut{Endpoint: rendered}, nil
	}
}

func webhookEndpointUpdateHandler(d Deps) func(context.Context, op.Principal, WebhookEndpointUpdateIn) (WebhookEndpointUpdateOut, error) {
	return func(ctx context.Context, p op.Principal, in WebhookEndpointUpdateIn) (WebhookEndpointUpdateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return WebhookEndpointUpdateOut{}, rerr
		}
		access, lerr := d.loadWebhookEndpoint(ctx, rq, in.ID)
		if lerr != nil {
			return WebhookEndpointUpdateOut{}, lerr
		}
		row := access.row
		var up store.WebhookEndpointUpdate
		eventTypes := row.EventTypes
		if in.Body.EventTypes != nil {
			knownUpdate := map[string]bool{}
			for _, name := range webhook.FreezeEventTypes() {
				knownUpdate[name] = true
			}
			seenUpdate := map[string]bool{}
			frozenUpdate := make([]string, 0, len(*in.Body.EventTypes))
			for _, name := range *in.Body.EventTypes {
				if !knownUpdate[name] {
					return WebhookEndpointUpdateOut{}, op.Errorf(op.CodeInvalid, "unknown webhook event type %q", name)
				}
				if !seenUpdate[name] {
					seenUpdate[name] = true
					frozenUpdate = append(frozenUpdate, name)
				}
			}
			if len(frozenUpdate) == 0 {
				return WebhookEndpointUpdateOut{}, op.Errorf(op.CodeInvalid, "webhook event types must not be empty")
			}
			slices.Sort(frozenUpdate)
			eventTypes = frozenUpdate
			up.EventTypes = &eventTypes
		}
		refGlobs := row.RefGlobs
		if in.Body.RefGlobs != nil {
			refGlobs = *in.Body.RefGlobs
			up.RefGlobs = &refGlobs
		}
		hasRefUpdated := false
		for _, name := range eventTypes {
			if name == "ref.updated" {
				hasRefUpdated = true
				break
			}
		}
		if len(refGlobs) > 0 && !hasRefUpdated {
			return WebhookEndpointUpdateOut{}, op.Errorf(op.CodeInvalid, "ref globs require the ref.updated event type")
		}
		if len(refGlobs) > webhookMaxRefGlobs {
			return WebhookEndpointUpdateOut{}, op.Errorf(op.CodeInvalid, "too many ref globs")
		}
		if in.Body.URL != nil {
			if strings.TrimSpace(*in.Body.URL) == "" {
				return WebhookEndpointUpdateOut{}, op.Errorf(op.CodeInvalid, "webhook url is required")
			}
			if _, perr := d.Webhooks.CheckURL(*in.Body.URL); perr != nil {
				return WebhookEndpointUpdateOut{}, op.Wrap(op.CodeInvalid, perr, "webhook url is not allowed")
			}
			up.URL = in.Body.URL
		}
		if in.Body.Active != nil {
			up.Active = in.Body.Active
			if *in.Body.Active {
				zero := int64(0)
				up.ConsecutiveFailures = &zero
				if row.DisabledAt != 0 || row.DisableReason != "" {
					disabledAt := int64(0)
					up.DisabledAt = &disabledAt
					empty := ""
					up.DisableReason = &empty
				}
			}
		}
		if in.Body.Concurrency != nil {
			clamped := *in.Body.Concurrency
			if clamped < webhookMinConcurrency {
				clamped = webhookMinConcurrency
			}
			if clamped > webhookMaxConcurrency {
				clamped = webhookMaxConcurrency
			}
			up.Concurrency = &clamped
		}
		if in.Body.TimeoutSeconds != nil {
			clamped := *in.Body.TimeoutSeconds
			if clamped < webhookMinTimeout {
				clamped = webhookMinTimeout
			}
			if clamped > webhookMaxTimeout {
				clamped = webhookMaxTimeout
			}
			up.TimeoutSeconds = &clamped
		}
		if in.Body.Note != nil {
			if len(*in.Body.Note) > webhookMaxNoteLen {
				return WebhookEndpointUpdateOut{}, op.Errorf(op.CodeInvalid, "webhook note is too long")
			}
			up.Note = in.Body.Note
		}
		now := d.now()
		var updated store.WebhookEndpointRow
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			var uerr error
			updated, uerr = tx.UpdateWebhookEndpoint(row.ID, now, up)
			return uerr
		})
		if werr != nil {
			return WebhookEndpointUpdateOut{}, mapWebhookStore(werr, "webhook endpoint was not found")
		}
		rendered, rerr := d.webhookEndpointJSON(ctx, updated, "")
		if rerr != nil {
			return WebhookEndpointUpdateOut{}, rerr
		}
		return WebhookEndpointUpdateOut{Endpoint: rendered}, nil
	}
}

func webhookEndpointDeleteHandler(d Deps) func(context.Context, op.Principal, WebhookEndpointDeleteIn) (WebhookEndpointDeleteOut, error) {
	return func(ctx context.Context, p op.Principal, in WebhookEndpointDeleteIn) (WebhookEndpointDeleteOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return WebhookEndpointDeleteOut{}, rerr
		}
		access, lerr := d.loadWebhookEndpoint(ctx, rq, in.ID)
		if lerr != nil {
			return WebhookEndpointDeleteOut{}, lerr
		}
		serr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			return tx.DeleteWebhookEndpoint(access.row.ID)
		})
		if serr != nil {
			return WebhookEndpointDeleteOut{}, mapWebhookStore(serr, "webhook endpoint was not found")
		}
		return WebhookEndpointDeleteOut{Deleted: true}, nil
	}
}

func webhookEndpointRotateHandler(d Deps) func(context.Context, op.Principal, WebhookEndpointRotateIn) (WebhookEndpointRotateOut, error) {
	return func(ctx context.Context, p op.Principal, in WebhookEndpointRotateIn) (WebhookEndpointRotateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return WebhookEndpointRotateOut{}, rerr
		}
		access, lerr := d.loadWebhookEndpoint(ctx, rq, in.ID)
		if lerr != nil {
			return WebhookEndpointRotateOut{}, lerr
		}
		display, raw, gerr := webhook.GenerateSecret()
		if gerr != nil {
			return WebhookEndpointRotateOut{}, op.Wrap(op.CodeInternal, gerr, "webhook secret generation failed")
		}
		if !webhook.IsValidSecretDisplay(display) {
			return WebhookEndpointRotateOut{}, op.Errorf(op.CodeInternal, "webhook secret generation failed")
		}
		sealed, serr := d.sealWebhookSecret(webhookSealAAD(access.row.ID), raw)
		if serr != nil {
			return WebhookEndpointRotateOut{}, serr
		}
		now := d.now()
		var row store.WebhookEndpointRow
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			var rerr error
			row, rerr = tx.RotateWebhookEndpointSecret(access.row.ID, now, sealed)
			return rerr
		})
		if werr != nil {
			return WebhookEndpointRotateOut{}, mapWebhookStore(werr, "webhook endpoint was not found")
		}
		rendered, jerr := d.webhookEndpointJSON(ctx, row, display)
		if jerr != nil {
			return WebhookEndpointRotateOut{}, jerr
		}
		return WebhookEndpointRotateOut{Endpoint: rendered}, nil
	}
}

func webhookEndpointTestHandler(d Deps) func(context.Context, op.Principal, WebhookEndpointTestIn) (WebhookEndpointTestOut, error) {
	return func(ctx context.Context, p op.Principal, in WebhookEndpointTestIn) (WebhookEndpointTestOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return WebhookEndpointTestOut{}, rerr
		}
		access, lerr := d.loadWebhookEndpoint(ctx, rq, in.ID)
		if lerr != nil {
			return WebhookEndpointTestOut{}, lerr
		}
		row := access.row
		if !row.Active {
			return WebhookEndpointTestOut{}, op.Errorf(op.CodeConflict, "webhook endpoint is not active")
		}
		if _, perr := d.Webhooks.CheckURL(row.URL); perr != nil {
			return WebhookEndpointTestOut{}, op.Wrap(op.CodeInvalid, perr, "webhook url is not allowed")
		}
		secret, serr := d.openWebhookSecret(row)
		if serr != nil {
			return WebhookEndpointTestOut{}, serr
		}
		now := d.now()
		msgID := "msg_" + string(store.NewID(now))
		eventTime := now.Unix()
		payload, berr := webhook.BuildEnvelope(msgID, "endpoint.tested", now, map[string]any{
			"endpoint_id": row.ID,
			"url":         row.URL,
			"tested_at":   now.UTC().Format(time.RFC3339),
		})
		if berr != nil {
			return WebhookEndpointTestOut{}, op.Wrap(op.CodeInternal, berr, "webhook test envelope failed")
		}
		signature := webhook.Sign(secret, msgID, eventTime, payload)
		if len(row.PrevSecretEnc) > 0 && row.PrevSecretExpiresAt > now.UnixMilli() {
			if prev, perr := d.openWebhookSecret(store.WebhookEndpointRow{
				ID: row.ID, SecretEnc: row.PrevSecretEnc,
			}); perr == nil {
				signature = webhook.SignHeader(secret, prev, msgID, eventTime, payload)
			}
		}
		timeout := row.TimeoutSeconds
		if timeout <= 0 {
			timeout = webhookDefaultTimeout
		}
		client := d.Webhooks.NewClient(time.Duration(timeout) * time.Second)
		req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, row.URL, bytes.NewReader(payload))
		if rerr != nil {
			return WebhookEndpointTestOut{}, op.Wrap(op.CodeInvalid, rerr, "webhook test request failed")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("webhook-id", msgID)
		req.Header.Set("webhook-timestamp", strconv.FormatInt(eventTime, 10))
		req.Header.Set("webhook-signature", signature)
		start := time.Now()
		resp, derr := client.Do(req)
		duration := time.Since(start).Milliseconds()
		if derr != nil {
			return WebhookEndpointTestOut{Ok: false, Error: derr.Error(), DurationMs: duration}, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return WebhookEndpointTestOut{
				Ok: false, HTTPStatus: int64(resp.StatusCode), DurationMs: duration,
				Error: fmt.Sprintf("endpoint returned HTTP %d", resp.StatusCode),
			}, nil
		}
		return WebhookEndpointTestOut{Ok: true, HTTPStatus: int64(resp.StatusCode), DurationMs: duration}, nil
	}
}

func webhookMessageListHandler(d Deps) func(context.Context, op.Principal, WebhookMessageListIn) (WebhookMessageListOut, error) {
	return func(ctx context.Context, p op.Principal, in WebhookMessageListIn) (WebhookMessageListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return WebhookMessageListOut{}, rerr
		}
		access, lerr := d.loadWebhookEndpoint(ctx, rq, in.ID)
		if lerr != nil {
			return WebhookMessageListOut{}, lerr
		}
		if strings.TrimSpace(in.State) != "" {
			switch in.State {
			case "pending", "inflight", "delivered", "dead":
			default:
				return WebhookMessageListOut{}, op.Errorf(op.CodeInvalid, "webhook message state is not valid")
			}
		}
		limit := webhookLimit(in.Limit, webhookListDefault, webhookListMax)
		out := WebhookMessageListOut{Messages: []WebhookMessageJSON{}}
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			rows, next, lerr := tx.ListWebhookMessages(access.row.ID, in.Cursor, limit)
			if lerr != nil {
				return lerr
			}
			for _, row := range rows {
				if in.State != "" && row.State != in.State {
					continue
				}
				out.Messages = append(out.Messages, webhookMessageJSON(row))
			}
			out.Next = next
			return nil
		})
		if serr != nil {
			return WebhookMessageListOut{}, mapWebhookStore(serr, "webhook message list failed")
		}
		return out, nil
	}
}

func (d Deps) loadWebhookMessage(ctx context.Context, access webhookEndpointAccess, mid string) (store.WebhookMessageRow, error) {
	if strings.TrimSpace(mid) == "" {
		return store.WebhookMessageRow{}, op.Errorf(op.CodeInvalid, "webhook message id is required")
	}
	var row store.WebhookMessageRow
	serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		var gerr error
		row, gerr = tx.GetWebhookMessage(mid)
		return gerr
	})
	if serr != nil {
		return store.WebhookMessageRow{}, mapWebhookStore(serr, "webhook message was not found")
	}
	if row.EndpointID != access.row.ID {
		return store.WebhookMessageRow{}, op.Errorf(op.CodeNotFound, "webhook message was not found")
	}
	return row, nil
}

func webhookMessageGetHandler(d Deps) func(context.Context, op.Principal, WebhookMessageGetIn) (WebhookMessageGetOut, error) {
	return func(ctx context.Context, p op.Principal, in WebhookMessageGetIn) (WebhookMessageGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return WebhookMessageGetOut{}, rerr
		}
		access, lerr := d.loadWebhookEndpoint(ctx, rq, in.ID)
		if lerr != nil {
			return WebhookMessageGetOut{}, lerr
		}
		row, lerr := d.loadWebhookMessage(ctx, access, in.MID)
		if lerr != nil {
			return WebhookMessageGetOut{}, lerr
		}
		return WebhookMessageGetOut{Message: webhookMessageJSONWithPayload(row)}, nil
	}
}

func webhookMessageRedeliverHandler(d Deps) func(context.Context, op.Principal, WebhookMessageRedeliverIn) (WebhookMessageRedeliverOut, error) {
	return func(ctx context.Context, p op.Principal, in WebhookMessageRedeliverIn) (WebhookMessageRedeliverOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return WebhookMessageRedeliverOut{}, rerr
		}
		access, lerr := d.loadWebhookEndpoint(ctx, rq, in.ID)
		if lerr != nil {
			return WebhookMessageRedeliverOut{}, lerr
		}
		if _, lerr := d.loadWebhookMessage(ctx, access, in.MID); lerr != nil {
			return WebhookMessageRedeliverOut{}, lerr
		}
		now := d.now().UnixMilli()
		var row store.WebhookMessageRow
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			var rerr error
			row, rerr = tx.RedeliverWebhookMessage(in.MID, now)
			return rerr
		})
		if werr != nil {
			if errors.Is(werr, store.ErrInvalid) {
				return WebhookMessageRedeliverOut{}, op.Errorf(op.CodeInvalid, "only dead or delivered messages redeliver")
			}
			return WebhookMessageRedeliverOut{}, mapWebhookStore(werr, "webhook message was not found")
		}
		return WebhookMessageRedeliverOut{Message: webhookMessageJSONWithPayload(row)}, nil
	}
}

func webhookAttemptListHandler(d Deps) func(context.Context, op.Principal, WebhookAttemptListIn) (WebhookAttemptListOut, error) {
	return func(ctx context.Context, p op.Principal, in WebhookAttemptListIn) (WebhookAttemptListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return WebhookAttemptListOut{}, rerr
		}
		access, lerr := d.loadWebhookEndpoint(ctx, rq, in.ID)
		if lerr != nil {
			return WebhookAttemptListOut{}, lerr
		}
		if _, lerr := d.loadWebhookMessage(ctx, access, in.MID); lerr != nil {
			return WebhookAttemptListOut{}, lerr
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 10
		}
		if limit > webhookAttemptListMax {
			limit = webhookAttemptListMax
		}
		out := WebhookAttemptListOut{Attempts: []WebhookAttemptJSON{}}
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			rows, lerr := tx.ListWebhookAttempts(in.MID, limit)
			if lerr != nil {
				return lerr
			}
			for _, row := range rows {
				out.Attempts = append(out.Attempts, webhookAttemptJSON(row))
			}
			return nil
		})
		if serr != nil {
			return WebhookAttemptListOut{}, mapWebhookStore(serr, "webhook attempt list failed")
		}
		return out, nil
	}
}
