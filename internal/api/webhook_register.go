package api

import (
	"fmt"
	"net/http"

	"github.com/gosuda/rukie/internal/op"
)

// RegisterWebhook registers the F8 signed-webhook operations. Repo-family
// paths carry the repo; owner-scope endpoints additionally exist under the
// owner hooks page (see webhook_views.go). Every endpoint read or mutation
// loads the endpoint by id and authorizes against the endpoint's own scope.
func RegisterWebhook(t *op.Table, deps Deps) error {
	if err := op.Register(t, op.Op[WebhookEndpointCreateIn, WebhookEndpointCreateOut]{
		Name: "webhook.endpoint.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/settings/hooks",
		Scope: op.ScopeWebhooksWrite, Summary: "Create a webhook endpoint",
		Description: "Create a repo- or owner-scope endpoint. The secret seals at rest and shows once. " +
			"Owner scope reads the owner from the path; the path repo is ignored. " +
			"Consumers verify deliveries with the Standard Webhooks recipe on the endpoint settings page.",
		Problems: []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeValidation, op.CodeForbidden, op.CodeConflict, op.CodeLimitExceeded},
		Handler:  webhookEndpointCreateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[WebhookEndpointListIn, WebhookEndpointListOut]{
		Name: "webhook.endpoint.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/settings/hooks",
		Scope: op.ScopeWebhooksRead, Summary: "List webhook endpoints",
		Description: "Endpoints of one repository (repo scope) or one owner (owner scope), oldest first. Secrets never leave the create and rotate responses.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out WebhookEndpointListOut) string {
			return fmt.Sprintf("\"webhook-endpoints-%d-%s\"", len(out.Endpoints), out.Next)
		},
		Pagination: &op.Pagination[WebhookEndpointListOut]{Default: 30, Max: 100, Next: func(out WebhookEndpointListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: webhookEndpointListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[WebhookEndpointGetIn, WebhookEndpointGetOut]{
		Name: "webhook.endpoint.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/settings/hooks/{id}",
		Scope: op.ScopeWebhooksRead, Summary: "Show a webhook endpoint",
		Description: "One endpoint by id, authorized against the endpoint's own scope. A disagreeing path repo is ignored.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out WebhookEndpointGetOut) string {
			return fmt.Sprintf("\"webhook-endpoint-%s-%d\"", out.Endpoint.ID, out.Endpoint.UpdatedAt)
		},
		Handler: webhookEndpointGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[WebhookEndpointUpdateIn, WebhookEndpointUpdateOut]{
		Name: "webhook.endpoint.update", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/settings/hooks/{id}",
		Scope: op.ScopeWebhooksWrite, Summary: "Update a webhook endpoint",
		Description: "Change endpoint settings. Setting active clears the disabled markers and resets the failure counters. The secret never changes here; rotate it instead.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeValidation, op.CodeForbidden, op.CodeConflict},
		Handler:     webhookEndpointUpdateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[WebhookEndpointDeleteIn, WebhookEndpointDeleteOut]{
		Name: "webhook.endpoint.delete", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/settings/hooks/{id}/delete",
		Scope: op.ScopeWebhooksWrite, Summary: "Delete a webhook endpoint",
		Description: "Delete an endpoint with its messages and attempts. Messages already claimed finish on their own.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		Handler:     webhookEndpointDeleteHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[WebhookEndpointRotateIn, WebhookEndpointRotateOut]{
		Name: "webhook.endpoint.rotate-secret", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/settings/hooks/{id}/rotate-secret",
		Scope: op.ScopeWebhooksWrite, Summary: "Rotate an endpoint secret",
		Description: "Move the current secret to the previous slot for 24 hours and mint a fresh one, shown once.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		Handler:     webhookEndpointRotateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[WebhookEndpointTestIn, WebhookEndpointTestOut]{
		Name: "webhook.endpoint.test", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/settings/hooks/{id}/test",
		Scope: op.ScopeWebhooksWrite, Summary: "Ping a webhook endpoint",
		Description: "Send a synthetic endpoint.tested envelope with a fresh webhook id and sign it like a real delivery, without touching the outbox or message tables. Refused while the endpoint is inactive. The ping carries the same headers the verification recipe on the endpoint settings page checks.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden, op.CodeConflict},
		Handler:     webhookEndpointTestHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[WebhookMessageListIn, WebhookMessageListOut]{
		Name: "webhook.message.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/settings/hooks/{id}/messages",
		Scope: op.ScopeWebhooksRead, Summary: "List endpoint messages",
		Description: "Outbox and delivery-log rows of one endpoint, newest first, with an optional state filter. Payload bytes stay on the single-message read.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out WebhookMessageListOut) string {
			return fmt.Sprintf("\"webhook-messages-%d-%s\"", len(out.Messages), out.Next)
		},
		Pagination: &op.Pagination[WebhookMessageListOut]{Default: 30, Max: 100, Next: func(out WebhookMessageListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: webhookMessageListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[WebhookMessageGetIn, WebhookMessageGetOut]{
		Name: "webhook.message.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/settings/hooks/{id}/messages/{mid}",
		Scope: op.ScopeWebhooksRead, Summary: "Show an endpoint message",
		Description: "One message by id with the exact payload bytes that were signed and sent.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out WebhookMessageGetOut) string {
			return fmt.Sprintf("\"webhook-message-%s-%d-%d\"", out.Message.ID, out.Message.AttemptCount, out.Message.NextAttemptAt)
		},
		Handler: webhookMessageGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[WebhookMessageRedeliverIn, WebhookMessageRedeliverOut]{
		Name: "webhook.message.redeliver", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/settings/hooks/{id}/messages/{mid}/redeliver",
		Scope: op.ScopeWebhooksWrite, Summary: "Redeliver a message",
		Description: "Reset a dead or delivered message to pending under the same webhook id and payload. Pending and inflight messages refuse.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		Handler:     webhookMessageRedeliverHandler(deps),
	}); err != nil {
		return err
	}
	return op.Register(t, op.Op[WebhookAttemptListIn, WebhookAttemptListOut]{
		Name: "webhook.attempt.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/settings/hooks/{id}/messages/{mid}/attempts",
		Scope: op.ScopeWebhooksRead, Summary: "List message attempts",
		Description: "The retained delivery attempts of one message, oldest first. Only the latest attempts are kept.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out WebhookAttemptListOut) string {
			return fmt.Sprintf("\"webhook-attempts-%d\"", len(out.Attempts))
		},
		Handler: webhookAttemptListHandler(deps),
	})
}
