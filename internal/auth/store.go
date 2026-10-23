package auth

import (
	"context"
	"sync"
	"time"

	"github.com/gosuda/rukie/internal/store"
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

type Service struct {
	store         *store.Store
	now           func() time.Time
	oidcProviders map[string]*oidcRuntime
	sealKey       []byte
	oidcMu        sync.Mutex
}

func New(st *store.Store) *Service {
	return &Service{store: st, now: time.Now}
}

func (s *Service) Store() *store.Store {
	return s.store
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

func lockoutPolicy() store.AuthLockoutPolicy {
	return store.AuthLockoutPolicy{
		Threshold:    lockoutThreshold,
		WindowMillis: lockoutWindow.Milliseconds(),
		BaseMillis:   lockoutBase.Milliseconds(),
		MaxMillis:    lockoutMax.Milliseconds(),
		RetainMillis: lockoutRetain.Milliseconds(),
	}
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

func authUserFromStore(au store.AuthUser) (User, string) {
	return User{
		PasswordAlgo:       au.PasswordAlgo,
		Name:               au.Name,
		LowerName:          au.Lower,
		Email:              au.Email,
		FullName:           au.FullName,
		LockedUntil:        au.LockedUntil,
		FailedLoginCount:   au.FailedLoginCount,
		ID:                 au.ID,
		PasswordChangedAt:  au.PasswordChangedAt,
		CreatedAt:          au.CreatedAt,
		UpdatedAt:          au.UpdatedAt,
		MustChangePassword: au.MustChange,
		TOTPEnabled:        au.TOTPEnabled,
		OIDCOnly:           au.OIDCOnly,
		IsAdmin:            au.IsAdmin,
		IsActive:           au.IsActive,
	}, au.PasswordHash
}

func (s *Service) authUserByName(ctx context.Context, lower string) (User, string, error) {
	au, err := s.store.GetAuthUserByName(ctx, lower)
	if err != nil {
		return User{}, "", err
	}
	u, hash := authUserFromStore(au)
	return u, hash, nil
}

func (s *Service) authUserByID(ctx context.Context, userID int64) (User, string, error) {
	au, err := s.store.GetAuthUserByID(ctx, userID)
	if err != nil {
		return User{}, "", err
	}
	u, hash := authUserFromStore(au)
	return u, hash, nil
}

func (s *Service) authUserByEmail(ctx context.Context, email string) (User, string, error) {
	au, err := s.store.GetAuthUserByEmail(ctx, email)
	if err != nil {
		return User{}, "", err
	}
	u, hash := authUserFromStore(au)
	return u, hash, nil
}
