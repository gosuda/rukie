package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"regexp"
	"strings"

	"github.com/gosuda/rukie/internal/reserved"
	"github.com/gosuda/rukie/internal/store"
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,38}$`)

func ReservedName(name string) bool {
	return reserved.Is(strings.ToLower(name))
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
	au, ierr := s.store.InsertAuthUser(ctx, store.AuthUserCreate{
		Name:     in.Name,
		Lower:    strings.ToLower(in.Name),
		Email:    in.Email,
		FullName: in.FullName,
		Hash:     hash,
		Algo:     argonAlgo,
		Now:      now,
		Admin:    in.IsAdmin,
	})
	if ierr != nil {
		if errors.Is(ierr, store.ErrConflict) {
			return User{}, errors.New("username is taken")
		}
		return User{}, ierr
	}
	u, _ := authUserFromStore(au)
	s.recordAudit(ctx, AuditEvent{Actor: actor, Action: "admin.user_create", Target: "user:" + in.Name, IP: ip, Result: "ok"})
	return u, nil
}

type CreateUserRandomInput struct {
	Name     string
	Email    string
	FullName string
	IsAdmin  bool
}

func (s *Service) CreateUserRandom(ctx context.Context, actor int64, in CreateUserRandomInput, ip string) (User, string, error) {
	if err := CheckUsername(in.Name); err != nil {
		return User{}, "", err
	}
	if len(in.Email) > 255 || len(in.FullName) > 255 {
		return User{}, "", errors.New("email and full name must fit 255 characters")
	}
	temp := randomTempPassword()
	hash, err := HashPassword(temp)
	if err != nil {
		return User{}, "", err
	}
	now := millis(s.clock())
	au, ierr := s.store.InsertAuthUser(ctx, store.AuthUserCreate{
		Name:       in.Name,
		Lower:      strings.ToLower(in.Name),
		Email:      in.Email,
		FullName:   in.FullName,
		Hash:       hash,
		Algo:       argonAlgo,
		Now:        now,
		MustChange: true,
		Admin:      in.IsAdmin,
	})
	if ierr != nil {
		if errors.Is(ierr, store.ErrConflict) {
			return User{}, "", errors.New("username is taken")
		}
		return User{}, "", ierr
	}
	u, _ := authUserFromStore(au)
	s.recordAudit(ctx, AuditEvent{Actor: actor, Action: "admin.user_create", Target: "user:" + in.Name, IP: ip, Result: "ok"})
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
	au, berr := s.store.BootstrapAuthAdmin(ctx, name, strings.ToLower(name), hash, argonAlgo, now)
	if berr != nil {
		if errors.Is(berr, store.ErrConflict) {
			return User{}, errors.New("username is taken")
		}
		if errors.Is(berr, store.ErrAdminExists) {
			return User{}, errors.New("an admin already exists")
		}
		return User{}, berr
	}
	u, _ := authUserFromStore(au)
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
	u, _, err := s.authUserByName(ctx, strings.ToLower(in.Target))
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
	var rerr error
	if in.RevokeTokens {
		rerr = s.store.ApplyPasswordResetRevokingTokens(ctx, u.ID, hash, boolToInt(in.Random) == 1, now)
	} else {
		rerr = s.store.ApplyPasswordReset(ctx, u.ID, hash, boolToInt(in.Random) == 1, now)
	}
	if rerr != nil {
		return "", rerr
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
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	out := make([]byte, 20)
	var raw [20]byte
	_, _ = rand.Read(raw[:])
	for i := range out {
		out[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(out)
}

func (s *Service) UnlockUser(ctx context.Context, actor int64, target string, ip string) error {
	u, _, err := s.authUserByName(ctx, strings.ToLower(target))
	if err != nil {
		return errors.New("unknown user")
	}
	now := millis(s.clock())
	if cerr := s.store.ClearAuthLockout(ctx, u.ID, now); cerr != nil {
		return cerr
	}
	s.recordAudit(ctx, AuditEvent{Actor: actor, Action: "admin.unlock", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	return nil
}

func (s *Service) CountActiveAdmins(ctx context.Context) int {
	n, _ := s.store.CountActiveAuthAdmins(ctx)
	return int(n)
}

type UpdateUserInput struct {
	Target   string
	FullName *string
	Email    *string
	IsActive *bool
	IfMatch  string
}

func (s *Service) UpdateUser(ctx context.Context, actor int64, in UpdateUserInput, ip string) (User, error) {
	u, _, err := s.authUserByName(ctx, strings.ToLower(in.Target))
	if err != nil {
		return User{}, errors.New("unknown user")
	}
	if in.IfMatch == "" {
		return User{}, errors.New("if-match is required")
	}
	if !etagMatch(in.IfMatch, quoteETag(u.ID, u.UpdatedAt)) {
		return User{}, errStale
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
	if uerr := s.store.UpdateAuthUserAdmin(ctx, u.ID, fullName, email, active, now); uerr != nil {
		if errors.Is(uerr, store.ErrLastAdmin) {
			return User{}, errors.New("cannot deactivate the last active admin")
		}
		return User{}, uerr
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
	recs, err := s.store.ListAuthUsers(ctx, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	var out []User
	for _, rec := range recs {
		u, _ := authUserFromStore(rec)
		out = append(out, u)
	}
	next := ""
	if len(out) > limit {
		next = out[limit-1].Name
		out = out[:limit]
	}
	return out, next, nil
}

func (s *Service) GetUser(ctx context.Context, name string) (User, error) {
	u, _, err := s.authUserByName(ctx, strings.ToLower(name))
	if err != nil {
		return User{}, errors.New("unknown user")
	}
	return u, nil
}

func (s *Service) GetUserByID(ctx context.Context, userID int64) (User, error) {
	u, _, err := s.authUserByID(ctx, userID)
	if err != nil {
		return User{}, errors.New("unknown user")
	}
	return u, nil
}

type UpdateSelfInput struct {
	FullName *string
	Email    *string
	IfMatch  string
}

func (s *Service) UpdateSelf(ctx context.Context, userID int64, in UpdateSelfInput) (User, error) {
	u, _, err := s.authUserByID(ctx, userID)
	if err != nil {
		return User{}, err
	}
	if in.IfMatch == "" {
		return User{}, errors.New("if-match is required")
	}
	if !etagMatch(in.IfMatch, quoteETag(u.ID, u.UpdatedAt)) {
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
	if uerr := s.store.UpdateAuthUserSelf(ctx, userID, fullName, email, now); uerr != nil {
		return User{}, uerr
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "user.self_update", Target: "user:" + u.Name, Result: "ok"})
	u.FullName = fullName
	u.Email = email
	u.UpdatedAt = now
	return u, nil
}

func (s *Service) DeleteSelf(ctx context.Context, userID int64, ifMatch, ip string) error {
	u, _, err := s.authUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if ifMatch == "" {
		return errors.New("if-match is required")
	}
	if !etagMatch(ifMatch, quoteETag(u.ID, u.UpdatedAt)) {
		return errStale
	}
	if u.IsAdmin && u.IsActive && s.CountActiveAdmins(ctx) <= 1 {
		return errors.New("cannot delete the last active admin")
	}
	if derr := s.store.DeleteAuthUser(ctx, userID); derr != nil {
		if errors.Is(derr, store.ErrSoleOwner) {
			return errors.New("cannot delete the last org owner")
		}
		return derr
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "user.self_delete", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	return nil
}
