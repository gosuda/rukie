package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

func openTestService(t *testing.T) (*Service, func(time.Time)) {
	t.Helper()
	path := t.TempDir() + "/auth.sqlite"
	st, err := store.Open(t.Context(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	svc := New(st)
	t.Cleanup(func() {
		_ = st.Close(context.Background())
	})
	current := time.Now()
	svc.now = func() time.Time { return current }
	return svc, func(ts time.Time) { current = ts }
}

func randomPassword(t *testing.T, n int) string {
	t.Helper()
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return "Aa1!" + base64.RawURLEncoding.EncodeToString(raw)[:n]
}

func mustScopes(t *testing.T, names []string) op.Scopes {
	t.Helper()
	scopes, err := ParseScopeSet(names)
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	return scopes
}

func mustCreate(t *testing.T, svc *Service, name, password string, admin bool) User {
	t.Helper()
	u, err := svc.CreateUser(t.Context(), 0, CreateUserInput{Name: name, Password: password, IsAdmin: admin}, "127.0.0.1")
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return u
}

func TestArgon2idParams(t *testing.T) {
	pw := randomPassword(t, 16)
	enc, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("params prefix = %q", enc[:40])
	}
	parts := strings.Split(enc, "$")
	if len(parts) != 6 {
		t.Fatalf("phc segments = %d", len(parts))
	}
	salt, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(parts[4], "="))
	if err != nil || len(salt) != 16 {
		t.Fatalf("salt len = %d, err = %v", len(salt), err)
	}
	hash, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(parts[5], "="))
	if err != nil || len(hash) != 32 {
		t.Fatalf("hash len = %d, err = %v", len(hash), err)
	}
	if strings.Contains(parts[4], "=") || strings.Contains(parts[5], "=") {
		t.Fatalf("encoder emitted padding")
	}
	ok, err := VerifyPassword(pw, enc)
	if err != nil || !ok {
		t.Fatalf("verify self = %v, %v", ok, err)
	}
	ok, err = VerifyPassword(pw+"x", enc)
	if err != nil || ok {
		t.Fatalf("verify wrong = %v, %v", ok, err)
	}
	padded := parts[0] + "$" + parts[1] + "$" + parts[2] + "$" + parts[3] + "$" +
		base64.StdEncoding.EncodeToString(salt) + "$" + base64.StdEncoding.EncodeToString(hash)
	ok, err = VerifyPassword(pw, padded)
	if err != nil || !ok {
		t.Fatalf("verify padded = %v, %v", ok, err)
	}
	if NeedsRehash(enc) {
		t.Fatal("fresh hash needs rehash")
	}
	weak := strings.Replace(enc, "m=19456,t=2,p=1", "m=4096,t=3,p=1", 1)
	if !NeedsRehash(weak) {
		t.Fatal("weak params do not need rehash")
	}
	if _, err := VerifyPassword(pw, "not-a-hash"); err == nil {
		t.Fatal("malformed hash verified")
	}
	for _, evil := range []string{"m=4294967295,t=2,p=1", "m=19456,t=2,p=255", "m=19456,t=99,p=1"} {
		crafted := strings.Replace(enc, "m=19456,t=2,p=1", evil, 1)
		if _, err := VerifyPassword(pw, crafted); err == nil {
			t.Fatalf("unsafe params %q verified", evil)
		}
		if !NeedsRehash(crafted) {
			t.Fatalf("unsafe params %q look current", evil)
		}
	}
	if BlocklistSize() < 10000 {
		t.Fatalf("blocklist = %d, want >= 10000", BlocklistSize())
	}
	if err := CheckPasswordPolicy("password", "someone"); err == nil {
		t.Fatal("blocklisted password accepted")
	}
	if err := CheckPasswordPolicy("short1!Aa", "someone"); err == nil {
		t.Fatal("11-char password accepted")
	}
	if err := CheckPasswordPolicy(randomPassword(t, 16), "someone"); err != nil {
		t.Fatalf("16-char password rejected: %v", err)
	}
	if err := CheckPasswordPolicy(strings.Repeat("x", 513), "someone"); err == nil {
		t.Fatal("513-byte password accepted")
	}
	if err := CheckPasswordPolicy("ExactlyTwelve", "exactlytwelve"); err == nil {
		t.Fatal("password equal to username accepted")
	}
}

func TestPasswordLockout(t *testing.T) {
	svc, setClock := openTestService(t)
	ctx := t.Context()
	start := time.Now()
	setClock(start)
	good := randomPassword(t, 16)
	bad := randomPassword(t, 16)
	mustCreate(t, svc, "ada", good, false)
	try := func(pw string) error {
		_, err := svc.Login(ctx, LoginInput{Username: "ada", Password: pw, IP: "127.0.0.1"})
		return err
	}
	for i := range 4 {
		if err := try(bad); err == nil {
			t.Fatalf("bad login %d accepted", i+1)
		}
	}
	windowed := randomPassword(t, 16)
	mustCreate(t, svc, "window", windowed, false)
	wtry := func(pw string) error {
		_, err := svc.Login(ctx, LoginInput{Username: "window", Password: pw, IP: "127.0.0.1"})
		return err
	}
	for range 4 {
		if err := wtry("wrongpassword1!"); err == nil {
			t.Fatal("windowed bad login accepted")
		}
	}
	setClock(start.Add(11 * time.Minute))
	if err := wtry("wrongpassword1!"); err == nil {
		t.Fatal("windowed bad login accepted")
	}
	setClock(start)
	wu, _, _ := svc.authUserByName(ctx, "window")
	if wu.LockedUntil != 0 {
		t.Fatalf("aged failures locked: %d", wu.LockedUntil)
	}
	if _, err := svc.Login(ctx, LoginInput{Username: "ada", Password: good, IP: "127.0.0.1"}); err != nil {
		t.Fatalf("login after 4 failures: %v", err)
	}
	for i := range 5 {
		if err := try(bad); err == nil {
			t.Fatalf("bad login %d accepted", i+1)
		}
	}
	u, _, err := svc.authUserByName(ctx, "ada")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if u.LockedUntil != millis(start.Add(15*time.Minute)) {
		t.Fatalf("locked_until = %d, want start+15m", u.LockedUntil)
	}
	if err := try(good); err == nil {
		t.Fatal("correct password accepted while locked")
	}
	if _, err := svc.Login(ctx, LoginInput{Username: "nobody", Password: good, IP: "127.0.0.1"}); err == nil {
		t.Fatal("unknown user accepted")
	} else if err.Error() != "Invalid username or password." {
		t.Fatalf("unknown user error = %q", err.Error())
	}
	if err := try(bad); err == nil {
		t.Fatal("accepted while locked")
	} else if err.Error() != "Invalid username or password." {
		t.Fatalf("locked error = %q", err.Error())
	}
	u2, _, _ := svc.authUserByName(ctx, "ada")
	if u2.LockedUntil <= u.LockedUntil {
		t.Fatalf("lock did not double: %d -> %d", u.LockedUntil, u2.LockedUntil)
	}
	if err := try(good); err == nil {
		t.Fatal("correct password accepted while lock persists")
	}
	setClock(start.Add(31 * time.Minute))
	if _, err := svc.Login(ctx, LoginInput{Username: "ada", Password: good, IP: "127.0.0.1"}); err != nil {
		t.Fatalf("login after lock expiry: %v", err)
	}
	for range 30 {
		latest, _, _ := svc.authUserByName(ctx, "ada")
		svc.applyLockout(ctx, latest, millis(svc.clock()))
	}
	u3, _, _ := svc.authUserByName(ctx, "ada")
	if u3.LockedUntil-millis(svc.clock()) > 24*60*60*1000 {
		t.Fatalf("lock exceeds 24h cap: %d", u3.LockedUntil)
	}
}

func TestMustChangePasswordPendingPrincipal(t *testing.T) {
	svc, _ := openTestService(t)
	svc.SetSealKey(testSealKey())
	u, password, err := svc.CreateUserRandom(t.Context(), 0, CreateUserRandomInput{Name: "must-change", IsAdmin: false}, "127.0.0.1")
	if err != nil {
		t.Fatalf("create temporary user: %v", err)
	}
	now := time.Now().UTC()
	enrollment, err := svc.EnrollTOTP(t.Context(), u.ID, "127.0.0.1")
	if err != nil {
		t.Fatalf("enroll totp: %v", err)
	}
	secret, err := base32NoPad.DecodeString(enrollment.SecretBase32)
	if err != nil {
		t.Fatalf("decode enrollment secret: %v", err)
	}
	setup, err := svc.Login(t.Context(), LoginInput{Username: u.Name, Password: password, IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("setup login: %v", err)
	}
	if _, verifyErr := svc.VerifyTOTP(t.Context(), VerifyTOTPInput{SessionSecret: setup.Secret, Code: TOTPCode(secret, TOTPCounter(now)), IP: "127.0.0.1"}); verifyErr != nil {
		t.Fatalf("promote totp: %v", verifyErr)
	}
	login, err := svc.Login(t.Context(), LoginInput{Username: u.Name, Password: password, IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	principal, err := NewAuthenticator(svc).Session(t.Context(), login.Secret)
	if err != nil {
		t.Fatalf("session principal: %v", err)
	}
	if !principal.MustChangePassword || !principal.Grants.Has(op.ScopeRepoRead) {
		t.Fatalf("pending principal = %+v, want mandatory password-change repo scope", principal)
	}
	if principal.Grants.Has(op.ScopeRepoWrite) || principal.Grants.Has(op.ScopeAdminWrite) {
		t.Fatalf("pending principal has write grants: %v", principal.Grants)
	}
}

func TestFreshAuthWindow(t *testing.T) {
	svc, setClock := openTestService(t)
	ctx := t.Context()
	start := time.Now()
	setClock(start)
	pw := randomPassword(t, 16)
	u := mustCreate(t, svc, "ada", pw, false)
	res, err := svc.Login(ctx, LoginInput{Username: "ada", Password: pw, IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	_ = u
	auther := NewAuthenticator(svc)
	sessPrincipal, err := auther.Session(ctx, res.Secret)
	if err != nil {
		t.Fatalf("session principal: %v", err)
	}
	if _, freshErr := RequireFresh(ctx, svc, sessPrincipal, start); freshErr != nil {
		t.Fatalf("fresh session rejected: %v", freshErr)
	}
	setClock(start.Add(14 * time.Minute))
	if _, freshErr := RequireFresh(ctx, svc, sessPrincipal, start.Add(14*time.Minute)); freshErr != nil {
		t.Fatalf("14-minute session rejected: %v", freshErr)
	}
	setClock(start.Add(16 * time.Minute))
	if _, freshErr := RequireFresh(ctx, svc, sessPrincipal, start.Add(16*time.Minute)); freshErr == nil {
		t.Fatal("16-minute session accepted as fresh")
	} else if freshErr.Error() != "fresh authentication required" {
		t.Fatalf("stale error = %q", freshErr.Error())
	}
	minted, err := svc.CreateToken(ctx, CreateTokenInput{UserID: u.ID, Name: "t", Scopes: mustScopes(t, []string{"repo:read"})})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	tokPrincipal, err := auther.Bearer(ctx, minted.Secret)
	if err != nil {
		t.Fatalf("token principal: %v", err)
	}
	if _, err := RequireFresh(ctx, svc, tokPrincipal, start); err == nil {
		t.Fatal("token accepted as session")
	}
	if _, _, err := RequireSession(tokPrincipal); err == nil {
		t.Fatal("token accepted by RequireSession")
	}
}

func TestSessionCookieHostPrefix(t *testing.T) {
	now := time.Now()
	c := SessionCookie("secret-value", now.Add(30*24*time.Hour), now)
	if !strings.HasPrefix(c.Name, "__Host-") {
		t.Fatalf("cookie name = %q", c.Name)
	}
	if !c.Secure || !c.HttpOnly || c.Path != "/" {
		t.Fatalf("cookie flags = %+v", c)
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("samesite = %v", c.SameSite)
	}
	if c.MaxAge <= 0 {
		t.Fatalf("maxage = %d", c.MaxAge)
	}
	plain := SessionCookie("x", now.Add(-time.Hour), now)
	if plain.MaxAge != 0 {
		t.Fatalf("expired maxage = %d", plain.MaxAge)
	}
	cleared := ClearSessionCookie()
	if !strings.HasPrefix(cleared.Name, "__Host-") || cleared.MaxAge >= 0 {
		t.Fatalf("clear cookie = %+v", cleared)
	}
	if SessionCookieName() != c.Name {
		t.Fatalf("name constant = %q", SessionCookieName())
	}
}

func TestBootstrapRefusesSecondAdmin(t *testing.T) {
	svc, _ := openTestService(t)
	ctx := t.Context()
	pw := randomPassword(t, 16)
	if _, err := svc.BootstrapAdmin(ctx, "ada", pw, "127.0.0.1"); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	if _, err := svc.BootstrapAdmin(ctx, "grace", randomPassword(t, 16), "127.0.0.1"); err == nil {
		t.Fatal("second bootstrap accepted")
	}
	racy, _ := openTestService(t)
	wins := make(chan bool, 2)
	go func() {
		_, err := racy.BootstrapAdmin(ctx, "racea", randomPassword(t, 16), "127.0.0.1")
		wins <- err == nil
	}()
	go func() {
		_, err := racy.BootstrapAdmin(ctx, "raceb", randomPassword(t, 16), "127.0.0.1")
		wins <- err == nil
	}()
	first, second := <-wins, <-wins
	if first == second {
		t.Fatal("concurrent bootstraps did not resolve to exactly one winner")
	}
	if _, err := svc.CreateUser(ctx, 0, CreateUserInput{Name: "ada", Password: randomPassword(t, 16)}, ""); err == nil {
		t.Fatal("duplicate username accepted")
	}
	if _, err := svc.CreateUser(ctx, 0, CreateUserInput{Name: "login", Password: randomPassword(t, 16)}, ""); err == nil {
		t.Fatal("reserved username accepted")
	}
	for _, squat := range []string{"llms.txt", "llms-full.txt"} {
		if _, err := svc.CreateUser(ctx, 0, CreateUserInput{Name: squat, Password: randomPassword(t, 16)}, ""); err == nil {
			t.Fatalf("route-squat username %q accepted", squat)
		}
	}
	if _, err := svc.CreateUser(ctx, 0, CreateUserInput{Name: "Bad Name!", Password: randomPassword(t, 16)}, ""); err == nil {
		t.Fatal("invalid username accepted")
	}
}

func TestPasswordFieldHasNoInlineFlag(t *testing.T) {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	src := RegisterSecretFlags(fs, "password")
	if fs.Lookup("password") != nil {
		t.Fatal("inline --password flag exists")
	}
	if fs.Lookup("password-stdin") == nil || fs.Lookup("password-file") == nil {
		t.Fatal("missing --password-stdin or --password-file")
	}
	if _, err := src.Resolve(strings.NewReader("")); err == nil {
		t.Fatal("empty sources resolved")
	}
	if err := fs.Set("password-stdin", "true"); err != nil {
		t.Fatalf("set stdin: %v", err)
	}
	if err := fs.Set("password-file", "/tmp/x"); err != nil {
		t.Fatalf("set file: %v", err)
	}
	if _, err := src.Resolve(strings.NewReader("secret")); err == nil {
		t.Fatal("dual sources resolved")
	}
	fs2 := flag.NewFlagSet("b2", flag.ContinueOnError)
	src2 := RegisterSecretFlags(fs2, "password")
	if err := fs2.Set("password-stdin", "true"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := src2.Resolve(strings.NewReader("hunter2\n"))
	if err != nil || got != "hunter2" {
		t.Fatalf("stdin resolve = %q, %v", got, err)
	}
	fs3 := flag.NewFlagSet("b3", flag.ContinueOnError)
	src3 := RegisterSecretFlags(fs3, "password")
	if setErr := fs3.Set("password-stdin", "true"); setErr != nil {
		t.Fatalf("set: %v", setErr)
	}
	if _, longErr := src3.Resolve(strings.NewReader(strings.Repeat("x", 513))); longErr == nil {
		t.Fatal("513-byte secret silently accepted")
	}
	exact, exactErr := src3.Resolve(strings.NewReader(strings.Repeat("y", 512) + "\n"))
	if exactErr != nil || len(exact) != 512 {
		t.Fatalf("512-byte secret rejected: %v", len(exact))
	}
}
func TestCreateUserRandomIsAdmin(t *testing.T) {
	svc, _ := openTestService(t)
	ctx := t.Context()
	made, _, err := svc.CreateUserRandom(ctx, 0, CreateUserRandomInput{Name: "randadmin", IsAdmin: true}, "127.0.0.1")
	if err != nil {
		t.Fatalf("random admin: %v", err)
	}
	if !made.IsAdmin {
		t.Fatal("random+is_admin user is not an admin")
	}
	stored, err := svc.GetUserByID(ctx, made.ID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !stored.IsAdmin {
		t.Fatal("stored random user lost is_admin")
	}
	plain, _, err := svc.CreateUserRandom(ctx, 0, CreateUserRandomInput{Name: "randplain"}, "127.0.0.1")
	if err != nil {
		t.Fatalf("random plain: %v", err)
	}
	if plain.IsAdmin {
		t.Fatal("random non-admin user gained admin")
	}
}
func TestLoginKeepsSessionsAcrossUserAgents(t *testing.T) {
	svc, _ := openTestService(t)
	ctx := t.Context()
	pw := randomPassword(t, 16)
	u := mustCreate(t, svc, "multiua", pw, false)
	first, err := svc.Login(ctx, LoginInput{Username: u.Name, Password: pw, IP: "127.0.0.1", UserAgent: "browser-a"})
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	second, err := svc.Login(ctx, LoginInput{Username: u.Name, Password: pw, IP: "127.0.0.1", UserAgent: "browser-b"})
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if _, _, err := svc.LookupSession(ctx, first.Secret); err != nil {
		t.Fatalf("first browser session wiped: %v", err)
	}
	if _, _, err := svc.LookupSession(ctx, second.Secret); err != nil {
		t.Fatalf("second browser session missing: %v", err)
	}
}
func TestDeleteSelfRefusesSoleOrgOwner(t *testing.T) {
	svc, _ := openTestService(t)
	ctx := t.Context()
	pw := randomPassword(t, 16)
	u := mustCreate(t, svc, "soleowner", pw, false)
	var orgID int64
	serr := svc.store.Write(ctx, func(tx *store.WriteTx) error {
		org, ierr := tx.InsertOrg(time.Now(), store.OrgInsert{Name: "acme", Visibility: "private"})
		if ierr != nil {
			return ierr
		}
		orgID = org.ID
		return tx.AddOwnersMember(orgID, u.ID)
	})
	if serr != nil {
		t.Fatalf("org setup: %v", serr)
	}
	match, err := svc.ETagFor(ctx, u.ID)
	if err != nil {
		t.Fatalf("etag: %v", err)
	}
	if err := svc.DeleteSelf(ctx, u.ID, match, "127.0.0.1"); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("sole owner delete err = %v, want owner refusal", err)
	}
	peer := mustCreate(t, svc, "coowner", randomPassword(t, 16), false)
	serr = svc.store.Write(ctx, func(tx *store.WriteTx) error {
		return tx.AddOwnersMember(orgID, peer.ID)
	})
	if serr != nil {
		t.Fatalf("second membership: %v", serr)
	}
	if err := svc.DeleteSelf(ctx, u.ID, match, "127.0.0.1"); err != nil {
		t.Fatalf("non-sole owner delete: %v", err)
	}
	if _, err := svc.GetUserByID(ctx, u.ID); err == nil {
		t.Fatal("user row survived self-delete")
	}
}
