package events

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/store"
)

const SpoolDrainInterval = 30 * time.Second

const (
	spoolDirName = "spool"
	spoolSuffix  = ".json"
)

type LandedRef struct {
	Name string `json:"name"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

type PushRecord struct {
	InvocationID string      `json:"invocation_id"`
	Landed       []LandedRef `json:"landed"`
	RepoID       int64       `json:"repo_id"`
	ActorID      int64       `json:"actor_id"`
	AttemptedAt  int64       `json:"attempted_at"`
}

type Spool struct {
	dir string
}

func OpenSpool(dataDir string) (*Spool, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("OpenSpool: data dir must not be empty")
	}
	dir := filepath.Join(dataDir, spoolDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("OpenSpool: %w", err)
	}
	return &Spool{dir: dir}, nil
}

func checkSpoolID(id string) error {
	if len(id) == 0 || len(id) > 96 {
		return fmt.Errorf("spool id must be 1 to 96 bytes")
	}
	for _, r := range id {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'
		if !ok {
			return fmt.Errorf("spool id %q carries an unsafe character", id)
		}
	}
	if id == "." || id == ".." {
		return fmt.Errorf("spool id %q is not a file name", id)
	}
	return nil
}

func (s *Spool) path(id string) (string, error) {
	if err := checkSpoolID(id); err != nil {
		return "", err
	}
	return filepath.Clean(filepath.Join(s.dir, id+spoolSuffix)), nil
}

func (s *Spool) Store(rec PushRecord) error {
	if err := checkSpoolID(rec.InvocationID); err != nil {
		return fmt.Errorf("Store: %w", err)
	}
	if rec.RepoID <= 0 {
		return fmt.Errorf("Store: %w: repo id must be positive", store.ErrInvalid)
	}
	if rec.AttemptedAt <= 0 {
		return fmt.Errorf("Store: %w: attempted_at must be after the Unix epoch", store.ErrInvalid)
	}
	raw, err := jsonv2.Marshal(rec)
	if err != nil {
		return fmt.Errorf("Store: %w", err)
	}
	dst, err := s.path(rec.InvocationID)
	if err != nil {
		return fmt.Errorf("Store: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, rec.InvocationID+".tmp.*")
	if err != nil {
		return fmt.Errorf("Store: %w", err)
	}
	tmpName := tmp.Name()
	if _, werr := tmp.Write(raw); werr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("Store: %w", werr)
	}
	if serr := tmp.Sync(); serr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("Store: %w", serr)
	}
	if cerr := tmp.Close(); cerr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("Store: %w", cerr)
	}
	if rerr := os.Rename(tmpName, dst); rerr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("Store: %w", rerr)
	}
	if derr := syncDir(s.dir); derr != nil {
		return fmt.Errorf("Store: %w", derr)
	}
	return nil
}

func syncDir(dir string) error {
	fd, err := os.Open(filepath.Clean(dir))
	if err != nil {
		return err
	}
	defer func() { _ = fd.Close() }()
	return fd.Sync()
}

func (s *Spool) Load(id string) (PushRecord, error) {
	var zero PushRecord
	dst, err := s.path(id)
	if err != nil {
		return zero, fmt.Errorf("Load: %w", err)
	}
	raw, err := os.ReadFile(filepath.Clean(dst))
	if err != nil {
		return zero, fmt.Errorf("Load: %w", err)
	}
	var rec PushRecord
	if err = jsonv2.Unmarshal(raw, &rec, jsonv1.DefaultOptionsV1()); err != nil {
		return zero, fmt.Errorf("Load: %w", err)
	}
	return rec, nil
}

func (s *Spool) Remove(id string) error {
	dst, err := s.path(id)
	if err != nil {
		return fmt.Errorf("Remove: %w", err)
	}
	if err = os.Remove(dst); err != nil {
		return fmt.Errorf("Remove: %w", err)
	}
	return nil
}

func (s *Spool) Pending() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("Pending: %w", err)
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		id, ok := strings.CutSuffix(entry.Name(), spoolSuffix)
		if !ok {
			continue
		}
		if checkSpoolID(id) != nil {
			continue
		}
		out = append(out, id)
	}
	return out, nil
}

func (s *Spool) Replay(ctx context.Context, st *store.Store, attempt func(*store.WriteTx, PushRecord) error) error {
	if attempt == nil {
		return fmt.Errorf("Replay: attempt must not be nil")
	}
	ids, err := s.Pending()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rec, lerr := s.Load(id)
		if lerr != nil {
			return lerr
		}
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			return attempt(tx, rec)
		})
		if werr != nil {
			return werr
		}
		if rerr := s.Remove(id); rerr != nil {
			return rerr
		}
	}
	return nil
}

func DrainLoop(ctx context.Context, sp *Spool, st *store.Store, attempt func(*store.WriteTx, PushRecord) error) error {
	if err := sp.Replay(ctx, st, attempt); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	timer := time.NewTimer(SpoolDrainInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		if err := sp.Replay(ctx, st, attempt); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		timer.Reset(SpoolDrainInterval)
	}
}
