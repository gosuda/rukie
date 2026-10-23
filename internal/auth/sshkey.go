package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gosuda/rukie/internal/store"
	"golang.org/x/crypto/ssh"
)

const (
	maxSSHLineChars     = 16384
	maxSSHCommentChars  = 512
	minRSABits          = 3072
	sshTouchGranularity = time.Minute
)

type ParsedSSHKey struct {
	Fingerprint string
	KeyType     string
	Comment     string
}

func ParseSSHPublicKey(line string) (ParsedSSHKey, error) {
	if len(line) > maxSSHLineChars {
		return ParsedSSHKey{}, fmt.Errorf("ssh key must fit in %d characters", maxSSHLineChars)
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return ParsedSSHKey{}, fmt.Errorf("ssh key must not be empty")
	}
	if strings.ContainsAny(trimmed, "\n\r") {
		return ParsedSSHKey{}, fmt.Errorf("ssh key must be a single line")
	}
	pub, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(trimmed))
	if err != nil {
		return ParsedSSHKey{}, fmt.Errorf("ssh key is not a valid authorized_keys line")
	}
	if len(options) > 0 || len(rest) > 0 {
		return ParsedSSHKey{}, fmt.Errorf("ssh key must be a single key without options")
	}
	if err := checkSSHKeyType(pub); err != nil {
		return ParsedSSHKey{}, err
	}
	if len(comment) > maxSSHCommentChars {
		return ParsedSSHKey{}, fmt.Errorf("ssh key comment must fit in %d characters", maxSSHCommentChars)
	}
	return ParsedSSHKey{
		Fingerprint: ssh.FingerprintSHA256(pub),
		KeyType:     pub.Type(),
		Comment:     comment,
	}, nil
}

func checkSSHKeyType(pub ssh.PublicKey) error {
	switch pub.Type() {
	case "ssh-ed25519":
		return nil
	case "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521":
		crypto, ok := pub.(ssh.CryptoPublicKey)
		if !ok {
			return fmt.Errorf("ssh key type %q is not allowed", pub.Type())
		}
		key, ok := crypto.CryptoPublicKey().(*ecdsa.PublicKey)
		if !ok {
			return fmt.Errorf("ssh key type %q is not allowed", pub.Type())
		}
		want := map[string]int{
			"ecdsa-sha2-nistp256": 256,
			"ecdsa-sha2-nistp384": 384,
			"ecdsa-sha2-nistp521": 521,
		}[pub.Type()]
		if key.Curve.Params().BitSize != want {
			return fmt.Errorf("ssh key type %q is not allowed", pub.Type())
		}
		return nil
	case "ssh-rsa":
		crypto, ok := pub.(ssh.CryptoPublicKey)
		if !ok {
			return fmt.Errorf("ssh-rsa keys need at least %d bits", minRSABits)
		}
		key, ok := crypto.CryptoPublicKey().(*rsa.PublicKey)
		if !ok || key.N.BitLen() < minRSABits {
			return fmt.Errorf("ssh-rsa keys need at least %d bits", minRSABits)
		}
		return nil
	default:
		return fmt.Errorf("ssh key type %q is not allowed", pub.Type())
	}
}

type SSHKey struct {
	Fingerprint string
	KeyType     string
	PublicKey   string
	Comment     string
	ID          int64
	UserID      int64
	CreatedAt   int64
	LastUsedAt  int64
}

func sshKeyFromStore(rec store.SSHKeyRecord) SSHKey {
	return SSHKey{
		Fingerprint: rec.Fingerprint,
		KeyType:     rec.KeyType,
		PublicKey:   rec.PublicKey,
		Comment:     rec.Comment,
		ID:          rec.ID,
		UserID:      rec.UserID,
		CreatedAt:   rec.CreatedAt,
		LastUsedAt:  rec.LastUsedAt,
	}
}

func (s *Service) AddSSHKey(ctx context.Context, userID int64, line, ip string) (SSHKey, error) {
	parsed, err := ParseSSHPublicKey(line)
	if err != nil {
		return SSHKey{}, err
	}
	trimmed := strings.TrimSpace(line)
	taken, terr := s.store.SSHKeyFingerprintTaken(ctx, parsed.Fingerprint)
	if terr != nil {
		return SSHKey{}, terr
	}
	if taken {
		return SSHKey{}, errors.New("ssh key is already registered")
	}
	now := millis(s.clock())
	id, ierr := s.store.InsertSSHKey(ctx, store.SSHKeyRecord{
		UserID:      userID,
		Fingerprint: parsed.Fingerprint,
		KeyType:     parsed.KeyType,
		PublicKey:   trimmed,
		Comment:     parsed.Comment,
		CreatedAt:   now,
	})
	if ierr != nil {
		return SSHKey{}, ierr
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "ssh.add", Target: "ssh_key:" + parsed.Fingerprint, IP: ip, Result: "ok"})
	return SSHKey{
		Fingerprint: parsed.Fingerprint,
		KeyType:     parsed.KeyType,
		PublicKey:   trimmed,
		Comment:     parsed.Comment,
		ID:          id,
		UserID:      userID,
		CreatedAt:   now,
	}, nil
}

func (s *Service) ListSSHKeys(ctx context.Context, userID int64) ([]SSHKey, error) {
	recs, err := s.store.ListSSHKeysByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]SSHKey, 0, len(recs))
	for _, rec := range recs {
		out = append(out, sshKeyFromStore(rec))
	}
	return out, nil
}

func (s *Service) RemoveSSHKey(ctx context.Context, userID, id int64, ip string) (bool, error) {
	deleted, err := s.store.DeleteSSHKey(ctx, id, userID)
	if err != nil {
		return false, err
	}
	if !deleted {
		return false, nil
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "ssh.remove", Target: "ssh_key:id", IP: ip, Result: "ok"})
	return true, nil
}

func (s *Service) LookupSSHKey(ctx context.Context, fingerprint string) (SSHKey, User, error) {
	rec, err := s.store.GetSSHKeyByFingerprint(ctx, fingerprint)
	if err != nil {
		return SSHKey{}, User{}, errors.New("unknown ssh key")
	}
	user, uerr := s.GetUserByID(ctx, rec.UserID)
	if uerr != nil {
		return SSHKey{}, User{}, errors.New("unknown ssh key")
	}
	if !user.IsActive {
		return SSHKey{}, User{}, errors.New("unknown ssh key")
	}
	return sshKeyFromStore(rec), user, nil
}

func (s *Service) TouchSSHKey(ctx context.Context, id int64, now time.Time) {
	_ = s.store.TouchSSHKey(ctx, id, now.UnixMilli(), now.Add(-sshTouchGranularity).UnixMilli())
}

func (s *Service) AuditSSHUnknownKey(ctx context.Context, fingerprint, ip string) {
	s.recordAudit(ctx, AuditEvent{Action: "ssh.unknown_key", Target: "ssh_key:" + fingerprint, IP: ip, Result: "denied"})
}
