package sysops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/store"
)

// MigrateResult reports an offline migration run.
type MigrateResult struct {
	Pending    []int64
	AppliedMax int64
	CodeMax    int64
	Applied    bool
}

// PendingMigrations lists the versions an offline migrate would apply,
// without touching the database. The meta.version marker refuses first
// when present; the table decides otherwise.
func PendingMigrations(cfg config.Config) (MigrateResult, error) {
	dbPath := cfg.Store.SQLite.Path
	code, err := CodeSchemaVersion()
	if err != nil {
		return MigrateResult{}, err
	}
	if marked, ok := ReadMetaVersion(cfg.DataDir); ok && marked > code {
		return MigrateResult{}, &DowngradeError{Have: marked, Want: code}
	}
	applied, err := AppliedSchemaVersion(dbPath)
	if err != nil {
		return MigrateResult{}, err
	}
	if applied > code {
		return MigrateResult{}, &DowngradeError{Have: applied, Want: code}
	}
	var pending []int64
	for v := applied + 1; v <= code; v++ {
		pending = append(pending, v)
	}
	return MigrateResult{AppliedMax: applied, CodeMax: code, Pending: pending}, nil
}

// Migrate applies pending migrations offline. A newer on-disk schema
// refuses as a downgrade; a live server refuses as ErrDataDirLive. The
// data-dir lock is held for the whole migration, not merely probed, so
// no server can start under the files being migrated.
func Migrate(ctx context.Context, cfg config.Config) (MigrateResult, error) {
	res, err := PendingMigrations(cfg)
	if err != nil {
		return MigrateResult{}, err
	}
	if len(res.Pending) == 0 {
		return res, nil
	}
	lock, lerr := AcquireDataDirLock(cfg.DataDir)
	if lerr != nil {
		return MigrateResult{}, fmt.Errorf("%w: migrate needs the server stopped", ErrDataDirLive)
	}
	defer func() { _ = lock.Release() }()
	st, err := store.Open(ctx, store.Options{Path: cfg.Store.SQLite.Path})
	if err != nil {
		return MigrateResult{}, fmt.Errorf("sysops: migrate: %w", err)
	}
	if cerr := st.Close(ctx); cerr != nil {
		return MigrateResult{}, fmt.Errorf("sysops: close migrated store: %w", cerr)
	}
	now, err := AppliedSchemaVersion(cfg.Store.SQLite.Path)
	if err != nil {
		return MigrateResult{}, err
	}
	if merr := RefreshMetaVersion(cfg); merr != nil {
		return MigrateResult{}, merr
	}
	res.AppliedMax = now
	res.Pending = nil
	res.Applied = true
	return res, nil
}

// StatusReport is the read-only inspection surface. It never opens the
// store read-write, so it stays available during a downgrade refusal.
type StatusReport struct {
	DataDir       string
	SchemaApplied int64
	SchemaCode    int64
	LastBackupAt  int64
	BackupCount   int
	RepoEntries   int
	Downgrade     bool
	StoreReadable bool
}

// Status inspects versions, data-dir health, and backup recency.
func Status(_ context.Context, cfg config.Config) (StatusReport, error) {
	rep := StatusReport{DataDir: cfg.DataDir}
	applied, err := AppliedSchemaVersion(cfg.Store.SQLite.Path)
	if err != nil {
		return StatusReport{}, err
	}
	code, err := CodeSchemaVersion()
	if err != nil {
		return StatusReport{}, err
	}
	rep.SchemaApplied = applied
	rep.SchemaCode = code
	rep.Downgrade = applied > code
	if !rep.Downgrade {
		if _, serr := os.Stat(cfg.Store.SQLite.Path); serr != nil {
			if !os.IsNotExist(serr) {
				return StatusReport{}, fmt.Errorf("sysops: stat metadata store: %w", serr)
			}
			rep.StoreReadable = applied == 0
		} else {
			rep.StoreReadable = true
		}
	}
	backups := filepath.Join(cfg.DataDir, "backups")
	entries, derr := os.ReadDir(backups)
	if derr == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			var doc ManifestDoc
			if _, terr := toml.DecodeFile(filepath.Join(backups, e.Name(), ManifestName), &doc); terr != nil {
				continue
			}
			rep.BackupCount++
			if doc.CreatedAt > rep.LastBackupAt {
				rep.LastBackupAt = doc.CreatedAt
			}
		}
	} else if !os.IsNotExist(derr) {
		return StatusReport{}, fmt.Errorf("sysops: scan backups: %w", derr)
	}
	if rents, rerr := os.ReadDir(cfg.Ruk.RepoRoot); rerr == nil {
		rep.RepoEntries = len(rents)
	}
	return rep, nil
}

// ReconcileReport contrasts a backup manifest against the live repo
// root: vanished entries are orphaned rows in waiting, extras are
// post-boundary additions, mismatches changed under the manifest.
type ReconcileReport struct {
	Orphaned   []string
	Created    []string
	Mismatched []string
	Checked    int
}

// Reconcile compares manifestDir against the configured repo root and
// reports drift. Without a manifest it inventories the root: every
// entry counts as created, which reviewers confirm before serving.
func Reconcile(_ context.Context, cfg config.Config, manifestDir string) (ReconcileReport, error) {
	var rep ReconcileReport
	live := map[string]string{}
	_ = filepath.WalkDir(cfg.Ruk.RepoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(cfg.Ruk.RepoRoot, path)
		if rerr != nil {
			return nil
		}
		sum, _, herr := hashFile(path)
		if herr != nil {
			rep.Mismatched = append(rep.Mismatched, rel+": unreadable")
			return nil
		}
		live[rel] = sum
		return nil
	})
	if manifestDir == "" {
		for rel := range live {
			rep.Created = append(rep.Created, rel)
		}
		rep.Checked = len(live)
		return rep, nil
	}
	doc, err := ReadManifest(manifestDir)
	if err != nil {
		return ReconcileReport{}, err
	}
	want := map[string]ManifestFile{}
	for _, f := range doc.Files {
		rel, ok := strings.CutPrefix(f.Path, "repos/")
		if !ok {
			continue
		}
		want[rel] = f
	}
	for rel, sum := range live {
		rep.Checked++
		rec, ok := want[rel]
		if !ok {
			rep.Created = append(rep.Created, rel)
			continue
		}
		if rec.SHA256 != sum {
			rep.Mismatched = append(rep.Mismatched, rel)
		}
	}
	for rel := range want {
		if _, ok := live[rel]; !ok {
			rep.Orphaned = append(rep.Orphaned, rel)
		}
	}
	return rep, nil
}
