package store

import (
	"strings"
)

func ftsPhrase(q string) string {
	return `"` + strings.ReplaceAll(strings.ToLower(q), `"`, `""`) + `"`
}

// SearchReposFTS returns repo rows whose indexed metadata contains q,
// in name order starting after (afterName, afterID). The caller applies
// visibility filtering per row like ListRepos does.
func (tx *ReadTx) SearchReposFTS(q, afterName string, afterID int64, limit int) ([]RepoRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	after := afterID
	var query string
	var arg any
	if len([]rune(q)) < 3 {
		query = `SELECT r.id, r.owner_user_id, r.org_id, r.name, r.visibility, r.description, r.website,
	r.topics, r.default_branch, r.archived, r.deleted_at, r.purge_after, r.ruk_path, r.created_at, r.updated_at
	FROM repos r
WHERE lower(r.name || ' ' || coalesce(r.description, '') || ' ' || coalesce(r.topics, '') || ' ' || coalesce(r.website, '')) LIKE ? ESCAPE '\'
AND r.deleted_at IS NULL AND (r.name, r.id) > (?, ?)
ORDER BY r.name, r.id LIMIT ?`
		arg = `%` + likeEscape(strings.ToLower(q)) + `%`
	} else {
		query = `SELECT r.id, r.owner_user_id, r.org_id, r.name, r.visibility, r.description, r.website,
	r.topics, r.default_branch, r.archived, r.deleted_at, r.purge_after, r.ruk_path, r.created_at, r.updated_at
	FROM repos r JOIN repos_fts f ON f.rowid = r.id
WHERE f.body MATCH ? AND r.deleted_at IS NULL AND (r.name, r.id) > (?, ?)
ORDER BY r.name, r.id LIMIT ?`
		arg = ftsPhrase(q)
	}
	rows, qerr := inner.QueryContext(tx.ctx, query, arg, afterName, after, limit+1)
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	var out []RepoRow
	for rows.Next() {
		row, serr := scanRepoRow(rows.Scan)
		if serr != nil {
			return nil, serr
		}
		out = append(out, row)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, rerr
	}
	return out, nil
}

func likeEscape(q string) string {
	var b strings.Builder
	for _, c := range q {
		if c == '\\' || c == '%' || c == '_' {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// SearchedUser holds the public directory fields of one user row.
type SearchedUser struct {
	Name     string
	FullName string
	ID       int64
}

// SearchUsersFTS returns active users whose indexed metadata contains q,
// in name order starting after (afterName, afterID).
func (tx *ReadTx) SearchUsersFTS(q, afterName string, afterID int64, limit int) ([]SearchedUser, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	after := afterID
	var query string
	var arg any
	if len([]rune(q)) < 3 {
		query = `SELECT u.id, u.name, u.full_name FROM users u
WHERE lower(u.name || ' ' || coalesce(u.full_name, '')) LIKE ? ESCAPE '\'
AND u.is_active = 1 AND (u.name, u.id) > (?, ?)
ORDER BY u.name, u.id LIMIT ?`
		arg = `%` + likeEscape(strings.ToLower(q)) + `%`
	} else {
		query = `SELECT u.id, u.name, u.full_name FROM users u JOIN users_fts f ON f.rowid = u.id
WHERE f.body MATCH ? AND u.is_active = 1 AND (u.name, u.id) > (?, ?)
ORDER BY u.name, u.id LIMIT ?`
		arg = ftsPhrase(q)
	}
	rows, qerr := inner.QueryContext(tx.ctx, query, arg, afterName, after, limit+1)
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	var out []SearchedUser
	for rows.Next() {
		var u SearchedUser
		if serr := rows.Scan(&u.ID, &u.Name, &u.FullName); serr != nil {
			return nil, serr
		}
		out = append(out, u)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, rerr
	}
	return out, nil
}
