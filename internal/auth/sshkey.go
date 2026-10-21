package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	maxSSHLineChars     = 16384
	maxSSHCommentChars  = 512
	minRSABits          = 3072
	sshTouchGranularity = time.Minute
)

// ParsedSSHKey carries the validated parts of one authorized_keys line.
type ParsedSSHKey struct {
	Fingerprint string
	KeyType     string
	Comment     string
}

// ParseSSHPublicKey validates one authorized_keys line against the v1 key policy.
func ParseSSHPublicKey(line string) (ParsedSSHKey, error) {
	var zero ParsedSSHKey
	if len(line) > maxSSHLineChars {
		return zero, fmt.Errorf("ssh key must fit in %d characters", maxSSHLineChars)
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return zero, fmt.Errorf("ssh key must not be empty")
	}
	if strings.ContainsAny(trimmed, "\n\r") {
		return zero, fmt.Errorf("ssh key must be a single line")
	}
	pub, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(trimmed))
	if err != nil {
		return zero, fmt.Errorf("ssh key is not a valid authorized_keys line")
	}
	if len(options) > 0 || len(rest) > 0 {
		return zero, fmt.Errorf("ssh key must be a single key without options")
	}
	if err := checkSSHKeyType(pub); err != nil {
		return zero, err
	}
	if len(comment) > maxSSHCommentChars {
		return zero, fmt.Errorf("ssh key comment must fit in %d characters", maxSSHCommentChars)
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

// SSHKey is one registered public key of a user.
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

func scanSSHKey(row interface {
	Scan(dest ...any) error
}, key *SSHKey) error {
	var last sql.NullInt64
	err := row.Scan(&key.ID, &key.UserID, &key.Fingerprint, &key.KeyType,
		&key.PublicKey, &key.Comment, &key.CreatedAt, &last)
	if err != nil {
		return err
	}
	if last.Valid {
		key.LastUsedAt = last.Int64
	}
	return nil
}

// AddSSHKey registers one authorized_keys line for a user.
func (s *Service) AddSSHKey(ctx context.Context, userID int64, line, ip string) (SSHKey, error) {
	parsed, err := ParseSSHPublicKey(line)
	if err != nil {
		return SSHKey{}, err
	}
	trimmed := strings.TrimSpace(line)
	var count int64
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ssh_keys WHERE fingerprint = ?`, parsed.Fingerprint).Scan(&count)
	if err != nil {
		return SSHKey{}, err
	}
	if count > 0 {
		return SSHKey{}, fmt.Errorf("ssh key is already registered")
	}
	now := millis(s.clock())
	res, err := s.db.ExecContext(ctx, `INSERT INTO ssh_keys
		(user_id, fingerprint, key_type, public_key, comment, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		userID, parsed.Fingerprint, parsed.KeyType, trimmed, parsed.Comment, now)
	if err != nil {
		return SSHKey{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return SSHKey{}, err
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

// ListSSHKeys returns every key of a user, newest first.
func (s *Service) ListSSHKeys(ctx context.Context, userID int64) ([]SSHKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, fingerprint, key_type,
		public_key, comment, created_at, last_used_at
		FROM ssh_keys WHERE user_id = ? ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SSHKey
	for rows.Next() {
		var key SSHKey
		if err := scanSSHKey(rows, &key); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// RemoveSSHKey deletes one own key by row id.
func (s *Service) RemoveSSHKey(ctx context.Context, userID, id int64, ip string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM ssh_keys WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "ssh.remove", Target: "ssh_key:id", IP: ip, Result: "ok"})
	return true, nil
}

// LookupSSHKey resolves a fingerprint to its key and active owner.
func (s *Service) LookupSSHKey(ctx context.Context, fingerprint string) (SSHKey, User, error) {
	var key SSHKey
	err := scanSSHKey(s.db.QueryRowContext(ctx, `SELECT id, user_id, fingerprint, key_type,
		public_key, comment, created_at, last_used_at
		FROM ssh_keys WHERE fingerprint = ?`, fingerprint), &key)
	if err != nil {
		return SSHKey{}, User{}, fmt.Errorf("unknown ssh key")
	}
	user, err := s.GetUserByID(ctx, key.UserID)
	if err != nil {
		return SSHKey{}, User{}, fmt.Errorf("unknown ssh key")
	}
	if !user.IsActive {
		return SSHKey{}, User{}, fmt.Errorf("unknown ssh key")
	}
	return key, user, nil
}

// TouchSSHKey records key use at one-minute granularity.
func (s *Service) TouchSSHKey(ctx context.Context, id int64, now time.Time) {
	_, _ = s.db.ExecContext(ctx, `UPDATE ssh_keys SET last_used_at = ?
		WHERE id = ? AND (last_used_at IS NULL OR last_used_at <= ?)`,
		now.UnixMilli(), id, now.Add(-sshTouchGranularity).UnixMilli())
}

// AuditSSHUnknownKey records a rejected handshake fingerprint.
func (s *Service) AuditSSHUnknownKey(ctx context.Context, fingerprint, ip string) {
	s.recordAudit(ctx, AuditEvent{Action: "ssh.unknown_key", Target: "ssh_key:" + fingerprint, IP: ip, Result: "denied"})
}
