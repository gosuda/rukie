## Register

| Dimension | Verdict | Reason |
|---|---|---|
| clarity | warn | Anchors mostly resolve. Three current-state claims are false: `land.prepare` is served, not refused; R3 says "add" `ByteSource::Streamed`, which exists; the `mod.rs:612` doc is accurate today. |
| impact | pass | Stream R stays inside `ruk`. External writes stay outside this stream. |
| audience | warn | An implementer cannot build R11 (no credential carrier) or R6 (no resolver owner) from the plan text alone. |
| risk | fail | R10 contradicts settled C15. R12 hardens code that R3 deletes. R1 as worded changes no behavior. |
| sequencing | fail | Plan R12 and R11 depend on state that plan R3 removes. Plan R6 depends on a helper that plan R9 creates. Changeset R13 and R22 have the same forward-dependency shape. |
| reversibility | pass | Every item is an additive commit on `main`. The schema bump is one minor step. |

Naming: `plan R#` means the plan's Stream R. `CS-R##` means a row in `ruk-changeset.md`. `D#` means a row in `ruk-serve-design.md` §0.

## Findings

1. **blocker** · `where` plan line 81 (R5): "Add the `heads-since` daemon query."
   **evidence** `ruk-changeset.md` C1 (line 13): "reject `op.feed`, `heads-since`, `R-REFJOURNAL`". §4 (line 182): "**Rejected** (C1)".
   **fix** Replace the sentence with: "Do not add a `heads-since` query. Decision C1 rejects it. Rukie reads `events.tail`."

2. **blocker** · `where` plan line 91 (R10): "Route receive-pack ref application through the publication lane."
   **evidence** C15 (`ruk-changeset.md` line 28): "receive-pack keeps direct CAS `Store::publish` … no lane operation for pushes." §4 (line 184): "`git.receive` / receive-pack through the lane … **Rejected** (C15)". Today `apply_refs` calls `dest.publish` directly (`crates/ruk-git/src/transport/session.rs:811`). `Store::publish` serializes writers under `lock_writer` (`crates/ruk-core/src/store/mod.rs:932`). The plan's wording reads as a lane submission, and C15 forbids that.
   **fix** Replace with: "Keep receive-pack on direct `Store::publish`. Writers serialize under `lock_writer`. Do not submit pushes to the publication lane."

3. **blocker** · `where` plan line 91 (R10): "Today the hosted path refuses both at `crates/ruk-core/src/coord/prepare.rs:297-305` and `3430-3484`."
   **evidence** `land.execute` is refused at `prepare.rs:296-301`. `prepare.rs:295` routes `Request::LandPrepare` to `land_prepare_parts` (defined at `prepare.rs:3447`). That function serves one exact candidate with empty requirements (`prepare.rs:3447-3510`). The `3430-3484` range is mostly the doc comment and the start of `land_prepare_parts`, not a refusal.
   **fix** Replace the sentence with: "Today `land.execute` is refused at `crates/ruk-core/src/coord/prepare.rs:296-301`. `land.prepare` is served in a restricted form: one exact candidate, empty requirements (`prepare.rs:3447-3510`). Extend it. Do not rebuild it."

4. **major** · `where` plan line 73 (R1): "Make `serve_store` parse `--stateless-rpc` and `--advertise-refs`."
   **evidence** The anchor is exact (`verbs_transport.rs:1859`). Both flags are absent. Every unknown `-` argument is dropped at `verbs_transport.rs:1867-1870`. Parsing alone therefore changes no behavior.
   **fix** Replace with: "R1 stateless RPC. `serve_store` drops every flag today (`verbs_transport.rs:1867-1870`). Accept `--stateless-rpc` and `--advertise-refs` in the serve verbs and carry them through `ServeStore`. `--advertise-refs` writes the ref advertisement and exits. `--stateless-rpc` reads one request round from stdin and writes one response."

5. **major** · `where` plan line 95 (R12): "Make `check_env_path_under_roots` symlink safe."
   **evidence** The function exists at `crates/ruk/src/daemon/mod.rs:1589`. `ruk-changeset.md` CS-R07 (line 78) deletes it with the argv path. Plan R3 (the envelope work) orders that deletion first. The fix targets code that no longer exists after R3.
   **fix** Replace the first clause with: "R3 deletes `check_env_path_under_roots`. Do not harden it. Apply the symlink-safe open (`openat2` with `RESOLVE_BENEATH` and `RESOLVE_NO_SYMLINKS`) to repository-path containment. `Registry` canonicalizes repository paths today (`daemon/mod.rs` module doc, lines 3-5). Put the check in the new git-service path."

6. **major** · `where` plan line 95 (R12): "Key the outcome ledger on `(uid, request_id)`."
   **evidence** `OutcomeLedger` is one string-keyed map (`daemon/mod.rs:540-543`). CS-R10 (line 87) keys `ServiceReceipts` on "(peer uid, canonical repo, request_id)". Its risk note: "keying must include uid+repo or cross-tenant replay returns (M8)".
   **fix** Replace with: "Key `ServiceReceipts` on `(peer uid, canonical repository, request_id)`. A replay under another repository must not return the stored report."

7. **major** · `where` plan line 93 (R11): "Accept per-call `GIT_AUTH_*` values in the command environment for hosted pushes."
   **evidence** CS-R07 (line 78) deletes the `env` field from `CommandOpen`. The plan's R3 removes only the argv interpreter, so the plan and CS-R07 conflict. CS-R29 adds `TransportAuth` to `InvocationOpen`. CS-R30 resolves helpers from a per-call `vars` map. No plan item creates either carrier.
   **fix** Replace the first two sentences with: "Add `TransportAuth` (`kind`: `basic`, `bearer`, or `none`) to `InvocationOpen`, per CS-R29. Resolve helper commands and config paths from the per-call `vars` map, per CS-R30. R3 deletes `CommandOpen.env`. Do not read credentials from the environment."

8. **major** · `where` plan line 97 (R13): "run `cargo clippy --workspace --all-targets` … Deny new warnings. The count of pre-existing `unreachable_pub` warnings must not grow."
   **evidence** CI runs `cargo clippy --workspace --all-targets --all-features --locked -- -D warnings` (`.github/workflows/ci.yml:36`). `Cargo.toml:104,106,113` set `missing_debug_implementations`, `unreachable_pub`, and `panic_in_result_fn` to `warn`. Under `-D warnings`, any such warning fails. A "pre-existing count" therefore has no meaning. `.outline/sdd/task-12r-report.md:3-4` reports workspace clippy red on pre-existing lints. That is a dated report, and I did not re-execute it.
   **fix** Replace the gate sentence with: "After each item, run `cargo clippy --workspace --all-targets --all-features --locked -- -D warnings`." Delete the `unreachable_pub` count sentence. Insert: "If the clippy gate fails on a pre-existing lint, fix that lint in the first Stream R commit."

9. **major** · `where` plan line 85 (R7): "Add `repo.init`, `repo.delete`, `repo.rename`, and `repo.stat` as `MethodKind::Admin`."
   **evidence** `MethodKind` has no `Admin` variant (`protocol.rs:1131-1136`). CS-R23 (line 110) makes init, delete, and rename admin. CS-R14 (line 96) defines `repo.stat` as a read that the repo home page and health checks use. CS-C11 is a one-line summary that says `repo.*`.
   **fix** Replace with: "Add `repo.init`, `repo.delete`, and `repo.rename` as `MethodKind::Admin`. Add `repo.stat` as `MethodKind::Read`."

10. **major** · `where` plan lines 83 and 85 (R6 and R9): dependency on `resolve_search_root`.
    **evidence** The search design creates `resolve_search_root(store, at)` as a shared helper "future `file`/`blame`/`diff` producers will reuse" (`ruk-search-design.md:76`). It is created in the R9 search work. Plan R6 (hosted reads) comes earlier and needs the same resolver. CS-R13 (line 95) also says "share the `resolve_search_root` helper" in the earlier hosted-read row, and no CS row creates it earlier. The changeset therefore has a forward dependency.
    **fix** Insert under R6: "Create the Head and Change resolver in `coord/hosted.rs` as `resolve_search_root`, with the signature in `ruk-search-design.md:76`. R9 reuses it. Do not create it in R9."

11. **major** · `where` plan line 81 (R5): "Emit an event inside `Store::publish` for every `FactTable::Refs` mutation … This covers receive-pack."
    **evidence** `apply_refs` (`session.rs:778`) is the receive-pack write path. It calls `dest.publish` with no event source today. Without a call-site change, receive-pack emits no events.
    **fix** Insert: "Update `apply_refs` (`crates/ruk-git/src/transport/session.rs:778-833`) to pass `EventSource::ReceivePack`. Update every other `Store::publish` caller in the same commit."

12. **major** · `where` plan line 83 (R6): "Serve hosted `file`, `log`, `diff`, `show`, `blame`, and `query`."
    **evidence** CS-R13 (line 95) also lists `status`. `read_gap` names `Method::Status` (`hosted.rs:1352-1354`). Serving six reads leaves `status` refused.
    **fix** Replace the list with: "Serve hosted `file`, `log`, `diff`, `show`, `blame`, `query`, and `status`."

13. **minor** · `where` plan line 77 (R3): "Add `ByteSource::Streamed`."
    **evidence** The variant already exists: `ruk-core/src/protocol.rs:691-694` (`Streamed { stream, size }`). The real gap is the frame cap. `MAX_IPC_CONTROL_BYTES` is 1 MiB (`ruk/src/daemon/ipc.rs:23`), and the schema limit is 16 MiB (`protocol.rs:3703`, asserted at `:5329`).
    **fix** Replace with: "Use the existing `ByteSource::Streamed` for replies above the inline cap. Align `MAX_IPC_CONTROL_BYTES` (`ipc.rs:23`) with the 16 MiB schema limit."

14. **minor** · `where` plan line 83 (R6): "Fix the stale doc at `daemon/mod.rs:612`."
    **evidence** The doc at `daemon/mod.rs:609-613` is accurate today. It says that `op.log`, `claim.list`, and `attest.list` are served (`hosted.rs:632-637` refuses the rest). The stale Forgejo wording is in `CommandOpen` at `mod.rs:391-392`.
    **fix** Replace with: "Update the doc at `daemon/mod.rs:609-613` after the new read arms land. Remove the Forgejo wording at `mod.rs:391-392` in R1 or R3."

15. **minor** · `where` plan line 97 (R13): schema-item list "R5, R7, R8, R9, R10".
    **evidence** R11 adds the `remote.refs` method row (CS-R22, line 104).
    **fix** Replace the list with: "R5, R7, R8, R9, R10, R11."

16. **minor** · `where` plan lines 87 and 89 (R8, R9): `KeysetPage` and `SearchPool` named as if they exist.
    **evidence** Neither symbol exists in `crates/ruk-core/src`. `KeysetPage<T>` is defined in `ruk-serve-design.md` D9 and §4.2. `SearchPool` is a new file, `crates/ruk-core/src/search.rs` (`ruk-search-design.md:106`).
    **fix** In R8, insert: "Add `KeysetPage<T>` with `items` and `next_after`, per `ruk-serve-design.md` §4.2." In R9, replace "Use the core-owned `SearchPool`" with "Add `SearchPool` in `crates/ruk-core/src/search.rs`."

17. **minor** · `where` plan line 81 (R5): `events.tail` fields "`after`, `limit`, and `wait_ms`."
    **evidence** CS-R20 (line 102) specifies `after`, `limit` (at most 1000), `wait_ms` (at most 30000), and `prefix`.
    **fix** Replace with: "Add `events.tail` with fields `after`, `limit` (at most 1000), `wait_ms` (at most 30000), and `prefix`."

18. **minor** · `where` plan line 97 (R13): "run `ruk model --format json` and write it to `rukie/docs/plan/ruk-model.json`."
    **evidence** The flag exists (`crates/ruk/src/main.rs:46-49`). The command writes to stdout (`main.rs:170`, `:304-307`).
    **fix** Replace with: "Run `ruk model --format json > rukie/docs/plan/ruk-model.json`."

19. **minor** · `where` plan line 75 (R2): "The handshake profile is `host` on the daemon side."
    **evidence** `negotiate` echoes the client profile (`ipc.rs:472-474`). It rejects only an empty profile (`ipc.rs:453`). The daemon sets no profile of its own. Fixtures use `"forgejo"` (`ipc.rs:643`).
    **fix** Replace with: "The daemon accepts any non-empty profile and echoes it. Fixtures and docs use `host`. Rukie sends `rukie`."

20. **minor** · `where` plan line 97 (R13): the nextest command.
    **evidence** CI runs `cargo nextest run --workspace --locked` after installing `cargo-nextest@0.9.148` (`ci.yml:52-53`). No `nextest.toml` or `.cargo/config.toml` exists.
    **fix** Replace the command with: "`cargo nextest run --workspace --locked`."

21. **major (dependency answer)** · Your question: does any R item depend on a later R item?
    **evidence** Within the plan, one item does. R6 needs the resolver that R9 creates (Finding 10). R11 needs the `TransportAuth` carrier that no item creates (Finding 7). The other direction also appears. R12 targets state that R3 deletes (Findings 5 and 6). Within the changeset, CS-R13 needs the helper from the search work (Finding 10). CS-R22 needs `TransportAuth` from CS-R29 (`ruk-changeset.md:104`).
    **fix** Add to the plan's Stream R preamble: "R6 creates the `resolve_search_root` resolver. R3 creates `TransportAuth`. R9 and R11 consume them." Add the same ordering note to the changeset.

## Claim check

| Claim | Status | Actual |
|---|---|---|
| `serve_store` at `verbs_transport.rs:1859` | true | `verbs_transport.rs:1859` |
| `--stateless-rpc` and `--advertise-refs` parsed there | false | flags dropped at `verbs_transport.rs:1867-1870` |
| `ForgejoCommand` at `main.rs:30-72` | true | enum `:30-38`; `Command::Forgejo` `:64-72` |
| `E_HOSTED` refusals at `hosted.rs:539-640` | true | refusal arm `hosted.rs:632-637`; `read_gap` `:1349-1366` |
| stale doc at `daemon/mod.rs:612` | drifted | `mod.rs:609-613` is accurate; stale wording at `mod.rs:391-392` |
| `InvocationOpen` at line 305 | true | `daemon/mod.rs:305` |
| `CommandOpen` at line 390 | true | `daemon/mod.rs:390` |
| `prepare.rs:297-305` refusal | drifted | `land.execute` refusal `prepare.rs:296-301` |
| `prepare.rs:3430-3484` refusal | false | `land_prepare_parts` serves `land.prepare`, `prepare.rs:3447-3510` |
| `coord/undo.rs` | true | `coord/undo.rs:1`; `coord/mod.rs:28` |
| `check_env_path_under_roots` | true, to be deleted | `daemon/mod.rs:1589`; deleted by CS-R07 |
| `OutcomeLedger` | true, to be deleted | `daemon/mod.rs:540` |
| `ReceiveHooks` | true | `ruk-git/src/transport/hooks.rs:77` |
| frame kind 9 `MergeNotices` | true | `daemon/ipc.rs:66`; rejection arms `daemon/mod.rs:850,1018,1754,1830,1959` |
| `ClaimRecord` | true | `protocol.rs:1718` (lacks task/change key) |
| `AttestRequirement` | true | `protocol.rs:2037` (lacks `valid_until`) |
| `AttestStatement` | true | `protocol.rs:1776` (lacks `predicate_digest`) |
| `FactTable::Refs` | true | `store/types.rs:240` |
| `Store::publish` | true | `store/mod.rs:911` |
| `PageCursor` | true | `protocol.rs:506` |
| `SearchPool` | false (absent) | new file `ruk-core/src/search.rs` per `ruk-search-design.md:106` |
| `ErrorDetail::Http` | false (absent) | only `Push` at `protocol.rs:2516-2523` |
| `ByteSource::Streamed` (R3 "add") | false | exists at `protocol.rs:691-694` |
| `SCHEMA_MINOR` | true | `protocol.rs:68` = 1 |
| method table, `required` array, consistency test | true | table `protocol.rs:3286-3332`; `required` `:5432-5478`; test `:5417-5484`, assert `:5483` |
| method count | drifted | 47 today (count of `Variant, "name", …;` rows at `:3286-3332`, which equals `required` length). C17's 60 is wrong: 47 + 14 = 61. |
| `README.md` method count | drifted | says 46 at `README.md:5` and `:64`; 47 today |
| `ruk model --format json` dumps the schema | true | `main.rs:46-49`; `:170`; `:304-307` |
| `ruk forgejo serve` exists | true | `main.rs:64-72`, `:201-203` |
| `ruk forgejo serve` exits 2 | unverified-by-run | I have no shell and did not execute the binary. The expectation is clap's usage-error exit code 2, with clap pinned at `=4.6.7` in `Cargo.toml`. |
| `cargo nextest` available locally | presence true, version unverified | `~/.cargo/bin/cargo-nextest` is listed (30.2 MB). I could not run `cargo nextest --version`. CI pins 0.9.148 (`ci.yml:52`). No nextest config file exists. |
| clippy passes with `-D warnings` | unverified-by-run; red per repo report | not executed. CI uses `-D warnings` (`ci.yml:36`). Three lints are warn-level (`Cargo.toml:104,106,113`). `.outline/sdd/task-12r-report.md:3-4` records workspace clippy red on pre-existing lints. No `clippy.toml` or `.cargo/config.toml` exists. |
| toolchain pinned | true | `rust-toolchain.toml`: channel `1.99.1`; CI `ci.yml:19` |
| R10 "publication lane" | false as written | contradicts C15 (`ruk-changeset.md:28`); `apply_refs` uses `dest.publish` at `session.rs:811` |

Advisories I rejected: the claim that README has no methods row (README has rows at lines 5 and 64); and the claim that `ruk forgejo serve` exit 2 is confirmed (I could not execute it).

### Critical Files for Implementation

- `ruk/crates/ruk-core/src/protocol.rs`: method table, `required` array, `SCHEMA_MINOR`, `KeysetPage`, `ErrorDetail::Http`.
- `ruk/crates/ruk/src/daemon/mod.rs`: `InvocationOpen` (305), `CommandOpen` (390), legacy argv path, `ServiceReceipts` call site.
- `ruk/crates/ruk-git/src/transport/session.rs`: `apply_refs` (778), the `EventSource::ReceivePack` call site.
- `ruk/crates/ruk-core/src/coord/hosted.rs`: read arms, `read_gap`, `resolve_search_root` owner.
- `ruk/crates/ruk-core/src/coord/prepare.rs`: `land.execute` refusal (296-301) and `land_prepare_parts` (3447-3510).

## Ledger rows

- Delete the R5 `heads-since` sentence, and state that `events.tail` is the only ref feed.
- Rewrite R10's receive-pack clause to keep direct `Store::publish` and reject lane submission.
- Rewrite R10's current-state sentence: `land.execute` refuses, `land.prepare` serves a restricted form.
- Rewrite R1 so `--stateless-rpc` and `--advertise-refs` change behavior, not just parse.
- Delete R12's `check_env_path_under_roots` clause. Move the symlink-safe open to the new git-service repository-path check.
- Change R12's ledger key to `(peer uid, canonical repository, request_id)`.
- Rewrite R11's credential source to `TransportAuth` on `InvocationOpen`, plus the per-call `vars` map.
- Correct the method count to 47 today and 61 after the campaign. Fix `README.md:5,64`. Correct changeset C17.
- Replace the R13 clippy gate with the CI command, `-D warnings` included. Delete the `unreachable_pub` count sentence.
- Split R7: `repo.init`, `repo.delete`, and `repo.rename` are Admin; `repo.stat` is Read.
- Create `resolve_search_root` in R6. R9 reuses it.
- Add the `apply_refs` `EventSource::ReceivePack` call-site change to R5.
- Add `status` to R6's hosted reads (seven reads, not six).
- Replace R3's "Add `ByteSource::Streamed`" with "use the existing variant" and align the 1 MiB IPC cap with the 16 MiB schema limit.
- Rewrite R6's stale-doc instruction to update the doc after the new read arms land.
- Add R11 to R13's schema-item gate list.
- State that `KeysetPage` and `SearchPool` are new, with citations.
- Add `prefix` and the limit bounds to R5's `events.tail` fields.
- Write the model dump with shell redirection in R13.
- Rewrite R2's handshake-profile sentence to match echo negotiation.
- Use `cargo nextest run --workspace --locked` in R13.
- Run `cargo clippy --workspace --all-targets --all-features --locked -- -D warnings` and `cargo nextest --version` on a machine with a shell, to settle the two unverified-by-run claims.
- Run `target/debug/ruk forgejo serve; echo $?` on a machine with a shell, to settle the exit-2 claim before R2 check 1 passes.