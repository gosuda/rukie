package events

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/review"
	"github.com/gosuda/rukie/internal/store"
)

func enqueueReviewRemap(tx *store.WriteTx, now time.Time, repoID int64, name, head string) error {
	if _, ok := review.PublishedTarget(repoID, name, head); !ok || isZeroID(head) {
		return nil
	}
	digest := sha256.Sum256(fmt.Appendf(nil, "%d\x00%s\x00%s", repoID, name, head))
	payload, err := jsonv2.Marshal(pushPayload{
		Landed: []LandedRef{{Name: name, New: head}},
		Repo:   repoID,
	})
	if err != nil {
		return fmt.Errorf("enqueueReviewRemap: %w", err)
	}
	if _, err := tx.EnqueueJob(store.JobSpec{
		Payload: string(payload), ID: "review-remap-ref:" + hex.EncodeToString(digest[:]),
		Kind: string(queue.KindReviewRemap), RepoID: repoID, At: now,
	}); err != nil {
		return fmt.Errorf("enqueueReviewRemap: %w", err)
	}
	return nil
}
