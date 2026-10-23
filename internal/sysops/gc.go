package sysops

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

// terminalJobRetention bounds the jobs table: done and dead rows older
// than this are pure history because retries re-enqueue under the same id.
const terminalJobRetention = 30 * 24 * time.Hour

// GCPending names one repo the collector could not finish and why.
type GCPending struct {
	Repo   string
	Reason string
}

// GCReport describes one metadata collection pass.
type GCReport struct {
	ReposPurged   []string
	ReposPending  []GCPending
	ReposFailed   []GCPending
	JobsDeleted   int64
	JobsWouldDrop int64
	DryRun        bool
}

// GC purges repository rows past their purge horizon and drops terminal
// jobs older than the retention window. Daemon removal goes through the
// daemon when its socket answers; rows whose daemon is unreachable are
// reported pending instead of half-purged. With dryRun nothing is written.
func GC(ctx context.Context, cfg config.Config, dryRun bool, repoFilter string) (GCReport, error) {
	rep := GCReport{DryRun: dryRun}
	lock, lerr := AcquireDataDirLock(cfg.DataDir)
	if lerr != nil {
		return GCReport{}, fmt.Errorf("%w: gc needs the server stopped", ErrDataDirLive)
	}
	defer func() { _ = lock.Release() }()
	st, err := store.Open(ctx, store.Options{Path: cfg.Store.SQLite.Path})
	if err != nil {
		return GCReport{}, fmt.Errorf("sysops: gc: %w", err)
	}
	defer func() { _ = st.Close(ctx) }()
	now := time.Now()
	var due []store.RepoRow
	if rerr := st.Read(ctx, func(tx *store.ReadTx) error {
		var lerr error
		due, lerr = tx.DuePurges(now)
		return lerr
	}); rerr != nil {
		return GCReport{}, fmt.Errorf("sysops: gc: %w", rerr)
	}
	if repoFilter != "" {
		due = filterGCRepos(ctx, st, due, repoFilter)
	}
	// Every daemon hop below is bounded: an unreachable daemon parks the
	// repo as pending instead of stalling the pass.
	var daemon *ruk.Client
	if !dryRun {
		if socket := cmp.Or(cfg.Ruk.Socket, cfg.Ruk.SocketPath); socket != "" {
			dctx, cancel := context.WithTimeoutCause(ctx, 10*time.Second, fmt.Errorf("sysops: gc daemon dial timed out"))
			defer cancel()
			if client, derr := ruk.Dial(dctx, socket); derr == nil {
				daemon = client
				defer func() { _ = client.Close() }()
			}
		}
	}
	cutoff := now.Add(-terminalJobRetention).UnixMilli()
	for _, row := range due {
		full, ferr := gcRepoFullName(ctx, st, row)
		if ferr != nil {
			rep.ReposFailed = append(rep.ReposFailed, GCPending{Repo: fmt.Sprintf("id=%d", row.ID), Reason: ferr.Error()})
			continue
		}
		if dryRun {
			rep.ReposPurged = append(rep.ReposPurged, full)
			continue
		}
		if row.RukPath != "" {
			if daemon == nil {
				rep.ReposPending = append(rep.ReposPending, GCPending{Repo: full, Reason: "daemon unreachable; metadata kept"})
				continue
			}
			// Bound below the dial timeout: a slow daemon parks the repo
			// as failed instead of stalling the pass.
			dctx, cancel := context.WithTimeoutCause(ctx, 30*time.Second, fmt.Errorf("sysops: gc repo %s timed out", full))
			repo, oerr := openGCDaemonRepo(dctx, daemon, cfg.Ruk.RepoRoot, row)
			if oerr == nil {
				oerr = callGCRepoDelete(dctx, daemon, repo, row.RukPath)
			}
			cancel()
			if oerr != nil {
				rep.ReposFailed = append(rep.ReposFailed, GCPending{Repo: full, Reason: oerr.Error()})
				continue
			}
		}
		jobID := fmt.Sprintf("repo-purge:%d", row.ID)
		if werr := st.Write(ctx, func(tx *store.WriteTx) error {
			if herr := tx.HardDeleteRepo(row.ID); herr != nil {
				return herr
			}
			_ = tx.DeleteJob(jobID)
			return nil
		}); werr != nil {
			rep.ReposFailed = append(rep.ReposFailed, GCPending{Repo: full, Reason: werr.Error()})
			continue
		}
		rep.ReposPurged = append(rep.ReposPurged, full)
	}
	if dryRun {
		n, cerr := st.CountTerminalJobs(ctx, cutoff)
		if cerr != nil {
			return GCReport{}, fmt.Errorf("sysops: gc: %w", cerr)
		}
		rep.JobsWouldDrop = n
		return rep, nil
	}
	n, perr := st.PruneTerminalJobs(ctx, cutoff)
	if perr != nil {
		return GCReport{}, fmt.Errorf("sysops: gc: %w", perr)
	}
	rep.JobsDeleted = n
	return rep, nil
}

func filterGCRepos(ctx context.Context, st *store.Store, due []store.RepoRow, filter string) []store.RepoRow {
	owner, name, found := splitOwnerRepo(filter)
	if !found {
		return nil
	}
	var out []store.RepoRow
	for _, row := range due {
		full, err := gcRepoFullName(ctx, st, row)
		if err != nil {
			continue
		}
		o, n, _ := splitOwnerRepo(full)
		if o == owner && n == name {
			out = append(out, row)
		}
	}
	return out
}

func splitOwnerRepo(full string) (string, string, bool) {
	if owner, name, found := strings.Cut(full, "/"); found && owner != "" && name != "" {
		return owner, name, true
	}
	return "", "", false
}

// gcRepoFullName resolves owner/name for a repo row without auth context.
func gcRepoFullName(ctx context.Context, st *store.Store, row store.RepoRow) (string, error) {
	if row.OwnerUserID.Valid && row.OwnerUserID.Int64 != 0 {
		meta, err := st.LookupUserByID(ctx, row.OwnerUserID.Int64)
		if err != nil {
			return "", err
		}
		return meta.Name + "/" + row.Name, nil
	}
	var name string
	if err := st.Read(ctx, func(tx *store.ReadTx) error {
		org, gerr := tx.GetOrg(row.OrgID)
		if gerr != nil {
			return gerr
		}
		name = org.Name
		return nil
	}); err != nil {
		return "", err
	}
	return name + "/" + row.Name, nil
}

// openGCDaemonRepo opens the daemon handle for a repo row like the server does.
func openGCDaemonRepo(ctx context.Context, client *ruk.Client, repoRoot string, row store.RepoRow) (ruk.Repo, error) {
	abs := row.RukPath
	if repoRoot != "" && !filepath.IsAbs(abs) {
		abs = filepath.Join(repoRoot, abs)
	}
	if !strings.HasSuffix(abs, ".ruk") {
		abs += "/.ruk"
	}
	return client.OpenRepo(ctx, abs)
}

// callGCRepoDelete removes the daemon repository with an explicit confirmation.
func callGCRepoDelete(ctx context.Context, client *ruk.Client, repo ruk.Repo, path string) error {
	_, err := ruk.Call[map[string]any, map[string]any](ctx, client, repo, wire.MethodRepoDelete,
		map[string]any{"repo": path, "confirm_path": path})
	return err
}
