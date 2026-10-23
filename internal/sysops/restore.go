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
	lock, err := AcquireDataDirLock(cfg.DataDir)
	if err != nil {
		return RestoreReport{}, fmt.Errorf("%w: restore needs the server stopped", ErrDataDirLive)
	}
	defer func() { _ = lock.Release() }()
	doc, err := ReadManifest(backupDir)
	if err != nil {
		return RestoreReport{}, err
	}
	if doc.BarrierID == "" && !force {
		return RestoreReport{}, fmt.Errorf("sysops: manifest carries no barrier_id (pre-barrier legacy backup); restore only with --force followed by `rukie admin reconcile --from-manifest` and operator review")
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
	if doc.BarrierID == "" {
		report.Warnings = append(report.Warnings, "pre-barrier manifest without barrier_id: run `rukie admin reconcile --from-manifest` and review before serving")
	}
	if !force {
		if verr := VerifyManifest(backupDir, doc); verr != nil {
			return RestoreReport{}, verr
		}
	} else {
		report.Warnings = append(report.Warnings, InspectManifest(backupDir, doc)...)
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
	placed, rerr := placeFilesStaged(cfg, backupDir, doc)
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

func resolveRestoreDst(cfg config.Config, manifestPath string) (string, error) {
	if manifestPath == "meta.sqlite" {
		return cfg.Store.SQLite.Path, nil
	}
	if rel, ok := strings.CutPrefix(manifestPath, "releases/"); ok {
		if rel == "" || strings.Contains(rel, "..") {
			return "", fmt.Errorf("sysops: manifest path %q is outside the restore set", manifestPath)
		}
		if filepath.IsAbs(rel) {
			return "", fmt.Errorf("sysops: manifest path %q is outside the restore set", manifestPath)
		}
		return filepath.Join(cfg.DataDir, "releases", filepath.FromSlash(rel)), nil
	}
	rel, ok := strings.CutPrefix(manifestPath, "repos/")
	if !ok || rel == "" || strings.Contains(rel, "..") {
		return "", fmt.Errorf("sysops: manifest path %q is outside the restore set", manifestPath)
	}
	return filepath.Join(cfg.Ruk.RepoRoot, rel), nil
}

func placeFilesStaged(cfg config.Config, backupDir string, doc ManifestDoc) (RestoreReport, error) {
	var report RestoreReport
	staging, err := os.MkdirTemp(filepath.Join(cfg.DataDir, "tmp"), "restore-*")
	if err != nil {
		return RestoreReport{}, fmt.Errorf("sysops: stage restore: %w", err)
	}
	stagedKept := false
	defer func() {
		if !stagedKept {
			_ = os.RemoveAll(staging)
		}
	}()
	type placement struct {
		staged    string
		dst       string
		aside     string
		moved     bool
		installed bool
	}
	rollbackDir := filepath.Join(staging, "rollback")
	if err := os.MkdirAll(rollbackDir, 0o700); err != nil {
		return RestoreReport{}, fmt.Errorf("sysops: stage rollback dir: %w", err)
	}
	var placements []placement
	rollback := func() {
		for i := len(placements) - 1; i >= 0; i-- {
			p := placements[i]
			if p.moved {
				if p.installed {
					_ = os.Remove(p.dst)
				}
				_ = os.Rename(p.aside, p.dst)
				for _, suffix := range []string{"-wal", "-shm"} {
					if _, serr := os.Stat(p.aside + suffix); serr == nil {
						_ = os.Rename(p.aside+suffix, p.dst+suffix)
					}
				}
			} else if p.installed {
				_ = os.Remove(p.dst)
			}
		}
	}
	stage := func(f ManifestFile) (placement, error) {
		var p placement
		var err error
		if p.dst, err = resolveRestoreDst(cfg, f.Path); err != nil {
			return p, err
		}
		p.staged = filepath.Join(staging, "files", filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p.staged), 0o700); err != nil {
			return p, fmt.Errorf("sysops: stage %s: %w", f.Path, err)
		}
		src := filepath.Join(backupDir, f.Path)
		srcSum, _, herr := hashFile(src)
		if herr != nil {
			return p, fmt.Errorf("sysops: stage %s: %w", f.Path, herr)
		}
		rec, cerr := copyFileHashed(src, p.staged, 0o600)
		if cerr != nil {
			return p, fmt.Errorf("sysops: stage %s: %w", f.Path, cerr)
		}
		if rec.SHA256 != srcSum {
			return p, fmt.Errorf("sysops: staged %s changed between read and place", f.Path)
		}
		p.aside = filepath.Join(rollbackDir, filepath.FromSlash(f.Path))
		return p, nil
	}
	for _, f := range doc.Files {
		p, serr := stage(f)
		if serr != nil {
			return RestoreReport{}, serr
		}
		placements = append(placements, p)
	}
	siblings := []string{"-wal", "-shm"}
	seen := map[string]bool{}
	for i := range placements {
		p := &placements[i]
		if err := os.MkdirAll(filepath.Dir(p.dst), 0o700); err != nil {
			rollback()
			return RestoreReport{}, fmt.Errorf("sysops: restore %s: %w", p.dst, err)
		}
		if _, serr := os.Stat(p.dst); serr == nil {
			if err := os.MkdirAll(filepath.Dir(p.aside), 0o700); err != nil {
				rollback()
				return RestoreReport{}, fmt.Errorf("sysops: park %s: %w", p.dst, err)
			}
			if rerr := os.Rename(p.dst, p.aside); rerr != nil {
				rollback()
				return RestoreReport{}, fmt.Errorf("sysops: park %s: %w", p.dst, rerr)
			}
			p.moved = true
			if p.dst == cfg.Store.SQLite.Path {
				for _, suffix := range siblings {
					if _, serr := os.Stat(p.dst + suffix); serr == nil {
						aside := p.aside + suffix
						if rerr := os.Rename(p.dst+suffix, aside); rerr != nil {
							rollback()
							return RestoreReport{}, fmt.Errorf("sysops: park %s: %w", p.dst+suffix, rerr)
						}
					}
				}
			}
		}
		if rerr := os.Rename(p.staged, p.dst); rerr != nil {
			rollback()
			return RestoreReport{}, fmt.Errorf("sysops: place %s: %w", p.dst, rerr)
		}
		p.installed = true
		seen[p.dst] = true
		report.Files++
	}
	report.Untracked = listUntrackedForReconcileReview(cfg, seen)
	stagedKept = true
	if rerr := os.RemoveAll(staging); rerr != nil {
		return report, fmt.Errorf("sysops: clean restore staging: %w", rerr)
	}
	return report, nil
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
