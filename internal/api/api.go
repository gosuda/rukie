package api

import (
	"context"
	"fmt"
	"hash/fnv"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/op"
)

type IndexIn struct{}

type IndexOut struct {
	Operations []op.ReferenceEntry `json:"operations"`
}

type OpenAPIIn struct{}

type OpenAPIOut struct {
	Body map[string]any
}

func indexETag(out IndexOut) string {
	h := fnv.New64a()
	for _, e := range out.Operations {
		_, _ = h.Write([]byte(e.Name + "\x00"))
	}
	return fmt.Sprintf("%x", h.Sum64())
}

func openAPIETag(out OpenAPIOut) string {
	h := fnv.New64a()
	raw, _ := jsonv2.Marshal(out.Body, jsonv1.DefaultOptionsV1())
	_, _ = h.Write(raw)
	return fmt.Sprintf("%x", h.Sum64())
}

func registerIndexOp(t *op.Table) error {
	return op.Register(t, op.Op[IndexIn, IndexOut]{
		Name:    "api.index",
		Kind:    op.KindRead,
		Method:  "GET",
		Path:    "/-/api",
		Scope:   op.ScopePublicRead,
		Summary: "List the operation table",
		Description: "The zero-JS server-rendered API reference. JSON lists every operation " +
			"with its CLI and MCP names; HTML renders the reference page.",
		ETag: indexETag,
		Handler: func(_ context.Context, _ op.Principal, _ IndexIn) (IndexOut, error) {
			return IndexOut{Operations: op.ReferenceEntries(t.Specs())}, nil
		},
	})
}

func registerOpenAPIOp(t *op.Table, docs *DocStore) error {
	return op.Register(t, op.Op[OpenAPIIn, OpenAPIOut]{
		Name:        "api.openapi",
		Kind:        op.KindRead,
		Method:      "GET",
		Path:        "/-/api/openapi.json",
		Scope:       op.ScopePublicRead,
		Summary:     "Serve the OpenAPI 3.1 document",
		Description: "The generated OpenAPI document. Each operationId is an operation name.",
		ETag:        openAPIETag,
		Handler: func(_ context.Context, _ op.Principal, _ OpenAPIIn) (OpenAPIOut, error) {
			raw := docs.Get()
			if len(raw) == 0 {
				return OpenAPIOut{}, op.Errorf(op.CodeUnavailable, "openapi document is not ready")
			}
			var body map[string]any
			if err := jsonv2.Unmarshal(raw, &body, jsonv1.DefaultOptionsV1()); err != nil {
				return OpenAPIOut{}, op.Wrap(op.CodeInternal, err, "openapi document is corrupt")
			}
			return OpenAPIOut{Body: body}, nil
		},
	})
}
