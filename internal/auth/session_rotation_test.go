package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/store"
)

func TestVerifyTOTPRevocationKeepsSecondFactorState(t *testing.T) {
	scenarios := []struct {
		factor   string
		verified bool
	}{
		{factor: "enrollment"},
		{factor: "counter"},
		{factor: "recovery"},
		{factor: "recovery", verified: true},
		{factor: "promotion", verified: true},
	}
	for _, scenario := range scenarios {
		name := scenario.factor
		if scenario.verified {
			name += "_verified"
		}
		t.Run(name, func(t *testing.T) {
			for _, entrypoint := range []string{"secret", "rowid"} {
				t.Run(entrypoint, func(t *testing.T) {
					svc, setNow := openTestService(t)
					svc.SetSealKey(testSealKey())
					ctx := t.Context()
					now := time.Unix(1754000000, 0).UTC()
					setNow(now)
					u := mustCreate(t, svc, "atomic", randomPassword(t, 16), false)
					enrollment, enrollErr := svc.EnrollTOTP(ctx, u.ID, "127.0.0.1")
					if enrollErr != nil {
						t.Fatal(enrollErr)
					}
					raw, decodeErr := base32NoPad.DecodeString(enrollment.SecretBase32)
					if decodeErr != nil {
						t.Fatal(decodeErr)
					}
					secret, _, mintErr := svc.MintSession(ctx, u.ID, fullPasswordLevel, "127.0.0.1", "bootstrap")
					if mintErr != nil {
						t.Fatal(mintErr)
					}
					var codes []string
					if scenario.factor != "enrollment" {
						initial, initialErr := svc.VerifyTOTP(ctx, VerifyTOTPInput{
							SessionSecret: secret, Code: TOTPCode(raw, TOTPCounter(now)),
							IP: "127.0.0.1", UserAgent: "bootstrap",
						})
						if initialErr != nil {
							t.Fatal(initialErr)
						}
						if len(initial.RecoveryCodes) != recoveryCodeCount {
							t.Fatalf("initial enrollment returned %d recovery codes", len(initial.RecoveryCodes))
						}
						secret, codes = initial.Secret, initial.RecoveryCodes
						now = now.Add(time.Minute)
						setNow(now)
						if !scenario.verified {
							var pendingErr error
							secret, _, pendingErr = svc.MintSession(ctx, u.ID, pendingPasswordLevel, "127.0.0.1", "candidate")
							if pendingErr != nil {
								t.Fatal(pendingErr)
							}
						}
					}
					if scenario.factor == "promotion" {
						pending, pendingErr := svc.EnrollTOTP(ctx, u.ID, "127.0.0.1")
						if pendingErr != nil {
							t.Fatal(pendingErr)
						}
						var pendingDecodeErr error
						raw, pendingDecodeErr = base32NoPad.DecodeString(pending.SecretBase32)
						if pendingDecodeErr != nil {
							t.Fatal(pendingDecodeErr)
						}
					}
					code := TOTPCode(raw, TOTPCounter(now))
					if scenario.factor == "recovery" {
						code = codes[0]
					}
					before, beforeErr := svc.store.GetTOTPCredentials(ctx, u.ID)
					if beforeErr != nil {
						t.Fatal(beforeErr)
					}
					userBefore, userBeforeErr := svc.store.GetAuthUserByID(ctx, u.ID)
					if userBeforeErr != nil {
						t.Fatal(userBeforeErr)
					}
					row, rowErr := svc.store.GetSessionByHash(ctx, hashBearer(secret))
					if rowErr != nil {
						t.Fatal(rowErr)
					}
					revokeAt := 2
					if entrypoint == "rowid" {
						revokeAt = 1
					}
					clockCalls := 0
					revoked := false
					svc.now = func() time.Time {
						clockCalls++
						if clockCalls == revokeAt {
							if _, revokeErr := svc.store.DeleteSessionByHash(ctx, row.IDHash); revokeErr != nil {
								t.Fatal(revokeErr)
							}
							revoked = true
						}
						return now
					}
					var out VerifyTOTPResult
					var verifyErr error
					if entrypoint == "rowid" {
						out, verifyErr = svc.VerifyTOTPByRowID(ctx, u.ID, row.RowID, code, "127.0.0.1", "candidate")
					} else {
						out, verifyErr = svc.VerifyTOTP(ctx, VerifyTOTPInput{
							SessionSecret: secret, Code: code, IP: "127.0.0.1", UserAgent: "candidate",
						})
					}
					svc.now = func() time.Time { return now }
					if !revoked || !errors.Is(verifyErr, errInvalidCredentials) {
						t.Fatalf("revoked = %v, verification error = %v", revoked, verifyErr)
					}
					if out.Secret != "" || len(out.RecoveryCodes) != 0 || out.Session != (Session{}) {
						t.Fatalf("failed verification returned credentials: %+v", out)
					}
					after, afterErr := svc.store.GetTOTPCredentials(ctx, u.ID)
					if afterErr != nil {
						t.Fatal(afterErr)
					}
					if after != before {
						t.Fatalf("rejected verification changed credentials: before %+v, after %+v", before, after)
					}
					userAfter, userAfterErr := svc.store.GetAuthUserByID(ctx, u.ID)
					if userAfterErr != nil {
						t.Fatal(userAfterErr)
					}
					if userAfter.TOTPEnabled != userBefore.TOTPEnabled {
						t.Fatal("rejected verification changed enrollment")
					}
					if _, lookupErr := svc.store.GetSessionByHash(ctx, row.IDHash); !errors.Is(lookupErr, store.ErrNotFound) {
						t.Fatalf("revoked session lookup = %v", lookupErr)
					}
					retrySecret, _, retryMintErr := svc.MintSession(ctx, u.ID, pendingPasswordLevel, "127.0.0.1", "retry")
					if retryMintErr != nil {
						t.Fatal(retryMintErr)
					}
					retry, retryErr := svc.VerifyTOTP(ctx, VerifyTOTPInput{
						SessionSecret: retrySecret, Code: code, IP: "127.0.0.1", UserAgent: "retry",
					})
					if retryErr != nil || !retry.Session.TOTPOk {
						t.Fatalf("fresh session could not reuse the rejected proof: %+v, %v", retry, retryErr)
					}
					if scenario.factor == "recovery" && !retry.RecoveryUsed {
						t.Fatal("recovery proof was not consumed by the successful retry")
					}
					if (scenario.factor == "enrollment" || scenario.factor == "promotion") &&
						len(retry.RecoveryCodes) != recoveryCodeCount {
						t.Fatalf("successful enrollment returned %d recovery codes", len(retry.RecoveryCodes))
					}
				})
			}
		})
	}
}

func TestVerifyTOTPRejectsChangedCredentialSnapshot(t *testing.T) {
	svc, setNow := openTestService(t)
	svc.SetSealKey(testSealKey())
	ctx := t.Context()
	now := time.Unix(1754000000, 0).UTC()
	setNow(now)
	u := mustCreate(t, svc, "changed", randomPassword(t, 16), false)
	enrollment, enrollErr := svc.EnrollTOTP(ctx, u.ID, "127.0.0.1")
	if enrollErr != nil {
		t.Fatal(enrollErr)
	}
	original, decodeErr := base32NoPad.DecodeString(enrollment.SecretBase32)
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	replacement := []byte("01234567890123456789")
	sealed, sealErr := svc.seal(replacement)
	if sealErr != nil {
		t.Fatal(sealErr)
	}
	secret, _, mintErr := svc.MintSession(ctx, u.ID, fullPasswordLevel, "127.0.0.1", "browser")
	if mintErr != nil {
		t.Fatal(mintErr)
	}
	clockCalls := 0
	svc.now = func() time.Time {
		clockCalls++
		if clockCalls == 2 {
			if stageErr := svc.store.StageTOTPSecret(ctx, u.ID, sealed, millis(now)); stageErr != nil {
				t.Fatal(stageErr)
			}
		}
		return now
	}
	_, verifyErr := svc.VerifyTOTP(ctx, VerifyTOTPInput{
		SessionSecret: secret, Code: TOTPCode(original, TOTPCounter(now)), IP: "127.0.0.1", UserAgent: "browser",
	})
	svc.now = func() time.Time { return now }
	if !errors.Is(verifyErr, errInvalidCredentials) {
		t.Fatalf("stale credential proof returned %v", verifyErr)
	}
	current, currentErr := svc.store.GetTOTPCredentials(ctx, u.ID)
	if currentErr != nil {
		t.Fatal(currentErr)
	}
	if current.Secret != sealed || current.HasVerified || current.LastCounter != -1 {
		t.Fatalf("stale proof changed replacement credentials: %+v", current)
	}
	out, freshErr := svc.VerifyTOTP(ctx, VerifyTOTPInput{
		SessionSecret: secret, Code: TOTPCode(replacement, TOTPCounter(now)), IP: "127.0.0.1", UserAgent: "browser",
	})
	if freshErr != nil || len(out.RecoveryCodes) != recoveryCodeCount {
		t.Fatalf("replacement proof failed: %+v, %v", out, freshErr)
	}
}
