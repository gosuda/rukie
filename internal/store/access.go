package store

import (
	"context"
	"database/sql"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/access"
)

type StoreResolver struct {
	s *Store
}

func (s *Store) Resolver() access.Resolver {
	return &StoreResolver{s: s}
}

func (r *StoreResolver) Repo(id int64) (access.RepoRecord, bool) {
	var out access.RepoRecord
	ok := false
	_ = r.s.Read(context.Background(), func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var vis string
		var owner sql.NullInt64
		serr := inner.QueryRowContext(tx.ctx,
			`SELECT id, owner_user_id, org_id, visibility FROM repos WHERE id = ?`, id).Scan(
			&out.ID, &owner, &out.OrgID, &vis)
		if serr != nil {
			return nil
		}
		if owner.Valid {
			out.OwnerUserID = owner.Int64
		}
		out.Visibility = access.Visibility(vis)
		ok = true
		return nil
	})
	return out, ok
}

func (r *StoreResolver) DirectRole(repoID, userID int64) (access.Role, bool) {
	var role access.Role
	found := false
	_ = r.s.Read(context.Background(), func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var raw string
		serr := inner.QueryRowContext(tx.ctx,
			`SELECT role FROM repo_grants WHERE repo_id = ? AND user_id = ?`, repoID, userID).Scan(&raw)
		if serr != nil {
			return nil
		}
		if parsed, ok := access.ParseRole(raw); ok {
			role = parsed
			found = true
		}
		return nil
	})
	return role, found
}

func (r *StoreResolver) TeamRole(repoID, userID int64) (access.Role, bool) {
	var best access.Role
	found := false
	_ = r.s.Read(context.Background(), func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		rows, qerr := inner.QueryContext(tx.ctx, `SELECT t.permission FROM teams t
JOIN team_members m ON m.team_id = t.id
JOIN repos r ON r.id = ? AND r.deleted_at IS NULL
WHERE m.user_id = ? AND t.org_id = r.org_id
AND (t.repo_scope = 'all' OR EXISTS
 (SELECT 1 FROM team_repos tr WHERE tr.team_id = t.id AND tr.repo_id = r.id))`,
			repoID, userID)
		if qerr != nil {
			return nil
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var raw string
			if serr := rows.Scan(&raw); serr != nil {
				return nil
			}
			if parsed, ok := access.ParseRole(raw); ok {
				if !found || parsed > best {
					best = parsed
					found = true
				}
			}
		}
		return nil
	})
	return best, found
}

type accessAllowJSON struct {
	Users []int64  `json:"users"`
	Teams []int64  `json:"teams"`
	Roles []string `json:"roles"`
}

type accessRequiredJSON struct {
	Kind       string          `json:"kind"`
	Result     string          `json:"result"`
	Trusted    accessAllowJSON `json:"trusted"`
	MinSigners int             `json:"min_signers"`
}

func accessAllowFromJSON(a accessAllowJSON) access.AllowEntry {
	var e access.AllowEntry
	e.Users = append(e.Users, a.Users...)
	e.Teams = append(e.Teams, a.Teams...)
	for _, s := range a.Roles {
		if role, ok := access.ParseRole(s); ok {
			e.Roles = append(e.Roles, role)
		}
	}
	return e
}

func parseAccessAllowList(raw string) []access.AllowEntry {
	var items []accessAllowJSON
	if jerr := jsonv2.Unmarshal([]byte(raw), &items, jsonv1.DefaultOptionsV1()); jerr != nil {
		return nil
	}
	out := make([]access.AllowEntry, 0, len(items))
	for _, item := range items {
		out = append(out, accessAllowFromJSON(item))
	}
	return out
}

func parseAccessRequiredList(raw string) []access.AttestationReq {
	var items []accessRequiredJSON
	if jerr := jsonv2.Unmarshal([]byte(raw), &items, jsonv1.DefaultOptionsV1()); jerr != nil {
		return nil
	}
	out := make([]access.AttestationReq, 0, len(items))
	for _, item := range items {
		out = append(out, access.AttestationReq{
			Kind:       item.Kind,
			Result:     item.Result,
			MinSigners: item.MinSigners,
			Trusted:    accessAllowFromJSON(item.Trusted),
		})
	}
	return out
}

func (r *StoreResolver) Rules(repoID int64) []access.BookmarkRule {
	var out []access.BookmarkRule
	_ = r.s.Read(context.Background(), func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		rows, qerr := inner.QueryContext(tx.ctx, `SELECT id, repo_id, pattern, mode, bypass_admin,
push_allow, land_allow, required, version FROM bookmark_rules WHERE repo_id = ?`, repoID)
		if qerr != nil {
			return nil
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var rule access.BookmarkRule
			var bypass int64
			var pushRaw, landRaw, reqRaw string
			if serr := rows.Scan(&rule.ID, &rule.RepoID, &rule.Pattern, &rule.Mode, &bypass,
				&pushRaw, &landRaw, &reqRaw, &rule.Version); serr != nil {
				return nil
			}
			rule.BypassAdmin = bypass != 0
			rule.PushAllow = parseAccessAllowList(pushRaw)
			rule.LandAllow = parseAccessAllowList(landRaw)
			rule.Required = parseAccessRequiredList(reqRaw)
			out = append(out, rule)
		}
		return nil
	})
	return out
}

func (r *StoreResolver) IsOrgMember(orgID, userID int64) bool {
	hit := false
	_ = r.s.Read(context.Background(), func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var n int64
		serr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM team_members m
JOIN teams t ON t.id = m.team_id WHERE t.org_id = ? AND m.user_id = ?`,
			orgID, userID).Scan(&n)
		hit = serr == nil && n > 0
		return nil
	})
	return hit
}

func (r *StoreResolver) IsOrgOwner(orgID, userID int64) bool {
	hit := false
	_ = r.s.Read(context.Background(), func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var n int64
		serr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM team_members m
JOIN teams t ON t.id = m.team_id
WHERE t.org_id = ? AND t.lower_name = 'owners' AND m.user_id = ?`,
			orgID, userID).Scan(&n)
		hit = serr == nil && n > 0
		return nil
	})
	return hit
}

func (r *StoreResolver) CanCreateRepo(orgID, userID int64) bool {
	if r.IsOrgOwner(orgID, userID) {
		return true
	}
	hit := false
	_ = r.s.Read(context.Background(), func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var n int64
		serr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM team_members m
JOIN teams t ON t.id = m.team_id
WHERE t.org_id = ? AND t.can_create_repo = 1 AND m.user_id = ?`,
			orgID, userID).Scan(&n)
		hit = serr == nil && n > 0
		return nil
	})
	return hit
}

func (r *StoreResolver) IsTeamMember(teamID, userID int64) bool {
	hit := false
	_ = r.s.Read(context.Background(), func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var n int64
		serr := inner.QueryRowContext(tx.ctx,
			`SELECT COUNT(*) FROM team_members WHERE team_id = ? AND user_id = ?`,
			teamID, userID).Scan(&n)
		hit = serr == nil && n > 0
		return nil
	})
	return hit
}

func (r *StoreResolver) TeamOrg(teamID int64) (int64, bool) {
	var org int64
	found := false
	_ = r.s.Read(context.Background(), func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		serr := inner.QueryRowContext(tx.ctx,
			`SELECT org_id FROM teams WHERE id = ?`, teamID).Scan(&org)
		if serr == nil {
			found = true
		}
		return nil
	})
	return org, found
}

func (r *StoreResolver) OrgPublic(orgID int64) bool {
	hit := false
	_ = r.s.Read(context.Background(), func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var vis string
		serr := inner.QueryRowContext(tx.ctx,
			`SELECT visibility FROM orgs WHERE id = ?`, orgID).Scan(&vis)
		hit = serr == nil && vis == "public"
		return nil
	})
	return hit
}

func (r *StoreResolver) IsSiteAdmin(userID int64) bool {
	hit := false
	_ = r.s.Read(context.Background(), func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var n int64
		serr := inner.QueryRowContext(tx.ctx,
			`SELECT COUNT(*) FROM users WHERE id = ? AND is_admin != 0`, userID).Scan(&n)
		hit = serr == nil && n > 0
		return nil
	})
	return hit
}

func (r *StoreResolver) IsActive(userID int64) bool {
	hit := false
	_ = r.s.Read(context.Background(), func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var active int64
		serr := inner.QueryRowContext(tx.ctx,
			`SELECT is_active FROM users WHERE id = ?`, userID).Scan(&active)
		hit = serr == nil && active != 0
		return nil
	})
	return hit
}
