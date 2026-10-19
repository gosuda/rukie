I couldn't write `threat-model.md`. Plan mode forbids file writes, and this session only has read, grep, ast_grep and web_search, so I didn't follow the advisory to create the file. The full artifact is below. Save it verbatim to:

`/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/threat-model.md`

**Summary**
- **Scope:** 14 trust boundaries (TB1–TB14) and 31 threats, ranked Critical, High, Medium or Low. Every threat has a severity, an OWASP Top 10:2025 / ASVS 5 tag, concrete mitigations and one owning module. Go module names under `rukie/internal/` are proposed (`inference`). Ruk-side owners are real paths.
- **Biggest finding:** the daemon's authorization is filesystem-only. A repo is allowed if its canonicalized path sits under `repo_roots` (`registry.rs` `resolve`/`open_by_handle`), and the caller is allowed if its uid passes `SO_PEERCRED`. But `allowed_uids` empty means every local peer is allowed (`daemon/mod.rs:65-66`, `ipc.rs:533-552`). Every invocation also runs as `Principal::from_peer_uid(peer.uid)` (`mod.rs:2017`), so the forge's uid is the only principal Ruk ever sees.
  - The forge must be the single policy point.
  - Ruk must refuse to start in hosted mode with an empty `allowed_uids`.
  - Ruk should accept an `on_behalf_of` attribution from trusted peers.
- **Other Ruk facts that shaped mitigations:**
  - **Hook scripts:** they run from `<repo>/hooks/<name>` and inherit the server environment, with a 60 s fail-closed timeout (`hooks.rs:14-47,67`).
  - **Push memory:** a pushed pack is held in memory up to 2 GiB compressed and 512 MiB per object (`pack.rs:22-25,44-63`).
  - **Outcome ledger:** it is keyed by a global `request_id`, and the client supplies the digest (`mod.rs:1149-1159`).
  - **Command env:** only `GIT_DIR` and `GIT_WORK_TREE` are confined to the roots (`mod.rs:1575-1580`).
  - **Credential helpers:** `!`-prefixed helpers run through `sh -c` from process-level git config files (`credentials.rs:10-16,121-161`).
  - **Private objects:** they are excluded from packs (`private.rs:1-16`).
  - **Attestations:** they are SSHSIG Ed25519 statements in the `ruk-attest` namespace, with trusted signers and expiry (`ruk-core/src/attestation/verify.rs`).
- **Critical (5):** CI sandbox escape; CI secret theft by untrusted changes; private repos leaking across tenants (search, explore, forks, fork-network objects, existence oracle); unauthorized local peers reaching the daemon socket; an operation shipping without its scope ∩ permission check.
- **High (9):** SSRF through webhooks, mirrors, CI egress and OIDC, including DNS rebinding; HTML/SVG injection; CSRF; mirror credential theft; OIDC account takeover; receive-pack hook RCE and ref-namespace abuse; path traversal and symlink races into the daemon; forged CI attestations; CI resource exhaustion.
- **Medium (11) and Low (6):** brute force and argon2 memory DoS; TOTP replay; session fixation; DoS through diffs, blame, search, packs and archives; audit log tampering; SSH channel abuse; prompt injection into agents over MCP; daemon ledger collisions; token leakage through the CLI; token lifecycle (expiry, revocation, leaked-token detection); webhook response handling; supply chain; IPv6 rate-limit evasion; error-message leaks; and admin reset abuse.
- **Concrete choices:**
  - **CSP:** `default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'`. `form-action` matters because `default-src` doesn't cover it.
  - **Content rendering:** no inline style attributes, and syntax highlighting uses classes only. External images become links. Raw files get a per-response `sandbox` CSP.
  - **CSRF:** Go 1.25 `http.CrossOriginProtection` plus a synchronizer token tied to the session.
  - **Passwords:** argon2id with m=64 MiB, t=3, p=4 (RFC 9106 §4), behind a concurrency semaphore.
  - **Tokens:** `rkp_` prefix, a CRC32 tail for secret scanning, HMAC-SHA256 stored hashes, and a mandatory expiry.
  - **Mirror secrets:** AES-256-GCM with the mirror and owner IDs as associated data (AAD), and the key loaded from a key file named in config.
  - **Egress:** one dialer that checks addresses at connect time to defeat DNS rebinding.

The Ruk change list and the test gates are in §5 and §6 of the artifact.

---

````markdown
# rukie threat model

Status: plan. Grounded in Ruk source at /home/alpha/rewrite/git-renewed/ruk (cited `path:line`). Go module names under `rukie/internal/` are proposed (`inference`).
Frames: OWASP Top 10:2025 (https://owasp.org/Top10/2025/) A01–A10; ASVS 5.0 chapters V1–V16.

## 1. System and trust boundaries

Actors: anonymous web visitor; signed-in human; agent holding a scoped token on a human account; org admin; site admin (local CLI); untrusted CI job code; remote mirror hosts; webhook receivers; OIDC IdPs.

Processes (each a distinct OS uid):
- `rukie` (Go): HTML, JSON API, MCP, smart HTTP, SSH, CI controller, mirror and webhook workers.
- `ruk forgejo serve` (Rust daemon): Unix socket, repo stores under `repo_roots`.
- CI runner hosts: one microVM per job.
- Metadata store: one module, backend still open.

| ID | Boundary | Crossing |
|---|---|---|
| TB1 | Internet → rukie HTTP (HTML forms, JSON API, MCP HTTP) | cookies, tokens, form posts |
| TB2 | Internet → rukie git smart HTTP | Basic auth carrying a token |
| TB3 | Internet → rukie SSH | public-key auth, exec channel |
| TB4 | rukie → Ruk daemon Unix socket | frames, InvocationOpen{repo,method}, CommandOpen{argv,dir,env} |
| TB5 | Ruk receive-pack → hooks / push policy | quarantine, ref updates |
| TB6 | rukie CI controller → runner sandbox (untrusted code) | job spec, secrets, logs, results |
| TB7 | CI sandbox → network | egress |
| TB8 | rukie/Ruk → mirror remotes (GitHub/GitLab/Forgejo) | credentials, pushes |
| TB9 | rukie → webhook receivers | signed POSTs |
| TB10 | Browser ↔ IdP ↔ rukie (OIDC) | codes, ID tokens |
| TB11 | Repo content → renderer → browser / agent | markdown, SVG, raw files, release assets, task text |
| TB12 | Tenant ↔ tenant inside rukie (private repos, search, explore, forks) | visibility predicates |
| TB13 | rukie → metadata store and audit log | rows, secrets at rest |
| TB14 | Admin CLI / config file → rukie | key files, password resets |

## 2. Ranked threat register

Severity = impact × reachability for a multi-tenant forge that runs untrusted agent code.

| ID | Sev | TB | Threat | OWASP / ASVS | Owner |
|---|---|---|---|---|---|
| C1 | Critical | TB6 | CI job escapes its sandbox, reaches the daemon socket, the metadata store or other jobs | A01, A02, A06 / V13, V15 | `internal/ci/sandbox` |
| C2 | Critical | TB6 | Untrusted change (refs/for from an agent or outsider) exfiltrates CI secrets or poisons the trusted cache | A01, A08 / V13.3, V14 | `internal/ci/secrets` |
| C3 | Critical | TB12 | Private repo data leaks through search, explore, forks, fork-network objects, webhooks or 403-vs-404 | A01 / V8 | `internal/authz` + Ruk `transport/private.rs` |
| C4 | Critical | TB4 | Unauthorized local peer drives the daemon (empty `allowed_uids` admits every local uid) | A01, A02 / V8, V13 | Ruk `daemon/ipc.rs`, `daemon/mod.rs` |
| C5 | Critical | TB1–3 | An operation ships without a scope check, or the token scope isn't intersected with the human's permission | A01 / V8.2, V9 | `internal/op` (guard chain) |
| H1 | High | TB7–9, TB10 | SSRF via webhook URL, mirror URL, CI egress or OIDC discovery; DNS rebinding to 169.254.169.254 or loopback | A01, A05 / V12, V15 | `internal/egress` |
| H2 | High | TB11 | Stored HTML injection: XSS via SVG/raw/assets, CSS exfiltration, form hijack, dangling markup | A05 / V1, V3 | `internal/render`, `internal/web` |
| H3 | High | TB1 | CSRF on zero-JS forms | A01 / V3.5 | `internal/web` |
| H4 | High | TB8, TB13 | Mirror credential theft (DB dump, logs, row swap, over-broad scopes) | A04, A09 / V11, V13.3 | `internal/secret`, `internal/mirror` |
| H5 | High | TB10 | OIDC account takeover (email linking, mix-up, code injection, missing PKCE/nonce/state) | A07 / V10 | `internal/authn/oidc` |
| H6 | High | TB5 | Hook-script RCE, ref-namespace abuse, protected-branch bypass | A01, A08 / V2, V8 | `internal/receive` + Ruk `transport/hooks.rs` |
| H7 | High | TB4 | Path traversal or symlink race from repo names into daemon paths | A01 / V5, V8 | `internal/rukclient` + Ruk `daemon/registry.rs` |
| H8 | High | TB6 | Forged CI attestation satisfies land requirements | A08 / V11 | `internal/ci/attest` |
| H9 | High | TB6 | CI resource exhaustion (CPU, memory, disk, fork bombs, queue flooding) | A06 / V15 | `internal/ci/sched`, `internal/ci/sandbox` |
| M1 | Medium | TB1 | Password brute force or stuffing; argon2 memory DoS; user enumeration | A07 / V6 | `internal/authn/password` |
| M2 | Medium | TB1 | TOTP brute force and replay | A07 / V6.5 | `internal/authn/totp` |
| M3 | Medium | TB1 | Session fixation, hijack, no revocation | A07 / V7 | `internal/authn/session` |
| M4 | Medium | TB1–4 | DoS via huge diffs, blame, search, revsets, packs, archives | A06, A10 / V15 | Ruk methods + `internal/budget` |
| M5 | Medium | TB13 | Audit log tampering or repudiation; secrets in logs | A09 / V16 | `internal/audit` |
| M6 | Medium | TB3 | SSH abuse: forwarding, PTY/env requests, command injection, weak keys | A05, A07 / V12 | `internal/gitwire/ssh` |
| M7 | Medium | TB11 | Prompt injection from repo/task content steers agents over MCP | A06 / V15 | `internal/op` (MCP derive) |
| M8 | Medium | TB4 | Daemon outcome-ledger collision: global request_id, client-supplied digest | A08 / V15 | Ruk `daemon/mod.rs` + `internal/rukclient` |
| M9 | Medium | TB2 | Token leaks via URLs, `--token` in argv/shell history, CLI config files | A07 / V9 | `internal/token`, `cmd/rukie` |
| M10 | Medium | TB1–3 | Token lifecycle: no expiry, slow revocation, leaked-token detection | A07 / V9 | `internal/token` |
| M11 | Medium | TB9 | Webhook secrets or private payloads exposed; response bodies rendered unsafely | A04, A05 | `internal/webhook` |
| L1 | Low | all | Supply chain: dependency or runner-image compromise | A03 / V15 | build/CI config |
| L2 | Low | TB1–3 | Rate-limit evasion via IPv6 rotation | A06 | `internal/ratelimit` |
| L3 | Low | TB1 | Internal errors leak paths or stack traces; fail-open on Ruk errors | A10 / V16 | `internal/op` (problem mapping) |
| L4 | Low | TB14 | Admin password reset abused or unaudited | A07, A09 | `cmd/rukie-admin`, `internal/audit` |
| L5 | Low | TB11 | Release-asset filename/path traversal into storage | A01 / V5 | `internal/release` |
| L6 | Low | TB9 | Receiver-side replay of webhooks | A08 | `internal/webhook` (timestamped signatures) |

## 3. Boundaries in detail

### TB4 — rukie → Ruk daemon (C4, H7, M8)
Current code, ground truth:
- **Peer check.** Peer auth is `SO_PEERCRED` checked against `allowed_uids`, and an empty list allows every local peer (`ruk/crates/ruk/src/daemon/mod.rs:65-66`, `ipc.rs:533-552`). The check runs once per connection, before the hello frame (`mod.rs:784`).
- **Repo check.** `Registry::new` canonicalizes `repo_roots`. `resolve` canonicalizes the requested path and returns `E_FORBIDDEN` unless `canonical.starts_with(root)` (component-wise `Path::starts_with`, so `/r/a` does not match `/r/ab`) (`registry.rs` `resolve`).
- **Path vs handle.** `open` treats anything starting with `/` as a path and anything else as an opaque handle `repo-N` from a process-wide counter. `open_by_handle` re-checks the roots. Handles are not bound to a connection or a principal.
- **Principal.** Every invocation runs as `Principal::from_peer_uid(peer.uid)` (`mod.rs:2017`). The repo owner principal comes from the store directory's uid (`registry.rs` `repository_owner`), so Ruk's own claim/lease policy can't tell rukie's users apart.
- **CommandOpen confinement.** `dir` resolves under the roots; `repository` must equal `dir`; only `GIT_DIR` and `GIT_WORK_TREE` are confined to the roots. Other env passes through to the hermetic command (`mod.rs:1547-1580`).
- **Outcome ledger.** It is keyed by `request_id` alone. `command_digest` comes from the client and is compared but never recomputed (`mod.rs:1149-1159`, `mod.rs:405-407`).

Threats and mitigations:
- C4 **Critical** — any local process (CI host agent, compromised sidecar, shell user) opens any repo with full rights. Mitigations:
  - (Ruk) Refuse `forgejo serve` when `allowed_uids` is empty. Make it a required config key.
  - (Ops) Socket in a 0700 directory owned by the ruk uid. rukie runs as a separate uid listed in `allowed_uids`. CI runners live on other hosts or in VMs and never mount the socket. Owner: Ruk `daemon/mod.rs` config validation; deployment unit files.
- C4b — principal collapse means Ruk claims and leases attribute everything to rukie. Mitigation (Ruk, breaking change): add an `on_behalf_of: {account_id, token_id}` field to InvocationOpen, accepted only from peers in `allowed_uids`. Ruk records it in `op.log` and claim holders, and rukie remains the authorization authority. Owner: Ruk `daemon/mod.rs`, `ruk-core/coord`.
- H7 **High** — traversal from `owner/name`. Mitigations:
  - rukie never builds paths from names. `(owner, name)` maps to an immutable repo ID (ULID) in the store, and the path is `<root>/<id[0:2]>/<id>`, built by `internal/rukclient` only.
  - Names match `^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`, with no `.`/`..`, no `.git` suffix collisions, and reserved words refused.
  - Always send absolute paths, never handles, so a stale `repo-N` can't be confused across restarts.
- H7b — TOCTOU: `resolve` canonicalizes, then `Store::open` reopens the path later (`registry.rs` `coordinator_for`), so a symlink swapped in between could redirect the open. Precondition: write access under the roots. Mitigations:
  - (Ops) Only the ruk uid can write under the roots, and nothing user-controlled is materialized there (stores aren't worktrees).
  - (Ruk) Open stores with `openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS)` relative to a root directory fd (`inference`: suggested hardening).
- M8 **Medium** — a reused `request_id` replays another command's recorded output, possibly from another repo. Mitigations:
  - rukie generates 128-bit random request ids per invocation (`internal/rukclient`).
  - (Ruk) Key the ledger by `(canonical_repo, request_id)` and compute the digest server-side over argv/dir/env, excluding values under a `secret_env` list.
- Env smuggling — CommandOpen env reaches the interpreter unconfined, apart from `GIT_DIR`/`GIT_WORK_TREE`. Mitigation: `internal/rukclient` sends an allowlisted env (`GIT_PROTOCOL`, `GIT_AUTH_*` for mirrors only), never user-supplied keys.

### TB1 — Internet → HTML / JSON / MCP (C5, H3, M1–M3, M9, M10, L2, L3)
- **C5 Critical: scope bypass.** `internal/op` is the only dispatch path for all four surfaces (HTML, API, MCP, CLI→API).
  - Each `Op` declares `Scope`, `Requires` and `Effect`. The build fails if any is missing (gate in §6).
  - Authorization is dual: token scope ∩ the account's permission on the resource ∩ the token's resource allowlist (owners, repos, ref globs).
  - Deny by default; an unknown scope is a deny.
  - MCP `tools/list` is filtered by the same guards.
  - Ruk-proxied methods map to a Ruk method allowlist per scope, so `op.prune`/`undo` need `repo:admin`.
- **H3 High: CSRF (zero-JS forms).** Owner: `internal/web`.
  - `http.CrossOriginProtection` (Go 1.25 stdlib; rejects cross-origin unsafe requests via `Sec-Fetch-Site`/`Origin`, https://pkg.go.dev/net/http) wraps every state-changing route.
  - Plus a synchronizer token: an HMAC over the session ID and form ID in a hidden field, compared in constant time. This covers older browsers and same-site subdomain attackers.
  - Session cookie `SameSite=Lax` (Lax, not Strict, so top-level returns from the OIDC redirect keep the pre-auth cookie).
  - GETs never mutate. Form method is POST; PRG.
  - Bearer-token API requests skip CSRF because no ambient credential is involved. A request carrying both a cookie and a bearer token is refused.
- **M3 Medium: sessions.** Owner: `internal/authn/session`.
  - Cookie `__Host-rukie_s`: `Secure; HttpOnly; Path=/; SameSite=Lax`. The value is 32 random bytes; the server stores SHA-256(value).
  - Rotate on login, on TOTP step-up and on privilege change.
  - Idle timeout 12 h, absolute 14 d (`inference`: tunable).
  - A session list with revoke. Password change revokes other sessions.
  - The pre-auth half-login (password OK, TOTP pending) is a separate short-lived cookie (5 min) with no privileges.
- **M1 Medium: passwords.** Owner: `internal/authn/password`.
  - argon2id via `golang.org/x/crypto/argon2`, m=64 MiB, t=3, p=4, 16-byte salt, 32-byte tag (RFC 9106 §4 second recommended option, https://www.rfc-editor.org/rfc/rfc9106#section-4). Encoded as a PHC string; rehash on login when params change.
  - A global semaphore of `max(1, GOMAXPROCS/2)` concurrent hashes, so peak memory is about 64 MiB × N.
  - Length 8–1024 bytes, 15+ encouraged (ASVS 5 V6.2). Check against a bundled local breached-password list (no network).
  - Unknown users run a dummy hash, and the response is identical, against enumeration.
  - Lockout is progressive delay per account (5 failures per 15 min) plus per-IP buckets. No hard lock, which would itself be a DoS.
- **M2 Medium: TOTP.** Owner: `internal/authn/totp`.
  - RFC 6238, SHA-1, 6 digits, 30 s, accept ±1 step.
  - Store the last accepted step counter per account and reject any step ≤ the last one (replay).
  - 5 attempts per pre-auth session, then restart from the password step. Per-account cap of 10 per 15 min.
  - The secret is encrypted at rest (`internal/secret`). 10 single-use recovery codes, stored as SHA-256 with pepper.
- **M9/M10 Medium: tokens.** Owner: `internal/token`.
  - Format `rkp_<id:12 base62>_<secret:32 bytes base62>_<crc32 base62>`: a fixed prefix plus checksum lets GitHub and other secret scanners detect leaks offline.
  - Store HMAC-SHA256(pepper, secret). Lookup by id; compare in constant time.
  - Mandatory `expires_at` (default 30 d, max 366 d; agent-intended tokens default 7 d). `last_used_at`/IP recorded.
  - Revocation checked on every request; any cache lives ≤ 5 s.
  - Tokens accepted only in `Authorization: Bearer` (API, MCP) or the Basic password slot (git). Query-string tokens get 400.
  - Scopes are `resource:action` and equal `Op.Scope` values.
  - CLI: `RUKIE_TOKEN` env or a 0600 file under `$XDG_CONFIG_HOME/rukie`; no `--token` flag value in argv (or a warning plus a `--token-file` alternative). Owner: `cmd/rukie`.
  - Leaked-token endpoint: a self-revoke op that takes the token itself (GitHub-style scanning partner hook later; `inference`).
- **L2 Low: rate limits.** Owner: `internal/ratelimit`.
  - Token buckets keyed per token, per account, per IPv4 /32 and per IPv6 /64.
  - Ops carry a cost weight (read 1, mutation 5, search/blame/diff 10, login/TOTP separate buckets).
  - Responses carry `429` + `Retry-After` and `RateLimit-*` headers.
  - Single-node in-memory with a bounded LRU key table.
- **L3 Low: errors.** `internal/op` maps Ruk error codes to RFC 9457 problems with fixed `detail` strings. No paths, stacks or Ruk-internal messages reach clients; full detail goes to the server log.
  - Fail closed: any daemon error (`E_HOSTED`, timeout, `E_COORDINATOR_UNRESPONSIVE`) becomes 503 and never skips a policy step (A10).
  - Panics are recovered per request.

### TB2 — git smart HTTP (C5, M4, M9)
Owner: `internal/gitwire/http`.
- Auth: Basic with any username and a token as the password; passwords are never accepted for git. Anonymous access only for public repos and upload-pack.
- Scope `repo:read` for upload-pack; `push:write` plus ref-glob allowlist for receive-pack.
- Unauthorized private access answers 404, not 401/403, unless credentials were presented (no existence oracle).
- Concurrency semaphore per token and per IP for upload-pack/receive-pack. Request bodies are streamed with a total byte cap.

### TB3 — SSH (M6)
Owner: `internal/gitwire/ssh` (`golang.org/x/crypto/ssh`).
- Public-key auth only; keys belong to human accounts and carry the same scope/allowlist/expiry record as tokens.
- Refuse RSA < 3072 and `ssh-rsa` SHA-1 signatures. `MaxAuthTries` 3; pre-auth connections rate-limited per IP; 10 s auth deadline.
- Exec only. Refuse `pty-req`, `env`, `x11-req`, `auth-agent-req`, `tcpip-forward`, `direct-tcpip` and subsystems.
- Parse the exec command with a strict grammar: `git-(upload-pack|receive-pack|upload-archive) '<owner>/<name>(.git)?'`. No shell, no escapes beyond the quoted path; map to a repo ID through `internal/rukclient`.
- The host key is persisted with 0600 perms and has a published fingerprint.

### TB5 — receive-pack / push policy (H6)
Ruk today:
- Hook scripts at `<repo_dir>/hooks/{pre-receive,update,post-receive}` run directly, with no shell, and inherit the server environment plus quarantine vars. A 60 s timeout kills the hook and refuses the push fail-closed (`ruk/crates/ruk-git/src/transport/hooks.rs:14-47,67`).
- Objects stay in quarantine until every refusing phase passes (`hooks.rs:1-12`).

Mitigations:
- (Ruk, breaking change) A hosted-mode flag disables script hooks. Policy becomes a typed pre-receive callback: the daemon sends the proposed ref updates as a frame to rukie and waits for a verdict, with the same 60 s fail-closed timeout. That leaves no executables in repo directories, so file write under the roots ≠ RCE. Owner: Ruk `transport/hooks.rs`, `daemon/mod.rs`.
- Policy, in rukie `internal/receive`:
  - Ref allowlist `refs/heads/*`, `refs/tags/*`, `refs/for/<branch>[%opts]`. Refuse `refs/changes/*`, attestation/internal namespaces, `HEAD` and symrefs.
  - Protected branches: no force, no delete, and land only via `land.execute` for configured branches.
  - Tag immutability opt-in. Push-option parsing on a strict grammar.
  - Per-repo ingest limits below Ruk's defaults (2 GiB pack, 512 MiB object, depth 128; `pack.rs:44-63`), e.g. 512 MiB / 100 MiB / 50 (`inference`: tunable).
  - A global concurrent-receive semaphore, because ingest holds all decompressed entries in memory (`pack.rs:22-25`).
- Post-receive effects (CI enqueue, webhooks, mirrors) are emitted from the durable store transaction, never from hook stdout.

### TB6/TB7 — built-in CI running untrusted agent code (C1, C2, H8, H9)
Trust tiers per job:
- `untrusted`: any `refs/for/*` change, or any change whose author lacks write.
- `trusted`: landed commits on protected branches, or manual approval by a writer.

Mitigations:
- **C1 Critical: escape.** Owner: `internal/ci/sandbox`.
  - One microVM per job (Firecracker or cloud-hypervisor, KVM), with a read-only base image pinned by digest, an ephemeral overlay disk and no host mounts. The runner host agent runs outside the VM.
  - No daemon socket and no metadata credentials anywhere on runner hosts. Source arrives as a pre-fetched snapshot (Ruk `snapshot`/`file`) pushed by the host agent over vsock, not as a clone credential.
  - Host agent runs unprivileged except for a VM-launch helper. Seccomp on the VMM.
  - The VM is destroyed and its disk discarded after each job; no reuse across repos or tiers.
  - Containers alone (shared kernel) are not accepted for multi-tenant untrusted code (`inference`: design judgment).
- **C2 Critical: secrets.** Owner: `internal/ci/secrets`.
  - Secrets are scoped per repo and environment and are write-only through every surface (no read op exists).
  - Delivered only to `trusted` jobs whose environment rule matches.
  - Delivered as files on a tmpfs inside the VM at job start; never in job spec, logs or attestations.
  - Log masking of exact values plus base64/URL-encoded variants.
  - `untrusted` jobs get no secrets, no write tokens and a read-only view of the trusted cache. Their cache writes go to a per-change namespace that's never restored into trusted jobs (cache poisoning).
  - The job token is an ephemeral token scoped to `repo:read` on that repo and `ci:log:write` for that job, valid only for the job's lifetime.
- **H1/TB7 egress.** Owner: `internal/egress` (CI proxy role).
  - Default deny. The VM's only route goes to an egress proxy on the host (HTTP CONNECT) that applies the SSRF policy below plus a per-repo domain allowlist (package registries).
  - Metadata IPs, the forge's own admin/internal addresses and RFC 1918 ranges are blocked.
  - Bandwidth and connection caps per job.
- **H8 High: attestation forgery.** Ruk verifies SSHSIG Ed25519 statements in namespace `ruk-attest` and checks land requirements against trusted signers with expiry (`ruk/crates/ruk-core/src/attestation/verify.rs`, `query.rs`).
  - The CI signing key lives only in `internal/ci/attest` on the controller, loaded from a key file named in config. Runners never see it.
  - The verdict comes from the host agent's view of the job (process exit status and artifact digests measured outside the VM), never from text the job prints.
  - The statement binds subject OID, job spec digest, runner image digest and tier. Land requirements accept only `trusted`-tier statements from the CI signer.
  - Key rotation goes through the trusted-signer list.
- **H9 High: exhaustion.** Owners: `internal/ci/sched` (quotas), `internal/ci/sandbox` (limits).
  - Per job: vCPU, memory, disk quota, pids inside the VM, wall-clock timeout (default 30 min) and log cap (default 16 MiB, then truncated with a marker).
  - Per owner: concurrency and queued-job quotas, fair-share scheduling, and a per-change dedupe that cancels superseded runs.
  - The queue is durable in the metadata store, so a flood can't evict trusted work.

### TB8 — push mirrors (H1, H4)
Ruk facts:
- Mirror push maps to `ruk push` with `--mirror` semantics (`ruk/crates/ruk-git/src/transport/mirror.rs:1-20`).
- Credentials come from `GIT_AUTH_*` env (e.g. `GIT_AUTH_TOKEN`, `credentials.rs` tests ~973) or external helpers from process-level git config. `!` helpers run via `sh -c` (`credentials.rs:10-16`). Config paths are system, `$HOME` and XDG, chosen by process env (`credentials.rs:121-161`); repo-level config isn't read.

Mitigations:
- **Encryption at rest.** AES-256-GCM (stdlib `crypto/aes`, `crypto/cipher`) with a 256-bit key from a key file named in config (0600, never in the DB). A key ID is prefixed for rotation; AAD = `mirror_id|owner_id|"mirror-cred"`, so swapping rows between mirrors fails to decrypt. Owner: `internal/secret`.
- **Never logged.** A `secret.Value` type whose `String`, `GoString`, `LogValue` (slog) and `MarshalJSON` all return `[redacted]`. URL userinfo is stripped before logging or display; webhook and audit payloads carry only a credential fingerprint. Credentials go to Ruk only via CommandOpen env `GIT_AUTH_TOKEN`, excluded from the Ruk digest/ledger (see M8). Owner: `internal/secret`, `internal/mirror`.
- **Daemon hermeticity.** Run the daemon with `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=` (empty pins discovery; `credentials.rs:1053-1054`) and an empty `HOME`, so no `!` helper ever runs. Owner: deployment unit for Ruk.
- **Least-privilege scopes** (UI guidance plus validation where the remote API allows it):
  - GitHub: fine-grained PAT limited to the single repo with Contents: Read and write; Workflows: write only if `.github/workflows` changes are mirrored (https://docs.github.com/en/rest/authentication/permissions-required-for-fine-grained-personal-access-tokens).
  - GitLab: project access token, `write_repository`, role Developer (Maintainer only if force-pushing protected branches) (https://docs.gitlab.com/security/tokens/access_token_scopes/).
  - Forgejo: token scope `write:repository` (`inference`).
  - Preferred alternative: a write deploy key over SSH, repo-scoped by construction.
- **Mirror URL.** Schemes `https` and `ssh` only (no `file`, `git`, `ext`, local paths). The host passes the egress SSRF check. For SSH, rukie pins the remote host key on first configure (TOFU, shown to the user).
- **Mode.** `exact` (force + prune) needs `repo:admin`; `ff-only` needs `repo:write`. The ref selection globs are validated.

### TB9 — webhooks (H1, M11, L6)
Owner: `internal/webhook` (signing), `internal/egress` (dial).
- Standard Webhooks: `webhook-id`, `webhook-timestamp`, `webhook-signature: v1,<base64 HMAC-SHA256(id.timestamp.body)>`. The secret is `whsec_` + ≥ 24 random bytes, stored encrypted. Rotation sends two signatures during overlap (https://www.standardwebhooks.com/).
- Only repo/org admins configure hooks. Payloads respect the visibility of the subscribing scope. Delivery logs store response status and the first 4 KiB of the body, rendered as escaped text only.

### Shared egress policy (H1) — `internal/egress`
- One `net.Dialer` whose `Control` hook rejects the actual connect address, so DNS rebinding between check and connect is defeated. Blocked:
  - loopback, `IsPrivate`, `IsLinkLocalUnicast`/`Multicast`, `IsUnspecified`;
  - 100.64.0.0/10, 192.0.0.0/24, 198.18.0.0/15, 0.0.0.0/8, 240.0.0.0/4;
  - fc00::/7, fe80::/10, 64:ff9b::/96 and IPv4-mapped forms (normalized with `netip.Addr.Unmap`).
- Redirects are not followed. Proxy env is ignored. Connect timeout 5 s, total 15 s, response cap 64 KiB. HTTPS required except for an admin allowlist.
- Admins can allowlist specific internal CIDRs.
- Ruk-dialed traffic (mirror pushes through `ruk-git/src/transport/net.rs`) needs the same policy. (Ruk change) Accept a `connect_to` pinned IP from rukie, or an egress-policy config with the same blocklist, checked at connect time.

### TB10 — OIDC (H5)
Owner: `internal/authn/oidc`. RFC 9700 (https://www.rfc-editor.org/rfc/rfc9700):
- Authorization code flow only, with PKCE S256 required (`golang.org/x/oauth2` `GenerateVerifier`/`S256ChallengeOption`).
- `state` (32 random bytes) and `nonce` stored in a `__Host-` pre-auth cookie bound to the browser; single use, 10 min TTL.
- Exact `redirect_uri` match. Per-provider callback path plus an `iss` response parameter check (RFC 9207) against mix-up.
- ID token verified against JWKS from discovery: alg allowlist (RS256, ES256, EdDSA; never `none` or HS*), `iss`, `aud == client_id`, `azp`, `exp`/`iat` with 60 s leeway, `nonce`.
- Accounts link by `(iss, sub)` only, never by email. Linking an IdP to an existing account requires being signed in to it (password + TOTP).
- The client secret is encrypted at rest (`internal/secret`). Discovery fetches go through `internal/egress`, and providers are admin-configured only.

### TB11 — rendering repo content (H2, M7, L5)
Owners: `internal/render` (markdown/sanitize/raw), `internal/web` (headers).
- **Markdown.** goldmark with raw HTML disabled. The output passes an allowlist sanitizer on `golang.org/x/net/html` tokens:
  - tags limited to text, structure and tables;
  - attributes `href`, `src`, `alt`, `title`, `class` (allowlisted prefixes), `id` (prefixed `user-content-`);
  - no `style`, `form`, `input`, `button`, `meta`, `base`, `link`, `iframe`, `object` or `svg`;
  - URL schemes `http`, `https`, `mailto` and relative;
  - links get `rel="nofollow noopener noreferrer ugc"`.
  - External images become links (no tracking pixels, no proxy).
- **Code view.** Highlighting with CSS classes only (no inline style attributes, which `style-src 'self'` would block anyway).
- **Page headers.** `Content-Security-Policy: default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'; upgrade-insecure-requests`. `default-src` doesn't cover `form-action`, `frame-ancestors` or `base-uri`, so they're set explicitly to stop injected-form phishing and framing. Also `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`, `Cross-Origin-Opener-Policy: same-origin`, `Cross-Origin-Resource-Policy: same-origin`, HSTS.
- **Raw files and release assets.** Per-response `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; sandbox`, plus nosniff.
  - `text/html` and anything not on the inline allowlist (png, jpeg, gif, webp, plain text) is served `Content-Disposition: attachment` with RFC 6266 filename encoding.
  - SVG is served as `image/svg+xml` under the sandbox CSP.
  - Assets are stored by content hash, so names never touch paths (L5).
  - Option: a separate raw origin for defense in depth.
- **M7 prompt injection.** MCP tool results put untrusted repo/task text in structured fields marked `"untrusted_content": true`, separate from server-authored fields. Destructive ops carry `destructiveHint`. Token scopes cap the blast radius. No op returns secrets (CI secrets write-only, mirror credentials never readable). Owner: `internal/op` (MCP derivation).

### TB12 — multi-tenant isolation (C3)
Owner: `internal/authz` (predicates) + `internal/store` (visibility-filtered queries) + Ruk.
- One `authz.CanRead(principal, repoID)` predicate. Every list (explore, stars, watches, forks, tasks, inbox, search) is built from a store query that joins visibility. There's no unfiltered "list all repos" function in the store API.
- **Search.** Ruk search runs per repository only. rukie fans out across the repo IDs the principal can read (capped, e.g. 200 repos per query; `inference`). No global Ruk index exists, so the cross-tenant join can't happen in Ruk.
- **Forks.** A fork of a private repo stays private, owned by a principal who already has read. Object storage isn't shared across visibility boundaries, so there's no alternates or promisor sharing between a private parent and a public fork. This avoids GitHub-style cross-fork object reachability (`inference`).
- **Upload-pack.** Wants are restricted to objects reachable from advertised refs. Ruk's private-object exclusion (`ruk/crates/ruk-git/src/transport/private.rs:1-16`) is applied to hidden refs (drafts, `refs/for` of private changes).
- **Existence.** 404 for unreadable private repos, tasks and changes on every surface, including git and MCP.
- **Webhooks and mirrors.** These configs and payloads are visible only to admins of the repo.

### TB13 — metadata store and audit log (M5, H4)
Owner: `internal/audit` (on the `internal/store` interface; backend-agnostic).
- Append-only audit rows: actor, token ID, surface, op ID, decision and reason, resource, Ruk op ID, IP. Each row includes `prev_hash = SHA-256(previous row canonical bytes)`.
- Every 1000 rows or 1 h, rukie signs a checkpoint (Ed25519, key file from config) and records it as a Ruk attestation in an internal system repo, so tampering requires both the store and Ruk.
- Record authN failures, authZ denials, token create/revoke, admin actions and secret access (A09).
- On PostgreSQL, the app role lacks UPDATE/DELETE on the audit table (`inference`: depends on store choice).
- Secrets never enter audit or log rows (the `secret.Value` type). Store backups are encrypted with a key separate from the mirror-secret key.

### TB14 — config and admin CLI (L4)
Owner: `cmd/rukie-admin`, `internal/config`.
- The config file and key files are 0600; startup refuses world-readable keys.
- Password reset is local-only (needs filesystem access to config and DB), always audited. It sets `must_change` and revokes all of the account's sessions; tokens are kept unless `--revoke-tokens`.
- No HTTP route exists for site-admin bootstrap.

### TB1–4 — DoS budgets (M4)
Owners: Ruk methods (budget enforcement) + `internal/budget` (per-op caps passed to Ruk).
- Every read op (`diff`, `blame`, `log`, `query`, `file`, search) carries a byte budget, a wall-clock deadline and a page continuation. Ruk already has `ByteBudget` and `PageLimit` (`ruk-core/src/budget.rs`, `protocol.rs` per sibling notes `local://ideate-context.md`).
- Proposed defaults (`inference`): diff 1 000 files / 1 MiB rendered per file / 8 MiB total; blame files ≤ 2 MiB and 2 s; search 1.5 s and 256 MiB scanned per repo with a linear-time regex engine (Rust `regex`); revset evaluation step cap.
- Results past a cap are truncated with an explicit marker and continuation, never a hang.
- Archives get a concurrency semaphore per IP and token. Upload-pack shares its semaphore with receive.

### Supply chain (L1)
- `go.sum` committed; `govulncheck` and `golangci-lint` in CI; minimal dependencies (stdlib, `x/crypto`, `x/net/html`, `x/oauth2`, goldmark, an OIDC verifier).
- Ruk: `cargo-deny` and `cargo audit`.
- CI runner images pinned by digest and rebuilt from source; release binaries reproducible.

## 4. Mitigation ownership summary

| Module | Owns |
|---|---|
| `internal/op` | C5, M7, L3: guard chain, scope ∩ permission, MCP filtering, problem mapping |
| `internal/authz` | C3: visibility predicates |
| `internal/authn/{password,totp,session,oidc}` | M1, M2, M3, H5 |
| `internal/token` | M9, M10 |
| `internal/web` | H3, H2 headers/CSP |
| `internal/render` | H2, L5 |
| `internal/gitwire/{http,ssh}` | TB2/TB3, M6 |
| `internal/receive` | H6 policy |
| `internal/rukclient` | H7 ID→path, M8 request ids, env allowlist |
| `internal/ci/{sandbox,secrets,sched,attest}` | C1, C2, H9, H8 |
| `internal/egress` | H1 (webhooks, OIDC, CI proxy) |
| `internal/secret` | H4: AEAD, redaction type |
| `internal/mirror` | H4 scopes, URL policy |
| `internal/webhook` | M11, L6 |
| `internal/ratelimit` | L2 + login/TOTP buckets |
| `internal/audit` | M5 |
| `internal/budget` | M4 caps |
| Ruk `daemon/{mod,ipc,registry}.rs` | C4, H7b, M8, on_behalf_of |
| Ruk `ruk-git/transport/{hooks,net,private,pack}.rs` | H6 hosted hooks, H1 dial policy, C3 private exclusion, ingest limits |

## 5. Required Ruk changes (breaking allowed)
1. Hosted mode refuses an empty `allowed_uids` (`daemon/mod.rs` config validation).
2. `on_behalf_of` on InvocationOpen and CommandOpen, recorded in `op.log` and claims (`daemon/mod.rs`, `ruk-core/coord`).
3. Outcome ledger keyed `(repo, request_id)`; digest computed server-side, excluding `secret_env` keys (`daemon/mod.rs:1149-1159`).
4. Hosted mode disables script hooks and adds a typed pre-receive verdict frame to rukie (`transport/hooks.rs`).
5. Egress policy or pinned `connect_to` for remote dials (`transport/net.rs`).
6. `openat2(RESOLVE_BENEATH)`-style store opens relative to root fds (`daemon/registry.rs`).
7. Per-invocation ingest limits passed from rukie (`transport/pack.rs` `IngestLimits`).
8. Per-repo `search` method with byte/time budget and page continuation.

## 6. Verification gates
- Op-table gate: every op has `Scope`/`Requires`/`Effect`. A matrix test sends each op as anonymous, wrong-scope token, right-scope-no-permission, and owner; expected 404/403/403/2xx on all four surfaces.
- Cross-tenant test: a private repo seeded with a canary string must stay invisible to an outsider in explore, search, forks, git ls-remote/fetch-by-SHA, MCP and webhooks.
- XSS corpus (SVG script, `javascript:` URLs, `<form>`, `<style>`, dangling `<img src='`) renders inert; a CSP header assertion runs on every route.
- SSRF tests: a rebinding DNS stub (first answer public, second 127.0.0.1) is refused at connect; redirects are not followed; IPv4-mapped IPv6 is blocked.
- CI escape drills: the job tries to reach the daemon socket path, the metadata host, 169.254.169.254 and another job's VM, and all fail. Secrets are absent in untrusted tier.
- Daemon tests: empty `allowed_uids` refuses to start; a path outside the roots gets `E_FORBIDDEN`; a symlink-swap race test; a request-id reuse across repos doesn't replay.
- TOTP replay: the same code twice within its window is refused the second time.
- Audit chain: editing one row breaks verification against the signed checkpoint.
````

### Critical Files for Implementation
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/registry.rs`: the roots/canonicalize authorization, handle table and owner principal (H7, the TOCTOU race).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/mod.rs`: `allowed_uids`, `CommandOpen` env confinement, the outcome ledger and `Principal::from_peer_uid` (C4, M8).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/ipc.rs`: the `SO_PEERCRED` peer check (C4).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-git/src/transport/hooks.rs`: script hooks that need to become a typed verdict frame (H6).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-git/src/transport/credentials.rs`: mirror credential handling and `sh -c` helpers (H4).

[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…

---
## Later corrections from the same agent (apply over the body above)

→ grep(GIT_AUTH|pub fn auth_header_for_url|pub fn credential_fill @ /home/alpha/rewrite/git-renewed/ruk/crates/ruk-git/src) ⇒ ok · 131 lines

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> Add the `ENotFound` lexical-fallba…

---
I accepted six review findings, each checked against the Ruk source, and rejected two. Below are the corrected sections for `threat-model.md`. Merge them into the artifact from my previous reply and save the result to `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/threat-model.md`. I still can't write the file myself: plan mode is read-only and this session has no write tool.

**Accepted**
1. **Symlink escape through a not-yet-existing path.** When `GIT_DIR` or `GIT_WORK_TREE` names a path that doesn't exist yet, `check_env_path_under_roots` (`daemon/mod.rs`, around lines 1590–1615) checks it only by text, using `normalize_lexical(..).starts_with(root)`. It never follows symlinks. If `<root>/x/link` points to `/etc`, then `GIT_DIR=<root>/x/link/new` passes the check and the command creates `/etc/new`. Added as its own threat, H7c.
2. **Hooks inherit the daemon's environment.** `hooks.rs:333-345` only adds variables and never clears the environment, which matches the module doc at `hooks.rs:40`. Any secret in the daemon's environment reaches hook scripts. Raised to High and added as H6b.
3. **Credential helpers come from the daemon's own config.** `helper_commands` (`credentials.rs:397-398`) and `config_paths` (`credentials.rs:125-126`) read the daemon process environment. In `auth_header_for_url` (`credentials.rs:772-775`), a configured helper takes priority over the per-mirror `GIT_AUTH_*` values. Only the `GIT_AUTH_*` fallback reads the command's variables (`http.rs:199-206`). So a helper in the daemon's `~/.gitconfig` answers for every tenant's mirror URL, and URL scopes match by prefix only. This means the doc's claim that commands inherit nothing from the daemon's environment (`mod.rs` `validate_command_fs`) doesn't hold for credentials. Added as H4b.
4. **Outcome ledger key.** It now includes the peer uid. The client keeps generating the request ID, because reconnect replay depends on that (`mod.rs:398-401`). The daemon recomputes the digest itself.
5. **New vs existing modules.** Every `internal/*` owner is now marked "(new)". The existing `rukie/` code is listed as reuse candidates, not owners.
6. **Forged CI attestations.** This was already covered as H8 (signing key kept out of the runner, verdict measured by the host agent). No change.

**Rejected**
- **Use the existing Forgejo modules as owners.** The settled decision is a ground-up rewrite that removes the Gitea surfaces, so `rukie/services/auth/source/oauth2/` and its neighbours are being deleted. They are listed below as code to lift, not as owners.
- **Use `rukie/modules/markup/camo.go` as the image proxy.** The design sends no external image requests at all (external images become links), so there is no `img-src` proxy to back.

---

**Replace row H6 and add four rows in §2:**
```markdown
| H6 | High | TB5 | Hook-script RCE, ref-namespace abuse, protected-branch bypass | A01, A08 / V2, V8 | `internal/receive` (new) + Ruk `transport/hooks.rs` |
| H6b | High | TB5 | Hook processes inherit the full daemon environment (`hooks.rs:40,333-345`, no `env_clear`), so any secret in the daemon env reaches hook scripts | A02, A04 / V13.3 | Ruk `transport/hooks.rs` |
| H4b | High | TB8 | Daemon-level credential helpers (`credentials.rs:125-126,397-398`, process env and daemon `~/.gitconfig`) take priority over per-mirror `GIT_AUTH_*` (`credentials.rs:772-775`) and answer for any tenant's URL (prefix scope match) | A01, A04 / V8, V13 | Ruk `transport/credentials.rs` |
| H7c | High | TB4 | `GIT_DIR`/`GIT_WORK_TREE` naming a not-yet-existing path is checked lexically only (`mod.rs` `check_env_path_under_roots`), so `<root>/link→/etc` + `/new` passes and the command writes outside the roots | A01 / V5 | Ruk `daemon/mod.rs` |
| M8 | Medium | TB4 | Ledger keyed by `request_id` alone with client-supplied digest (`mod.rs:1149-1159`), one ledger for all connections (`mod.rs:721`): a second allowed uid can block another caller's command or receive its recorded output | A08 / V15 | Ruk `daemon/mod.rs` + `internal/rukclient` (new) |
```

**§3 TB4, append after H7b:**
```markdown

[Showing lines 1-536 of 579 (49.7KB limit). Use :537 to continue]
```markdown
- H7c **High** — symlink escape through the lexical fallback. Fix (Ruk): when canonicalize returns `ENotFound`, walk up to the nearest existing ancestor, canonicalize that, and require it to be under a root. Then refuse any remaining path component that exists as a symlink at creation time (`O_NOFOLLOW` / `openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS)` from the root directory fd). Until that lands, `internal/rukclient` (new) never sends `GIT_DIR`/`GIT_WORK_TREE`.
```
**§3 TB4, replace the M8 bullet:**
```markdown
- M8 **Medium** — the client keeps owning `request_id`, which reconnect replay requires (`mod.rs:398-401`). Fix (Ruk): key the ledger by `(peer.uid, canonical_repo, request_id)` and recompute `command_digest` server-side over argv/dir/env (values of keys in `secret_env` replaced by their SHA-256), ignoring the client value. `internal/rukclient` (new) generates 128-bit random IDs.
```
**§3 TB5, append:**
```markdown
- H6b **High** — until hosted mode removes script hooks, `run_hook` must call `env_clear()` and add only `PATH=/usr/bin:/bin`, `GIT_DIR`, the quarantine variables and `GIT_PUSH_OPTION_*` (`hooks.rs:333-345`). Deployment rule: the daemon environment holds no secrets. Mirror tokens arrive per command in `CommandOpen.env`, and every other key comes from a file.
```
**§3 TB8, replace the "Daemon hermeticity" bullet:**
```markdown
- **H4b** — Fix (Ruk): resolve helpers and config paths from the command's `vars` map (the `*_in` variants already exist: `config_paths_in`, `helper_commands_in`, `helper_chain_for_in`) instead of `std::env::vars()`. Resolve `~` from `vars["HOME"]` (`credentials.rs:268`). In hosted mode, skip helpers entirely when `GIT_AUTH_*` is present. Interim deployment: daemon runs with `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=` (an empty value pins discovery, `credentials.rs:1053-1054`) and an empty `HOME`.
```
**§4 table:** add "(new)" to every `internal/*` and `cmd/*` owner. Add these rows: Ruk `transport/hooks.rs` → H6, H6b; Ruk `transport/credentials.rs` → H4b; Ruk `daemon/mod.rs` → C4, H7c, M8, on_behalf_of.

**§4, new paragraph "Existing rukie code to lift (not owners):"**
```markdown
- `rukie/modules/auth/password/hash/argon2.go` + `setting.go:23`: the current default is `argon2$2$65536$8$50` (t=2, m=64 MiB, p=8, 50-byte key). The new `internal/authn/password` (new) sets t=3, p=4 (RFC 9106 §4), and on login rehashes any stored hash that uses the old parameters.
- `rukie/modules/auth/password/hash/dummy.go`: pattern for the dummy hash against user enumeration.
- `rukie/services/auth/source/oauth2/`: reference only; it is deleted with the Gitea surfaces.
```
**§5 Ruk changes, replace items 3 and 4 and add 9–10:**
```markdown
3. Ledger keyed `(peer.uid, repo, request_id)`; digest recomputed server-side with `secret_env` values hashed (`daemon/mod.rs:721,1149-1159`).
4. Hosted mode disables script hooks and adds a typed pre-receive verdict frame. Until then, `env_clear()` plus an allowlist in `run_hook` (`transport/hooks.rs:333-345`).
9. Credential resolution reads only the command `vars` (`transport/credentials.rs:125-126,268,397-398,616-617`).
10. Not-yet-existing path check canonicalizes the nearest existing ancestor and refuses symlinks below it (`daemon/mod.rs` `check_env_path_under_roots`).
```
**§6 gates, add:**
```markdown
- `GIT_DIR=<root>/link/new` with `link → /tmp/outside` gets `E_FORBIDDEN`.
- A hook that prints `env` shows only allowlisted keys.
- With a helper in the daemon `~/.gitconfig` and `GIT_AUTH_TOKEN` in the command env, the push uses the command token and the helper never runs.
- Two allowed uids reusing the same `request_id`: the second neither replays the first's output nor blocks its command.
```

### Critical Files for Implementation
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/mod.rs`: `check_env_path_under_roots` (H7c), `claim_request_slot`/ledger (M8), `allowed_uids` (C4).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/registry.rs`: root resolution and the symlink race between check and open (H7b).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-git/src/transport/hooks.rs`: hook environment inheritance (H6b).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-git/src/transport/credentials.rs`: helper resolution from the process environment (H4b).
- `/home/alpha/rewrite/git-renewed/rukie/modules/auth/password/hash/argon2.go`: argon2 parameters to lift and upgrade (M1).