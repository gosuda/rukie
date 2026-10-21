package sysops

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/store"
)

const barrierHoldSLO = 60 * time.Second

// ManifestVersion identifies the K10 manifest layout.
const ManifestVersion = 1

// ManifestFile records one backed-up file by content hash.
type ManifestFile struct {
	Path   string `toml:"path"`
	SHA256 string `toml:"sha256"`
	Size   int64  `toml:"size"`
}

// ManifestDoc is the backup manifest. It records the secret key id so
// restore can refuse a foreign backup; it never carries key bytes. The
// barrier fields prove t_M and t_R are one linearizable point per
// ops-design §5.2: restore refuses a manifest without barrier_id unless
// forced. Positions carries per-repo op-log tails for forward replay;
// it stays empty until the op.log wrapper lands (F2) and reconcile
// compares content hashes meanwhile.
type ManifestDoc struct {
	Positions      map[string]uint64 `toml:"positions"`
	BarrierID      string            `toml:"barrier_id"`
	SnapshotSource string            `toml:"snapshot_source"`
	KeyID          string            `toml:"key_id"`
	Files          []ManifestFile    `toml:"files"`
	Version        int               `toml:"version"`
	CreatedAt      int64             `toml:"created_at"`
	SchemaVersion  int64             `toml:"schema_version"`
	TM             int64             `toml:"t_m"`
	TR             int64             `toml:"t_r"`
	BarrierHoldMs  int64             `toml:"barrier_hold_ms"`
	IncludeRepos   bool              `toml:"include_repos"`
}

// ManifestName is the manifest file name inside a backup directory.
const ManifestName = "manifest.toml"

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func copyFileHashed(src, dst string, mode os.FileMode) (ManifestFile, error) {
	in, err := os.Open(filepath.Clean(src))
	if err != nil {
		return ManifestFile{}, err
	}
	defer func() { _ = in.Close() }()
	if merr := os.MkdirAll(filepath.Dir(dst), 0o700); merr != nil {
		return ManifestFile{}, merr
	}
	out, err := os.OpenFile(filepath.Clean(dst), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return ManifestFile{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	serr := out.Sync()
	cerr := out.Close()
	if err != nil {
		return ManifestFile{}, err
	}
	if serr != nil {
		return ManifestFile{}, serr
	}
	if cerr != nil {
		return ManifestFile{}, cerr
	}
	return ManifestFile{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}

// Backup takes a paired snapshot (meta@t_M, ruk@t_R) into a fresh
// timestamped directory under outBase and writes the manifest. The
// secret key is hashed for its id only; no key byte enters the backup.
// The caller must hold an open store; Backup never opens one.
//
// Consistency (ops-design §5.2): while a server is live, Backup holds
// the step-0 write barrier from before the metadata snapshot until the
// repo copy verifies, so t_M and t_R denote one linearizable point. New
// mutations get 503 backup/in-progress during the hold, and taking the
// barrier waits out in-flight ones up to 60 s, then aborts. Ruk's store
// appends to segment tails in place, so no hardlink or copy trick
// snapshots it: the copy reads repos/ only while quiesced under the
// barrier, refs first so any interleaving stays a valid repo, and a
// post-copy mtime sweep aborts on any write that slipped through.
// Offline (no server lock) the copy is trivially stable and records
// snapshot_source B.
func Backup(ctx context.Context, st *store.Store, cfg config.Config, outBase string) (string, error) {
	if outBase == "" {
		return "", fmt.Errorf("sysops: backup output dir is required")
	}
	key, err := LoadSecretKey(cfg.SecretKeyFile)
	if err != nil {
		return "", err
	}
	schema, err := AppliedSchemaVersion(cfg.Store.SQLite.Path)
	if err != nil {
		return "", err
	}
	var (
		barrierID      string
		snapshotSource = "B"
		barrierHoldMs  int64
		barrier        *Barrier
		offlineLock    *DirLock
	)
	switch lock, lerr := AcquireDataDirLock(cfg.DataDir); {
	case lerr == nil:
		barrierID = ulidNow()
		offlineLock = lock
		defer func() { _ = offlineLock.Release() }()
	case errors.Is(lerr, ErrDataDirLive):
		b, berr := BeginBarrier(cfg.DataDir)
		if berr != nil {
			return "", berr
		}
		barrier = b
		barrierID = b.ID()
		snapshotSource = "barrier-held"
	default:
		return "", lerr
	}
	dir, err := freshBackupDir(outBase)
	if err != nil {
		if barrier != nil {
			_ = barrier.Close()
		}
		return "", err
	}
	placed := false
	defer func() {
		if !placed {
			_ = os.RemoveAll(dir)
		}
	}()
	release := func() {
		if barrier != nil {
			barrierHoldMs = barrier.HeldFor().Milliseconds()
			_ = barrier.Close()
			barrier = nil
		}
	}
	defer release()
	if _, berr := st.Backup(ctx, filepath.Join(dir, "meta.sqlite")); berr != nil {
		return "", fmt.Errorf("sysops: snapshot metadata: %w", berr)
	}
	tM := time.Now().UnixMilli()
	metaHash, metaSize, herr := hashFile(filepath.Join(dir, "meta.sqlite"))
	if herr != nil {
		return "", fmt.Errorf("sysops: hash metadata snapshot: %w", herr)
	}
	doc := ManifestDoc{
		BarrierID:      barrierID,
		SnapshotSource: snapshotSource,
		Version:        ManifestVersion,
		CreatedAt:      time.Now().UnixMilli(),
		SchemaVersion:  schema,
		TM:             tM,
		KeyID:          KeyID(key),
		IncludeRepos:   true,
		Positions:      map[string]uint64{},
		Files:          []ManifestFile{{Path: "meta.sqlite", SHA256: metaHash, Size: metaSize}},
	}
	skip, _ := filepath.Abs(cfg.SecretKeyFile)
	copyStart := time.Now()
	repoFiles, rerr := copyRepoTree(cfg.Ruk.RepoRoot, filepath.Join(dir, "repos"), skip)
	if rerr != nil {
		return "", rerr
	}
	if barrier != nil {
		if verr := verifyNoWritesDuringCopy(cfg.Ruk.RepoRoot, copyStart); verr != nil {
			return "", verr
		}
	}
	doc.Files = append(doc.Files, repoFiles...)
	doc.TR = time.Now().UnixMilli()
	release()
	doc.BarrierHoldMs = barrierHoldMs
	if held := time.Duration(barrierHoldMs) * time.Millisecond; held > barrierHoldSLO {
		slog.Default().Warn("backup barrier hold exceeded its SLO",
			slog.String("hold", held.String()), slog.String("manifest", filepath.Join(dir, ManifestName)))
	}
	if merr := writeManifest(dir, doc); merr != nil {
		return "", merr
	}
	placed = true
	return filepath.Join(dir, ManifestName), nil
}

func freshBackupDir(outBase string) (string, error) {
	if err := os.MkdirAll(outBase, 0o700); err != nil {
		return "", fmt.Errorf("sysops: create backup base: %w", err)
	}
	base := time.Now().UTC().Format("20060102-150405")
	for i := 0; ; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		dir := filepath.Join(outBase, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", fmt.Errorf("sysops: create backup dir: %w", err)
		}
		return dir, nil
	}
}

func copyRepoTree(repoRoot, backupDst, excludedAbsPath string) ([]ManifestFile, error) {
	info, err := os.Stat(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("sysops: repo root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("sysops: repo root %s is not a directory", repoRoot)
	}
	var rels []string
	walk := func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		if abs, aerr := filepath.Abs(path); aerr == nil && abs == excludedAbsPath {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("sysops: repo entry %s is not a regular file", path)
		}
		rel, rerr := filepath.Rel(repoRoot, path)
		if rerr != nil {
			return rerr
		}
		rels = append(rels, rel)
		return nil
	}
	if err := filepath.WalkDir(repoRoot, walk); err != nil {
		return nil, fmt.Errorf("sysops: copy repo tree: %w", err)
	}
	slices.SortFunc(rels, func(a, b string) int {
		return cmp.Or(refFirstRank(a)-refFirstRank(b), cmp.Compare(a, b))
	})
	files := make([]ManifestFile, 0, len(rels))
	for _, rel := range rels {
		rec, cerr := copyFileHashed(filepath.Join(repoRoot, rel), filepath.Join(backupDst, rel), 0o600)
		if cerr != nil {
			return nil, cerr
		}
		rec.Path = filepath.Join("repos", rel)
		files = append(files, rec)
	}
	slices.SortFunc(files, func(a, b ManifestFile) int { return cmp.Compare(a.Path, b.Path) })
	return files, nil
}

func refFirstRank(rel string) int {
	base := filepath.Base(rel)
	if base == "HEAD" || base == "config" || base == "packed-refs" {
		return 0
	}
	for part := rel; ; {
		var head string
		head, part, _ = strings.Cut(part, string(filepath.Separator))
		if head == "refs" {
			return 0
		}
		if part == "" {
			break
		}
	}
	return 1
}

func verifyNoWritesDuringCopy(repoRoot string, copyStart time.Time) error {
	var changed []string
	walk := func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if !info.ModTime().Before(copyStart) {
			rel, _ := filepath.Rel(repoRoot, path)
			changed = append(changed, rel)
		}
		return nil
	}
	if err := filepath.WalkDir(repoRoot, walk); err != nil {
		return fmt.Errorf("sysops: sweep repo tree: %w", err)
	}
	if len(changed) > 0 {
		slices.Sort(changed)
		if len(changed) > 5 {
			changed = append(changed[:5], fmt.Sprintf("... and %d more", len(changed)-5))
		}
		return fmt.Errorf("sysops: repos changed under the barrier during copy (%s); aborting rather than installing a torn repo", strings.Join(changed, ", "))
	}
	return nil
}

func writeManifest(dir string, doc ManifestDoc) error {
	var b strings.Builder
	if err := toml.NewEncoder(&b).Encode(doc); err != nil {
		return fmt.Errorf("sysops: encode manifest: %w", err)
	}
	path := filepath.Join(dir, ManifestName)
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("sysops: write manifest: %w", err)
	}
	f, err := os.Open(filepath.Clean(dir))
	if err != nil {
		return fmt.Errorf("sysops: sync backup dir: %w", err)
	}
	serr := f.Sync()
	cerr := f.Close()
	if serr != nil {
		return fmt.Errorf("sysops: sync backup dir: %w", serr)
	}
	if cerr != nil {
		return fmt.Errorf("sysops: sync backup dir: %w", cerr)
	}
	return nil
}

// ReadManifest parses the manifest in backupDir.
func ReadManifest(backupDir string) (ManifestDoc, error) {
	var doc ManifestDoc
	path := filepath.Join(backupDir, ManifestName)
	if _, err := toml.DecodeFile(path, &doc); err != nil {
		return ManifestDoc{}, fmt.Errorf("sysops: read manifest: %w", err)
	}
	if doc.Version != ManifestVersion {
		return ManifestDoc{}, fmt.Errorf("sysops: manifest version %d, want %d", doc.Version, ManifestVersion)
	}
	if doc.KeyID == "" {
		return ManifestDoc{}, fmt.Errorf("sysops: manifest carries no key id; refusing a keyless backup")
	}
	return doc, nil
}

// VerifyManifest rehashes every listed file and refuses on any problem.
func VerifyManifest(backupDir string, doc ManifestDoc) error {
	if problems := InspectManifest(backupDir, doc); len(problems) > 0 {
		return fmt.Errorf("sysops: backup verification failed: %s", strings.Join(problems, "; "))
	}
	return nil
}

// InspectManifest returns one entry per unreadable or mismatched file,
// empty when the backup verifies.
func InspectManifest(backupDir string, doc ManifestDoc) []string {
	var problems []string
	for _, f := range doc.Files {
		sum, _, herr := hashFile(filepath.Join(backupDir, f.Path))
		if herr != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", f.Path, herr))
			continue
		}
		if sum != f.SHA256 {
			problems = append(problems, fmt.Sprintf("%s: hash mismatch", f.Path))
		}
	}
	return problems
}
