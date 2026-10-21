package auth

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func applyMigrationFiles(t *testing.T, db *sql.DB) {
	t.Helper()
	entries, err := os.ReadDir("../store/migrations")
	if err != nil {
		t.Fatalf("migrations dir: %v", err)
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join("../store/migrations", e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatalf("apply %s: %v", e.Name(), err)
		}
	}
}

func openSSHKeyTestService(t *testing.T) *Service {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sshkeys.sqlite")
	raw, openErr := sql.Open("sqlite", "file:"+path+"?_txlock=immediate&_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_busy_timeout=5000")
	if openErr != nil {
		t.Fatalf("sql open: %v", openErr)
	}
	applyMigrationFiles(t, raw)
	if closeErr := raw.Close(); closeErr != nil {
		t.Fatalf("sql close: %v", closeErr)
	}
	svc, err := Open(path)
	if err != nil {
		t.Fatalf("auth open: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func mustUser(t *testing.T, svc *Service, name string, admin bool) User {
	t.Helper()
	u, err := svc.CreateUser(t.Context(), 0, CreateUserInput{Name: name, Email: name + "@example.test", Password: "long-test-password-01", IsAdmin: admin}, "127.0.0.1")
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return u
}

func ed25519Line(t *testing.T, comment string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return strings.TrimSpace(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " " + comment)
}

func ecdsaLine(t *testing.T, curve elliptic.Curve) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
}

func rsaLine(t *testing.T, bits int) string {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatalf("generate %d: %v", bits, err)
	}
	sshPub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
}

type stubKey struct {
	keyType string
}

func (k stubKey) Type() string { return k.keyType }

func (k stubKey) Marshal() []byte { return []byte(k.keyType) }

func (k stubKey) Verify(_ []byte, _ *ssh.Signature) error { return nil }

func TestParseSSHPublicKeyAcceptsAllowedTypes(t *testing.T) {
	lines := map[string]string{
		"ssh-ed25519":         ed25519Line(t, "ada-laptop"),
		"ecdsa-sha2-nistp256": ecdsaLine(t, elliptic.P256()),
		"ecdsa-sha2-nistp384": ecdsaLine(t, elliptic.P384()),
		"ecdsa-sha2-nistp521": ecdsaLine(t, elliptic.P521()),
		"ssh-rsa":             rsaLine(t, 3072),
	}
	for want, line := range lines {
		parsed, err := ParseSSHPublicKey(line)
		if err != nil {
			t.Fatalf("%s: %v", want, err)
		}
		if parsed.KeyType != want {
			t.Fatalf("type = %q, want %q", parsed.KeyType, want)
		}
		if !strings.HasPrefix(parsed.Fingerprint, "SHA256:") {
			t.Fatalf("fingerprint = %q", parsed.Fingerprint)
		}
	}
}

func TestParseSSHPublicKeyRejectsShapes(t *testing.T) {
	cases := map[string]string{
		"empty":   "",
		"spaces":  "   ",
		"junk":    "not a key",
		"options": "command=\"x\" " + ed25519Line(t, ""),
		"multi":   ed25519Line(t, "") + "\n" + ed25519Line(t, ""),
		"huge":    strings.Repeat("a", 16385),
		"cert":    strings.Replace(ed25519Line(t, ""), "ssh-ed25519", "ssh-ed25519-cert-v01@openssh.com", 1),
		"sk":      strings.Replace(ed25519Line(t, ""), "ssh-ed25519", "sk-ssh-ed25519@openssh.com", 1),
		"dss":     strings.Replace(ed25519Line(t, ""), "ssh-ed25519", "ssh-dss", 1),
		"rsa2048": rsaLine(t, 2048),
		"rsa3064": rsaLine(t, 3064),
	}
	for name, line := range cases {
		if _, err := ParseSSHPublicKey(line); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestParseSSHPublicKeyOversizeNamesLimit(t *testing.T) {
	if _, err := ParseSSHPublicKey(strings.Repeat("a", 16385)); err == nil || !strings.Contains(err.Error(), "16384") {
		t.Fatalf("oversize err = %v", err)
	}
}

func TestParseSSHPublicKeyCommentBoundaries(t *testing.T) {
	fields := strings.Fields(ed25519Line(t, ""))
	if len(fields) < 2 {
		t.Fatalf("key line has %d fields", len(fields))
	}
	prefix := fields[0] + " " + fields[1] + " "
	if _, err := ParseSSHPublicKey(prefix + strings.Repeat("c", 512)); err != nil {
		t.Fatalf("512-char comment: %v", err)
	}
	if _, err := ParseSSHPublicKey(prefix + strings.Repeat("c", 513)); err == nil {
		t.Fatal("513-char comment accepted")
	}
}

func TestCheckSSHKeyTypeRejectsPolicy(t *testing.T) {
	for _, typ := range []string{
		"ssh-dss",
		"sk-ssh-ed25519@openssh.com",
		"sk-ecdsa-sha2-nistp256@openssh.com",
		"ssh-ed25519-cert-v01@openssh.com",
		"ssh-rsa-cert-v01@openssh.com",
		"ssh-mldsa-44",
	} {
		if err := checkSSHKeyType(stubKey{keyType: typ}); err == nil {
			t.Fatalf("%s accepted", typ)
		}
	}
}

func TestAddSSHKeyRoundTrip(t *testing.T) {
	svc := openSSHKeyTestService(t)
	user := mustUser(t, svc, "ada", true)
	line := ed25519Line(t, "ada-laptop")
	key, err := svc.AddSSHKey(t.Context(), user.ID, line, "127.0.0.1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if key.UserID != user.ID || key.KeyType != "ssh-ed25519" || key.Comment != "ada-laptop" {
		t.Fatalf("key = %+v", key)
	}
	if key.PublicKey != strings.TrimSpace(line) || !strings.HasPrefix(key.Fingerprint, "SHA256:") {
		t.Fatalf("stored = %+v", key)
	}
	if key.CreatedAt <= 0 || key.ID == 0 {
		t.Fatalf("identity = %+v", key)
	}
	other := mustUser(t, svc, "bob", false)
	if _, err := svc.AddSSHKey(t.Context(), other.ID, line, "127.0.0.1"); err == nil {
		t.Fatal("duplicate fingerprint accepted for a second user")
	} else if !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate err = %v", err)
	}
	if _, err := svc.AddSSHKey(t.Context(), 999999, ed25519Line(t, ""), "127.0.0.1"); err == nil {
		t.Fatal("orphan key accepted")
	}
}

func TestListSSHKeysNewestFirst(t *testing.T) {
	svc := openSSHKeyTestService(t)
	user := mustUser(t, svc, "ada", true)
	first, err := svc.AddSSHKey(t.Context(), user.ID, ed25519Line(t, "one"), "127.0.0.1")
	if err != nil {
		t.Fatalf("add one: %v", err)
	}
	second, err := svc.AddSSHKey(t.Context(), user.ID, ed25519Line(t, "two"), "127.0.0.1")
	if err != nil {
		t.Fatalf("add two: %v", err)
	}
	keys, err := svc.ListSSHKeys(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 2 || keys[0].ID != second.ID || keys[1].ID != first.ID {
		t.Fatalf("order = %+v", keys)
	}
	other := mustUser(t, svc, "bob", false)
	keys, err = svc.ListSSHKeys(t.Context(), other.ID)
	if err != nil {
		t.Fatalf("list other: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("cross-user leak = %+v", keys)
	}
}

func TestRemoveSSHKeyOwnership(t *testing.T) {
	svc := openSSHKeyTestService(t)
	user := mustUser(t, svc, "ada", true)
	other := mustUser(t, svc, "bob", false)
	key, err := svc.AddSSHKey(t.Context(), user.ID, ed25519Line(t, ""), "127.0.0.1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if ok, err := svc.RemoveSSHKey(t.Context(), other.ID, key.ID, "127.0.0.1"); err != nil || ok {
		t.Fatalf("cross-user remove = %v, %v", ok, err)
	}
	if ok, err := svc.RemoveSSHKey(t.Context(), user.ID, key.ID, "127.0.0.1"); err != nil || !ok {
		t.Fatalf("own remove = %v, %v", ok, err)
	}
	if ok, err := svc.RemoveSSHKey(t.Context(), user.ID, key.ID, "127.0.0.1"); err != nil || ok {
		t.Fatalf("repeat remove = %v, %v", ok, err)
	}
}

func TestLookupSSHKeyUnknownAndInactive(t *testing.T) {
	svc := openSSHKeyTestService(t)
	admin := mustUser(t, svc, "ada", true)
	user := mustUser(t, svc, "bob", false)
	key, err := svc.AddSSHKey(t.Context(), user.ID, ed25519Line(t, ""), "127.0.0.1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	found, owner, err := svc.LookupSSHKey(t.Context(), key.Fingerprint)
	if err != nil || found.ID != key.ID || owner.ID != user.ID {
		t.Fatalf("lookup = %+v, %+v, %v", found, owner, err)
	}
	if _, _, lookupErr := svc.LookupSSHKey(t.Context(), "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); lookupErr == nil {
		t.Fatal("unknown fingerprint resolved")
	}
	tag, err := svc.ETagFor(user.ID)
	if err != nil {
		t.Fatalf("etag: %v", err)
	}
	inactive := false
	if _, err := svc.UpdateUser(t.Context(), admin.ID, UpdateUserInput{Target: user.Name, IsActive: &inactive, IfMatch: tag}, "127.0.0.1"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if _, _, err := svc.LookupSSHKey(t.Context(), key.Fingerprint); err == nil {
		t.Fatal("inactive owner resolved")
	}
}

func TestTouchSSHKeyGranularity(t *testing.T) {
	svc := openSSHKeyTestService(t)
	user := mustUser(t, svc, "ada", true)
	key, err := svc.AddSSHKey(t.Context(), user.ID, ed25519Line(t, ""), "127.0.0.1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	base := time.UnixMilli(key.CreatedAt).Add(time.Minute)
	svc.TouchSSHKey(t.Context(), key.ID, base)
	found, _, err := svc.LookupSSHKey(t.Context(), key.Fingerprint)
	if err != nil || found.LastUsedAt != base.UnixMilli() {
		t.Fatalf("touch = %+v, %v", found, err)
	}
	svc.TouchSSHKey(t.Context(), key.ID, base.Add(30*time.Second))
	found, _, err = svc.LookupSSHKey(t.Context(), key.Fingerprint)
	if err != nil || found.LastUsedAt != base.UnixMilli() {
		t.Fatalf("sub-minute touch rewrote %+v", found)
	}
	svc.TouchSSHKey(t.Context(), key.ID, base.Add(61*time.Second))
	found, _, err = svc.LookupSSHKey(t.Context(), key.Fingerprint)
	if err != nil || found.LastUsedAt != base.Add(61*time.Second).UnixMilli() {
		t.Fatalf("minute touch = %+v", found)
	}
}

func TestAuditSSHUnknownKeyRow(t *testing.T) {
	svc := openSSHKeyTestService(t)
	fp := "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	svc.AuditSSHUnknownKey(t.Context(), fp, "127.0.0.1")
	var count int64
	err := svc.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_events WHERE action = 'ssh.unknown_key' AND target_type = 'ssh_key' AND target_id = ?`, fp).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("audit rows = %d, %v", count, err)
	}
}
