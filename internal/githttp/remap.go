package githttp

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/review"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
)

type diffByteString string

func (b *diffByteString) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '"' {
		var value string
		if err := jsonv2.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("diffByteString.UnmarshalJSON: %w", err)
		}
		*b = diffByteString(value)
		return nil
	}
	var value struct {
		Hex *string `json:"hex"`
	}
	if err := jsonv2.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("diffByteString.UnmarshalJSON: %w", err)
	}
	if value.Hex == nil {
		return fmt.Errorf("diffByteString.UnmarshalJSON: missing hex bytes")
	}
	decoded, err := hex.DecodeString(*value.Hex)
	if err != nil {
		return fmt.Errorf("diffByteString.UnmarshalJSON: %w", err)
	}
	*b = diffByteString(decoded)
	return nil
}

type diffFileItem struct {
	OldPath *diffByteString   `json:"old_path"`
	Path    diffByteString    `json:"path"`
	Body    jsonv1.RawMessage `json:"body"`
}

type diffFilesPage struct {
	Items []diffFileItem    `json:"items"`
	Next  jsonv1.RawMessage `json:"next"`
}

type diffReplyJSON struct {
	Files *diffFilesPage `json:"files"`
}

func (s *Server) changeSpans(ctx context.Context, repoID int64, oldHead, newHead string) (map[string][]review.Span, error) {
	path, err := s.repoDaemonPath(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("changeSpans: %w", err)
	}
	client, err := s.clientFor(ctx)
	if err != nil {
		return nil, fmt.Errorf("changeSpans: %w", err)
	}
	repo, err := client.OpenRepo(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("changeSpans: %w", err)
	}
	page := map[string]any{"limit": 1000}
	args := map[string]any{
		"left":  map[string]any{"head": map[string]any{"head": map[string]any{"oid": oldHead, "type": "git"}}},
		"right": map[string]any{"head": map[string]any{"head": map[string]any{"oid": newHead, "type": "git"}}},
		"paths": "all",
		"page":  page,
	}
	return collectChangeSpans(ctx, func(ctx context.Context, cursor jsonv1.RawMessage) (diffReplyJSON, error) {
		if len(cursor) != 0 {
			page["cursor"] = cursor
		}
		return ruk.Call[map[string]any, diffReplyJSON](ctx, client, repo, wire.MethodDiff, args)
	})
}

func collectChangeSpans(ctx context.Context, read func(context.Context, jsonv1.RawMessage) (diffReplyJSON, error)) (map[string][]review.Span, error) {
	spans := make(map[string][]review.Span)
	renamed := make(map[string]struct{})
	var cursor jsonv1.RawMessage
	for {
		reply, err := read(ctx, cursor)
		if err != nil {
			return nil, fmt.Errorf("collectChangeSpans: %w", err)
		}
		if reply.Files == nil || reply.Files.Items == nil {
			return nil, fmt.Errorf("collectChangeSpans: diff is missing its file list")
		}
		for _, file := range reply.Files.Items {
			patch, text, err := unifiedPatchBytes(file.Body)
			if err != nil {
				return nil, fmt.Errorf("collectChangeSpans: %w", err)
			}
			path := string(file.Path)
			if path == "" {
				return nil, fmt.Errorf("collectChangeSpans: diff file has no path")
			}
			spans[path] = nil
			if file.OldPath != nil {
				oldPath := string(*file.OldPath)
				if oldPath == "" {
					return nil, fmt.Errorf("collectChangeSpans: rename has no source path")
				}
				renamed[oldPath] = struct{}{}
				renamed[path] = struct{}{}
				continue
			}
			if text {
				fileSpans, err := review.SpansFromPatch(map[string]string{path: patch})
				if err != nil {
					return nil, fmt.Errorf("collectChangeSpans: %w", err)
				}
				spans[path] = fileSpans[path]
			}
		}
		if len(reply.Files.Next) == 0 || bytes.Equal(bytes.TrimSpace(reply.Files.Next), []byte("null")) {
			for path := range renamed {
				spans[path] = nil
			}
			return spans, nil
		}
		if bytes.Equal(cursor, reply.Files.Next) {
			return nil, fmt.Errorf("collectChangeSpans: diff cursor did not advance")
		}
		cursor = reply.Files.Next
	}
}

func unifiedPatchBytes(raw jsonv1.RawMessage) (string, bool, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte(`"binary"`)) {
		return "", false, nil
	}
	var body struct {
		Unified *struct {
			Patch struct {
				Inline *struct {
					Data *diffByteString `json:"data"`
				} `json:"inline"`
			} `json:"patch"`
		} `json:"unified"`
	}
	if err := jsonv2.Unmarshal(raw, &body, jsonv1.DefaultOptionsV1()); err != nil {
		return "", false, fmt.Errorf("unifiedPatchBytes: %w", err)
	}
	if body.Unified == nil || body.Unified.Patch.Inline == nil || body.Unified.Patch.Inline.Data == nil {
		return "", false, fmt.Errorf("unifiedPatchBytes: diff is not an inline patch")
	}
	return string(*body.Unified.Patch.Inline.Data), true, nil
}
