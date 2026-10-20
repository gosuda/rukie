package sysops

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/store"
)

// ManifestVersion identifies the K10 manifest layout.
const ManifestVersion = 1

// ManifestFile records one backed-up file by content hash.
type ManifestFile struct {
	Path   string `toml:"path"`
	SHA256 string `toml:"sha256"`
	Size   int64  `toml:"size"`
}

// ManifestDoc is the backup manifest. It records the secret key id so
// restore can refuse a foreign backup; it never carries key bytes.
type ManifestDoc struct {
	KeyID         string         `toml:"key_id"`
	Files         []ManifestFile `toml:"files"`
	Version       int            `toml:"version"`
	CreatedAt     int64          `toml:"created_at"`
	SchemaVersion int64          `toml:"schema_version"`
	IncludeRepos  bool           `toml:"include_repos"`
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

// Backup takes an online metadata snapshot plus a repo-tree copy into a
// fresh timestamped directory under outBase and writes the manifest.
// The secret key is hashed for its id only; no key byte enters the
// backup. The caller must hold an open store; Backup never opens one.
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
	dir, err := freshBackupDir(outBase)
	if err != nil {
		return "", err
	}
	placed := false
	defer func() {
		if !placed {
			_ = os.RemoveAll(dir)
		}
	}()
	if _, berr := st.Backup(ctx, filepath.Join(dir, "meta.sqlite")); berr != nil {
		return "", fmt.Errorf("sysops: snapshot metadata: %w", berr)
	}
	metaHash, metaSize, herr := hashFile(filepath.Join(dir, "meta.sqlite"))
	if herr != nil {
		return "", fmt.Errorf("sysops: hash metadata snapshot: %w", herr)
	}
	doc := ManifestDoc{
		Version:       ManifestVersion,
		CreatedAt:     time.Now().UnixMilli(),
		SchemaVersion: schema,
		KeyID:         KeyID(key),
		IncludeRepos:  true,
		Files:         []ManifestFile{{Path: "meta.sqlite", SHA256: metaHash, Size: metaSize}},
	}
	skip, _ := filepath.Abs(cfg.SecretKeyFile)
	repoFiles, rerr := copyRepoTree(cfg.Ruk.RepoRoot, filepath.Join(dir, "repos"), skip)
	if rerr != nil {
		return "", rerr
	}
	doc.Files = append(doc.Files, repoFiles...)
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
	var files []ManifestFile
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
		rec, cerr := copyFileHashed(path, filepath.Join(backupDst, rel), 0o600)
		if cerr != nil {
			return cerr
		}
		rec.Path = filepath.Join("repos", rel)
		files = append(files, rec)
		return nil
	}
	if err := filepath.WalkDir(repoRoot, walk); err != nil {
		return nil, fmt.Errorf("sysops: copy repo tree: %w", err)
	}
	slices.SortFunc(files, func(a, b ManifestFile) int { return cmp.Compare(a.Path, b.Path) })
	return files, nil
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
