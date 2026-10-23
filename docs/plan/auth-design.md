# rukie Authentication & Session Design (`auth-design.md`)

Status: design for Phase F (auth). Replaces all Gitea auth (`rukie/services/auth/`, `rukie/services/auth/source/*`, `rukie/models/auth/`, `rukie/models/user/`, `rukie/modules/session/`, `rukie/modules/web/middleware/auth*`, OAuth2-provider, mail/SMTP, LDAP/PAM/SSPI/reverse-proxy/impersonate/httpsign callers — all cut per settled decisions). New module path `github.com/gosuda/rukie`; new code lives under `internal/auth/` + `internal/store/` (one store module hiding SQLite/Pebble/PostgreSQL). Zero-JS: every flow is a plain HTML form + redirect; no fetch/XHR, no WebAuthn, no JS PKCE helpers.

Settled constraints honored: argon2id + TOTP-or-OIDC for humans; agents use **scoped tokens on human accounts** (no agent principals); **no email delivery** (in-app inbox + signed webhooks only; admin CLI resets passwords); `/{owner}/{repo}.git` clone shape kept.

## 0. References (normative)

- **RFC 9700** (BCP 240, Jan 2025) — OAuth 2.0 Security BCP; mandates authorization-code + PKCE S256, `state`, `nonce`, exact redirect matching, sender-constrained refresh where applicable. <https://www.rfc-editor.org/rfc/rfc9700.html>
- **RFC 7636** — PKCE S256 (`code_challenge = BASE64URL(SHA256(code_verifier))`). Applied per RFC 9700 §4.5.
- **RFC 6238** — TOTP (HOTP RFC 4226 with time counter `T = floor(now − T0)/X`). <https://www.rfc-editor.org/rfc/rfc6238>
- **RFC 4226** — HOTP (HMAC-SHA-1 truncated to 6 digits).
- **RFC 7617** — HTTP Basic authentication (git Smart HTTP path).
- **RFC 6265 / 6265bis §4.1.3.2** — `__Host-` cookie prefix (requires `Secure`, `Path=/`, no `Domain`).
- **RFC 9457** — problem-details errors for the JSON API (auth failures are `401`/`403`/`429` problems, never HTML on API routes).
- **OWASP Password Storage Cheat Sheet** — Argon2id first choice; params below. <https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet>
- **OWASP Authentication Cheat Sheet** — generic login errors, lockout, no truncation, TOTP window, recovery-code handling. <https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet>
- **OWASP Session Management Cheat Sheet** — server-side sessions, rotation, idle+absolute expiry, `Secure/HttpOnly/SameSite`. <https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet>
- **OWASP Forgot Password Cheat Sheet** — no email token flow here; admin-mediated reset instead (see §9).

## 1. Go libraries (pinned at implementation)

| Concern | Library | Notes |
|---|---|---|
| Password hash | `golang.org/x/crypto/argon2` (already in `rukie/go.mod` as `v0.57.0`) + `crypto/rand`, `crypto/subtle` | `argon2.IDKey`; PHC encode/decode hand-rolled (~40 lines, no new dep). |
| TOTP | `github.com/pquerna/otp v1.5.0` (already in `go.mod`), subpackages `…/otp/totp`, `…/otp/hotp` | Validate with `totp.ValidateCustom`; `otpauth://` URI rendered as text (zero-JS, no QR image dep). |
| OIDC client | `github.com/coreos/go-oidc/v3/oidc` (new dep, track `v3` tip at build, e.g. `v3.20.0` per <https://github.com/coreos/go-oidc>) + `golang.org/x/oauth2 v0.37.0` (already in `go.mod`) | `oidc.NewProvider` (discovery), `Verifier.Verify`, `oauth2.Exchange` + PKCE (`oauth2.S256ChallengeOption` / manual `SHA256` — see §4). |
| SSH keys | `golang.org/x/crypto/ssh v0.57.0` (same module as argon2) | `ssh.ParseAuthorizedKey`, `FingerprintSHA256`; no new dep. |
| Rate limit | `golang.org/x/time/rate` (new, tiny) or hand token-bucket | Per-token + per-IP login buckets. Prefer `x/time/rate` to avoid custom math. |
| Migrations | existing `internal/store` migrator (SQL DDL below; no ORM — `database/sql` + `modernc.org/sqlite` for SQLite, `pgx` for PostgreSQL, KV-encode for Pebble behind the same interface) | No xorm (cut). |

No bcrypt/scrypt fallback, no JWT sessions, no gorilla/sessions, no goth (all Gitea deps cut).

## 2. Module & op-table layout

```
internal/auth/
  password.go      # argon2id hash/verify, PHC codec, rehash check
  totp.go          # provision/verify, window, replay guard, recovery codes
  oidc.go          # provider registry, PKCE/state/nonce, code exchange, claims→user
  session.go       # create/rotate/refresh/revoke, cookie mint/parse
  csrf.go          # synchronizer token mint/check + Origin check
  token.go         # rk_ mint, SHA-256 store, scope check, rate limit hook
  sshkey.go        # key parse, policy (types/sizes), fingerprint
  gitbasic.go      # Basic-auth → scoped-token resolution for git endpoints
  lockout.go       # counters, lock windows, generic-error helper
  audit.go         # audit event emit helpers
  admin.go         # reset-password / unlock / totp-reset service fns (CLI calls these)
internal/store/
  auth_schema.sql  # §11 DDL (source of truth; Pebble impl encodes same rows)
  store.go         # AuthStore interface (methods per table below)
internal/ops/authops.go  # Op[In,Out] registrations (each op = HTTP+OpenAPI+MCP+CLI+HTML form)
```

Ops (all derived from the one `Op` registry; HTML form `POST` + JSON + MCP tool + `rukie <op> --json` share one handler): `auth.login`, `auth.logout`, `auth.totp.enroll`, `auth.totp.verify`, `auth.totp.disable`, `auth.recovery.regenerate`, `auth.oidc.start`, `auth.oidc.callback`, `auth.oidc.link`, `auth.token.create`, `auth.token.revoke`, `auth.token.list`, `auth.sshkey.add`, `auth.sshkey.remove`, `auth.password.change`, `admin.user.reset-password`, `admin.user.unlock`, `admin.user.totp-reset`, `auth.session.list`, `auth.session.revoke`.

## 3. Password hashing (argon2id)

### 3.1 Parameters (normative)

| Param | Value | Source |
|---|---|---|
| Algorithm | **argon2id**, version `19` (`0x13`) | OWASP Password Storage Cheat Sheet |
| Memory `m` | **19456 KiB** (~19 MiB) | Task requirement = OWASP first-choice recommendation |
| Iterations `t` | **2** | ditto |
| Parallelism `p` | **1** | ditto |
| Salt | **16 bytes** (128-bit) from `crypto/rand`, per password | OWASP; never reused |
| Hash length | **32 bytes** (256-bit output of `argon2.IDKey`) | — |
| Password input limits | UTF-8 bytes as-is; **min 12 chars, max 512 bytes**; reject over-limit with `400`; **never truncate** (avoids bcrypt-72 trap) | OWASP Authentication Cheat Sheet (length over complexity); max bounds DoS |
| Password policy | min-length 12 only; **no character-class rules**; check against local blocklist of ≥10k common passwords (`internal/auth/blocklist.txt`, exact lowercase match) + reject `password == username/email` case-insensitively | OWASP/NIST guidance |
| Work-factor agility | params embedded in stored string; `NeedsRehash()` compares to current constants and rehashes transparently on next successful login | — |

### 3.2 Encoded format (PHC, single `TEXT` column)

```
$argon2id$v=19$m=19456,t=2,p=1$<base64-raw-std-no-pad(salt)>$<base64-raw-std-no-pad(hash)>
```

Example shape (values random, 22-char salt / 43-char hash for 16/32 bytes):

```
$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0c2E$Xy8n6K2p5mQ0v9s7t1u3w5y7a9c1e3g5i7k9m1o3
```

- Base64 = RFC 4648 **raw Std** (no padding; `argon2` PHC convention). Parser MUST accept `=` padding on read for robustness but never emit it.
- Verification: parse params → `subtle.ConstantTimeCompare(IDKey(candidate), stored) == 1`. Failure and unknown-user paths both run one full `IDKey` against a **fixed dummy hash** (see §10) so timing does not reveal user existence.
- `password_algo` column records `argon2id-v19` for future agility; `password_changed_at` set on every (re)hash.

### 3.3 Table fields

Covered by `users` in §11: `password_hash TEXT NOT NULL` (PHC string), `password_algo TEXT NOT NULL DEFAULT 'argon2id-v19'`, `password_changed_at TIMESTAMPTZ NOT NULL`, plus `oidc_only BOOL` (§4.5: password login disabled when true).

## 4. TOTP (RFC 6238) + recovery codes

### 4.1 Parameters

| Param | Value |
|---|---|
| Standard | RFC 6238 TOTP over HOTP RFC 4226, HMAC-**SHA-1**, **6 digits**, `T0 = 0`, time step `X = 30 s` |
| Secret | **20 bytes** (160-bit) from `crypto/rand`; provisioned as base32-no-pad; `otpauth://totp/<issuer>:<account>?secret=…&issuer=rukie&algorithm=SHA1&digits=6&period=30` (rendered as copyable text + manual key — no JS) |
| Issuer label | `rukie` (fixed; account = username) |
| Acceptance window | **±1 step** (accept counter `T−1, T, T+1`; i.e. ±30 s skew) |
| Skew handling | window only; no clock probing; server clock MUST be NTP-synced (ops note) |
| Replay prevention | `user_totp.last_counter BIGINT`; accept only if candidate counter `T_candidate > last_counter`; on success set `last_counter = T_candidate`. Concurrent verifies serialized per user (`SELECT … FOR UPDATE` / store mutex) |
| Enrollment | two-step: `auth.totp.enroll` stores secret with `verified_at = NULL` and shows manual key; `auth.totp.verify` with a fresh code flips `verified_at`, sets `users.totp_enabled = TRUE`, burns any old recovery codes and issues new ones |
| Failure accounting | wrong code → `login_attempts` row + per-user TOTP fail counter; **10 fails → invalidate the pending session's `totp_ok` and force full re-login** (§10) |

### 4.2 Recovery codes

| Param | Value |
|---|---|
| Count | **10** single-use codes per issuance |
| Entropy/format | each code 80-bit from `crypto/rand`, displayed once as **16 chars Crockford-base32** grouped `4-4-4-4` with `-` (e.g. `7KQ4-M9XA-2P6D-8F3H`); grouping chars stripped before hashing |
| Storage | **SHA-256 hex** of normalized code (`code_sha256 CHAR(64)`), per-code row; plaintext never stored; hashes compared with `subtle.ConstantTimeCompare` on decoded bytes |
| Lifecycle | any use → mark `used_at`; enrollment/regen (`auth.recovery.regenerate`, requires fresh full auth ≤15 min) deletes all unused rows and issues 10 new; codes listed once on HTML page + `rukie` CLI JSON (once) |
| Login use | on TOTP prompt, a valid unused recovery code passes the second factor, marks used, emits audit `totp.recovery_used`, and inbox-notifies the user |

### 4.3 Table fields

`user_totp(user_id PK/FK, secret_sealed TEXT, last_counter BIGINT DEFAULT -1, created_at, verified_at NULL)` — `secret_sealed` is the base32 secret **AES-256-GCM sealed** with server key `auth.totp_seal_key` (32 bytes from config/env; nonce prepended, `base64(nonce‖ciphertext)`); re-seal lazily on key rotation. `recovery_codes(id PK, user_id FK/INDEX, code_sha256 CHAR(64) UNIQUE, used_at NULL, created_at)`.

## 5. OIDC login (authorization code + PKCE S256)

### 5.1 Provider configuration (admin file / `oidc_providers` table, no UI editing in v1)

Per provider: `id (e.g. "google", "kanidm")`, `issuer URL` (exact, `https:`), `client_id`, `client_secret` (confidential clients; sealed like TOTP secret; public clients use PKCE-only + exact redirect), `redirect_uri = https://<rukie-host>/auth/oidc/<id>/callback` (exact match, registered at OP), `scopes = "openid profile email"` (no offline_access in v1), `allow_signup BOOL` (default TRUE for first configured provider, else FALSE), `link_policy = 'confirm'` (fixed in v1, see §5.4).

Discovery via `oidc.NewProvider(ctx, issuer)` (validates `issuer` matches discovery doc); JWKS cached by `go-oidc` remote key set with its default refresh.

### 5.2 Flow (RFC 9700 compliant)

1. `GET /auth/oidc/<id>/start` (plain link, no JS): server mints **`state` 32 bytes** + **`nonce` 32 bytes** + **PKCE `code_verifier` 64 bytes** (`crypto/rand`, verifier alphabet `[A-Za-z0-9-._~]`, 64 chars ∈ 43–128), `code_challenge = BASE64URL-RAW(SHA256(verifier))`, `code_challenge_method=S256`. Stores `SHA256(state)` + nonce + verifier + provider + `return_to` + expiry in `oidc_states` (10-min TTL, single-use). Redirects to OP with `response_type=code&client_id&redirect_uri&scope&state&nonce&code_challenge&code_challenge_method=S256`.
2. Callback `GET /auth/oidc/<id>/callback?code&state`: look up `SHA256(state)`; missing/expired/consumed → `400` + audit `oidc.bad_state`. Mark consumed **before** exchange (single-use). Exchange with `oauth2` + `code_verifier` (form `grant_type=authorization_code&code&redirect_uri&client_id[&client_secret]&code_verifier`).
3. Verify ID token with `oidc.Verifier{ClientID: client_id}`: signature (JWKS), **`iss` exact-equals configured issuer**, **`aud` contains `client_id`** (and if `azp` present it MUST equal `client_id`), `exp/nbf/iat` sane, **`nonce` equals stored nonce**. Any failure → `401` + audit `oidc.bad_token` (log `iss/aud/exp` only, never token bytes).
4. `email_verified` MUST be `true` before email is trusted (§5.3).

### 5.3 Claims → user mapping

| OIDC claim | rukie field | Rule |
|---|---|---|
| `sub` + `iss` | `oidc_links(issuer, subject)` | stable link key; never reused as username |
| `preferred_username` | `users.name` candidate | normalize: NFKC, lowercase compare, keep `[a-z0-9][a-z0-9._-]{0,38}`; truncate to 39 chars; on collision/invalid fall through |
| `email` (only if `email_verified == true`) | `users.email`; fallback username = local-part sanitized same as above | unverified email still stored with `email_verified = FALSE` and never used for linking |
| else | `oidc-<base36(8 chars of SHA256(iss‖sub))>` | guaranteed-valid fallback |
| `name` | `users.full_name` (free text ≤255) | — |

Username reservation: `admin`, `root`, `system`, `rukie`, `api`, `git`, `support`, `abuse` rejected (append `-<suffix>` suggestion in form error).

### 5.4 Account linking rules (fixed `confirm` policy)

1. `(issuer, subject)` already in `oidc_links` → log in that user (then password/TOTP step-up per §5.5).
2. Else request carries a **logged-in session** (`auth.oidc.link`) → link `(issuer, subject)` to current user (requires fresh full auth ≤15 min; emits audit + inbox notice).
3. Else verified-email matches an existing user → **do NOT auto-link** (prevents pre-hijack). Show interstitial: "Sign in with password+TOTP (or admin) then link". Offer signup-link only after proving ownership.
4. Else if `allow_signup` → create user (username per §5.3, `oidc_only = TRUE` unless they later set a password), insert `oidc_links`, log in. Else `403` "signup disabled — ask an admin to link your account".

### 5.5 Step-up & session

OIDC alone yields `auth_level = 'oidc'` session. If the linked user has `totp_enabled`, OIDC login **still requires the TOTP code** (OIDC replaces only the password factor; TOTP is never skipped). `oidc_only` users have no password login; setting a password flips `oidc_only = FALSE`.

### 5.6 Table fields

`oidc_providers(id TEXT PK, issuer TEXT UNIQUE, client_id TEXT, client_secret_sealed TEXT NULL, scopes TEXT DEFAULT 'openid profile email', allow_signup BOOL DEFAULT TRUE)`, `oidc_links(issuer TEXT, subject TEXT, user_id FK, linked_at, PRIMARY KEY(issuer, subject), INDEX(user_id))`, `oidc_states(state_sha256 CHAR(64) PK, provider_id FK, nonce TEXT, verifier TEXT, return_to TEXT, created_at, expires_at)`.

## 6. Sessions (server-side table + `__Host-` cookie)

### 6.1 Parameters

| Param | Value |
|---|---|
| Storage | **server-side only**; cookie is an opaque lookup key, all state in `sessions` |
| Session id | **256-bit** from `crypto/rand`, transported as **base64url-raw (43 chars)**; stored as **SHA-256 hex** (`id_hash CHAR(64) PK`) — DB leak alone does not yield sessions |
| Cookie | name **`__Host-rukie-session`**; `Path=/`; `Secure`; `HttpOnly`; `SameSite=Lax`; **no `Domain`** (`__Host-` rules); `Max-Age` = seconds until absolute expiry; `Expires` mirrored. `Secure` always (dev on plain HTTP must terminate TLS first; config has no insecure-cookie flag) |
| Idle timeout | **24 h sliding** — refreshed on each authenticated request if `now − last_seen_at > 5 min`; past idle → delete row, clear cookie, redirect to login |
| Absolute timeout | **30 days** from creation — never extended; past → delete, re-login required |
| Rotation | **new id on every login** (old rows for the user-agent deleted first); **new id after TOTP step-up** (privilege change); periodic rotation every 24 h via refresh path (old hash deleted after new row commits). Never accept the pre-login id post-login (fixation-proof) |
| Fresh-auth (step-up) | sensitive ops (password change, TOTP disable, recovery regen, token create, SSH add, OIDC link) require `authed_at ≤ 15 min ago` at current level, else redirect to re-auth (preserves `return_to`) |
| Logout | `auth.logout` deletes current row + clears cookie (`Max-Age=0`); "log out all devices" deletes all user rows |
| Concurrency | no fixed device cap in v1; `auth.session.list` shows `(created, last_seen, ip, UA)` with per-row revoke |

### 6.2 Table fields

`sessions(id_hash CHAR(64) PK, user_id BIGINT FK/INDEX, csrf_token TEXT NOT NULL, auth_level TEXT NOT NULL, authed_at TIMESTAMPTZ, totp_ok BOOL DEFAULT FALSE, created_at, last_seen_at, absolute_expires_at, created_ip TEXT, user_agent TEXT)`. Index `(user_id, last_seen_at)`. Cookie sessions never carry `auth_level = 'token'`.

## 7. CSRF for zero-JS forms

| Param | Value |
|---|---|
| Mechanism | **synchronizer token per session** + `Origin` check (defense in depth) |
| Token | **256-bit** `crypto/rand`, base64url-raw, stored plaintext in `sessions.csrf_token`, rotated on session rotation; emitted as hidden field `_csrf` in **every** `POST` form and checked with `subtle.ConstantTimeCompare`; missing/mismatch → `403` + audit `csrf.block` |
| Origin check | if `Origin` header present on `POST`, it MUST equal `https://<host>` (scheme+host+effective port from request; `X-Forwarded-*` trusted only from configured `trusted_proxies`); mismatch → `403` even with valid token. `Referer` fallback same rule when `Origin` absent but `Referer` present |
| Scope | all cookie-authenticated `POST/PUT/PATCH/DELETE` (HTML forms use `POST` only + `_method` where needed). `GET/HEAD/OPTIONS` never mutate. Token/MCP/Basic-authenticated API calls are **exempt** from CSRF (no cookie) but MUST NOT accept the session cookie (cookie ignored when `Authorization` present) |
| Login CSRF | login/oidc-start forms carry a **pre-session** (anon row or signed `__Host-rukie-anon` cookie + `_csrf`); keeps login CSRF-safe without user enumeration |

## 8. Scoped tokens for agents (`rk_`)

### 8.1 Format & storage

| Param | Value |
|---|---|
| Format | `rk_` + **43 chars base64url-raw(32 bytes entropy)** → total 46 chars, regex `rk_[A-Za-z0-9_-]{43}` (chosen for secret-scanner precision; document in `SECURITY.md` scanner section) |
| Example (shape) | `rk_7Kq4m9Xa2p6d8F3hJ5tY1vW4nB6cD8eF0gH2jK4m` (random at mint; never this literal) |
| Storage | **SHA-256 hex of the full token string** (`token_hash CHAR(64) PK`); plus `public_prefix CHAR(12)` = first 12 chars (`rk_`+8) for support lookup; plaintext shown **once** at creation (HTML once-page + CLI JSON once), never logged/emailed |
| Lookup | hash on receipt; `subtle.ConstantTimeCompare` on decoded hash bytes; unknown → generic `401` (same timing class as bad password) |

### 8.2 Scopes, restriction, expiry (all mandatory)

Scope vocabulary (least-privilege; checked per op **and** per repo):

```
repo:read  repo:write            # browse/fetch vs push/change-create
tasks:read tasks:write           # tasks incl. claim via Ruk leases
ci:read    ci:trigger            # read results / queue runs
webhooks:read webhooks:write
releases:read releases:write
mirrors:read  mirrors:trigger
admin:read admin:write           # admin CLI/ops only; requires human + fresh auth to mint
```

- `scopes TEXT` = space-joined subset, validated against vocabulary at mint; empty = reject.
- **Repo restriction**: `token_repos(token_hash FK, repo_id BIGINT FK, PRIMARY KEY(token_hash, repo_id))`. Empty set = all repos the *user* may access (filtered by team membership at check time); non-empty = intersection of listed repos and user access. `repo:write` + listed repo is what enables `push` to that repo's `refs/for/*` and direct branches per branch policy.
- **Expiry required**: `expires_at TIMESTAMPTZ NOT NULL`; max TTL **366 days**, default **90 days**; no never-expiring tokens. Expired → `401` + audit `token.expired`.
- **Revocation**: `revoked_at NULL`; revoked → `401`. `auth.token.list` shows prefix/scope/expiry/last-use with one-click revoke (fresh-auth gated). Admin password reset revokes all tokens by default (see §11); `revoked_at` is set by `auth.token.revoke`, `auth.password.change` (opt-in), and `admin.user.reset-password` (default).

### 8.3 Last-used tracking & per-token rate limit

- On each authenticated use (throttled to ≥1-min granularity to bound writes): `last_used_at = now`, `last_used_ip = client IP`. Surfaced in token list + audit on anomaly (operator query).
- **Per-token bucket: 300 requests/min sustained, burst 600** (sliding-window counter in memory + persisted `window_start/count` on the token row for multi-process correctness); over-limit → `429` + `Retry-After` + audit `token.ratelimited`. Login/mint endpoints have separate IP buckets (§10). CI fetch of attestations and git Smart HTTP count against the same bucket.

### 8.4 Table fields

`scoped_tokens(token_hash CHAR(64) PK, public_prefix CHAR(12) NOT NULL, user_id BIGINT FK/INDEX, name TEXT, scopes TEXT NOT NULL, expires_at NOT NULL, created_at, created_ip TEXT, last_used_at NULL, last_used_ip TEXT NULL, revoked_at NULL, window_start TIMESTAMPTZ, window_count INT DEFAULT 0)` + `token_repos` above. Index `(user_id, expires_at)`.

## 9. SSH key auth → users & git over SSH

| Param | Value |
|---|---|
| Allowed key types | `ssh-ed25519` (preferred), `ecdsa-sha2-nistp256/384/521`, `ssh-rsa` with **≥3072-bit** modulus only. Reject `ssh-dss`, `ssh-rsa <3072`, FIDO `sk-*` in v1 (explicit `400` naming the reason) |
| Storage | full OpenSSH public line; `fingerprint = FingerprintSHA256(key)` (`SHA256:<base64>`); uniqueness on `fingerprint` globally |
| Mapping | `ssh_keys(id PK, user_id FK/INDEX, fingerprint TEXT UNIQUE, key_type TEXT, public_key TEXT, comment TEXT, created_at, last_used_at NULL)`; Ruk daemon's SSH entrypoint resolves fingerprint→`(user_id, scopes∩repo:read/write)` via `AuthStore.LookupSSHKey` (daemons never read the DB directly) |
| Auth flow | SSH transport handshake → key verified by `x/crypto/ssh` server → fingerprint lookup → inject caller identity into Ruk `push`/`fetch` ephemeral credentials (plan R4): short-lived `(user_id, token_hash-prefix, scopes, repo_id, expires≈5 min)` struct over the Unix socket — never the raw token. Unknown key → `Permission denied (publickey)` + audit `ssh.unknown_key` (fingerprint logged, key bytes not) |
| Management | `auth.sshkey.add` (paste line; fresh-auth gated; validates type/size, shows fingerprint for confirmation), `auth.sshkey.remove` (fresh-auth gated); last-used updated ≥1-min granularity |
| Compromise handling | SSH keys are **not** derived from the password, so password reset does **not** auto-delete them. `admin.user.reset-password` and `admin.user.totp-reset` accept `--revoke-ssh-keys` (delete all rows for the user) and otherwise emit an inbox notice listing key fingerprints for owner review + audit `ssh.review_required`. Default without the flag: keys survive, notice fires. |

## 10. HTTP Basic auth for git (Smart HTTP) & lockout/backoff

### 10.1 Git Basic auth

- Realm `rukie git`; endpoint requires `Authorization: Basic` with **username = rukie username** (`lower_name` compare) and **password = full `rk_…` scoped token**. Passwords (argon2 strings), session ids, TOTP codes, OIDC tokens are **never accepted** here (explicit `401` naming "use a scoped token as password").
- Username MUST equal the token's owner (mismatch → `401` even if token valid).
- Scope mapping: `GET /{owner}/{repo}.git/info/refs?service=git-upload-pack` and fetch require `repo:read`; `git-receive-pack` requires `repo:write`; both additionally require repo in `token_repos` scope when non-empty. Failures → `401` (no token/invalid) or `403` (valid token, insufficient scope) per RFC 7617 + RFC 9457 problem on non-git JSON routes.
- Transport terminates in rukie HTTP, then calls the Ruk daemon with **caller-injected ephemeral credentials** (plan R4): short-lived struct over the Unix socket — never the raw token.

### 10.2 Account lockout & backoff (anti-brute-force)

| Vector | Rule |
|---|---|
| Password failures | **5 fails / 10-min window** (per `LOWER(username)` + per-IP counted separately) → **lock 15 min** (`users.locked_until`); each further fail while locked doubles (`15→30→60…` capped at **24 h**); success clears count. While locked, handler still runs dummy argon2 then returns generic error |
| Error message | always `Invalid username or password.` (same string + same HTTP status for unknown user, bad password, locked, inactive, `oidc_only`) — enumeration resistance per OWASP Authentication Cheat Sheet. Lock state revealed only to admins (`admin.user.show`) and in audit/inbox |
| TOTP failures | **10 fails** on one pending session → destroy pending session, require full re-login; counted in `login_attempts(reason='totp')` |
| OIDC `state` abuse | 20 bad-state callbacks / 10 min / IP → 1-h IP block on OIDC routes |
| Git/token probing | per-IP 60 `401`s / min on git routes → 10-min IP cool-down (429 thereafter); per-token bucket §8.3 independent |
| Dummy-hash equalizer | unknown/inactive/`oidc_only` user → `argon2.IDKey` against fixed 16-byte salt + 32-byte hash at current params, then generic error — constant work class |

Backing table `login_attempts(id PK, username_lower TEXT INDEX, ip TEXT INDEX, at TIMESTAMPTZ INDEX, success BOOL, reason TEXT)`; pruned >30 days by janitor (audit keeps aggregates).

## 11. Admin CLI password reset (no email)

```
rukie admin user reset-password --user <name> [--random|--password-stdin|--password-file <path>]
                                [--revoke-ssh-keys] [--keep-tokens]
rukie admin user unlock --user <name>
rukie admin user totp-reset --user <name> [--revoke-sessions] [--revoke-ssh-keys]
rukie admin user disable|enable --user <name>
```

- Interactive default: prompts twice on a TTY (no echo), enforces §3 policy + blocklist, writes new PHC hash via `admin.go` service (never via SQL hand-edit).
- `--random`: mints 20-char alphanumeric temp password, prints **once**, sets `must_change_password = TRUE` forcing change at next login; `--password-stdin` for automation (reads one line, zeroes buffer).
- Every `reset-password` invocation **atomically**: (a) writes the new PHC hash + bumps `password_changed_at` + clears `failed_login_count`/`locked_until`; (b) **deletes all rows in `sessions` for the user**; (c) **sets `scoped_tokens.revoked_at = now` for every unrevoked, unexpired token of the user** (so a stolen `rk_` dies with the password) unless `--keep-tokens` is passed, which requires interactive re-confirmation, is audit-logged as `admin.password_reset_keep_tokens`, and is rejected when the reset reason is compromise; (d) emits audit `admin.password_reset` (actor admin id, target id, method, `tokens_revoked` count, `ssh_revoked` bool) + inbox notice to the target user listing revoked token prefixes and surviving SSH fingerprints. User-initiated `auth.password.change` revokes other sessions and offers (but does not default to) token revocation via a `revoke_tokens=true` form checkbox.
- SSH keys survive by default (see §9); `--revoke-ssh-keys` deletes all `ssh_keys` rows for the user in the same transaction (used on compromise). Without the flag the inbox notice requires the owner to review/revoke.
- Totp-reset clears `user_totp` + burns recovery codes (requires `--revoke-sessions` unless `--keep-sessions` with second confirmation); it does **not** revoke scoped tokens by itself (tokens are bearer-scoped, TOTP is a human factor) — combine with `reset-password` on compromise.
- Binary requires local operator (Unix-socket/file-perm gate) or `admin:write` token + fresh human auth; CLI never accepts the target's own non-admin token for its own reset.

`users` gains `must_change_password BOOL DEFAULT FALSE`, `is_active BOOL DEFAULT TRUE`.

## 12. Audit events (append-only, no email)

`audit_events(id PK, at INDEX, actor_user_id NULL, action INDEX, target_type/id, ip, user_agent, session_hash_prefix CHAR(12) NULL, result, details JSON)` — retention **365 days**, prune-only (no update/delete API; `admin:read` can list).

- `session_hash_prefix` is the **first 12 hex chars of `sessions.id_hash`** (the SHA-256 of the bearer), never a substring of the bearer itself. It correlates audit rows to a session row for support without leaking bearer bits; raw session ids and CSRF tokens never appear in audit, logs, or error pages (consistent with §5.2 "log iss/aud/exp only, never token bytes").
- Mandatory actions (each with `result` + minimal `details`): `password.login`, `password.change`, `admin.password_reset`, `admin.password_reset_keep_tokens`, `admin.unlock`, `admin.totp_reset`, `totp.enroll`, `totp.verify`, `totp.recovery_used`, `oidc.start`, `oidc.callback`, `oidc.link`, `session.create`, `session.rotate`, `session.destroy`, `session.destroy_all`, `csrf.block`, `token.create`, `token.revoke`, `token.revoked_by_reset`, `token.expired`, `token.ratelimited`, `ssh.add`, `ssh.remove`, `ssh.unknown_key`, `ssh.review_required`, `git.auth`, `lockout.lock`, `lockout.unlock`, `ratelimit.block`. Security-relevant ones (`password.change`, `totp.*`, `token.create`, `ssh.add`, `admin.*`, `lockout.lock`) also drop an **inbox notice** (per settled no-email rule) and are webhook-eligible (signed Standard Webhooks).

## 13. Store DDL (portable; `internal/store/auth_schema.sql`)

SQLite-first (pure-Go `modernc.org/sqlite`), PostgreSQL-compatible with `GENERATED` + `JSONB` notes inline. Times `TIMESTAMPTZ` (SQLite: `TEXT` ISO-8601 UTC; PG: native). Pebble impl KV-encodes the same rows behind `AuthStore`.

```sql
CREATE TABLE users(
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  lower_name TEXT NOT NULL UNIQUE,
  email TEXT NOT NULL,
  email_verified BOOL NOT NULL DEFAULT FALSE,
  full_name TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL,
  password_algo TEXT NOT NULL DEFAULT 'argon2id-v19',
  password_changed_at TIMESTAMPTZ NOT NULL,
  must_change_password BOOL NOT NULL DEFAULT FALSE,
  totp_enabled BOOL NOT NULL DEFAULT FALSE,
  oidc_only BOOL NOT NULL DEFAULT FALSE,
  is_admin BOOL NOT NULL DEFAULT FALSE,
  is_active BOOL NOT NULL DEFAULT TRUE,
  failed_login_count INT NOT NULL DEFAULT 0,
  locked_until TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  UNIQUE(name)
);
CREATE TABLE user_totp(
  user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  secret_sealed TEXT NOT NULL,
  last_counter BIGINT NOT NULL DEFAULT -1,
  created_at TIMESTAMPTZ NOT NULL,
  verified_at TIMESTAMPTZ NULL
);
CREATE TABLE recovery_codes(
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_sha256 CHAR(64) NOT NULL UNIQUE,
  used_at TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_recovery_user ON recovery_codes(user_id);
CREATE TABLE sessions(
  id_hash CHAR(64) PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf_token TEXT NOT NULL,
  auth_level TEXT NOT NULL,
  authed_at TIMESTAMPTZ NOT NULL,
  totp_ok BOOL NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL,
  last_seen_at TIMESTAMPTZ NOT NULL,
  absolute_expires_at TIMESTAMPTZ NOT NULL,
  created_ip TEXT NOT NULL,
  user_agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_sessions_user_seen ON sessions(user_id, last_seen_at);
CREATE TABLE oidc_providers(
  id TEXT PRIMARY KEY,
  issuer TEXT NOT NULL UNIQUE,
  client_id TEXT NOT NULL,
  client_secret_sealed TEXT NULL,
  scopes TEXT NOT NULL DEFAULT 'openid profile email',
  allow_signup BOOL NOT NULL DEFAULT TRUE
);
CREATE TABLE oidc_links(
  issuer TEXT NOT NULL,
  subject TEXT NOT NULL,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  linked_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY(issuer, subject)
);
CREATE INDEX idx_oidc_links_user ON oidc_links(user_id);
CREATE TABLE oidc_states(
  state_sha256 CHAR(64) PRIMARY KEY,
  provider_id TEXT NOT NULL REFERENCES oidc_providers(id) ON DELETE CASCADE,
  nonce TEXT NOT NULL,
  verifier TEXT NOT NULL,
  return_to TEXT NOT NULL DEFAULT '/',
  created_at TIMESTAMPTZ NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE scoped_tokens(
  token_hash CHAR(64) PRIMARY KEY,
  public_prefix CHAR(12) NOT NULL,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL DEFAULT '',
  scopes TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  created_ip TEXT NOT NULL,
  last_used_at TIMESTAMPTZ NULL,
  last_used_ip TEXT NULL,
  revoked_at TIMESTAMPTZ NULL,
  window_start TIMESTAMPTZ NOT NULL,
  window_count INT NOT NULL DEFAULT 0
);
CREATE INDEX idx_tokens_user_exp ON scoped_tokens(user_id, expires_at);
CREATE TABLE token_repos(
  token_hash CHAR(64) NOT NULL REFERENCES scoped_tokens(token_hash) ON DELETE CASCADE,
  repo_id INTEGER NOT NULL,
  PRIMARY KEY(token_hash, repo_id)
);
CREATE TABLE ssh_keys(
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  fingerprint TEXT NOT NULL UNIQUE,
  key_type TEXT NOT NULL,
  public_key TEXT NOT NULL,
  comment TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL,
  last_used_at TIMESTAMPTZ NULL
);
CREATE INDEX idx_ssh_user ON ssh_keys(user_id);
CREATE TABLE login_attempts(
  id INTEGER PRIMARY KEY,

[62 more lines in resource. Use :1810 to continue]
  id INTEGER PRIMARY KEY,
  username_lower TEXT NOT NULL,
  ip TEXT NOT NULL,
  at TIMESTAMPTZ NOT NULL,
  success BOOL NOT NULL,
  reason TEXT NOT NULL DEFAULT 'password'
);
CREATE INDEX idx_attempts_user_at ON login_attempts(username_lower, at);
CREATE INDEX idx_attempts_ip_at ON login_attempts(ip, at);
CREATE TABLE audit_events(
  id INTEGER PRIMARY KEY,
  at TIMESTAMPTZ NOT NULL,
  actor_user_id INTEGER NULL REFERENCES users(id) ON DELETE SET NULL,
  action TEXT NOT NULL,
  target_type TEXT NOT NULL DEFAULT '',
  target_id TEXT NOT NULL DEFAULT '',
  ip TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT '',
  session_hash_prefix CHAR(12) NULL,
  result TEXT NOT NULL,
  details TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_audit_action_at ON audit_events(action, at);
CREATE INDEX idx_audit_actor_at ON audit_events(actor_user_id, at);
```

Notes: PG uses `GENERATED ALWAYS AS IDENTITY` for `INTEGER PRIMARY KEY` columns and `JSONB` for `audit_events.details`. Every `CREATE INDEX` is a top-level statement (never nested inside `CREATE TABLE`).

## 14. Security notes & non-goals

- TLS everywhere (prod); `Secure` cookies unconditional; `trusted_proxies` gates `X-Forwarded-*` used by Origin check and IP attribution.
- Secrets at rest: `client_secret`, TOTP `secret_sealed` AES-256-GCM sealed (key `auth.seal_key`, rotation = dual-read window + lazy re-seal); token/session ids only as hashes; audit stores hash prefixes, never bearer substrings.
- Agent-first: token scopes are the authorization primitive for MCP/CLI/git/CI; humans supervise via token list, session list, inbox notices, audit. Password reset kills tokens by default so a compromised `rk_` does not survive rotation.
- Non-goals v1: WebAuthn/passkeys, TOTP-SHA256/SHA512 variants, FIDO SK SSH keys, OIDC group→team sync, email-based recovery, "remember me" extension, multi-device session cap.

## 15. Acceptance checklist (maps to ticket)

- [x] argon2id `m=19456,t=2,p=1`, PHC format, salt/hash sizes, limits — §3 + `users` row.
- [x] TOTP RFC 6238/30 s/6-digit/±1 window/replay counter/recovery-code hashing — §4 + `user_totp`,`recovery_codes`.
- [x] OIDC code+PKCE-S256/state/nonce/iss-aud checks/linking/claims→username — §5 + `oidc_*`.
- [x] Server-side session table, 256-bit `__Host-` cookie flags, idle 24 h + absolute 30 d, rotation — §6 + `sessions`.
- [x] CSRF synchronizer-per-session + Origin check for zero-JS forms — §7.
- [x] `rk_`-prefixed tokens, SHA-256 store, scopes, repo restriction, required expiry, last-used, per-token 300/min — §8 + `scoped_tokens`,`token_repos`.
- [x] SSH key→user mapping — §9 + `ssh_keys`.
- [x] Git Basic-auth (token-as-password) — §10.1.
- [x] Admin CLI reset (revokes sessions + tokens by default; SSH via flag) — §11.
- [x] Lockout/backoff + generic errors + dummy hash — §10.2 + `login_attempts`.
- [x] Audit event taxonomy + table (hash prefix, no bearer parts) — §12 + `audit_events`.
- [x] Libraries `x/crypto/argon2`, `go-oidc/v3`, `x/oauth2` + all store tables — §1, §13.
- [x] RFC 9700, RFC 6238, OWASP cheat sheets cited — §0.

### Critical Files for Implementation

- `rukie/go.mod` — add `github.com/coreos/go-oidc/v3`, `golang.org/x/time`; keep `golang.org/x/crypto v0.57.0`, `golang.org/x/oauth2 v0.37.0`, `github.com/pquerna/otp v1.5.0`.
- `rukie/internal/store/auth_schema.sql` (new) — §13 DDL; sole schema source behind the store interface.
- `rukie/internal/auth/session.go` (new) — §6 cookie/session/rotation/fresh-auth logic.
- `rukie/internal/auth/token.go` (new) — §8 `rk_` mint/hash/scope/repo-check/rate-limit.
- `rukie/internal/auth/oidc.go` (new) — §5 PKCE/state/nonce/verify/link flow.


[advisor] <advisory advisor="qwen" severity="blocker" guidance="weigh, don't blindly obey"> Save design to auth-design.md now. Fi…

[advisor] <advisory advisor="muse" severity="blocker" guidance="weigh, don't blindly obey"> Write the file now. file auth-design.…