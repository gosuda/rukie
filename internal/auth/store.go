package auth

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const (
	sessionIdleTTL      = 24 * time.Hour
	sessionAbsoluteTTL  = 30 * 24 * time.Hour
	sessionRefreshAfter = 5 * time.Minute
	sessionRotateEvery  = 24 * time.Hour
	freshAuthWindow     = 15 * time.Minute

	lockoutThreshold = 5
	lockoutWindow    = 10 * time.Minute
	lockoutRetain    = 30 * 24 * time.Hour
	lockoutBase      = 15 * time.Minute
	lockoutMax       = 24 * time.Hour

	tokenDefaultTTL          = 90 * 24 * time.Hour
	tokenMaxTTL              = 366 * 24 * time.Hour
	tokenUseTrackGranularity = time.Minute
)

const authDSNParams = "file:%s?_txlock=immediate&_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on" +
	"&_busy_timeout=5000&_dqs=0&_pragma=temp_store(MEMORY)&_pragma=mmap_size(0)" +
	"&_pragma=trusted_schema(OFF)"

type Service struct {
	db  *sql.DB
	now func() time.Time
}

func Open(path string) (*Service, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf(authDSNParams, path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(16)
	return &Service{db: db, now: time.Now}, nil
}

func (s *Service) Close() error {
	return s.db.Close()
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func millis(t time.Time) int64 {
	return t.UTC().UnixMilli()
}

func (s *Service) execTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

type User struct {
	PasswordAlgo       string
	Name               string
	LowerName          string
	Email              string
	FullName           string
	LockedUntil        int64
	FailedLoginCount   int64
	ID                 int64
	PasswordChangedAt  int64
	CreatedAt          int64
	UpdatedAt          int64
	MustChangePassword bool
	TOTPEnabled        bool
	OIDCOnly           bool
	IsAdmin            bool
	IsActive           bool
}

const userColumns = `id, name, lower_name, email, full_name, password_algo,
	must_change_password, totp_enabled, oidc_only, is_admin, is_active,
	failed_login_count, locked_until, password_changed_at, created_at, updated_at`

func (s *Service) userByName(ctx context.Context, q queryer, lower string) (User, string, error) {
	var u User
	var hash string
	var mustChange, totp, oidc, admin, active int64
	var locked sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT `+userColumns+`, password_hash FROM users WHERE lower_name = ?`, lower).Scan(
		&u.ID, &u.Name, &u.LowerName, &u.Email, &u.FullName,
		&u.PasswordAlgo, &mustChange, &totp, &oidc, &admin, &active,
		&u.FailedLoginCount, &locked, &u.PasswordChangedAt, &u.CreatedAt, &u.UpdatedAt, &hash)
	if err != nil {
		return User{}, "", err
	}
	u.MustChangePassword = mustChange != 0
	u.TOTPEnabled = totp != 0
	u.OIDCOnly = oidc != 0
	u.IsAdmin = admin != 0
	u.IsActive = active != 0
	if locked.Valid {
		u.LockedUntil = locked.Int64
	}
	return u, hash, nil
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
