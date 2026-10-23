# task-0e2cc1171807

## Goal
Make rukie @rukie/ diverge completely from gitea, uses only ruk as backend and highly optimized for Heavy Agentic Engineering. Also better designs and remove all the old stacks, and no-js UI, and highly optimize Go codebase, and most importantly best interfaces for agents and humans for Agentic-first Programming.

## Success criteria
- [ ] SC-01: `git grep -niI gitea -- ':!LICENSE' ':!docs/plan' ':!tasks' ':!.agent-tasks'` prints no line.
- [ ] SC-02: `go list -deps ./...` lists no package path containing `gitea`, `go-git`, or `git2go`, and `git grep -n 'exec.Command("git"' -- '*.go' ':!*_test.go'` prints no line.
- [ ] SC-03: `git ls-files` lists no `package.json`, `pnpm-lock.yaml`, `tsconfig.json`, `pyproject.toml`, `uv.lock`, or `flake.nix`, and no path ending in `.ts`, `.tsx`, `.js`, `.mjs`, `.vue`, or `.py`.
- [ ] SC-04: `TestAllHTMLRoutesZeroJS` passes, and html-validate reports zero errors and zero warnings on every page `TestDumpRenderedPages` writes.
- [ ] SC-05: `TestNegotiationMatrix` passes, and `TestMCPDefaultToolBudget` passes.
- [ ] SC-06: every `TestAgentFlow` subtest passes against the real daemon.
- [ ] SC-07: `TestPerfBudget` passes on the release binaries against the plan O30 budgets.

## Verification
- SC-01: `.agent-tasks/task-0e2cc1171807/tests/sc_01_test.go`, `go test -count=1 -run '^TestSC01$' ./.agent-tasks/task-0e2cc1171807/tests`
- SC-02: `.agent-tasks/task-0e2cc1171807/tests/sc_02_test.go`, `go test -count=1 -run '^TestSC02$' ./.agent-tasks/task-0e2cc1171807/tests`
- SC-03: `.agent-tasks/task-0e2cc1171807/tests/sc_03_test.go`, `go test -count=1 -run '^TestSC03$' ./.agent-tasks/task-0e2cc1171807/tests`
- SC-04: `.agent-tasks/task-0e2cc1171807/tests/sc_04_test.go`, `go test -count=1 -run '^TestSC04$' ./.agent-tasks/task-0e2cc1171807/tests`
- SC-05: `.agent-tasks/task-0e2cc1171807/tests/sc_05_test.go`, `go test -count=1 -run '^TestSC05$' ./.agent-tasks/task-0e2cc1171807/tests`
- SC-06: `.agent-tasks/task-0e2cc1171807/tests/sc_06_test.go`, `go test -count=1 -run '^TestSC06$' ./.agent-tasks/task-0e2cc1171807/tests`
- SC-07: `.agent-tasks/task-0e2cc1171807/tests/sc_07_test.go`, `go test -count=1 -run '^TestSC07$' ./.agent-tasks/task-0e2cc1171807/tests`

## Boundaries
- Project-stable rules remain in project configuration.
- Task-specific goals and criteria remain under `.agent-tasks/task-0e2cc1171807/`.
