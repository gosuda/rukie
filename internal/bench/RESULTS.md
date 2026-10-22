# Daemon-path benchmark results

`tests/perf/daemonpath_test.go` (`TestDaemonPathBudget`, build tag `perf`)
appends one dated block per run. Each block records the binary provenance
(ruk binary, version, commit, and schema from `RUK_VERSION`, `RUK_COMMIT`,
and `RUK_SCHEMA`, git binary, host, CPU, Go version), the method (100
warmups, then 1000 paired samples with alternating stock/daemon order;
setup and verification outside the timer; 16 tokens minted for ada and
rotated per daemon request; nearest-rank p50/p95/p99; clone uses 2 warmups
and 200 paired samples as report-only data), the fixture (200
mixed-size commits, 4 branches, 1000 tags pushed through the daemon path;
the CGI baseline serves an identical bare clone), and a table with the
columns below. Incremental fetch stages one genuinely new commit per sample
on both servers outside timing, then each timed fetch acquires one distinct,
previously missing commit and verifies its exact OID on both receivers. The
100 warmup plus 1,000 measured commits and refs are pre-staged in one setup
phase to avoid timing source pushes; fetch remains the timed operation. The perf stack disables
request-rate limiting so the measurements capture serving latency, not
admission throttling. Single-ref lookup runs
`git ls-remote <URL> refs/heads/main` against the daemon Smart HTTP
endpoint and against the stock CGI endpoint, comparing the exact returned
OID against the seeded main OID on both sides. Only a fully completed run
appends a block; a run that errors mid-way records nothing and reports the
measurement as incomplete rather than as a budget verdict.

Table columns: workload, stock p50, stock p95, stock p99, daemon p50,
daemon p95, daemon p99, ratio p95, budget, authority, result. The authority
column cites the budget source: CEO-08 for fetch and lookup, verification
§7 for info/refs, and the V5 brief for the report-only clone.

Budget authorities: incremental-fetch ratio at most 7.5x from CEO-08;
single-ref-lookup ratio at most 4x from CEO-08; info-refs daemon p95 at most
150ms from verification §7; clone report-only with no threshold from the V5
brief. A FAIL verdict stops the dogfood gate and needs a new Ruk row.

No budget measurement has landed yet. The following diagnostic runs are not
budget results and do not produce pass/fail ratios:

- 300 daemon-only fetches, followed by 2,000 stock-only fetches, passed in
  971.56 seconds. The stock server served a separate bare clone.
- A fixed-client 300-daemon-fetch run passed in 194.64 seconds with stable
  daemon counts: six threads, 18 file descriptors, and 5.2–8.6 MiB Go heap.
  Test cleanup then reported a non-empty temporary pack directory.
- The paired 1,000-sample run first stopped at daemon fetch 798 with
  `early EOF` and `invalid index-pack output`. A regression test proved that
  legal upload-pack stdout during stdin refill was rejected by the client.
  `internal/ruk/command.go` now writes and credits those stdout frames while
  waiting for stdin credit, and captures bounded stderr during refill.
- A second paired run timed out at 30 minutes while a daemon fetch awaited
  `CommandAccepted`; no percentile table was produced. The 500-fetch diagnostic
  and optimized paired gate are in progress.

This file holds the method, the table schema, and diagnostic evidence only.

## 2026-10-23 (transcribed partial runs; the test appended nothing; superseded by the completed ae8d221 runs below, kept for history)

The test appends a block only on a fully completed run. Neither run below
completed, so neither appended; the fetch and lookup rows are transcribed
from the run logs (/tmp/v5-daemonpath-optimized2.log,
/tmp/v5-daemonpath-bounded.log) because the V5 lane needs the measured
numbers on record. Binary provenance was not captured in those logs
(RUK_BIN unrecorded); no binary claim is made here. Clone and info/refs
produced no rows in either run and stay unmeasured.

Run A (2026-10-23 ~04:55 UTC): fetch and lookup passed, then the run hit
the 30-minute timeout in a later phase; overall FAIL, no clone/info-refs
rows. Run B (2026-10-23 ~05:19 UTC): fetch and lookup passed, then the
harness panicked with index out of range in `samples.p` (an older test
revision predating the committed empty-sample guard); overall FAIL, no
clone/info-refs rows.

| workload | stock p50 | stock p95 | stock p99 | daemon p50 | daemon p95 | daemon p99 | ratio p95 | budget | authority | result |
|---|---|---|---|---|---|---|---|---|---|---|
| incremental-fetch (run A) | 246.412629ms | 328.898748ms | n/a | 632.563799ms | 1.241796024s | n/a | 3.78x | <= 7.5x stock p95 | CEO-08 | PASS |
| single-ref-lookup (run A) | 53.39755ms | 57.005058ms | n/a | 84.850088ms | 90.285332ms | n/a | 1.58x | <= 4x stock p95 | CEO-08 | PASS |
| incremental-fetch (run B) | 246.5533ms | 325.328489ms | n/a | 639.982376ms | 1.19919917s | n/a | 3.69x | <= 7.5x stock p95 | CEO-08 | PASS |
| single-ref-lookup (run B) | 54.706149ms | 58.535492ms | n/a | 84.621112ms | 88.918738ms | n/a | 1.52x | <= 4x stock p95 | CEO-08 | PASS |
| clone (runs A, B) | unmeasured | unmeasured | unmeasured | unmeasured | unmeasured | unmeasured | unmeasured | report-only, no threshold | V5 brief | INCOMPLETE |
| info-refs (runs A, B) | unmeasured | unmeasured | unmeasured | unmeasured | unmeasured | unmeasured | unmeasured | <= 150ms daemon p95 | verification §7 | INCOMPLETE |

At transcription time no rebuilt release binary existed yet, so no budget
verdict was claimed for clone or info/refs. The rebuild has since landed
(ruk main `bf3063c`); the bounded run below is the follow-up.

## 2026-10-23 (bounded run against the bf3063c rebuild; overall FAIL)

Command (exact, in `rukie/.outline/worktree/dogfood`):
`RUK_BIN=ruk/.outline/worktree/model-build/target/release/ruk
GIT_EXEC=$(command -v git) RUK_COORDINATOR=off go test -count=1 -tags perf
-timeout 40m -run '^TestDaemonPathBudget$' -v ./tests/perf/`.
Binary: 84,857,792 bytes, stamped 2026-10-23 06:57 +0900, built from ruk
main `bf3063c` (stdin payload admission against the window, separate frame
bound with slack). RUK_VERSION/RUK_COMMIT/RUK_SCHEMA env were unset (the
briefed command sets none), so no version claims beyond the build stamp.
Full log: /tmp/v5-daemonpath-final.log. Test exited 1 after 1247.90s; the
test appended nothing (only fully completed runs append).

| workload | stock p50 | stock p95 | stock p99 | daemon p50 | daemon p95 | daemon p99 | ratio p95 | budget | authority | result |
|---|---|---|---|---|---|---|---|---|---|---|
| incremental-fetch | 245.984245ms | 325.54296ms | n/a | 642.149795ms | 1.221979234s | n/a | 3.75x | <= 7.5x stock p95 | CEO-08 | PASS |
| single-ref-lookup | 53.892665ms | 58.315363ms | n/a | 85.615875ms | 92.29617ms | n/a | 1.58x | <= 4x stock p95 | CEO-08 | PASS |
| clone | no samples (stock=0 daemon=0) | no samples | no samples | no samples | no samples | no samples | unmeasured | report-only, no threshold | V5 brief | INCOMPLETE |
| info-refs | unmeasured | unmeasured | unmeasured | unmeasured | unmeasured | unmeasured | unmeasured | <= 150ms daemon p95 | verification §7 | INCOMPLETE |

The clone workload produced zero samples on both sides, so the committed
empty-sample guard stopped the run with `workload clone produced no
samples (stock=0 daemon=0); nothing recorded` instead of indexing an empty
slice. Daemon output for parent diagnosis:
`/tmp/TestDaemonPathBudget2056305955/001/ruk-4137305063.log`. No diagnosis
or tuning was done in this lane; per the brief, the failed budget stops
the dogfood gate and needs a new Ruk row. That stop is now lifted by the
completed runs below: the zero-sample cause was the unset clone
workload totals, fixed by setting warmups 2 and total 200 (cloneSamples).

## 2026-10-22T23:32:25Z

- ruk binary: /home/alpha/rewrite/git-renewed/ruk/.outline/worktree/model-build/target/release/ruk
- ruk version: unset
- ruk commit: unset
- ruk schema: unset
- git binary: /usr/bin/git
- host: alpha-Precision-7920-Tower
- cpu: 80/amd64
- go: go1.27.3
- method: 100 warmups, then 1000 paired samples with alternating stock/daemon order; prepare and verify outside the timer; 16 tokens minted for ada and rotated per daemon request; nearest-rank p50/p95/p99

| workload | stock p50 | stock p95 | stock p99 | daemon p50 | daemon p95 | daemon p99 | ratio p95 | budget | authority | result |
|---|---|---|---|---|---|---|---|---|---|---|
| incremental-fetch | 249.718275ms | 333.619631ms | 369.613872ms | 666.771468ms | 1.240287905s | 1.446051348s | 3.72x | <= 7.5x stock p95 (CEO-08) | CEO-08 | PASS |
| single-ref-lookup | 56.41962ms | 67.535625ms | 94.875045ms | 85.160094ms | 99.59004ms | 132.387513ms | 1.47x | <= 4x stock p95 (CEO-08) | CEO-08 | PASS |
| clone | 261.264802ms | 275.588993ms | 300.033186ms | 328.9257ms | 348.983954ms | 359.519417ms | 1.27x |  | V5 brief | REPORT |
| info-refs | 11.966025ms | 14.47581ms | 15.967233ms | 6.364254ms | 8.472101ms | 10.475506ms | 0.59x | <= 150ms daemon p95 (verification §7) | verification §7 | PASS |

## 2026-10-22T23:58:11Z

- ruk binary: /home/alpha/rewrite/git-renewed/ruk/.outline/worktree/model-build/target/release/ruk
- ruk version: unset
- ruk commit: unset
- ruk schema: unset
- git binary: /usr/bin/git
- host: alpha-Precision-7920-Tower
- cpu: 80/amd64
- go: go1.27.3
- method: 100 warmups, then 1000 paired samples with alternating stock/daemon order; prepare and verify outside the timer; 16 tokens minted for ada and rotated per daemon request; nearest-rank p50/p95/p99

| workload | stock p50 | stock p95 | stock p99 | daemon p50 | daemon p95 | daemon p99 | ratio p95 | budget | authority | result |
|---|---|---|---|---|---|---|---|---|---|---|
| incremental-fetch | 259.080783ms | 366.067975ms | 454.214716ms | 712.336734ms | 1.373328822s | 1.664995605s | 3.75x | <= 7.5x stock p95 (CEO-08) | CEO-08 | PASS |
| single-ref-lookup | 55.840196ms | 62.496093ms | 84.734543ms | 85.810028ms | 95.695773ms | 125.848208ms | 1.53x | <= 4x stock p95 (CEO-08) | CEO-08 | PASS |
| clone | 259.672604ms | 269.683494ms | 281.803582ms | 329.890047ms | 344.528273ms | 348.133321ms | 1.28x |  | V5 brief | REPORT |
| info-refs | 11.114565ms | 12.457431ms | 13.715871ms | 6.212459ms | 7.779608ms | 8.641173ms | 0.62x | <= 150ms daemon p95 (verification §7) | verification §7 | PASS |

Provenance for both complete blocks above: RUK_BIN was the model-build
release binary, 84,918,512 bytes stamped 2026-10-23 08:30 +0900, built
from ruk main `ae8d221` (hardened streaming). The blocks record version,
commit, and schema as unset because the briefed command sets none of
RUK_VERSION, RUK_COMMIT, or RUK_SCHEMA. Both runs reach the same verdict
(fetch PASS around 3.7x, lookup PASS around 1.5x, clone REPORT around
1.3x, info-refs PASS well under 150 ms daemon p95), so the V5 capacity
gate is met and the earlier stop is lifted.
