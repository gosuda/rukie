package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ReleaseRow struct {
	CreatedAt     time.Time
	DeletedAt     time.Time
	PublishedAt   time.Time
	TagName       string
	TargetOID     string
	TargetKind    string
	Title         string
	NotesMarkdown string
	NotesHTML     string
	State         string
	PublicID      string
	ID            int64
	AuthorID      int64
	RepoID        int64
	Prerelease    bool
	Revision      int64
}

type AssetRow struct {
	CreatedAt   time.Time
	PublishedAt time.Time
	PublicID    string
	Name        string
	SHA256      string
	ContentType string
	State       string
	Path        string
	ID          int64
	ReleaseID   int64
	UploaderID  int64
	Size        int64
}

type ReleaseInsert struct {
	CreatedAt  time.Time
	PublicID   string
	Tag        string
	Title      string
	Notes      string
	RepoID     int64
	AuthorID   int64
	Prerelease bool
}

func scanReleaseRow(row *sql.Row) (ReleaseRow, error) {
	var r ReleaseRow
	var pre int
	var ca, pa, da int64
	err := row.Scan(&r.ID, &r.RepoID, &r.AuthorID, &r.PublicID, &r.TagName, &r.TargetOID, &r.TargetKind, &r.Title, &r.NotesMarkdown, &r.NotesHTML, &pre, &r.State, &ca, &pa, &da, &r.Revision)
	if err != nil {
		return r, fmt.Errorf("scanReleaseRow: %w", err)
	}
	r.Prerelease = pre != 0
	r.CreatedAt = time.Unix(ca, 0).UTC()
	if pa > 0 {
		r.PublishedAt = time.Unix(pa, 0).UTC()
	}
	if da > 0 {
		r.DeletedAt = time.Unix(da, 0).UTC()
	}
	return r, nil
}

func scanAssetRow(scanner interface {
	Scan(dest ...any) error
}) (AssetRow, error) {
	var a AssetRow
	var ca, pa int64
	err := scanner.Scan(&a.ID, &a.ReleaseID, &a.UploaderID, &a.PublicID, &a.Name, &a.SHA256, &a.Size, &a.ContentType, &a.State, &a.Path, &ca, &pa)
	if err != nil {
		return a, fmt.Errorf("scanAssetRow: %w", err)
	}
	a.CreatedAt = time.Unix(ca, 0).UTC()
	if pa > 0 {
		a.PublishedAt = time.Unix(pa, 0).UTC()
	}
	return a, nil
}

const releaseColumns = `id,repo_id,author_id,public_id,tag_name,target_oid,target_kind,title,notes_markdown,notes_html,is_prerelease,state,created_at,published_at,deleted_at,revision`

type ReleasePage struct {
	PublishedOnly bool
	Limit         int
	AfterCreated  int64
	AfterID       int64
}

func (tx *ReadTx) listReleases(repoID int64, page ReleasePage) ([]ReleaseRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, fmt.Errorf("listReleases: %w", err)
	}
	query := `SELECT ` + releaseColumns + ` FROM releases WHERE repo_id=? AND deleted_at=0`
	argv := []any{repoID}
	if page.PublishedOnly {
		query += ` AND state='published'`
	}
	if page.AfterCreated > 0 {
		query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		argv = append(argv, page.AfterCreated, page.AfterCreated, page.AfterID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	argv = append(argv, page.Limit)
	rows, err := inner.QueryContext(tx.ctx, query, argv...)
	if err != nil {
		return nil, fmt.Errorf("listReleases: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ReleaseRow
	for rows.Next() {
		var r ReleaseRow
		var pre int
		var ca, pa, da int64
		if serr := rows.Scan(&r.ID, &r.RepoID, &r.AuthorID, &r.PublicID, &r.TagName, &r.TargetOID, &r.TargetKind, &r.Title, &r.NotesMarkdown, &r.NotesHTML, &pre, &r.State, &ca, &pa, &da, &r.Revision); serr != nil {
			return nil, fmt.Errorf("listReleases: %w", serr)
		}
		r.Prerelease = pre != 0
		r.CreatedAt = time.Unix(ca, 0).UTC()
		if pa > 0 {
			r.PublishedAt = time.Unix(pa, 0).UTC()
		}
		if da > 0 {
			r.DeletedAt = time.Unix(da, 0).UTC()
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listReleases: %w", err)
	}
	return out, nil
}

func (tx *ReadTx) ListPublishedReleases(repoID int64, page ReleasePage) ([]ReleaseRow, error) {
	page.PublishedOnly = true
	rows, err := tx.listReleases(repoID, page)
	if err != nil {
		return nil, fmt.Errorf("ListPublishedReleases: %w", err)
	}
	return rows, nil
}

func (tx *ReadTx) ListReleasesIncludingDrafts(repoID int64, page ReleasePage) ([]ReleaseRow, error) {
	page.PublishedOnly = false
	rows, err := tx.listReleases(repoID, page)
	if err != nil {
		return nil, fmt.Errorf("ListReleasesIncludingDrafts: %w", err)
	}
	return rows, nil
}

func (tx *ReadTx) GetRelease(repoID int64, tag string) (ReleaseRow, error) {
	inner, err := tx.live()
	if err != nil {
		return ReleaseRow{}, fmt.Errorf("GetRelease: %w", err)
	}
	row, serr := scanReleaseRow(inner.QueryRowContext(tx.ctx, `SELECT `+releaseColumns+` FROM releases WHERE repo_id=? AND lower_tag_name=lower(?) AND deleted_at=0`, repoID, tag))
	if serr != nil {
		if errors.Is(serr, sql.ErrNoRows) {
			return ReleaseRow{}, fmt.Errorf("GetRelease: %w: repo %d tag %s", ErrNotFound, repoID, tag)
		}
		return ReleaseRow{}, fmt.Errorf("GetRelease: %w", serr)
	}
	return row, nil
}

func (tx *ReadTx) GetReleaseByID(repoID, id int64) (ReleaseRow, error) {
	inner, err := tx.live()
	if err != nil {
		return ReleaseRow{}, fmt.Errorf("GetReleaseByID: %w", err)
	}
	row, serr := scanReleaseRow(inner.QueryRowContext(tx.ctx, `SELECT `+releaseColumns+` FROM releases WHERE id=? AND repo_id=? AND deleted_at=0`, id, repoID))
	if serr != nil {
		if errors.Is(serr, sql.ErrNoRows) {
			return ReleaseRow{}, fmt.Errorf("GetReleaseByID: %w: release %d", ErrNotFound, id)
		}
		return ReleaseRow{}, fmt.Errorf("GetReleaseByID: %w", serr)
	}
	return row, nil
}

func (tx *WriteTx) InsertRelease(in ReleaseInsert) (int64, error) {
	inner, err := tx.live()
	if err != nil {
		return 0, fmt.Errorf("InsertRelease: %w", err)
	}
	pre := 0
	if in.Prerelease {
		pre = 1
	}
	res, err := inner.ExecContext(tx.ctx, `INSERT INTO releases(repo_id,author_id,public_id,tag_name,lower_tag_name,title,notes_markdown,state,is_prerelease,created_at) VALUES(?,?,?,?,lower(?),?,?,'draft',?,?)`, in.RepoID, in.AuthorID, in.PublicID, in.Tag, in.Tag, in.Title, in.Notes, pre, in.CreatedAt.Unix())
	if err != nil {
		return 0, fmt.Errorf("InsertRelease: %w", mapErr(err))
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("InsertRelease: %w", err)
	}
	return id, nil
}

func (tx *WriteTx) UpdateDraftRelease(id int64, title, notes string, prerelease bool) error {
	inner, err := tx.live()
	if err != nil {
		return fmt.Errorf("UpdateDraftRelease: %w", err)
	}
	pre := 0
	if prerelease {
		pre = 1
	}
	result, err := inner.ExecContext(tx.ctx, `UPDATE releases SET title=?,notes_markdown=?,is_prerelease=?,revision=revision+1 WHERE id=? AND state='draft' AND deleted_at=0`, title, notes, pre, id)
	if err != nil {
		return fmt.Errorf("UpdateDraftRelease: %w", err)
	}
	moved, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("UpdateDraftRelease: %w", err)
	}
	if moved != 1 {
		return fmt.Errorf("UpdateDraftRelease: %w: release %d", ErrNotFound, id)
	}
	return nil
}

func (tx *WriteTx) PublishRelease(id int64, target, kind, html string, now time.Time) error {
	inner, err := tx.live()
	if err != nil {
		return fmt.Errorf("PublishRelease: %w", err)
	}
	result, err := inner.ExecContext(tx.ctx, `UPDATE releases SET state='published',target_oid=?,target_kind=?,notes_html=?,published_at=?,revision=revision+1 WHERE id=? AND state='draft' AND deleted_at=0`, target, kind, html, now.Unix(), id)
	if err != nil {
		return fmt.Errorf("PublishRelease: %w", err)
	}
	moved, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("PublishRelease: %w", err)
	}
	if moved != 1 {
		return fmt.Errorf("PublishRelease: %w: release %d", ErrNotFound, id)
	}
	return nil
}

func (tx *WriteTx) HardDeleteRelease(id int64) error {
	inner, err := tx.live()
	if err != nil {
		return fmt.Errorf("HardDeleteRelease: %w", err)
	}
	result, err := inner.ExecContext(tx.ctx, `DELETE FROM releases WHERE id=? AND state='draft'`, id)
	if err != nil {
		return fmt.Errorf("HardDeleteRelease: %w", err)
	}
	moved, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("HardDeleteRelease: %w", err)
	}
	if moved != 1 {
		return fmt.Errorf("HardDeleteRelease: %w: release %d", ErrNotFound, id)
	}
	return nil
}

func (tx *WriteTx) DeleteReleaseAssets(releaseID int64) error {
	inner, err := tx.live()
	if err != nil {
		return fmt.Errorf("DeleteReleaseAssets: %w", err)
	}
	_, err = inner.ExecContext(tx.ctx, `DELETE FROM release_assets WHERE release_id=?`, releaseID)
	if err != nil {
		return fmt.Errorf("DeleteReleaseAssets: %w", err)
	}
	return nil
}

func (tx *WriteTx) DeleteRelease(id int64, now time.Time) error {
	inner, err := tx.live()
	if err != nil {
		return fmt.Errorf("DeleteRelease: %w", err)
	}
	result, err := inner.ExecContext(tx.ctx, `UPDATE releases SET deleted_at=? WHERE id=? AND deleted_at=0`, now.Unix(), id)
	if err != nil {
		return fmt.Errorf("DeleteRelease: %w", err)
	}
	moved, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("DeleteRelease: %w", err)
	}
	if moved != 1 {
		return fmt.Errorf("DeleteRelease: %w: release %d", ErrNotFound, id)
	}
	return nil
}

const assetColumns = `id,release_id,uploader_id,public_id,name,sha256,size_bytes,content_type,state,path,created_at,published_at`

func (tx *ReadTx) ListAssets(releaseID int64) ([]AssetRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, fmt.Errorf("ListAssets: %w", err)
	}
	rows, err := inner.QueryContext(tx.ctx, `SELECT `+assetColumns+` FROM release_assets WHERE release_id=? ORDER BY name`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("ListAssets: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []AssetRow
	for rows.Next() {
		asset, serr := scanAssetRow(rows)
		if serr != nil {
			return nil, fmt.Errorf("ListAssets: %w", serr)
		}
		out = append(out, asset)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListAssets: %w", err)
	}
	return out, nil
}

func (tx *ReadTx) GetAssetByPublicID(publicID string) (AssetRow, error) {
	inner, err := tx.live()
	if err != nil {
		return AssetRow{}, fmt.Errorf("GetAssetByPublicID: %w", err)
	}
	asset, serr := scanAssetRow(inner.QueryRowContext(tx.ctx, `SELECT `+assetColumns+` FROM release_assets WHERE public_id=?`, publicID))
	if serr != nil {
		if errors.Is(serr, sql.ErrNoRows) {
			return AssetRow{}, fmt.Errorf("GetAssetByPublicID: %w: %s", ErrNotFound, publicID)
		}
		return AssetRow{}, fmt.Errorf("GetAssetByPublicID: %w", serr)
	}
	return asset, nil
}

func (tx *WriteTx) GetAssetForUpdate(releaseID int64, name string) (AssetRow, bool, error) {
	inner, err := tx.live()
	if err != nil {
		return AssetRow{}, false, fmt.Errorf("GetAssetForUpdate: %w", err)
	}
	asset, serr := scanAssetRow(inner.QueryRowContext(tx.ctx, `SELECT `+assetColumns+` FROM release_assets WHERE release_id=? AND lower(name)=lower(?)`, releaseID, name))
	if serr != nil {
		if errors.Is(serr, sql.ErrNoRows) {
			return AssetRow{}, false, nil
		}
		return AssetRow{}, false, fmt.Errorf("GetAssetForUpdate: %w", serr)
	}
	return asset, true, nil
}

func (tx *WriteTx) InsertAsset(a AssetRow) error {
	inner, err := tx.live()
	if err != nil {
		return fmt.Errorf("InsertAsset: %w", err)
	}
	_, err = inner.ExecContext(tx.ctx, `INSERT INTO release_assets(public_id,release_id,uploader_id,name,sha256,size_bytes,content_type,state,path,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, a.PublicID, a.ReleaseID, a.UploaderID, a.Name, a.SHA256, a.Size, a.ContentType, a.State, a.Path, a.CreatedAt.Unix())
	if err != nil {
		return fmt.Errorf("InsertAsset: %w", mapErr(err))
	}
	return nil
}

func (tx *WriteTx) PublishAssets(releaseID int64, now time.Time) error {
	inner, err := tx.live()
	if err != nil {
		return fmt.Errorf("PublishAssets: %w", err)
	}
	_, err = inner.ExecContext(tx.ctx, `UPDATE release_assets SET state='published',published_at=? WHERE release_id=? AND state='draft'`, now.Unix(), releaseID)
	if err != nil {
		return fmt.Errorf("PublishAssets: %w", err)
	}
	return nil
}

func (tx *WriteTx) DeleteAssetByPublicID(publicID string) error {
	inner, err := tx.live()
	if err != nil {
		return fmt.Errorf("DeleteAssetByPublicID: %w", err)
	}
	result, err := inner.ExecContext(tx.ctx, `DELETE FROM release_assets WHERE public_id=? AND state='draft'`, publicID)
	if err != nil {
		return fmt.Errorf("DeleteAssetByPublicID: %w", err)
	}
	moved, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("DeleteAssetByPublicID: %w", err)
	}
	if moved != 1 {
		return fmt.Errorf("DeleteAssetByPublicID: %w: %s", ErrNotFound, publicID)
	}
	return nil
}

func (s *Store) ReleaseByTag(ctx context.Context, repoID int64, tag string) (ReleaseRow, error) {
	var row ReleaseRow
	err := s.Read(ctx, func(tx *ReadTx) error {
		var inner error
		row, inner = tx.GetRelease(repoID, strings.TrimSpace(tag))
		return inner
	})
	if err != nil {
		return ReleaseRow{}, fmt.Errorf("ReleaseByTag: %w", err)
	}
	return row, nil
}

func (s *Store) ReleaseAssets(ctx context.Context, id int64) ([]AssetRow, error) {
	var assets []AssetRow
	err := s.Read(ctx, func(tx *ReadTx) error {
		var inner error
		assets, inner = tx.ListAssets(id)
		return inner
	})
	if err != nil {
		return nil, fmt.Errorf("ReleaseAssets: %w", err)
	}
	return assets, nil
}

func (s *Store) AssetByPublicID(ctx context.Context, publicID string) (AssetRow, error) {
	var asset AssetRow
	err := s.Read(ctx, func(tx *ReadTx) error {
		var inner error
		asset, inner = tx.GetAssetByPublicID(publicID)
		return inner
	})
	if err != nil {
		return AssetRow{}, fmt.Errorf("AssetByPublicID: %w", err)
	}
	return asset, nil
}
