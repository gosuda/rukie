package api

import (
	context "context"
	sql "database/sql"
	errors "errors"
	fmt "fmt"
	http "net/http"
	strings "strings"
	time "time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/review"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

type IntentJSON struct {
	PublicID   string   `json:"public_id,omitempty"`
	Title      string   `json:"title,omitempty"`
	Body       string   `json:"body,omitempty"`
	State      string   `json:"state,omitempty"`
	LeaseMode  string   `json:"lease_mode,omitempty"`
	Holder     string   `json:"holder,omitempty"`
	Owner      string   `json:"owner,omitempty"`
	Repo       string   `json:"repo,omitempty"`
	ClaimPaths []string `json:"claim_paths,omitempty"`
	Number     int64    `json:"number,omitzero"`
	ExpiresAt  int64    `json:"expires_at,omitzero"`
	Version    int64    `json:"version,omitzero"`
}

type IntentChangeJSON struct {
	ChangeID string `json:"change_id,omitempty"`
	HeadOid  string `json:"head_oid,omitempty"`
	Relation string `json:"relation,omitempty"`
}

type AskJSON struct {
	PublicID    string          `json:"public_id,omitempty"`
	Question    string          `json:"question,omitempty"`
	State       string          `json:"state,omitempty"`
	Answer      string          `json:"answer,omitempty"`
	Options     []AskOptionJSON `json:"options,omitempty"`
	DeadlineMs  int64           `json:"deadline_ms,omitzero"`
	BlocksClaim bool            `json:"blocks_claim,omitzero"`
}

type IntentListOut struct {
	Next    string       `json:"-"`
	Intents []IntentJSON `json:"intents"`
}

type IntentGetOut struct {
	Changes []IntentChangeJSON `json:"changes"`
	Asks    []AskJSON          `json:"asks"`
	Intent  IntentJSON         `json:"intent"`
}

type AskListOut struct {
	Next string    `json:"-"`
	Asks []AskJSON `json:"asks"`
}

type AskGetOut struct {
	Ask AskJSON `json:"ask"`
}

type IntentCreateIn struct {
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           struct {
		Title      string            `json:"title"`
		Body       string            `json:"body,omitempty"`
		LeaseMode  string            `json:"lease_mode,omitempty"`
		ClaimPaths []string          `json:"claim_paths,omitempty"`
		Contract   jsonv1.RawMessage `json:"contract,omitempty"`
	}
}

type IntentUpdateIn struct {
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           struct {
		Title      *string            `json:"title,omitempty"`
		Body       *string            `json:"body,omitempty"`
		LeaseMode  *string            `json:"lease_mode,omitempty"`
		Contract   *jsonv1.RawMessage `json:"contract,omitempty"`
		ClaimPaths []string           `json:"claim_paths,omitempty"`
		Version    int64              `json:"version"`
	}
	Number int64 `path:"number"`
}

type IntentStateIn struct {
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           struct {
		State   string `json:"state"`
		Reason  string `json:"reason"`
		Version int64  `json:"version"`
	}
	Number int64 `path:"number"`
}

type IntentLeaseIn struct {
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           struct {
		TTL int64 `json:"ttl_s,omitzero"`
	}
	Number int64 `path:"number"`
}

type IntentChangeAddIn struct {
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           struct {
		ChangeID string `json:"change_id"`
		HeadOid  string `json:"head_oid,omitempty"`
		Relation string `json:"relation,omitempty"`
	}
	Number int64 `path:"number"`
}

type IntentChangeRemoveIn struct {
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Change         string `path:"change"`
	IdempotencyKey string `header:"Idempotency-Key"`
	Number         int64  `path:"number"`
}

type AskCreateIn struct {
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           struct {
		Question    string            `json:"question"`
		Options     jsonv1.RawMessage `json:"options,omitempty"`
		DeadlineMs  int64             `json:"deadline_ms,omitzero"`
		BlocksClaim bool              `json:"blocks_claim,omitzero"`
	}
	Number int64 `path:"number"`
}

type AskUpdateIn struct {
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Ask            string `path:"ask"`
	IdempotencyKey string `header:"Idempotency-Key"`
	Number         int64  `path:"number"`
}

type AskGetIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	Ask   string `path:"ask"`
}

type AskAnswerIn struct {
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Ask            string `path:"ask"`
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           struct {
		Answer string `json:"answer"`
	}
}

type IntentLeaseOut struct {
	ClaimID   string   `json:"claim_id,omitempty"`
	Holder    string   `json:"holder,omitempty"`
	Mode      string   `json:"mode,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	ExpiresAt int64    `json:"expires_at,omitzero"`
}

type IntentChangeOut struct {
	Change IntentChangeJSON `json:"change"`
}

type IntentStateOut struct {
	Intent IntentJSON `json:"intent"`
}

type AskCreateOut struct {
	Ask AskJSON `json:"ask"`
}

type AskUpdateOut struct {
	Ask AskJSON `json:"ask"`
}

type AskAnswerOut struct {
	Ask    AskJSON    `json:"ask"`
	Intent IntentJSON `json:"intent"`
}

type IntentLeaseEmpty struct{}

type intentListIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	State  string `query:"state"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

type intentGetIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Number int64  `path:"number"`
}

func intentJSON(owner, repo string, r store.IntentRow) IntentJSON {
	out := IntentJSON{
		Number: r.Number, PublicID: r.PublicID, Title: r.Title, Body: r.Body,
		State: r.State, LeaseMode: r.LeaseMode, Version: r.Version,
		Owner: owner, Repo: repo,
	}
	if r.ClaimPaths != "" {
		var paths []string
		if uerr := jsonv2.Unmarshal([]byte(r.ClaimPaths), &paths, jsonv1.DefaultOptionsV1()); uerr == nil {
			out.ClaimPaths = paths
		}
	}
	return out
}

func askJSON(r store.AskRow) AskJSON {
	out := AskJSON{
		PublicID: r.PublicID, Question: r.Question, State: r.State,
		BlocksClaim: r.BlocksClaim, Answer: r.Answer,
	}
	if r.ExpiresAt.Valid {
		out.DeadlineMs = r.ExpiresAt.Int64
	} else if r.DeadlineMS != 0 {
		out.DeadlineMs = r.DeadlineMS
	}
	if r.Options != "" {
		var opts []AskOptionJSON
		if uerr := jsonv2.Unmarshal([]byte(r.Options), &opts, jsonv1.DefaultOptionsV1()); uerr == nil {
			out.Options = opts
		}
	}
	return out
}

func intentChangeJSON(r store.IntentChangeRow) IntentChangeJSON {
	out := IntentChangeJSON{ChangeID: r.ChangeID, Relation: r.Relation}
	if r.HeadOID.Valid {
		out.HeadOid = r.HeadOID.String
	}
	return out
}

func intentRepo(ctx context.Context, d Deps, p op.Principal, owner, repo, action string) (requester, repoContext, error) {
	rq, err := d.requester(ctx, p)
	if err != nil {
		return rq, repoContext{}, err
	}
	rc, err := d.resolveRepo(ctx, rq, owner, repo, false)
	if err != nil {
		return rq, rc, err
	}
	if err := d.checkRepoAccess(rq, rc.row, action, ""); err != nil {
		return rq, rc, err
	}
	return rq, rc, nil
}

func loadIntent(ctx context.Context, d Deps, repoID, number int64) (store.IntentRow, error) {
	var row store.IntentRow
	err := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		var rerr error
		row, rerr = tx.ReadIntentByNumber(repoID, number)
		return rerr
	})
	return row, err
}

func mapIntentStore(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return op.Errorf(op.CodeNotFound, "intent was not found")
	}
	if errors.Is(err, store.ErrStale) {
		return op.Errorf(op.CodeStale, "intent changed under this request")
	}
	if errors.Is(err, store.ErrInvalid) {
		return op.Errorf(op.CodeValidation, "intent input is not valid")
	}
	return op.Wrap(op.CodeInternal, err, "intent store operation failed")
}

func validIntentState(state string) bool {
	switch state {
	case "open", "blocked", "closed":
		return true
	default:
		return false
	}
}

func validCloseReason(reason string) bool {
	switch reason {
	case "completed", "duplicate", "wontfix", "abandoned":
		return true
	default:
		return false
	}
}

func validLeaseMode(mode string) bool {
	return mode == "enforce" || mode == "advise"
}

func ttlSeconds(in int64) (int64, error) {
	if in == 0 {
		return 3600, nil
	}
	if in < 1 || in > 86400 {
		return 0, op.Errorf(op.CodeValidation, "ttl must be 1 to 86400 seconds")
	}
	return in, nil
}

func expiryMs(raw string) int64 {
	if raw == "" {
		return 0
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return 0
	}
	return parsed.UnixMilli()
}

type claimSubject struct {
	Task   string `json:"task"`
	Change string `json:"change"`
}

type claimPayload struct {
	Claim     string        `json:"claim"`
	Holder    string        `json:"holder"`
	Mode      string        `json:"mode"`
	ExpiresAt string        `json:"expires_at"`
	Subject   *claimSubject `json:"subject"`
	Paths     []string      `json:"paths"`
}

type claimPage struct {
	Claims struct {
		Items []claimPayload `json:"items"`
	} `json:"claims"`
}

type claimReply struct {
	Claim claimPayload `json:"claim"`
}

func (d Deps) liveClaims(ctx context.Context, daemon ruk.Repo) ([]claimPayload, error) {
	var reply claimPage
	if err := d.callRuk(ctx, daemon, wire.MethodClaimList, map[string]any{"holder": "any", "page": map[string]any{"limit": 100}}, &reply); err != nil {
		return nil, err
	}
	return reply.Claims.Items, nil
}

func findLiveClaim(items []claimPayload, publicID string) *claimPayload {
	for i := range items {
		if items[i].Subject != nil && items[i].Subject.Task == publicID {
			return &items[i]
		}
	}
	return nil
}

func declaredPaths(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{}, nil
	}
	var paths []string
	if err := jsonv2.Unmarshal([]byte(raw), &paths, jsonv1.DefaultOptionsV1()); err != nil {
		return nil, op.Wrap(op.CodeInternal, err, "intent claim paths are corrupt")
	}
	for i, path := range paths {
		trimmed := strings.Trim(path, "/")
		if trimmed == "" {
			return nil, op.Errorf(op.CodeValidation, "claim paths must not be empty")
		}
		for part := range strings.SplitSeq(trimmed, "/") {
			if part == "" || part == "." || part == ".." || part == ".git" {
				return nil, op.Errorf(op.CodeValidation, "claim paths must be repository-relative without empty, dot, or .git components")
			}
		}
		paths[i] = trimmed
	}
	return paths, nil
}

type contractRequirement struct {
	Kind       string `json:"kind"`
	Result     string `json:"result"`
	Quorum     int    `json:"quorum"`
	MinSigners int    `json:"min_signers"`
	MaxAgeSecs int64  `json:"max_age_secs"`
}

type acceptanceContract struct {
	Requirements []contractRequirement `json:"requirements"`
}

func (d Deps) evalIntentClose(ctx context.Context, repo store.RepoRow, intent store.IntentRow, links []store.IntentChangeRow) error {
	raw := strings.TrimSpace(intent.Contract)
	var doc acceptanceContract
	if raw != "" {
		if uerr := jsonv2.Unmarshal([]byte(raw), &doc, jsonv1.DefaultOptionsV1()); uerr != nil {
			return op.Wrap(op.CodeInternal, uerr, "intent contract is corrupt")
		}
	}
	if len(doc.Requirements) == 0 {
		if len(links) == 0 {
			return op.Errorf(op.CodeConflict, "acceptance contract is not satisfied: no linked changes and no acceptance requirements")
		}
		return nil
	}
	hasTaskAcceptance := false
	for _, req := range doc.Requirements {
		if req.Kind == "task/acceptance" {
			hasTaskAcceptance = true
			break
		}
	}
	if len(links) == 0 {
		if hasTaskAcceptance {
			return nil
		}
		return op.Errorf(op.CodeConflict, "acceptance contract is not satisfied: no linked changes")
	}
	for _, link := range links {
		if !link.HeadOID.Valid || link.HeadOID.String == "" {
			return op.Errorf(op.CodeConflict, "acceptance contract is not satisfied: change %s is conflicted", link.ChangeID)
		}
	}
	daemon, oerr := d.openDaemonRepo(ctx, repo)
	if oerr != nil {
		return oerr
	}
	reqs := make([]review.Requirement, 0, len(doc.Requirements))
	for _, req := range doc.Requirements {
		need := req.MinSigners
		if need <= 0 {
			need = req.Quorum
		}
		if need <= 0 {
			need = 1
		}
		reqs = append(reqs, review.Requirement{Kind: req.Kind, Result: req.Result, MinSigners: need, MaxAgeSecs: req.MaxAgeSecs})
	}
	for _, link := range links {
		head := ""
		if link.HeadOID.Valid {
			head = link.HeadOID.String
		}
		var reply attestListReply
		if rerr := d.callRuk(ctx, daemon, wire.MethodAttestList, map[string]any{"subject": head, "page": map[string]any{"limit": 100}}, &reply); rerr != nil {
			return rerr
		}
		facts := make([]review.AttestationFact, 0, len(reply.Records.Items))
		for _, record := range reply.Records.Items {
			facts = append(facts, review.AttestationFact{
				Kind: record.Kind, Result: record.Result, OID: head,
				Signer: record.Signer, Live: record.Status == "live",
			})
		}
		if gaps := review.GapsForChange(head, intent.CreatedBy, reqs, facts); len(gaps) > 0 {
			return op.Errorf(op.CodeConflict, "acceptance contract is not satisfied: %s %s", gaps[0].Reason, gaps[0].Detail)
		}
	}
	return nil
}

func intentListHandler(d Deps) func(context.Context, op.Principal, intentListIn) (IntentListOut, error) {
	return func(ctx context.Context, p op.Principal, in intentListIn) (IntentListOut, error) {
		_, rc, err := intentRepo(ctx, d, p, in.Owner, in.Repo, "task.view")
		if err != nil {
			return IntentListOut{}, err
		}
		if in.State != "" && !validIntentState(in.State) {
			return IntentListOut{}, op.Errorf(op.CodeValidation, "state must be open, blocked, or closed")
		}
		limit := clampLimit(in.Limit, 30, 100)
		out := IntentListOut{Intents: []IntentJSON{}}
		rerr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			rows, lerr := tx.ListIntents(rc.row.ID, in.State, limit)
			if lerr != nil {
				return lerr
			}
			for _, row := range rows {
				out.Intents = append(out.Intents, intentJSON(rc.owner, rc.name, row))
			}
			return nil
		})
		if rerr != nil {
			return IntentListOut{}, mapIntentStore(rerr)
		}
		if daemon, oerr := d.openDaemonRepo(ctx, rc.row); oerr == nil {
			if items, lerr := d.liveClaims(ctx, daemon); lerr == nil {
				bySubject := map[string]claimPayload{}
				for _, item := range items {
					if item.Subject != nil && item.Subject.Task != "" {
						bySubject[item.Subject.Task] = item
					}
				}
				for i := range out.Intents {
					if live, ok := bySubject[out.Intents[i].PublicID]; ok {
						out.Intents[i].Holder = live.Holder
						out.Intents[i].ExpiresAt = expiryMs(live.ExpiresAt)
					}
				}
			}
		}
		return out, nil
	}
}

func intentGetHandler(d Deps) func(context.Context, op.Principal, intentGetIn) (IntentGetOut, error) {
	return func(ctx context.Context, p op.Principal, in intentGetIn) (IntentGetOut, error) {
		_, rc, err := intentRepo(ctx, d, p, in.Owner, in.Repo, "task.view")
		if err != nil {
			return IntentGetOut{}, err
		}
		row, err := loadIntent(ctx, d, rc.row.ID, in.Number)
		if err != nil {
			return IntentGetOut{}, mapIntentStore(err)
		}
		out := IntentGetOut{Intent: intentJSON(rc.owner, rc.name, row), Changes: []IntentChangeJSON{}, Asks: []AskJSON{}}
		rerr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			links, lerr := tx.ListIntentChanges(row.ID)
			if lerr != nil {
				return lerr
			}
			for _, link := range links {
				out.Changes = append(out.Changes, intentChangeJSON(link))
			}
			asks, aerr := tx.ListAsks(rc.row.ID, row.ID, "", 100)
			if aerr != nil {
				return aerr
			}
			for _, ask := range asks {
				out.Asks = append(out.Asks, askJSON(ask))
			}
			return nil
		})
		if rerr != nil {
			return IntentGetOut{}, mapIntentStore(rerr)
		}
		if daemon, oerr := d.openDaemonRepo(ctx, rc.row); oerr == nil {
			if items, lerr := d.liveClaims(ctx, daemon); lerr == nil {
				if live := findLiveClaim(items, row.PublicID); live != nil {
					out.Intent.Holder = live.Holder
					out.Intent.ExpiresAt = expiryMs(live.ExpiresAt)
				}
			}
		}
		return out, nil
	}
}

func intentCreateHandler(d Deps) func(context.Context, op.Principal, IntentCreateIn) (IntentGetOut, error) {
	return func(ctx context.Context, p op.Principal, in IntentCreateIn) (IntentGetOut, error) {
		rq, rc, err := intentRepo(ctx, d, p, in.Owner, in.Repo, "task.create")
		if err != nil {
			return IntentGetOut{}, err
		}
		title := strings.TrimSpace(in.Body.Title)
		if title == "" {
			return IntentGetOut{}, op.Errorf(op.CodeValidation, "title is required")
		}
		if len(title) > 200 {
			return IntentGetOut{}, op.Errorf(op.CodeValidation, "title is too long")
		}
		if len(in.Body.Body) > 65536 {
			return IntentGetOut{}, op.Errorf(op.CodeValidation, "body is too long")
		}
		mode := strings.TrimSpace(in.Body.LeaseMode)
		if mode == "" {
			mode = "advise"
		}
		if !validLeaseMode(mode) {
			return IntentGetOut{}, op.Errorf(op.CodeValidation, "lease mode must be enforce or advise")
		}
		for _, path := range in.Body.ClaimPaths {
			if strings.TrimSpace(path) == "" {
				return IntentGetOut{}, op.Errorf(op.CodeValidation, "claim paths must not be empty")
			}
		}
		contract := ""
		if len(in.Body.Contract) != 0 {
			if !jsonv1.Valid([]byte(in.Body.Contract)) {
				return IntentGetOut{}, op.Errorf(op.CodeValidation, "contract is not valid JSON")
			}
			contract = string(in.Body.Contract)
		}
		encoded, merr := jsonv2.Marshal(in.Body.ClaimPaths, jsonv1.DefaultOptionsV1())
		if merr != nil {
			return IntentGetOut{}, op.Wrap(op.CodeInternal, merr, "claim paths could not be encoded")
		}
		if in.Body.ClaimPaths == nil {
			encoded = []byte("[]")
		}
		var saved store.IntentRow
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			row, ierr := tx.InsertIntent(d.now(), store.IntentRow{
				Title: title, Body: in.Body.Body, RepoID: rc.row.ID,
				CreatedBy: rq.userID, LeaseMode: mode,
				ClaimPaths: string(encoded), Contract: contract,
			})
			if ierr != nil {
				return ierr
			}
			saved = row
			return nil
		})
		if werr != nil {
			return IntentGetOut{}, mapIntentStore(werr)
		}
		return IntentGetOut{Intent: intentJSON(rc.owner, rc.name, saved), Changes: []IntentChangeJSON{}, Asks: []AskJSON{}}, nil
	}
}

func intentUpdateHandler(d Deps) func(context.Context, op.Principal, IntentUpdateIn) (IntentGetOut, error) {
	return func(ctx context.Context, p op.Principal, in IntentUpdateIn) (IntentGetOut, error) {
		_, rc, err := intentRepo(ctx, d, p, in.Owner, in.Repo, "task.edit")
		if err != nil {
			return IntentGetOut{}, err
		}
		row, err := loadIntent(ctx, d, rc.row.ID, in.Number)
		if err != nil {
			return IntentGetOut{}, mapIntentStore(err)
		}
		if in.Body.Version != 0 && in.Body.Version != row.Version {
			return IntentGetOut{}, op.Errorf(op.CodeStale, "intent changed under this request")
		}
		updated := row
		if in.Body.Title != nil {
			title := strings.TrimSpace(*in.Body.Title)
			if title == "" {
				return IntentGetOut{}, op.Errorf(op.CodeValidation, "title must not be empty")
			}
			if len(title) > 200 {
				return IntentGetOut{}, op.Errorf(op.CodeValidation, "title is too long")
			}
			updated.Title = title
		}
		if in.Body.Body != nil {
			if len(*in.Body.Body) > 65536 {
				return IntentGetOut{}, op.Errorf(op.CodeValidation, "body is too long")
			}
			updated.Body = *in.Body.Body
		}
		if in.Body.LeaseMode != nil {
			mode := strings.TrimSpace(*in.Body.LeaseMode)
			if !validLeaseMode(mode) {
				return IntentGetOut{}, op.Errorf(op.CodeValidation, "lease mode must be enforce or advise")
			}
			updated.LeaseMode = mode
		}
		if in.Body.ClaimPaths != nil {
			for _, path := range in.Body.ClaimPaths {
				if strings.TrimSpace(path) == "" {
					return IntentGetOut{}, op.Errorf(op.CodeValidation, "claim paths must not be empty")
				}
			}
			encoded, merr := jsonv2.Marshal(in.Body.ClaimPaths, jsonv1.DefaultOptionsV1())
			if merr != nil {
				return IntentGetOut{}, op.Wrap(op.CodeInternal, merr, "claim paths could not be encoded")
			}
			updated.ClaimPaths = string(encoded)
		}
		if in.Body.Contract != nil {
			if len(*in.Body.Contract) != 0 && !jsonv1.Valid([]byte(*in.Body.Contract)) {
				return IntentGetOut{}, op.Errorf(op.CodeValidation, "contract is not valid JSON")
			}
			updated.Contract = string(*in.Body.Contract)
		}
		var saved store.IntentRow
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			var uerr error
			saved, uerr = tx.UpdateIntent(d.now(), updated, row.Version)
			return uerr
		})
		if werr != nil {
			return IntentGetOut{}, mapIntentStore(werr)
		}
		return IntentGetOut{Intent: intentJSON(rc.owner, rc.name, saved), Changes: []IntentChangeJSON{}, Asks: []AskJSON{}}, nil
	}
}

func intentStateSetHandler(d Deps) func(context.Context, op.Principal, IntentStateIn) (IntentStateOut, error) {
	return func(ctx context.Context, p op.Principal, in IntentStateIn) (IntentStateOut, error) {
		rq, rc, err := intentRepo(ctx, d, p, in.Owner, in.Repo, "task.edit")
		if err != nil {
			return IntentStateOut{}, err
		}
		if !validIntentState(in.Body.State) {
			return IntentStateOut{}, op.Errorf(op.CodeValidation, "state must be open, blocked, or closed")
		}
		row, err := loadIntent(ctx, d, rc.row.ID, in.Number)
		if err != nil {
			return IntentStateOut{}, mapIntentStore(err)
		}
		if in.Body.Version != 0 && in.Body.Version != row.Version {
			return IntentStateOut{}, op.Errorf(op.CodeStale, "intent changed under this request")
		}
		reason := strings.TrimSpace(in.Body.Reason)
		if in.Body.State == "closed" {
			if !validCloseReason(reason) {
				return IntentStateOut{}, op.Errorf(op.CodeValidation, "close reason must be completed, duplicate, wontfix, or abandoned")
			}
			if reason == "duplicate" || reason == "wontfix" {
				if derr := d.checkRepoAccess(rq, rc.row, "task.delete", ""); derr != nil {
					return IntentStateOut{}, op.Errorf(op.CodeForbidden, "maintainer sign-off is required")
				}
			} else {
				var links []store.IntentChangeRow
				rerr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
					var lerr error
					links, lerr = tx.ListIntentChanges(row.ID)
					return lerr
				})
				if rerr != nil {
					return IntentStateOut{}, mapIntentStore(rerr)
				}
				if cerr := d.evalIntentClose(ctx, rc.row, row, links); cerr != nil {
					if rerr, ok := errors.AsType[*ruk.Error](cerr); ok {
						_ = rerr
						return IntentStateOut{}, mapRukError(cerr)
					}
					if oerr, ok := errors.AsType[*op.Error](cerr); ok {
						return IntentStateOut{}, oerr
					}
					return IntentStateOut{}, mapIntentStore(cerr)
				}
			}
		} else if reason != "" {
			return IntentStateOut{}, op.Errorf(op.CodeValidation, "reason is only for close")
		}
		now := d.now()
		var saved store.IntentRow
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			var serr error
			saved, serr = tx.SetIntentStateVersion(now, row.ID, row.Version, in.Body.State, reason)
			return serr
		})
		if werr != nil {
			return IntentStateOut{}, mapIntentStore(werr)
		}
		return IntentStateOut{Intent: intentJSON(rc.owner, rc.name, saved)}, nil
	}
}

func claimIntentHandler(d Deps) func(context.Context, op.Principal, IntentLeaseIn) (IntentLeaseOut, error) {
	return func(ctx context.Context, p op.Principal, in IntentLeaseIn) (IntentLeaseOut, error) {
		_, rc, err := intentRepo(ctx, d, p, in.Owner, in.Repo, "task.claim")
		if err != nil {
			return IntentLeaseOut{}, err
		}
		row, err := loadIntent(ctx, d, rc.row.ID, in.Number)
		if err != nil {
			return IntentLeaseOut{}, mapIntentStore(err)
		}
		if row.State == "closed" {
			return IntentLeaseOut{}, op.Errorf(op.CodeConflict, "intent is closed")
		}
		ttl, err := ttlSeconds(in.Body.TTL)
		if err != nil {
			return IntentLeaseOut{}, err
		}
		now := d.now()
		blocked := int64(0)
		rerr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			var cerr error
			blocked, cerr = tx.CountOpenBlockingAsksAt(now, row.ID)
			return cerr
		})
		if rerr != nil {
			return IntentLeaseOut{}, mapIntentStore(rerr)
		}
		if blocked > 0 {
			return IntentLeaseOut{}, op.Errorf(op.CodeLeaseConflict, "lease-hold: an open blocking ask prevents claim")
		}
		paths, err := declaredPaths(row.ClaimPaths)
		if err != nil {
			return IntentLeaseOut{}, err
		}
		mode := row.LeaseMode
		if mode == "" {
			mode = "advise"
		}
		if !validLeaseMode(mode) {
			return IntentLeaseOut{}, op.Errorf(op.CodeValidation, "lease mode must be enforce or advise")
		}
		daemon, err := d.openDaemonRepo(ctx, rc.row)
		if err != nil {
			return IntentLeaseOut{}, err
		}
		var reply claimReply
		cerr := d.callRuk(ctx, daemon, wire.MethodClaimAcquire, map[string]any{
			"paths": paths, "ttl": ttl, "mode": mode,
			"subject": map[string]any{"task": row.PublicID},
		}, &reply)
		if cerr != nil {
			if rerr, ok := errors.AsType[*ruk.Error](cerr); ok && rerr.Code == wire.CodeClaimed {
				if items, lerr := d.liveClaims(ctx, daemon); lerr == nil {
					if live := findLiveClaim(items, row.PublicID); live != nil {
						return IntentLeaseOut{}, op.Errorf(op.CodeLeaseConflict, "intent is claimed by %s until %d", live.Holder, expiryMs(live.ExpiresAt))
					}
				}
			}
			return IntentLeaseOut{}, mapRukError(cerr)
		}
		_ = d.Store.Write(ctx, func(tx *store.WriteTx) error {
			return store.EmitTaskEvent(tx, d.now(), "task.claimed", row)
		})
		outPaths := reply.Claim.Paths
		if len(outPaths) == 0 {
			outPaths = paths
		}
		return IntentLeaseOut{
			ClaimID: reply.Claim.Claim, Holder: reply.Claim.Holder,
			Paths: outPaths, Mode: reply.Claim.Mode, ExpiresAt: expiryMs(reply.Claim.ExpiresAt),
		}, nil
	}
}

func renewIntentHandler(d Deps) func(context.Context, op.Principal, IntentLeaseIn) (IntentLeaseOut, error) {
	return func(ctx context.Context, p op.Principal, in IntentLeaseIn) (IntentLeaseOut, error) {
		_, rc, err := intentRepo(ctx, d, p, in.Owner, in.Repo, "task.claim")
		if err != nil {
			return IntentLeaseOut{}, err
		}
		row, err := loadIntent(ctx, d, rc.row.ID, in.Number)
		if err != nil {
			return IntentLeaseOut{}, mapIntentStore(err)
		}
		ttl, err := ttlSeconds(in.Body.TTL)
		if err != nil {
			return IntentLeaseOut{}, err
		}
		daemon, err := d.openDaemonRepo(ctx, rc.row)
		if err != nil {
			return IntentLeaseOut{}, err
		}
		items, err := d.liveClaims(ctx, daemon)
		if err != nil {
			return IntentLeaseOut{}, mapRukError(err)
		}
		live := findLiveClaim(items, row.PublicID)
		if live == nil {
			return IntentLeaseOut{}, op.Errorf(op.CodeLeaseConflict, "no live claim exists for intent")
		}
		var reply claimReply
		if rerr := d.callRuk(ctx, daemon, wire.MethodClaimRenew, map[string]any{"claim": live.Claim, "ttl": ttl}, &reply); rerr != nil {
			return IntentLeaseOut{}, mapRukError(rerr)
		}
		holder := reply.Claim.Holder
		if holder == "" {
			holder = live.Holder
		}
		claimID := reply.Claim.Claim
		if claimID == "" {
			claimID = live.Claim
		}
		expires := expiryMs(reply.Claim.ExpiresAt)
		if expires == 0 {
			expires = expiryMs(live.ExpiresAt)
		}
		return IntentLeaseOut{ClaimID: claimID, Holder: holder, Paths: live.Paths, Mode: row.LeaseMode, ExpiresAt: expires}, nil
	}
}

func releaseIntentHandler(d Deps) func(context.Context, op.Principal, IntentLeaseIn) (IntentLeaseEmpty, error) {
	return func(ctx context.Context, p op.Principal, in IntentLeaseIn) (IntentLeaseEmpty, error) {
		_, rc, err := intentRepo(ctx, d, p, in.Owner, in.Repo, "task.unclaim")
		if err != nil {
			return IntentLeaseEmpty{}, err
		}
		row, err := loadIntent(ctx, d, rc.row.ID, in.Number)
		if err != nil {
			return IntentLeaseEmpty{}, mapIntentStore(err)
		}
		daemon, err := d.openDaemonRepo(ctx, rc.row)
		if err != nil {
			return IntentLeaseEmpty{}, err
		}
		items, err := d.liveClaims(ctx, daemon)
		if err != nil {
			return IntentLeaseEmpty{}, mapRukError(err)
		}
		live := findLiveClaim(items, row.PublicID)
		if live == nil {
			return IntentLeaseEmpty{}, op.Errorf(op.CodeLeaseConflict, "no live claim exists for intent")
		}
		if rerr := d.callRuk(ctx, daemon, wire.MethodClaimRelease, map[string]any{"claim": live.Claim}, &struct{}{}); rerr != nil {
			return IntentLeaseEmpty{}, mapRukError(rerr)
		}
		return IntentLeaseEmpty{}, nil
	}
}

func changeAddHandler(d Deps) func(context.Context, op.Principal, IntentChangeAddIn) (IntentChangeOut, error) {
	return func(ctx context.Context, p op.Principal, in IntentChangeAddIn) (IntentChangeOut, error) {
		rq, rc, err := intentRepo(ctx, d, p, in.Owner, in.Repo, "task.edit")
		if err != nil {
			return IntentChangeOut{}, err
		}
		row, err := loadIntent(ctx, d, rc.row.ID, in.Number)
		if err != nil {
			return IntentChangeOut{}, mapIntentStore(err)
		}
		changeID := strings.TrimSpace(in.Body.ChangeID)
		if changeID == "" {
			return IntentChangeOut{}, op.Errorf(op.CodeValidation, "change id is required")
		}
		if len(changeID) > 128 {
			return IntentChangeOut{}, op.Errorf(op.CodeValidation, "change id is too long")
		}
		var head sql.NullString
		if strings.TrimSpace(in.Body.HeadOid) != "" {
			headOid := strings.ToLower(strings.TrimSpace(in.Body.HeadOid))
			if !isFullOid(headOid) {
				return IntentChangeOut{}, op.Errorf(op.CodeValidation, "head oid is not valid")
			}
			head = sql.NullString{String: headOid, Valid: true}
		}
		relation := strings.TrimSpace(in.Body.Relation)
		if relation == "" {
			relation = "implements"
		}
		now := d.now()
		var saved store.IntentChangeRow
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			var ierr error
			saved, ierr = tx.InsertIntentChange(now, store.IntentChangeRow{
				IntentID: row.ID, RepoID: rc.row.ID, ChangeID: changeID,
				HeadOID: head, Relation: relation, AddedBy: rq.userID,
			})
			return ierr
		})
		if werr != nil {
			return IntentChangeOut{}, mapIntentStore(werr)
		}
		return IntentChangeOut{Change: intentChangeJSON(saved)}, nil
	}
}

func changeRemoveHandler(d Deps) func(context.Context, op.Principal, IntentChangeRemoveIn) (IntentLeaseEmpty, error) {
	return func(ctx context.Context, p op.Principal, in IntentChangeRemoveIn) (IntentLeaseEmpty, error) {
		_, rc, err := intentRepo(ctx, d, p, in.Owner, in.Repo, "task.edit")
		if err != nil {
			return IntentLeaseEmpty{}, err
		}
		row, err := loadIntent(ctx, d, rc.row.ID, in.Number)
		if err != nil {
			return IntentLeaseEmpty{}, mapIntentStore(err)
		}
		changeID := strings.TrimSpace(in.Change)
		if changeID == "" {
			return IntentLeaseEmpty{}, op.Errorf(op.CodeValidation, "change id is required")
		}
		now := d.now()
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			return tx.DeleteIntentChange(now, row.ID, changeID)
		})
		if werr != nil {
			return IntentLeaseEmpty{}, mapIntentStore(werr)
		}
		return IntentLeaseEmpty{}, nil
	}
}

func RegisterIntents(t *op.Table, d Deps) error {
	if err := op.Register(t, op.Op[intentListIn, IntentListOut]{
		Name: "intent.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/tasks",
		Scope: op.ScopeIntentsRead, Summary: "List intents",
		Description: "Open, blocked, or closed intents of one repository with live lease holders.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden, op.CodeValidation},
		ETag: func(out IntentListOut) string {
			return fmt.Sprintf("intents-%d", len(out.Intents))
		},
		Pagination: &op.Pagination[IntentListOut]{Default: 30, Max: 100, Next: func(_ IntentListOut) (string, bool) {
			return "", false
		}},
		Handler: intentListHandler(d),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[intentGetIn, IntentGetOut]{
		Name: "intent.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/tasks/{number}",
		Scope: op.ScopeIntentsRead, Summary: "Show an intent",
		Description: "One intent with linked changes, asks, and the live lease holder.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden},
		ETag: func(out IntentGetOut) string {
			return fmt.Sprintf("intent-%d-v%d", out.Intent.Number, out.Intent.Version)
		},
		Handler: intentGetHandler(d),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[IntentCreateIn, IntentGetOut]{
		Name: "intent.create", Location: func(IntentGetOut) string { return "" }, Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/tasks/new",
		Scope: op.ScopeIntentsWrite, Summary: "Create an intent",
		Description: "Open an intent with declared claim paths and lease mode.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeNotFound},
		Handler:     intentCreateHandler(d),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[IntentUpdateIn, IntentGetOut]{
		Name: "intent.update", Location: func(IntentGetOut) string { return "" }, Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/tasks/{number}/edit", Status: http.StatusOK,
		Scope: op.ScopeIntentsWrite, Summary: "Update an intent",
		Description: "Patch title, body, lease mode, claim paths, or contract with version compare-and-swap.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden, op.CodeValidation, op.CodeStale},
		Handler:     intentUpdateHandler(d),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[IntentStateIn, IntentStateOut]{
		Name: "intent.state.set", Location: func(IntentStateOut) string { return "" }, Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/tasks/{number}/state", Status: http.StatusOK,
		Scope: op.ScopeIntentsWrite, Summary: "Set intent state",
		Description: "Close requires a satisfied acceptance contract except duplicate or wontfix with maintainer sign-off.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden, op.CodeValidation, op.CodeConflict, op.CodeStale},
		Handler:     intentStateSetHandler(d),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[IntentLeaseIn, IntentLeaseOut]{
		Name: "intent.lease.create", Location: func(IntentLeaseOut) string { return "" }, Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/tasks/{number}/claim", Status: http.StatusOK,
		Scope: op.ScopeIntentsWrite, Summary: "Claim an intent",
		Description: "Acquire a task-keyed Ruk claim over the declared paths. Enforce conflicts name holder and expiry.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden, op.CodeValidation, op.CodeLeaseConflict},
		Handler:     claimIntentHandler(d),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[IntentLeaseIn, IntentLeaseOut]{
		Name: "intent.lease.update", Location: func(IntentLeaseOut) string { return "" }, Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/tasks/{number}/claim/renew", Status: http.StatusOK,
		Scope: op.ScopeIntentsWrite, Summary: "Renew an intent claim",
		Description: "Extend the live task-keyed claim found through claim.list.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden, op.CodeValidation, op.CodeLeaseConflict},
		Handler:     renewIntentHandler(d),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[IntentLeaseIn, IntentLeaseEmpty]{
		Name: "intent.lease.delete", Location: func(IntentLeaseEmpty) string { return "" }, Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/tasks/{number}/release", Status: http.StatusOK,
		Scope: op.ScopeIntentsWrite, Summary: "Release an intent claim",
		Description: "Release the live task-keyed claim found through claim.list.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden, op.CodeLeaseConflict},
		Handler:     releaseIntentHandler(d),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[IntentChangeAddIn, IntentChangeOut]{
		Name: "intent.change.add", Location: func(IntentChangeOut) string { return "" }, Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/tasks/{number}/changes",
		Scope: op.ScopeIntentsWrite, Summary: "Link a change",
		Description: "Attach a Ruk change id to the intent.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden, op.CodeValidation},
		Handler:     changeAddHandler(d),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[IntentChangeRemoveIn, IntentLeaseEmpty]{
		Name: "intent.change.remove", Location: func(IntentLeaseEmpty) string { return "" }, Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/tasks/{number}/changes/{change}/remove", Status: http.StatusOK,
		Scope: op.ScopeIntentsWrite, Summary: "Unlink a change",
		Description: "Detach a Ruk change id from the intent.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden, op.CodeValidation},
		Handler:     changeRemoveHandler(d),
	}); err != nil {
		return err
	}
	return registerAskOps(t, d)
}
