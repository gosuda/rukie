package sysops

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gosuda/rukie/internal/config"
)

// MetaVersionName is the on-disk code-pinned schema marker beside the
// database. The schema_migrations table stays authoritative; the file
// is a fail-fast guard that needs no database open to read.
const MetaVersionName = "meta.version"

func metaVersionPath(dataDir string) string { return filepath.Join(dataDir, MetaVersionName) }

// ReadMetaVersion returns the marker version and whether a readable
// marker exists. Absent or malformed markers report false and leave
// the table check to decide.
func ReadMetaVersion(dataDir string) (int64, bool) {
	if dataDir == "" {
		return 0, false
	}
	raw, err := os.ReadFile(filepath.Clean(metaVersionPath(dataDir)))
	if err != nil {
		return 0, false
	}
	version, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || version < 0 {
		return 0, false
	}
	return version, true
}

// WriteMetaVersion records the code version after a successful
// migration. It never runs before the migrated transaction commits.
func WriteMetaVersion(dataDir string, version int64) error {
	if dataDir == "" {
		return fmt.Errorf("sysops: data_dir is required for the schema marker")
	}
	body := strconv.FormatInt(version, 10) + "\n"
	if err := os.WriteFile(metaVersionPath(dataDir), []byte(body), 0o600); err != nil {
		return fmt.Errorf("sysops: write schema marker: %w", err)
	}
	return nil
}

// RefreshMetaVersion records the currently applied schema version so a
// normal serve startup leaves the marker behind for later downgrade
// checks. Serve, migrate, and restore all refresh through here.
func RefreshMetaVersion(cfg config.Config) error {
	applied, err := AppliedSchemaVersion(cfg.Store.SQLite.Path)
	if err != nil {
		return err
	}
	return WriteMetaVersion(cfg.DataDir, applied)
}
