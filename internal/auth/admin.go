package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"regexp"
	"strings"
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,38}$`)

var reservedNames = map[string]bool{
	"admin": true, "root": true, "system": true, "rukie": true, "api": true,
	"git": true, "support": true, "abuse": true, "login": true, "logout": true,
	"settings": true, "account": true, "explore": true, "ops": true, "problems": true,
	"mcp": true, "static": true, "session": true, "healthz": true, "readyz": true,
	"-": true,
}

func ReservedName(name string) bool {
	return reservedNames[strings.ToLower(name)]
}

func CheckUsername(name string) error {
	if !usernamePattern.MatchString(name) {
		return errors.New("username must match [a-z0-9][a-z0-9._-]{0,38}")
	}
	if ReservedName(name) {
		return errors.New("username is reserved")
	}
	return nil
}

type CreateUserInput struct {
	Name     string
	Email    string
	FullName string
	Password string
	IsAdmin  bool
}

func (s *Service) CreateUser(ctx context.Context, actor int64, in CreateUserInput, ip string) (User, error) {
	if err := CheckUsername(in.Name); err != nil {
		return User{}, err
	}
	if err := CheckPasswordPolicy(in.Password, in.Name); err != nil {
		return User{}, err
	}
	if len(in.Email) > 255 || len(in.FullName) > 255 {
		return User{}, errors.New("email and full name must fit 255 characters")
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return User{}, err
	}
	now := millis(s.clock())
	admin := 0
	if in.IsAdmin {
		admin = 1
	}
	var u User
	err = s.execTx(ctx, func(tx *sql.Tx) error {
		res, txErr := tx.ExecContext(ctx, `INSERT INTO users
			(name, lower_name, email, full_name, password_hash, password_algo, password_changed_at, is_admin, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'argon2id-v19', ?, ?, ?, ?)`,
			in.Name, strings.ToLower(in.Name), in.Email, in.FullName, hash, now, admin, now, now)
		if txErr != nil {
			return txErr
		}
		id, txErr := res.LastInsertId()
		if txErr != nil {
			return txErr
		}
		u = User{ID: id, Name: in.Name, LowerName: strings.ToLower(in.Name), Email: in.Email,
			FullName: in.FullName, PasswordAlgo: argonAlgo, IsAdmin: in.IsAdmin, IsActive: true,
			PasswordChangedAt: now, CreatedAt: now, UpdatedAt: now}
		return nil
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, errors.New("username is taken")
		}
		return User{}, err
	}
	s.recordAudit(ctx, AuditEvent{Actor: actor, Action: "admin.user_create", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	return u, nil
}

func (s *Service) CreateUserRandom(ctx context.Context, actor int64, name, email, fullName, ip string) (User, string, error) {
	if err := CheckUsername(name); err != nil {
		return User{}, "", err
	}
	if len(email) > 255 || len(fullName) > 255 {
		return User{}, "", errors.New("email and full name must fit 255 characters")
	}
	temp := randomTempPassword()
	hash, err := HashPassword(temp)
	if err != nil {
		return User{}, "", err
	}
	now := millis(s.clock())
	var u User
	err = s.execTx(ctx, func(tx *sql.Tx) error {
		res, txErr := tx.ExecContext(ctx, `INSERT INTO users
			(name, lower_name, email, full_name, password_hash, password_algo, password_changed_at, must_change_password, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'argon2id-v19', ?, 1, ?, ?)`,
			name, strings.ToLower(name), email, fullName, hash, now, now, now)
		if txErr != nil {
			return txErr
		}
		id, txErr := res.LastInsertId()
		if txErr != nil {
			return txErr
		}
		u = User{ID: id, Name: name, LowerName: strings.ToLower(name), Email: email,
			FullName: fullName, PasswordAlgo: argonAlgo, IsActive: true, MustChangePassword: true,
			PasswordChangedAt: now, CreatedAt: now, UpdatedAt: now}
		return nil
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, "", errors.New("username is taken")
		}
		return User{}, "", err
	}
	s.recordAudit(ctx, AuditEvent{Actor: actor, Action: "admin.user_create", Target: "user:" + name, IP: ip, Result: "ok"})
	return u, temp, nil
}

func (s *Service) BootstrapAdmin(ctx context.Context, name, password, ip string) (User, error) {
	if err := CheckUsername(name); err != nil {
		return User{}, err
	}
	if err := CheckPasswordPolicy(password, name); err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	now := millis(s.clock())
	var u User
	err = s.execTx(ctx, func(tx *sql.Tx) error {
		res, txErr := tx.ExecContext(ctx, `INSERT INTO users
			(name, lower_name, email, full_name, password_hash, password_algo, password_changed_at, is_admin, created_at, updated_at)
			SELECT ?, ?, '', '', ?, 'argon2id-v19', ?, 1, ?, ?
			WHERE NOT EXISTS (SELECT 1 FROM users WHERE is_admin != 0)`,
			name, strings.ToLower(name), hash, now, now, now)
		if txErr != nil {
			return txErr
		}
		affected, txErr := res.RowsAffected()
		if txErr != nil {
			return txErr
		}
		if affected == 0 {
			var dup int64
			if qerr := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE lower_name = ?`, strings.ToLower(name)).Scan(&dup); qerr == nil {
				return errors.New("username is taken")
			}
			return errors.New("an admin already exists")
		}
		id, txErr := res.LastInsertId()
		if txErr != nil {
			return txErr
		}
		u = User{ID: id, Name: name, LowerName: strings.ToLower(name), PasswordAlgo: argonAlgo,
			IsAdmin: true, IsActive: true, PasswordChangedAt: now, CreatedAt: now, UpdatedAt: now}
		return nil
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, errors.New("username is taken")
		}
		return User{}, err
	}
	s.recordAudit(ctx, AuditEvent{Action: "admin.user_create", Target: "user:" + name, IP: ip, Result: "ok"})
	return u, nil
}

type ResetPasswordInput struct {
	Target       string
	Password     string
	Random       bool
	RevokeTokens bool
}

func (s *Service) ResetPassword(ctx context.Context, actor int64, in ResetPasswordInput, ip string) (string, error) {
	u, _, err := s.userByName(ctx, s.db, strings.ToLower(in.Target))
	if err != nil {
		return "", errors.New("unknown user")
	}
	next := in.Password
	if in.Random {
		next = randomTempPassword()
	}
	if policyErr := CheckPasswordPolicy(next, u.Name); policyErr != nil {
		return "", policyErr
	}
	hash, err := HashPassword(next)
	if err != nil {
		return "", err
	}
	now := millis(s.clock())
	err = s.execTx(ctx, func(tx *sql.Tx) error {
		if _, txErr := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, password_changed_at = ?,
			must_change_password = ?, failed_login_count = 0, locked_until = NULL, updated_at = ? WHERE id = ?`,
			hash, now, boolToInt(in.Random), now, u.ID); txErr != nil {
			return txErr
		}
		if _, txErr := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, u.ID); txErr != nil {
			return txErr
		}
		if in.RevokeTokens {
			_, txErr := tx.ExecContext(ctx, `UPDATE scoped_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, u.ID)
			if txErr != nil {
				return txErr
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	s.recordAudit(ctx, AuditEvent{Actor: actor, Action: "admin.password_reset", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	if in.RevokeTokens {
		s.recordAudit(ctx, AuditEvent{Actor: actor, Action: "token.revoked_by_reset", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	}
	if in.Random {
		return next, nil
	}
	return "", nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func randomTempPassword() string {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, 20)
	var raw [20]byte
	_, _ = rand.Read(raw[:])
	for i := range out {
		out[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(out)
}

func (s *Service) UnlockUser(ctx context.Context, actor int64, target, ip string) error {
	u, _, err := s.userByName(ctx, s.db, strings.ToLower(target))
	if err != nil {
		return errors.New("unknown user")
	}
	now := millis(s.clock())
	_, err = s.db.ExecContext(ctx, `UPDATE users SET failed_login_count = 0, locked_until = NULL, updated_at = ? WHERE id = ?`, now, u.ID)
	if err != nil {
		return err
	}
	s.recordAudit(ctx, AuditEvent{Actor: actor, Action: "admin.unlock", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	return nil
}

func (s *Service) CountActiveAdmins(ctx context.Context) int {
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin != 0 AND is_active != 0`).Scan(&n)
	return n
}

type UpdateUserInput struct {
	Target   string
	FullName *string
	Email    *string
	IsActive *bool
	IfMatch  string
}

func (s *Service) UpdateUser(ctx context.Context, actor int64, in UpdateUserInput, ip string) (User, error) {
	u, _, err := s.userByName(ctx, s.db, strings.ToLower(in.Target))
	if err != nil {
		return User{}, errors.New("unknown user")
	}
	if in.IfMatch == "" {
		return User{}, errors.New("if-match is required")
	}
	if !etagMatch(in.IfMatch, quoteETag(u.ID, u.UpdatedAt)) {
		return User{}, errStale
	}
	if in.IsActive != nil && !*in.IsActive && u.IsAdmin && u.IsActive {
		if s.CountActiveAdmins(ctx) <= 1 {
			return User{}, errors.New("cannot deactivate the last active admin")
		}
	}
	now := millis(s.clock())
	fullName := u.FullName
	if in.FullName != nil {
		if len(*in.FullName) > 255 {
			return User{}, errors.New("full name must fit 255 characters")
		}
		fullName = *in.FullName
	}
	email := u.Email
	if in.Email != nil {
		if len(*in.Email) > 255 {
			return User{}, errors.New("email must fit 255 characters")
		}
		email = *in.Email
	}
	active := u.IsActive
	if in.IsActive != nil {
		active = *in.IsActive
	}
	_, err = s.db.ExecContext(ctx, `UPDATE users SET full_name = ?, email = ?, is_active = ?, updated_at = ? WHERE id = ?`,
		fullName, email, boolToInt(active), now, u.ID)
	if err != nil {
		return User{}, err
	}
	s.recordAudit(ctx, AuditEvent{Actor: actor, Action: "admin.user_update", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	u.FullName = fullName
	u.Email = email
	u.IsActive = active
	u.UpdatedAt = now
	return u, nil
}

func (s *Service) ListUsers(ctx context.Context, after string, limit int) ([]User, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users WHERE name > ? ORDER BY name LIMIT ?`, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	var out []User
	for rows.Next() {
		var u User
		var mustChange, totp, oidc, admin, active int64
		var locked sql.NullInt64
		if err := rows.Scan(&u.ID, &u.Name, &u.LowerName, &u.Email, &u.FullName,
			&u.PasswordAlgo, &mustChange, &totp, &oidc, &admin, &active,
			&u.FailedLoginCount, &locked, &u.PasswordChangedAt, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, "", err
		}
		u.MustChangePassword = mustChange != 0
		u.TOTPEnabled = totp != 0
		u.OIDCOnly = oidc != 0
		u.IsAdmin = admin != 0
		u.IsActive = active != 0
		if locked.Valid {
			u.LockedUntil = locked.Int64
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		next = out[limit-1].Name
		out = out[:limit]
	}
	return out, next, nil
}

func (s *Service) GetUser(ctx context.Context, name string) (User, error) {
	u, _, err := s.userByName(ctx, s.db, strings.ToLower(name))
	if err != nil {
		return User{}, errors.New("unknown user")
	}
	return u, nil
}

func (s *Service) GetUserByID(ctx context.Context, userID int64) (User, error) {
	u, _, err := s.userByNameID(ctx, userID)
	if err != nil {
		return User{}, errors.New("unknown user")
	}
	return u, nil
}

func (s *Service) DB() *sql.DB {
	return s.db
}

type UpdateSelfInput struct {
	FullName *string
	Email    *string
	IfMatch  string
}

func (s *Service) UpdateSelf(ctx context.Context, userID int64, in UpdateSelfInput) (User, error) {
	u, _, err := s.userByNameID(ctx, userID)
	if err != nil {
		return User{}, err
	}
	if in.IfMatch == "" {
		return User{}, errors.New("if-match is required")
	}
	if !etagMatch(in.IfMatch, quoteETag(userID, u.UpdatedAt)) {
		return User{}, errStale
	}
	fullName := u.FullName
	if in.FullName != nil {
		if len(*in.FullName) > 255 {
			return User{}, errors.New("full name must fit 255 characters")
		}
		fullName = *in.FullName
	}
	email := u.Email
	if in.Email != nil {
		if len(*in.Email) > 255 {
			return User{}, errors.New("email must fit 255 characters")
		}
		email = *in.Email
	}
	now := millis(s.clock())
	_, err = s.db.ExecContext(ctx, `UPDATE users SET full_name = ?, email = ?, updated_at = ? WHERE id = ?`,
		fullName, email, now, userID)
	if err != nil {
		return User{}, err
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "user.self_update", Target: "user:" + u.Name, Result: "ok"})
	u.FullName = fullName
	u.Email = email
	u.UpdatedAt = now
	return u, nil
}

func (s *Service) DeleteSelf(ctx context.Context, userID int64, ifMatch, ip string) error {
	u, _, err := s.userByNameID(ctx, userID)
	if err != nil {
		return err
	}
	if ifMatch == "" {
		return errors.New("if-match is required")
	}
	if !etagMatch(ifMatch, quoteETag(userID, u.UpdatedAt)) {
		return errStale
	}
	if u.IsAdmin && u.IsActive && s.CountActiveAdmins(ctx) <= 1 {
		return errors.New("cannot delete the last active admin")
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
	if err != nil {
		return err
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "user.self_delete", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	return nil
}
