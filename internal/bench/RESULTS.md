# Daemon-path benchmark results

`tests/perf/daemonpath_test.go` (`TestDaemonPathBudget`, build tag `perf`)
appends one dated block per run. Each block records the binary provenance
(ruk binary, version, commit, and schema from `RUK_VERSION`, `RUK_COMMIT`,
and `RUK_SCHEMA`, git binary, host, CPU, Go version), the method (100
warmups, then 1000 paired samples with alternating stock/daemon order;
setup and verification outside the timer; 16 tokens minted for ada and
rotated per daemon request; nearest-rank p50/p95/p99; clone uses 20 warmups
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
