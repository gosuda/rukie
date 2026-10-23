package api

import (
	context "context"
	errors "errors"
	fmt "fmt"
	http "net/http"
	strings "strings"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

type AskOptionJSON struct {
	Key   string `json:"id"`
	Label string `json:"label"`
}

type AskListIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	State  string `query:"state"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

type AskShowIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	ID    string `path:"id"`
}

type AskCancelIn struct {
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	ID             string `path:"id"`
	IdempotencyKey string `header:"Idempotency-Key"`
}

type AskAnswerCreateIn struct {
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	ID             string `path:"id"`
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           struct {
		Answer string `json:"answer"`
	}
}

func mapAskStore(err error, missing string, stale string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return op.Errorf(op.CodeNotFound, "%s", missing)
	}
	if errors.Is(err, store.ErrStale) {
		return op.Errorf(op.CodeConflict, "%s", stale)
	}
	if errors.Is(err, store.ErrInvalid) {
		return op.Errorf(op.CodeValidation, "ask input is not valid")
	}
	return op.Wrap(op.CodeInternal, err, "ask store operation failed")
}

func validAskState(state string) bool {
	switch state {
	case "", "asked", "answered", "expired":
		return true
	default:
		return false
	}
}

func askListHandler(d Deps) func(context.Context, op.Principal, AskListIn) (AskListOut, error) {
	return func(ctx context.Context, p op.Principal, in AskListIn) (AskListOut, error) {
		_, rc, err := intentRepo(ctx, d, p, in.Owner, in.Repo, "task.view")
		if err != nil {
			return AskListOut{}, err
		}
		if !validAskState(in.State) {
			return AskListOut{}, op.Errorf(op.CodeValidation, "state must be asked, answered, or expired")
		}
		limit := clampLimit(in.Limit, 30, 100)
		out := AskListOut{Asks: []AskJSON{}}
		rerr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			rows, lerr := tx.ListAsks(rc.row.ID, 0, in.State, limit)
			if lerr != nil {
				return lerr
			}
			for _, row := range rows {
				out.Asks = append(out.Asks, askJSON(row))
			}
			return nil
		})
		if rerr != nil {
			return AskListOut{}, mapAskStore(rerr, "asks were not found", "asks changed under this request")
		}
		return out, nil
	}
}

func askGetHandler(d Deps) func(context.Context, op.Principal, AskShowIn) (AskGetOut, error) {
	return func(ctx context.Context, p op.Principal, in AskShowIn) (AskGetOut, error) {
		_, rc, err := intentRepo(ctx, d, p, in.Owner, in.Repo, "task.view")
		if err != nil {
			return AskGetOut{}, err
		}
		if strings.TrimSpace(in.ID) == "" {
			return AskGetOut{}, op.Errorf(op.CodeValidation, "ask id must not be empty")
		}
		var row store.AskRow
		rerr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			var gerr error
			row, gerr = tx.ReadAskByPublicID(rc.row.ID, in.ID)
			return gerr
		})
		if rerr != nil {
			return AskGetOut{}, mapAskStore(rerr, "ask was not found", "ask changed under this request")
		}
		return AskGetOut{Ask: askJSON(row)}, nil
	}
}

func askCreateHandler(d Deps) func(context.Context, op.Principal, AskCreateIn) (AskCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in AskCreateIn) (AskCreateOut, error) {
		rq, rc, err := intentRepo(ctx, d, p, in.Owner, in.Repo, "task.edit")
		if err != nil {
			return AskCreateOut{}, err
		}
		question := strings.TrimSpace(in.Body.Question)
		if question == "" {
			return AskCreateOut{}, op.Errorf(op.CodeValidation, "question must not be empty")
		}
		if len(question) > 8192 {
			return AskCreateOut{}, op.Errorf(op.CodeValidation, "question is too long")
		}
		var opts []AskOptionJSON
		if len(in.Body.Options) > 0 {
			if uerr := jsonv2.Unmarshal([]byte(in.Body.Options), &opts, jsonv1.DefaultOptionsV1()); uerr != nil {
				return AskCreateOut{}, op.Errorf(op.CodeValidation, "options are not valid")
			}
		}
		if len(opts) > 8 {
			return AskCreateOut{}, op.Errorf(op.CodeValidation, "options hold at most 8 entries")
		}
		for _, opt := range opts {
			if strings.TrimSpace(opt.Key) == "" || len(opt.Key) > 64 {
				return AskCreateOut{}, op.Errorf(op.CodeValidation, "option id must be 1 to 64 characters")
			}
			if strings.TrimSpace(opt.Label) == "" || len(opt.Label) > 200 {
				return AskCreateOut{}, op.Errorf(op.CodeValidation, "option label must be 1 to 200 characters")
			}
		}
		encoded := []byte("[]")
		if len(opts) > 0 {
			encoded, err = jsonv2.Marshal(opts, jsonv1.DefaultOptionsV1())
			if err != nil {
				return AskCreateOut{}, op.Wrap(op.CodeInternal, err, "ask options could not be encoded")
			}
		}
		nowMs := d.now().UnixMilli()
		deadline := in.Body.DeadlineMs
		if deadline != 0 {
			if deadline <= nowMs {
				return AskCreateOut{}, op.Errorf(op.CodeValidation, "deadline must be in the future")
			}
			if deadline > nowMs+30*24*3600*1000 {
				return AskCreateOut{}, op.Errorf(op.CodeValidation, "deadline is at most 30 days out")
			}
		}
		intent, err := loadIntent(ctx, d, rc.row.ID, in.Number)
		if err != nil {
			return AskCreateOut{}, mapIntentStore(err)
		}
		var saved store.AskRow
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			row, ierr := tx.InsertAsk(d.now(), store.AskRow{
				IntentID:    intent.ID,
				Question:    question,
				Options:     string(encoded),
				BlocksClaim: in.Body.BlocksClaim,
				DeadlineMS:  deadline,
				CreatedBy:   rq.userID,
			})
			if ierr != nil {
				return ierr
			}
			saved = row
			return nil
		})
		if werr != nil {
			return AskCreateOut{}, mapAskStore(werr, "intent was not found", "intent changed under this request")
		}
		return AskCreateOut{Ask: askJSON(saved)}, nil
	}
}

func askCancelHandler(d Deps) func(context.Context, op.Principal, AskCancelIn) (AskUpdateOut, error) {
	return func(ctx context.Context, p op.Principal, in AskCancelIn) (AskUpdateOut, error) {
		_, rc, err := intentRepo(ctx, d, p, in.Owner, in.Repo, "task.edit")
		if err != nil {
			return AskUpdateOut{}, err
		}
		if strings.TrimSpace(in.ID) == "" {
			return AskUpdateOut{}, op.Errorf(op.CodeValidation, "ask id must not be empty")
		}
		var saved store.AskRow
		var ask store.AskRow
		rerr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			var gerr error
			ask, gerr = tx.ReadAskByPublicID(rc.row.ID, in.ID)
			return gerr
		})
		if rerr != nil {
			return AskUpdateOut{}, mapAskStore(rerr, "ask was not found", "ask changed under this request")
		}
		now := d.now()
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			if cerr := tx.CancelAsk(now, ask.ID, ask.Version); cerr != nil {
				return cerr
			}
			var gerr error
			saved, gerr = tx.GetAsk(ask.ID)
			return gerr
		})
		if werr != nil {
			return AskUpdateOut{}, mapAskStore(werr, "ask was not found", "ask is not open")
		}
		return AskUpdateOut{Ask: askJSON(saved)}, nil
	}
}

func askAnswerHandler(d Deps) func(context.Context, op.Principal, AskAnswerCreateIn) (AskAnswerOut, error) {
	return func(ctx context.Context, p op.Principal, in AskAnswerCreateIn) (AskAnswerOut, error) {
		_, rc, err := intentRepo(ctx, d, p, in.Owner, in.Repo, "task.edit")
		if err != nil {
			return AskAnswerOut{}, err
		}
		if p.TokenID >= 0 {
			return AskAnswerOut{}, op.Errorf(op.CodeForbidden, "ask answers require a human session")
		}
		answer := strings.TrimSpace(in.Body.Answer)
		if answer == "" {
			return AskAnswerOut{}, op.Errorf(op.CodeValidation, "answer must not be empty")
		}
		if len(answer) > 8192 {
			return AskAnswerOut{}, op.Errorf(op.CodeValidation, "answer is too long")
		}
		if strings.TrimSpace(in.ID) == "" {
			return AskAnswerOut{}, op.Errorf(op.CodeValidation, "ask id must not be empty")
		}
		var ask store.AskRow
		rerr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			var gerr error
			ask, gerr = tx.ReadAskByPublicID(rc.row.ID, in.ID)
			return gerr
		})
		if rerr != nil {
			return AskAnswerOut{}, mapAskStore(rerr, "ask was not found", "ask changed under this request")
		}
		now := d.now()
		var savedAsk store.AskRow
		var savedIntent store.IntentRow
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			if aerr := tx.AnswerAskAt(now, ask.IntentID, ask.ID, p.UserID, answer); aerr != nil {
				return aerr
			}
			var gerr error
			savedAsk, gerr = tx.GetAsk(ask.ID)
			if gerr != nil {
				return gerr
			}
			savedIntent, gerr = tx.GetIntent(ask.IntentID)
			return gerr
		})
		if werr != nil {
			if errors.Is(werr, store.ErrStale) {
				return AskAnswerOut{}, op.Errorf(op.CodeConflict, "ask is expired or already answered")
			}
			return AskAnswerOut{}, mapAskStore(werr, "ask was not found", "ask is expired or already answered")
		}
		return AskAnswerOut{Ask: askJSON(savedAsk), Intent: intentJSON(rc.owner, rc.name, savedIntent)}, nil
	}
}

func registerAskOps(t *op.Table, d Deps) error {
	if err := op.Register(t, op.Op[AskListIn, AskListOut]{
		Name: "ask.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/asks",
		Scope: op.ScopeIntentsRead, Summary: "List asks",
		Description: "Structured agent-to-human questions of one repository, newest first.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden, op.CodeValidation},
		ETag: func(out AskListOut) string {
			return fmt.Sprintf("asks-%d", len(out.Asks))
		},
		Pagination: &op.Pagination[AskListOut]{Default: 30, Max: 100, Next: func(_ AskListOut) (string, bool) {
			return "", false
		}},
		Handler: askListHandler(d),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AskShowIn, AskGetOut]{
		Name: "ask.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/asks/{id}",
		Scope: op.ScopeIntentsRead, Summary: "Show an ask",
		Description: "One structured question with its answer state.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden, op.CodeValidation},
		ETag: func(out AskGetOut) string {
			return fmt.Sprintf("ask-%s-%s", out.Ask.PublicID, out.Ask.State)
		},
		Handler: askGetHandler(d),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AskCreateIn, AskCreateOut]{
		Name: "ask.create", Location: func(AskCreateOut) string { return "" }, Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/tasks/{number}/asks",
		Scope: op.ScopeIntentsWrite, Summary: "Ask a question",
		Description: "Blocking asks flip the intent to blocked until answered or expired.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeNotFound},
		Handler:     askCreateHandler(d),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AskCancelIn, AskUpdateOut]{
		Name: "ask.update", Location: func(AskUpdateOut) string { return "" }, Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/asks/{id}/cancel", Status: http.StatusOK,
		Scope: op.ScopeIntentsWrite, Summary: "Cancel an ask",
		Description: "Cancel an open ask. Cancellation expires it and unblocks the lease.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeNotFound, op.CodeConflict},
		Handler:     askCancelHandler(d),
	}); err != nil {
		return err
	}
	return op.Register(t, op.Op[AskAnswerCreateIn, AskAnswerOut]{
		Name: "ask.answer.create", Location: func(AskAnswerOut) string { return "" }, Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/asks/{id}/answer", Status: http.StatusOK,
		Scope: op.ScopeIntentsWrite, Summary: "Answer an ask",
		Description: "Human-only answer. Answering unblocks the lease in the same transaction.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeNotFound, op.CodeConflict},
		Handler:     askAnswerHandler(d),
	})
}
