package auth

import (
	"strings"
	"testing"
	"time"
)

func testSealKey() []byte {
	return []byte("0123456789abcdef0123456789abcdef")
}

func enrollEnabled(t *testing.T, svc *Service, u User, now time.Time) []string {
	t.Helper()
	enr, err := svc.EnrollTOTP(t.Context(), u.ID, "127.0.0.1")
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if enr.SecretBase32 == "" || !strings.HasPrefix(enr.URI, "otpauth://totp/rukie:") {
		t.Fatalf("enrollment shape = %+v", enr)
	}
	if strings.Contains(enr.URI, " ") || !strings.Contains(enr.URI, "algorithm=SHA1") {
		t.Fatalf("enrollment uri = %q", enr.URI)
	}
	secret, err := base32NoPad.DecodeString(enr.SecretBase32)
	if err != nil || len(secret) != totpSecretBytes {
		t.Fatalf("secret decode len = %d, err = %v", len(secret), err)
	}
	code := TOTPCode(secret, TOTPCounter(now))
	res, err := svc.Login(t.Context(), LoginInput{Username: u.Name, Password: testPasswords[u.Name], IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	out, err := svc.VerifyTOTP(t.Context(), VerifyTOTPInput{SessionSecret: res.Secret, Code: code, IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(out.RecoveryCodes) != recoveryCodeCount {
		t.Fatalf("recovery codes = %d", len(out.RecoveryCodes))
	}
	return out.RecoveryCodes
}

var testPasswords = map[string]string{}

func mustCreateWithPassword(t *testing.T, svc *Service, name string, admin bool) User {
	t.Helper()
	pw := randomPassword(t, 16)
	u := mustCreate(t, svc, name, pw, admin)
	testPasswords[name] = pw
	return u
}

func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	vectors := []struct {
		want string
		unix int64
	}{
		{unix: 59, want: "287082"},
		{unix: 1111111109, want: "081804"},
		{unix: 1111111111, want: "050471"},
		{unix: 1234567890, want: "005924"},
		{unix: 2000000000, want: "279037"},
		{unix: 20000000000, want: "353130"},
	}
	for _, v := range vectors {
		at := time.Unix(v.unix, 0).UTC()
		if got := TOTPCode(secret, TOTPCounter(at)); got != v.want {
			t.Fatalf("TOTPCode(%d) = %q, want %q", v.unix, got, v.want)
		}
		if _, ok := TOTPCheck(secret, v.want, at, -1); !ok {
			t.Fatalf("TOTPCheck(%d) rejected RFC vector %q", v.unix, v.want)
		}
	}
	at := time.Unix(1111111109, 0).UTC()
	adjacent := TOTPCode(secret, TOTPCounter(at))
	if _, ok := TOTPCheck(secret, adjacent, at.Add(30*time.Second), -1); !ok {
		t.Fatal("window rejected the +1 step code")
	}
	if _, ok := TOTPCheck(secret, adjacent, at.Add(-30*time.Second), -1); !ok {
		t.Fatal("window rejected the -1 step code")
	}
	if _, ok := TOTPCheck(secret, adjacent, at.Add(60*time.Second), -1); ok {
		t.Fatal("window accepted the +2 step code")
	}
	if _, ok := TOTPCheck(secret, adjacent, at.Add(-60*time.Second), -1); ok {
		t.Fatal("window accepted the -2 step code")
	}
}

func TestTOTPReplayRejected(t *testing.T) {
	svc, setNow := openTestService(t)
	svc.SetSealKey(testSealKey())
	now := time.Unix(1754000000, 0).UTC()
	setNow(now)
	u := mustCreateWithPassword(t, svc, "replay", false)
	enr, err := svc.EnrollTOTP(t.Context(), u.ID, "127.0.0.1")
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	secret, _ := base32NoPad.DecodeString(enr.SecretBase32)
	bootstrap, err := svc.Login(t.Context(), LoginInput{Username: u.Name, Password: testPasswords[u.Name], IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if bootstrap.TOTPRequired {
		t.Fatal("login parked before enrollment completed")
	}
	code := TOTPCode(secret, TOTPCounter(now))
	if _, verr := svc.VerifyTOTP(t.Context(), VerifyTOTPInput{SessionSecret: bootstrap.Secret, Code: code, IP: "127.0.0.1"}); verr != nil {
		t.Fatalf("first verify: %v", verr)
	}
	res, err := svc.Login(t.Context(), LoginInput{Username: u.Name, Password: testPasswords[u.Name], IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if !res.TOTPRequired {
		t.Fatal("login did not park on a second-factor challenge")
	}
	setNow(now.Add(31 * time.Second))
	next := TOTPCode(secret, TOTPCounter(now.Add(31*time.Second)))
	if _, verr := svc.VerifyTOTP(t.Context(), VerifyTOTPInput{SessionSecret: res.Secret, Code: next, IP: "127.0.0.1"}); verr != nil {
		t.Fatalf("second verify: %v", verr)
	}
	res2, err := svc.Login(t.Context(), LoginInput{Username: u.Name, Password: testPasswords[u.Name], IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if _, err := svc.VerifyTOTP(t.Context(), VerifyTOTPInput{SessionSecret: res2.Secret, Code: code, IP: "127.0.0.1"}); err == nil {
		t.Fatal("replayed counter accepted")
	}
	stale := TOTPCode(secret, TOTPCounter(now)-1)
	if _, err := svc.VerifyTOTP(t.Context(), VerifyTOTPInput{SessionSecret: res2.Secret, Code: stale, IP: "127.0.0.1"}); err == nil {
		t.Fatal("older counter accepted after a newer one")
	}
}

func TestTOTPLockoutKillsPending(t *testing.T) {
	svc, setNow := openTestService(t)
	svc.SetSealKey(testSealKey())
	now := time.Unix(1754000000, 0).UTC()
	setNow(now)
	u := mustCreateWithPassword(t, svc, "lockme", false)
	enrollEnabled(t, svc, u, now)
	res, err := svc.Login(t.Context(), LoginInput{Username: u.Name, Password: testPasswords[u.Name], IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	pending := res.Secret
	for i := range lockoutThreshold - 1 {
		if _, lerr := svc.Login(t.Context(), LoginInput{Username: u.Name, Password: "wrong-password", IP: "127.0.0.1"}); lerr == nil {
			t.Fatalf("bad login %d accepted", i)
		}
		if _, _, serr := svc.LookupSession(t.Context(), pending); serr != nil {
			t.Fatalf("pending challenge died at failure %d of %d", i+1, lockoutThreshold)
		}
	}
	if _, lerr := svc.Login(t.Context(), LoginInput{Username: u.Name, Password: "wrong-password", IP: "127.0.0.1"}); lerr == nil {
		t.Fatal("fifth bad login accepted")
	}
	fresh, _, err := svc.authUserByName(t.Context(), u.LowerName)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if fresh.LockedUntil <= millis(now) {
		t.Fatal("account is not locked at the fifth failure")
	}
	if _, _, err := svc.LookupSession(t.Context(), pending); err == nil {
		t.Fatal("pending challenge survived the lockout")
	}
	if _, err := svc.VerifyTOTP(t.Context(), VerifyTOTPInput{SessionSecret: pending, Code: "000000", IP: "127.0.0.1"}); err == nil {
		t.Fatal("verify on a killed challenge succeeded")
	}
}

func TestTOTPFailuresFeedLockout(t *testing.T) {
	svc, setNow := openTestService(t)
	svc.SetSealKey(testSealKey())
	now := time.Unix(1754000000, 0).UTC()
	setNow(now)
	u := mustCreateWithPassword(t, svc, "totplock", false)
	enrollEnabled(t, svc, u, now)
	res, err := svc.Login(t.Context(), LoginInput{Username: u.Name, Password: testPasswords[u.Name], IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	pending := res.Secret
	for i := range lockoutThreshold - 1 {
		if _, verr := svc.VerifyTOTP(t.Context(), VerifyTOTPInput{SessionSecret: pending, Code: "000000", IP: "127.0.0.1"}); verr == nil {
			t.Fatalf("bad code %d accepted", i)
		}
		if _, _, serr := svc.LookupSession(t.Context(), pending); serr != nil {
			t.Fatalf("pending challenge died at totp failure %d of %d", i+1, lockoutThreshold)
		}
	}
	if _, verr := svc.VerifyTOTP(t.Context(), VerifyTOTPInput{SessionSecret: pending, Code: "000000", IP: "127.0.0.1"}); verr == nil {
		t.Fatal("fifth bad code accepted")
	}
	fresh, _, err := svc.authUserByName(t.Context(), u.LowerName)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if fresh.LockedUntil <= millis(now) {
		t.Fatal("account is not locked at the fifth totp failure")
	}
	if _, _, err := svc.LookupSession(t.Context(), pending); err == nil {
		t.Fatal("pending challenge survived the totp-driven lockout")
	}
}

func TestRecoveryCodesSingleUse(t *testing.T) {
	svc, setNow := openTestService(t)
	svc.SetSealKey(testSealKey())
	now := time.Unix(1754000000, 0).UTC()
	setNow(now)
	u := mustCreateWithPassword(t, svc, "recover", false)
	codes := enrollEnabled(t, svc, u, now)
	res, err := svc.Login(t.Context(), LoginInput{Username: u.Name, Password: testPasswords[u.Name], IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	out, err := svc.VerifyTOTP(t.Context(), VerifyTOTPInput{SessionSecret: res.Secret, Code: codes[0], IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("first recovery use: %v", err)
	}
	if !out.RecoveryUsed {
		t.Fatal("recovery use not flagged")
	}
	if !out.Session.TOTPOk {
		t.Fatal("recovery use did not step up the session")
	}
	res2, err := svc.Login(t.Context(), LoginInput{Username: u.Name, Password: testPasswords[u.Name], IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if _, verr := svc.VerifyTOTP(t.Context(), VerifyTOTPInput{SessionSecret: res2.Secret, Code: codes[0], IP: "127.0.0.1"}); verr == nil {
		t.Fatal("used recovery code accepted twice")
	}
	if _, verr := svc.VerifyTOTP(t.Context(), VerifyTOTPInput{SessionSecret: res2.Secret, Code: codes[1], IP: "127.0.0.1"}); verr != nil {
		t.Fatalf("fresh recovery code rejected: %v", verr)
	}
	fresh, err := svc.RegenerateRecoveryCodes(t.Context(), u.ID, "127.0.0.1")
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	if len(fresh) != recoveryCodeCount {
		t.Fatalf("regenerated codes = %d", len(fresh))
	}
	res3, err := svc.Login(t.Context(), LoginInput{Username: u.Name, Password: testPasswords[u.Name], IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("third login: %v", err)
	}
	if _, err := svc.VerifyTOTP(t.Context(), VerifyTOTPInput{SessionSecret: res3.Secret, Code: codes[1], IP: "127.0.0.1"}); err == nil {
		t.Fatal("pre-regeneration code survived regeneration")
	}
}
