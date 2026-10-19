# Exhaustive manifest: rukie-ruk-native-forge-plan.md, round 2

Enumeration: requirement space. The universe has two parts. Part A is every blocker and major finding in the six round-2 reviews (58 cells). Part B is every open decision axis the reviews and the user answers raised (12 cells). Checks ran on 2026-10-20 against the amended plan file.

## Part A: review findings (58 cells)

Check: a script parsed each review file for findings with severity blocker or major and matched them one to one against `review-coverage.md`. Missing: none. Extra: none. Fourteen string probes on the plan confirmed the high-risk fixes (delete set from the base tree, approval acts, no `heads-since`, direct publish, refusal line numbers, no env credential channel, K7 spool and reconciler, secret-key restore rule, CLI spelling, spacing, CSP, exact tool pins, no old step ids, user answers). All 14 passed.

| Review | Cells | Class |
|---|---|---|
| CEO | 1 to 10 | covered |
| Design | 1 to 6 | covered |
| DX | 1 to 11, 14 to 17 | covered |
| Eng ruk | 1 to 12, 21 | covered |
| Eng rukie | 1 to 12 | covered |
| Pins | 1, 2 | covered |

`review-coverage.md` names the plan location of each cell.

## Part B: decision axes (12 cells)

| Id | Axis | Class | Reason |
|---|---|---|---|
| B01 | Fonts | covered | User: webfonts from a CDN. O24. |
| B02 | Font CDN | covered | User: Bunny Fonts. O24, CSP verified against the live endpoint (38 font faces). |
| B03 | HTML validity gate | covered | User: keep html-validate. O38; `html-validate@11.16.4` exists on npm. |
| B04 | Build order | covered | User: dogfood early. O37. |
| B05 | Agent repo creation | covered | User: add `repo:create`. O19. |
| B06 | CI op verbs | covered | Decided on spine (J.R.R. Tolkien over Claude Shannon): closed verb set, O39. |
| B07 | Lint and vuln install | covered | `go run` pins; `config verify` and `run` passed with v2.15.0, govulncheck v1.9.0 ran clean. |
| B08 | Pad go.sum | covered | Deleted at the pad; `go mod tidy -diff` and `go mod verify` exit 0 with no go.sum. |
| B09 | Strict validation, rukie | covered | Step 0 item 3; Go runs tests in `.agent-tasks` only by explicit path, verified. |
| B10 | Strict validation, ruk | deferred | Skill refusal: `config-conflict` on `unsafe_code` and `task-test-location-unsupported`. Owner: the user, if ruk should adopt `forbid`. |
| B11 | Per-account token bucket | deferred | No rate in the corpus. Owner: K4 follow-up once a measured rate exists (ENG-39). |
| B12 | Last commit per tree entry | deferred | No Ruk row (`ruk-changeset.md` §4). Owner: a future Ruk row. |

Wildcard catch-all assertion: not applicable; the target is a plan, not a code state space.

Tally: covered: 67, gap: 0, deferred: 3, total: 70.

Ordered gaps: none.
