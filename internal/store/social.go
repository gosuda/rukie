package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const repoForkJobKind = "repo_fork"

type StarRow struct {
	RepoName  string
	OwnerName string
	UserName  string
	RepoID    int64
	UserID    int64
	StarredAt int64
}

type StarFilter struct {
	Cursor       string
	RepoID       int64
	UserID       int64
	ViewerUserID int64
	Limit        int
}

// SocialDefaultLimit is the page size the social listings (stars, forks, and
// explore) use when the caller passes no limit. The API frontier loops
// budget refills against the same constant so token-filtered pages fill to
// the size an unfiltered page would have.
const SocialDefaultLimit = 30

// SocialMaxLimit caps the social listing page size.
const SocialMaxLimit = 100

func socialLimit(limit int) int {
	if limit <= 0 {
		return SocialDefaultLimit
	}
	if limit > SocialMaxLimit {
		return SocialMaxLimit
	}
	return limit
}

// StarUserPageCursor seals the continuation for a visible user-star page.
// The cursor carries only sort keys, so deriving it from the last visible
// row keeps hidden rows out of the continuation.
func StarUserPageCursor(row StarRow) string {
	return starPageCursor(row.StarredAt, row.RepoID)
}

// StarRepoPageCursor seals the continuation for a visible repository-star
// page. The cursor carries only sort keys, so deriving it from the last
// visible row keeps hidden rows out of the continuation.
func StarRepoPageCursor(row StarRow) string {
	return starPageCursor(row.StarredAt, row.UserID)
}

func starPageCursor(starredAt, tie int64) string {
	return makeSocialCursor(starredAt, tie)
}

// ExplorePageCursor seals the continuation for a visible explore page. The
// name sort embeds the repository name, so deriving the cursor from the last
// visible row is what keeps hidden repository names out of continuations. An
// empty sort reads as the store's default updated sort.
func ExplorePageCursor(sort string, row RepoRow) string {
	if sort == "name" {
		return makeSocialNameCursor(strings.ToLower(row.Name), row.ID)
	}
	value := row.UpdatedAt
	switch sort {
	case "created":
		value = row.CreatedAt
	case "stars":
		value = row.StarCount
	}
	return makeSocialCursor(value, row.ID)
}

func socialCursor(raw string) (int64, int64, error) {
	if raw == "" {
		return 0, 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, 0, fmt.Errorf("socialCursor: %w: %w", ErrInvalid, err)
	}
	parts := strings.Split(string(decoded), "\x1f")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("socialCursor: %w", ErrInvalid)
	}
	first, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || first < 0 {
		return 0, 0, fmt.Errorf("socialCursor: %w", ErrInvalid)
	}
	second, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || second < 0 {
		return 0, 0, fmt.Errorf("socialCursor: %w", ErrInvalid)
	}
	return first, second, nil
}

func makeSocialCursor(first, second int64) string {
	raw := fmt.Sprintf("%d\x1f%d", first, second)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func socialNameCursor(raw string) (string, int64, error) {
	if raw == "" {
		return "", 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", 0, fmt.Errorf("socialNameCursor: %w", ErrInvalid)
	}
	parts := strings.Split(string(decoded), "\x1f")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("socialNameCursor: %w", ErrInvalid)
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id < 0 {
		return "", 0, fmt.Errorf("socialNameCursor: %w", ErrInvalid)
	}
	return parts[0], id, nil
}

func makeSocialNameCursor(name string, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s\x1f%d", name, id)))
}

func (tx *WriteTx) AddStar(repoID, userID int64, at time.Time) error {
	inner, err := tx.live()
	if err != nil {
		return fmt.Errorf("AddStar: %w", err)
	}
	when := msOf(at)
	if repoID <= 0 || userID <= 0 || when <= 0 {
		return fmt.Errorf("AddStar: %w", ErrInvalid)
	}
	var live int
	if serr := inner.QueryRowContext(tx.ctx, `SELECT 1 FROM repos WHERE id = ? AND deleted_at IS NULL`, repoID).Scan(&live); errors.Is(serr, sql.ErrNoRows) {
		return fmt.Errorf("AddStar: %w: repo %d", ErrNotFound, repoID)
	} else if serr != nil {
		return fmt.Errorf("AddStar: %w", serr)
	}
	res, err := inner.ExecContext(tx.ctx, `INSERT INTO stars(user_id, repo_id, starred_at) VALUES (?, ?, ?) ON CONFLICT(user_id, repo_id) DO NOTHING`, userID, repoID, when)
	if err != nil {
		return fmt.Errorf("AddStar: %w", mapErr(err))
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("AddStar: %w", err)
	}
	if affected == 0 {
		return nil
	}
	_, err = inner.ExecContext(tx.ctx, `UPDATE repos SET star_count = star_count + 1 WHERE id = ? AND deleted_at IS NULL`, repoID)
	if err != nil {
		return fmt.Errorf("AddStar: %w", mapErr(err))
	}
	return nil
}

func (tx *WriteTx) RemoveStar(repoID, userID int64) error {
	inner, err := tx.live()
	if err != nil {
		return fmt.Errorf("RemoveStar: %w", err)
	}
	if repoID <= 0 || userID <= 0 {
		return fmt.Errorf("RemoveStar: %w", ErrInvalid)
	}
	res, err := inner.ExecContext(tx.ctx, `DELETE FROM stars WHERE user_id = ? AND repo_id = ?`, userID, repoID)
	if err != nil {
		return fmt.Errorf("RemoveStar: %w", mapErr(err))
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("RemoveStar: %w", err)
	}
	if affected == 0 {
		return nil
	}
	_, err = inner.ExecContext(tx.ctx, `UPDATE repos SET star_count = CASE WHEN star_count > 0 THEN star_count - 1 ELSE 0 END WHERE id = ?`, repoID)
	if err != nil {
		return fmt.Errorf("RemoveStar: %w", mapErr(err))
	}
	return nil
}

const visibleRepoPredicate = `(r.visibility = 'public' OR (r.visibility = 'internal' AND ? > 0) OR r.owner_user_id = ? OR EXISTS (SELECT 1 FROM repo_grants g WHERE g.repo_id = r.id AND g.user_id = ?) OR EXISTS (SELECT 1 FROM teams t JOIN team_members m ON m.team_id = t.id WHERE m.user_id = ? AND t.org_id = r.org_id AND (t.repo_scope = 'all' OR EXISTS (SELECT 1 FROM team_repos tr WHERE tr.team_id = t.id AND tr.repo_id = r.id))))`

func (tx *ReadTx) ListStars(filter StarFilter) ([]StarRow, string, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, "", fmt.Errorf("ListStars: %w", err)
	}
	if (filter.RepoID <= 0) == (filter.UserID <= 0) {
		return nil, "", fmt.Errorf("ListStars: %w", ErrInvalid)
	}
	limit := socialLimit(filter.Limit)
	cursorValue, cursorID, err := socialCursor(filter.Cursor)
	if err != nil {
		return nil, "", err
	}
	key := "s.user_id"
	if filter.UserID > 0 {
		key = "s.repo_id"
	}
	query := `SELECT s.repo_id, s.user_id, r.name, COALESCE(uo.name, o.name, ''), u.name, s.starred_at FROM stars s JOIN repos r ON r.id = s.repo_id LEFT JOIN users uo ON uo.id = r.owner_user_id LEFT JOIN orgs o ON o.id = r.org_id JOIN users u ON u.id = s.user_id WHERE r.deleted_at IS NULL AND ` + visibleRepoPredicate
	args := []any{filter.ViewerUserID, filter.ViewerUserID, filter.ViewerUserID, filter.ViewerUserID}
	if filter.RepoID > 0 {
		query += ` AND s.repo_id = ?`
		args = append(args, filter.RepoID)
	} else {
		query += ` AND s.user_id = ?`
		args = append(args, filter.UserID)
	}
	if filter.Cursor != "" {
		query += ` AND (s.starred_at, ` + key + `) < (?, ?)`
		args = append(args, cursorValue, cursorID)
	}
	query += ` ORDER BY s.starred_at DESC, ` + key + ` DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := inner.QueryContext(tx.ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("ListStars: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]StarRow, 0, limit+1)
	for rows.Next() {
		var row StarRow
		if err := rows.Scan(&row.RepoID, &row.UserID, &row.RepoName, &row.OwnerName, &row.UserName, &row.StarredAt); err != nil {
			return nil, "", fmt.Errorf("ListStars: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("ListStars: %w", err)
	}
	next := ""
	if len(out) > limit {
		last := out[limit-1]
		out = out[:limit]
		if filter.UserID > 0 {
			next = StarUserPageCursor(last)
		} else {
			next = StarRepoPageCursor(last)
		}
	}
	return out, next, nil
}

type ForkSpec struct {
	Job          JobSpec
	Repo         RepoInsert
	SourceRepoID int64
}

func (tx *WriteTx) CreateFork(spec ForkSpec, now time.Time) (int64, error) {
	inner, err := tx.live()
	if err != nil {
		return 0, fmt.Errorf("CreateFork: %w", err)
	}
	if spec.SourceRepoID <= 0 || spec.Job.ID == "" || spec.Job.Payload == "" {
		return 0, fmt.Errorf("CreateFork: %w", ErrInvalid)
	}
	var sourceVisibility, sourceState string
	var sourceRoot int64
	serr := inner.QueryRowContext(tx.ctx, `SELECT visibility, state, COALESCE(fork_root_repo_id, id) FROM repos WHERE id = ? AND deleted_at IS NULL`, spec.SourceRepoID).Scan(&sourceVisibility, &sourceState, &sourceRoot)
	if errors.Is(serr, sql.ErrNoRows) {
		return 0, fmt.Errorf("CreateFork: %w", ErrNotFound)
	} else if serr != nil {
		return 0, fmt.Errorf("CreateFork: %w", serr)
	}
	if sourceState != "active" {
		return 0, fmt.Errorf("CreateFork: %w: source is not active", ErrInvalid)
	}
	switch spec.Repo.Visibility {
	case "public", "internal", "private":
	default:
		return 0, fmt.Errorf("CreateFork: %w: visibility", ErrInvalid)
	}
	if (sourceVisibility == "private" && spec.Repo.Visibility != "private") || (sourceVisibility == "internal" && spec.Repo.Visibility == "public") {
		return 0, fmt.Errorf("CreateFork: %w: visibility", ErrInvalid)
	}
	if spec.Job.Kind != "" && spec.Job.Kind != repoForkJobKind {
		return 0, fmt.Errorf("CreateFork: %w: job kind", ErrInvalid)
	}
	spec.Job.Kind = repoForkJobKind
	at := msOf(now)
	if at <= 0 {
		return 0, fmt.Errorf("CreateFork: %w", ErrInvalid)
	}
	topics := spec.Repo.Topics
	if topics == "" {
		topics = "[]"
	}
	var id int64
	if ierr := inner.QueryRowContext(tx.ctx, `INSERT INTO repos(owner_user_id, org_id, name, visibility, description, website, topics, default_branch, archived, deleted_at, purge_after, ruk_path, created_at, updated_at, fork_of_repo_id, fork_root_repo_id, state) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, NULL, NULL, ?, ?, ?, ?, ?, 'forking') RETURNING id`, spec.Repo.OwnerUserID, spec.Repo.OrgID, spec.Repo.Name, spec.Repo.Visibility, spec.Repo.Description, spec.Repo.Website, topics, spec.Repo.DefaultBranch, spec.Repo.RukPath, at, at, spec.SourceRepoID, sourceRoot).Scan(&id); ierr != nil {
		return 0, fmt.Errorf("CreateFork: %w", mapErr(ierr))
	}
	if _, uerr := inner.ExecContext(tx.ctx, `UPDATE repos SET forks_count = forks_count + 1 WHERE id = ?`, spec.SourceRepoID); uerr != nil {
		return 0, fmt.Errorf("CreateFork: %w", mapErr(uerr))
	}
	spec.Job.RepoID = id
	spec.Job.At = now
	if jerr := checkJobSpec(spec.Job); jerr != nil {
		return 0, jerr
	}
	moved, merr := execAffected(tx.ctx, inner, sqlJobInsert, spec.Job.ID, spec.Job.Kind, spec.Job.RepoID, spec.Job.Payload, at)
	if merr != nil {
		return 0, fmt.Errorf("CreateFork: %w", mapErr(merr))
	}
	if moved == 0 {
		return 0, fmt.Errorf("CreateFork: %w: job id is already taken", ErrConflict)
	}
	return id, nil
}

func (tx *WriteTx) ClaimForkJob(repoID int64, jobID, owner, token string, now time.Time, lease time.Duration) error {
	inner, err := tx.live()
	if err != nil {
		return fmt.Errorf("ClaimForkJob: %w", err)
	}
	at := msOf(now)
	if repoID <= 0 || jobID == "" || owner == "" || token == "" || at <= 0 || lease <= 0 {
		return fmt.Errorf("ClaimForkJob: %w", ErrInvalid)
	}
	moved, err := execAffected(tx.ctx, inner, `UPDATE jobs SET state = 'leased', lease_owner = ?, lease_token = ?, lease_expires_at = ?, updated_at = ? WHERE id = ? AND repo_id = ? AND kind = ? AND state = 'queued'`, owner, token, msOf(now.Add(lease)), at, jobID, repoID, repoForkJobKind)
	if err != nil {
		return fmt.Errorf("ClaimForkJob: %w", mapErr(err))
	}
	if moved == 0 {
		return fmt.Errorf("ClaimForkJob: %w", ErrStale)
	}
	return nil
}
func (tx *WriteTx) CompleteFork(repoID int64, owner, jobID, token string, now time.Time) error {
	inner, err := tx.live()
	if err != nil {
		return fmt.Errorf("CompleteFork: %w", err)
	}
	at := msOf(now)
	if repoID <= 0 || owner == "" || jobID == "" || token == "" || at <= 0 {
		return fmt.Errorf("CompleteFork: %w", ErrInvalid)
	}
	var jobRepo int64
	jerr := inner.QueryRowContext(tx.ctx, `SELECT repo_id FROM jobs WHERE id = ? AND kind = ? AND state = 'leased' AND lease_owner = ? AND lease_token = ?`, jobID, repoForkJobKind, owner, token).Scan(&jobRepo)
	if errors.Is(jerr, sql.ErrNoRows) {
		return fmt.Errorf("CompleteFork: %w", ErrStale)
	} else if jerr != nil {
		return fmt.Errorf("CompleteFork: %w", jerr)
	}
	if jobRepo != repoID {
		return fmt.Errorf("CompleteFork: %w", ErrStale)
	}
	var repoState string
	var forkOf sql.NullInt64
	if serr := inner.QueryRowContext(tx.ctx, `SELECT state, fork_of_repo_id FROM repos WHERE id = ? AND deleted_at IS NULL`, repoID).Scan(&repoState, &forkOf); errors.Is(serr, sql.ErrNoRows) {
		return fmt.Errorf("CompleteFork: %w", ErrNotFound)
	} else if serr != nil {
		return fmt.Errorf("CompleteFork: %w", serr)
	}
	if !forkOf.Valid {
		return fmt.Errorf("CompleteFork: %w", ErrNotFound)
	}
	if repoState != "forking" && repoState != "active" {
		return fmt.Errorf("CompleteFork: %w", ErrNotFound)
	}
	if repoState == "forking" {
		fmoved, ferr := execAffected(tx.ctx, inner, `UPDATE repos SET state = 'active', updated_at = ? WHERE id = ? AND state = 'forking' AND fork_of_repo_id IS NOT NULL AND deleted_at IS NULL`, at, repoID)
		if ferr != nil {
			return fmt.Errorf("CompleteFork: %w", mapErr(ferr))
		}
		if fmoved == 0 {
			return fmt.Errorf("CompleteFork: %w", ErrNotFound)
		}
	}
	moved, err := execAffected(tx.ctx, inner, sqlJobDone, at, jobID, owner, token)
	if err != nil {
		return fmt.Errorf("CompleteFork: %w", mapErr(err))
	}
	if moved == 0 {
		return fmt.Errorf("CompleteFork: %w", ErrStale)
	}
	return nil
}

func (tx *ReadTx) IsStarred(ctx context.Context, userID, repoID int64) (bool, error) {
	inner, err := tx.live()
	if err != nil {
		return false, fmt.Errorf("IsStarred: %w", err)
	}
	var one int
	err = inner.QueryRowContext(ctx, `SELECT 1 FROM stars WHERE user_id = ? AND repo_id = ?`, userID, repoID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("IsStarred: %w", err)
	}
	return true, nil
}

func (tx *ReadTx) ListStarsByUser(_ context.Context, userID, viewerID int64, limit int, cursor string) ([]StarRow, string, error) {
	return tx.ListStars(StarFilter{UserID: userID, ViewerUserID: viewerID, Limit: limit, Cursor: cursor})
}

func (s *Store) ListStarsForRepo(ctx context.Context, repoID, viewerUserID int64, limit int, cursor string) ([]StarRow, string, error) {
	var rows []StarRow
	var next string
	err := s.Read(ctx, func(tx *ReadTx) error {
		var err error
		rows, next, err = tx.ListStars(StarFilter{RepoID: repoID, ViewerUserID: viewerUserID, Limit: limit, Cursor: cursor})
		return err
	})
	return rows, next, err
}

const ownedRepoPredicate = `(repos.visibility = 'public' OR (repos.visibility = 'internal' AND ? > 0) OR repos.owner_user_id = ? OR EXISTS (SELECT 1 FROM repo_grants g WHERE g.repo_id = repos.id AND g.user_id = ?) OR EXISTS (SELECT 1 FROM teams t JOIN team_members m ON m.team_id = t.id WHERE m.user_id = ? AND t.org_id = repos.org_id AND (t.repo_scope = 'all' OR EXISTS (SELECT 1 FROM team_repos tr WHERE tr.team_id = t.id AND tr.repo_id = repos.id))))`

func ownedRepoArgs(viewer int64) []any {
	return []any{viewer, viewer, viewer, viewer}
}

func (tx *ReadTx) ListForks(sourceRepoID int64, viewerUserID int64, cursor string, limit int) ([]RepoRow, string, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, "", fmt.Errorf("ListForks: %w", err)
	}
	if sourceRepoID <= 0 {
		return nil, "", fmt.Errorf("ListForks: %w", ErrInvalid)
	}
	at, id, err := socialCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	query := `SELECT ` + sqlRepoColumns + ` FROM repos WHERE fork_of_repo_id = ? AND deleted_at IS NULL`
	args := []any{sourceRepoID}
	if viewerUserID <= 0 {
		query += ` AND repos.visibility = 'public'`
	} else {
		query += ` AND ` + ownedRepoPredicate
		args = append(args, ownedRepoArgs(viewerUserID)...)
	}
	if cursor != "" {
		query += ` AND (repos.created_at, repos.id) < (?, ?)`
		args = append(args, at, id)
	}
	lim := socialLimit(limit)
	query += ` ORDER BY repos.created_at DESC, repos.id DESC LIMIT ?`
	args = append(args, lim+1)
	rows, err := inner.QueryContext(tx.ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("ListForks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]RepoRow, 0, lim+1)
	for rows.Next() {
		row, err := scanRepoRow(rows.Scan)
		if err != nil {
			return nil, "", fmt.Errorf("ListForks: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("ListForks: %w", err)
	}
	next := ""
	if len(out) > lim {
		last := out[lim-1]
		out = out[:lim]
		next = makeSocialCursor(last.CreatedAt, last.ID)
	}
	return out, next, nil
}

type ExploreRepoFilter struct {
	Sort         string
	Topic        string
	Cursor       string
	ViewerUserID int64
	Limit        int
}

type ExploreUserRow struct {
	Name      string
	FullName  string
	ID        int64
	CreatedAt int64
}

type ExploreOrgRow struct {
	OrgRow
}

func (tx *ReadTx) ExploreRepos(filter ExploreRepoFilter) ([]RepoRow, string, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, "", fmt.Errorf("ExploreRepos: %w", err)
	}
	lim := socialLimit(filter.Limit)
	sort := filter.Sort
	if sort == "" {
		sort = "updated"
	}
	switch sort {
	case "updated", "created", "stars", "name":
	default:
		return nil, "", fmt.Errorf("ExploreRepos: %w: sort", ErrInvalid)
	}
	query := `SELECT ` + sqlRepoColumns + ` FROM repos WHERE deleted_at IS NULL AND ` + ownedRepoPredicate
	args := ownedRepoArgs(filter.ViewerUserID)
	if filter.Topic != "" {
		query += ` AND EXISTS (SELECT 1 FROM json_each(repos.topics) WHERE value = ?)`
		args = append(args, filter.Topic)
	}
	if filter.Cursor != "" {
		if sort == "name" {
			name, id, cerr := socialNameCursor(filter.Cursor)
			if cerr != nil {
				return nil, "", cerr
			}
			query += ` AND (repos.name COLLATE NOCASE, repos.id) > (?, ?)`
			args = append(args, name, id)
		} else {
			value, id, verr := socialCursor(filter.Cursor)
			if verr != nil {
				return nil, "", verr
			}
			switch sort {
			case "created":
				query += ` AND (repos.created_at, repos.id) < (?, ?)`
			case "stars":
				query += ` AND (repos.star_count, repos.id) < (?, ?)`
			default:
				query += ` AND (repos.updated_at, repos.id) < (?, ?)`
			}
			args = append(args, value, id)
		}
	}
	switch sort {
	case "created":
		query += ` ORDER BY repos.created_at DESC, repos.id DESC`
	case "stars":
		query += ` ORDER BY repos.star_count DESC, repos.id DESC`
	case "name":
		query += ` ORDER BY repos.name COLLATE NOCASE ASC, repos.id ASC`
	default:
		query += ` ORDER BY repos.updated_at DESC, repos.id DESC`
	}
	query += ` LIMIT ?`
	args = append(args, lim+1)
	rows, qerr := inner.QueryContext(tx.ctx, query, args...)
	if qerr != nil {
		return nil, "", fmt.Errorf("ExploreRepos: %w", qerr)
	}
	defer func() { _ = rows.Close() }()
	out := make([]RepoRow, 0, lim+1)
	for rows.Next() {
		row, serr := scanRepoRow(rows.Scan)
		if serr != nil {
			return nil, "", fmt.Errorf("ExploreRepos: %w", serr)
		}
		out = append(out, row)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, "", fmt.Errorf("ExploreRepos: %w", rerr)
	}
	next := ""
	if len(out) > lim {
		last := out[lim-1]
		out = out[:lim]
		next = ExplorePageCursor(sort, last)
	}
	return out, next, nil
}

func (tx *ReadTx) ExploreUsers(cursor string, limit int, sortOpt ...string) ([]ExploreUserRow, string, error) {
	sort := "name"
	if len(sortOpt) > 0 && sortOpt[0] != "" {
		sort = sortOpt[0]
	}
	inner, err := tx.live()
	if err != nil {
		return nil, "", fmt.Errorf("ExploreUsers: %w", err)
	}
	if sort != "name" && sort != "created" {
		return nil, "", fmt.Errorf("ExploreUsers: %w", ErrInvalid)
	}
	lim := socialLimit(limit)
	query := `SELECT id, name, full_name, created_at FROM users WHERE is_active = 1`
	args := []any{}
	if cursor != "" {
		if sort == "name" {
			name, id, cerr := socialNameCursor(cursor)
			if cerr != nil {
				return nil, "", cerr
			}
			query += ` AND (lower_name, id) > (?, ?)`
			args = append(args, name, id)
		} else {
			at, id, verr := socialCursor(cursor)
			if verr != nil {
				return nil, "", verr
			}
			query += ` AND (created_at, id) < (?, ?)`
			args = append(args, at, id)
		}
	}
	if sort == "name" {
		query += ` ORDER BY lower_name ASC, id ASC`
	} else {
		query += ` ORDER BY created_at DESC, id DESC`
	}
	query += ` LIMIT ?`
	args = append(args, lim+1)
	rows, qerr := inner.QueryContext(tx.ctx, query, args...)
	if qerr != nil {
		return nil, "", fmt.Errorf("ExploreUsers: %w", qerr)
	}
	defer func() { _ = rows.Close() }()
	out := []ExploreUserRow{}
	for rows.Next() {
		var row ExploreUserRow
		if serr := rows.Scan(&row.ID, &row.Name, &row.FullName, &row.CreatedAt); serr != nil {
			return nil, "", fmt.Errorf("ExploreUsers: %w", serr)
		}
		out = append(out, row)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, "", fmt.Errorf("ExploreUsers: %w", rerr)
	}
	next := ""
	if len(out) > lim {
		last := out[lim-1]
		out = out[:lim]
		if sort == "name" {
			next = makeSocialNameCursor(strings.ToLower(last.Name), last.ID)
		} else {
			next = makeSocialCursor(last.CreatedAt, last.ID)
		}
	}
	return out, next, nil
}

func (tx *ReadTx) ExploreOrgs(cursor string, limit int, sortOpt ...string) ([]OrgRow, string, error) {
	sort := "name"
	if len(sortOpt) > 0 && sortOpt[0] != "" {
		sort = sortOpt[0]
	}
	inner, err := tx.live()
	if err != nil {
		return nil, "", fmt.Errorf("ExploreOrgs: %w", err)
	}
	if sort != "name" && sort != "created" {
		return nil, "", fmt.Errorf("ExploreOrgs: %w", ErrInvalid)
	}
	lim := socialLimit(limit)
	query := `SELECT ` + sqlOrgColumns + ` FROM orgs WHERE visibility = 'public'`
	args := []any{}
	if cursor != "" {
		if sort == "name" {
			name, id, cerr := socialNameCursor(cursor)
			if cerr != nil {
				return nil, "", cerr
			}
			query += ` AND (lower_name, id) > (?, ?)`
			args = append(args, name, id)
		} else {
			at, id, verr := socialCursor(cursor)
			if verr != nil {
				return nil, "", verr
			}
			query += ` AND (created_at, id) < (?, ?)`
			args = append(args, at, id)
		}
	}
	if sort == "name" {
		query += ` ORDER BY lower_name ASC, id ASC`
	} else {
		query += ` ORDER BY created_at DESC, id DESC`
	}
	query += ` LIMIT ?`
	args = append(args, lim+1)
	rows, qerr := inner.QueryContext(tx.ctx, query, args...)
	if qerr != nil {
		return nil, "", fmt.Errorf("ExploreOrgs: %w", qerr)
	}
	defer func() { _ = rows.Close() }()
	out := []OrgRow{}
	for rows.Next() {
		row, serr := scanOrg(rows.Scan)
		if serr != nil {
			return nil, "", fmt.Errorf("ExploreOrgs: %w", serr)
		}
		out = append(out, row)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, "", fmt.Errorf("ExploreOrgs: %w", rerr)
	}
	next := ""
	if len(out) > lim {
		last := out[lim-1]
		out = out[:lim]
		if sort == "name" {
			next = makeSocialNameCursor(strings.ToLower(last.LowerName), last.ID)
		} else {
			next = makeSocialCursor(last.CreatedAt, last.ID)
		}
	}
	return out, next, nil
}
