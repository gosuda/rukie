package sysops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/store"
)

// ErrKeyMismatch refuses a backup sealed under a different secret key.
// There is no override: the operator must present the key the backup
// was taken with.
var ErrKeyMismatch = errors.New("sysops: backup key id differs from the configured secret key")

// IsKeyMismatch reports a foreign-backup refusal.
func IsKeyMismatch(err error) bool { return errors.Is(err, ErrKeyMismatch) }

// RestoreReport summarizes what restore placed and what it left alone.
// Warnings carries the --force bypasses for the operator log.
type RestoreReport struct {
	Untracked []string
	Warnings  []string
	Files     int
}

// Restore replays a backup directory into the configured data dir.
// It is offline-only: a live server refuses with ErrDataDirLive.
// A key-id mismatch always refuses. Hash mismatches refuse; use
// RestoreLenient to proceed past them loudly. A backup newer than the
// binary refuses as a downgrade; an older schema migrates up on the
// final open.
func Restore(ctx context.Context, cfg config.Config, backupDir string) (RestoreReport, error) {
	return restore(ctx, cfg, backupDir, false)
}

// RestoreLenient replays past hash warnings with force semantics. The
// key-id check stays strict: no flag bypasses a foreign backup.
func RestoreLenient(ctx context.Context, cfg config.Config, backupDir string) (RestoreReport, error) {
	return restore(ctx, cfg, backupDir, true)
}

func restore(ctx context.Context, cfg config.Config, backupDir string, force bool) (RestoreReport, error) {
	if IsDataDirLive(cfg.DataDir) {
		return RestoreReport{}, fmt.Errorf("%w: restore needs the server stopped", ErrDataDirLive)
	}
	doc, err := ReadManifest(backupDir)
	if err != nil {
		return RestoreReport{}, err
	}
	key, err := LoadSecretKey(cfg.SecretKeyFile)
	if err != nil {
		return RestoreReport{}, err
	}
	if KeyID(key) != doc.KeyID {
		return RestoreReport{}, fmt.Errorf("%w: backup %s, config %s",
			ErrKeyMismatch, doc.KeyID, KeyID(key))
	}
	var report RestoreReport
	if !force {
		if verr := VerifyManifest(backupDir, doc); verr != nil {
			return RestoreReport{}, verr
		}
	} else {
		report.Warnings = InspectManifest(backupDir, doc)
	}
	code, err := CodeSchemaVersion()
	if err != nil {
		return RestoreReport{}, err
	}
	if doc.SchemaVersion > code {
		return RestoreReport{}, &DowngradeError{Have: doc.SchemaVersion, Want: code}
	}
	if err := EnsureDataDir(cfg.DataDir); err != nil {
		return RestoreReport{}, err
	}
	placed, rerr := placeFiles(cfg, backupDir, doc)
	if rerr != nil {
		return RestoreReport{}, rerr
	}
	report.Files = placed.Files
	report.Untracked = placed.Untracked
	st, oerr := store.Open(ctx, store.Options{Path: cfg.Store.SQLite.Path})
	if oerr != nil {
		return RestoreReport{}, fmt.Errorf("sysops: migrate restored metadata: %w", oerr)
	}
	if cerr := st.Close(ctx); cerr != nil {
		return RestoreReport{}, fmt.Errorf("sysops: close restored store: %w", cerr)
	}
	if merr := RefreshMetaVersion(cfg); merr != nil {
		return RestoreReport{}, merr
	}
	return report, nil
}

func placeFiles(cfg config.Config, backupDir string, doc ManifestDoc) (RestoreReport, error) {
	var report RestoreReport
	seen := map[string]bool{}
	for _, f := range doc.Files {
		var dst string
		switch f.Path {
		case "meta.sqlite":
			dst = cfg.Store.SQLite.Path
			dropStaleSQLiteSiblings(dst)
		default:
			rel, ok := strings.CutPrefix(f.Path, "repos/")
			if !ok || rel == "" || strings.Contains(rel, "..") {
				return RestoreReport{}, fmt.Errorf("sysops: manifest path %q is outside the restore set", f.Path)
			}
			dst = filepath.Join(cfg.Ruk.RepoRoot, rel)
		}
		src := filepath.Join(backupDir, f.Path)
		srcSum, _, herr := hashFile(src)
		if herr != nil {
			return RestoreReport{}, fmt.Errorf("sysops: restore %s: %w", f.Path, herr)
		}
		rec, cerr := copyFileHashed(src, dst, 0o600)
		if cerr != nil {
			return RestoreReport{}, fmt.Errorf("sysops: restore %s: %w", f.Path, cerr)
		}
		if rec.SHA256 != srcSum {
			return RestoreReport{}, fmt.Errorf("sysops: %s changed between read and place", f.Path)
		}
		seen[dst] = true
		report.Files++
	}
	report.Untracked = listUntrackedForReconcileReview(cfg, seen)
	return report, nil
}

func dropStaleSQLiteSiblings(dbPath string) {
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
	_ = os.Remove(dbPath)
}

func listUntrackedForReconcileReview(cfg config.Config, seen map[string]bool) []string {
	var untracked []string
	_ = filepath.WalkDir(cfg.Ruk.RepoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !seen[path] {
			rel, rerr := filepath.Rel(cfg.Ruk.RepoRoot, path)
			if rerr == nil {
				untracked = append(untracked, rel)
			}
		}
		return nil
	})
	return untracked
}
