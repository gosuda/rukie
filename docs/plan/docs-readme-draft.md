# docs-readme-draft.md

## Purpose and status

This artifact contains the proposed replacement `README.md`, the documentation information architecture, prepared GitHub metadata, and the evidence required before publication.

**This is a post-rewrite draft, not documentation for the current checkout.** Present-tense descriptions in the README describe the settled target. Commands, operation IDs, configuration examples, generated reference files, and measurement harnesses proposed below are implementation contracts, not observed functionality. They must land with the features they describe.

No files were changed, no commands were executed, and no GitHub settings were changed.

### Audience and voice

- **Evaluators:** developers deciding whether to host repositories on rukie.
- **Operators:** people installing and maintaining the service.
- **Users:** developers and agents submitting changes, reviewing work, and running CI.
- **Contributors:** people changing rukie or its Ruk backend.

Use direct sentences, name the actor, and explain unfamiliar terms when they first appear. Keep the Ruk README’s concrete command examples and explicit limits. Do not copy its benchmark results into rukie’s README: those results concern Ruk workloads, not the new forge.

### Source grounding

| Source | Confirmed fact or constraint |
|---|---|
| `/home/alpha/rewrite/git-renewed/ruk/README.md:1-16` | Ruk describes itself as an agent-native Rust VCS with Git object interoperability, native repository state, a typed protocol, MCP, and a hosted daemon. Its existing measurements include explicit workload and hardware limits. |
| `/home/alpha/rewrite/git-renewed/ruk/README.md:35-55` | Ruk has a Cargo release-build path, a Git-compatible CLI, and a repository-import command. |
| `/home/alpha/rewrite/git-renewed/ruk/README.md:59-69` | The currently documented daemon command is `ruk forgejo serve --config`; this is existing naming, not a decision to preserve Forgejo branding. |
| `/home/alpha/rewrite/git-renewed/ruk/Cargo.toml:1-18` | Ruk is a Cargo workspace containing the `ruk` binary crate; its workspace license is Apache. |
| `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/mod.rs:58-78,176-223` | The current daemon uses a Unix socket, repository roots, configurable resource bounds, and TOML configuration. |
| `/home/alpha/rewrite/git-renewed/rukie/README.md:1-63` | The existing README still documents Gitea, Gitea services, and Gitea commands. Those installation and feature claims are not evidence for the rewrite. |
| `/home/alpha/rewrite/git-renewed/rukie/go.mod:1-5` | The existing module is still `gitea.dev`; the target module path is a required cutover, not current behavior. |
| `/home/alpha/rewrite/git-renewed/rukie/docs/development.md:1-28,72-85` | Existing development documentation describes the old frontend build and Swagger workflow. It must be replaced rather than presented as the new workflow. |
| `/home/alpha/rewrite/git-renewed/rukie/LICENSE:1-21` | The checkout carries MIT permission terms and Gitea/Gogs copyright notices. |
| Assignment’s settled decisions | Ruk-only storage, stock Git access, change-and-stack review, branch grouping, claimable tasks, built-in CI attestations, signed webhooks, push mirrors, authentication, no email, no JavaScript, and operation-derived interfaces. |

The existing Ruk README contains method counts and performance results. Neither is repeated here: the method count is unnecessary to explain rukie, and Ruk’s measured CLI performance does not establish rukie’s service performance.

---

## Full proposed `README.md`

The following block is the complete replacement README. Placeholder values are deliberately unresolved. The measurement commands are defined in the implementation contract after the draft.

````markdown
# rukie

## What is this

rukie hosts repositories, reviews changes, tracks tasks, and runs CI for developers and coding agents. It uses Ruk as its only repository backend, so agents can work with changes, stacks, task leases, and recorded CI results through typed operations. Compared with a Gitea, Forgejo, or GitHub workflow, rukie puts Ruk changes and stacks at the center of review, gives agents and people access to the same operations, and serves a browser interface that works without JavaScript.

- **Browser cost:** {{browser_javascript_bytes}} JavaScript bytes transferred across the documented browser workflows. Measure with `./bin/rukie-doccheck measure browser --scenario docs/measurements/browser.toml --field javascript_bytes`.
- **Page latency:** {{p95_page_ms}} ms at the percentile, offered load, and host specification recorded in the [measurement report](docs/measurements/README.md). Measure with `./bin/rukie-doccheck measure pages --scenario docs/measurements/pages.toml --field page_latency_p95_ms`.
- **Idle memory:** {{idle_rss_mib}} MiB for rukie and its Ruk daemon together, under the conditions in the measurement report. Measure with `./bin/rukie-doccheck measure idle --scenario docs/measurements/idle.toml --field combined_rss_mib`.
- **First push:** {{quickstart_seconds}} seconds from a ready installation to a repository pushed with stock Git. Measure with `./bin/rukie-doccheck measure quickstart --scenario docs/measurements/quickstart.toml --field elapsed_seconds`.

An agent can find a task, claim it, and inspect the branch’s changes:

```bash
rukie task.list --repo ada/hello --state open --json
# {"tasks":[{"id":"task_demo","title":"Add a greeting","state":"open"}]}
rukie task.claim --repo ada/hello --task task_demo --json
# {"task_id":"task_demo","lease_id":"lease_demo","state":"claimed"}
rukie change.list --repo ada/hello --branch main --json
# {"changes":[]}
```

The example uses a prepared demo task. Your identifiers and repository contents will differ. See [Work on a task as an agent](docs/tutorials/agent-task.md) for token setup and the complete workflow.

[Install](#install) · [Quick start](#quick-start) · [Features](#features) · [Usage](#usage) · [Documentation](#documentation) · [Development](#development) · [License](#license)

---

## Install

### Build from source

Install Git, Go, and Rust with Cargo. Use the Go toolchain declared by rukie and the Rust toolchain declared by Ruk. See [Installation](docs/how-to/install.md) for tested operating systems and toolchain setup.

From an empty working directory:

```bash
git clone https://github.com/gosuda/ruk.git
git clone https://github.com/gosuda/rukie.git
mkdir -p bin
cargo build --manifest-path ruk/Cargo.toml --release --bin ruk
go -C rukie build -o ../bin/rukie ./cmd/rukie
export PATH="$PWD/bin:$PWD/ruk/target/release:$PATH"
```

Keep this terminal open for the quick start.

Ruk stores repository data. rukie manages accounts, access, reviews, tasks, and service operations. You need both binaries.

## Quick start

Create a local account, host a repository, and push a commit using stock Git.

**This example is for a local development instance.** Do not expose it to a network. Follow [Deploy rukie](docs/how-to/deploy.md) before hosting other users.

### Start the service

The checked-in local configuration uses paths inside the checkout. Its environment file sets the matching client URL.

```bash
cd rukie
. ./examples/local.env
rukie bootstrap --config examples/local.toml --username ada
rukie serve --config examples/local.toml
```

`bootstrap` asks for a password and creates the first administrator. It refuses to bootstrap an instance that already has an administrator.

`serve` starts and supervises the Ruk daemon configured by `examples/local.toml`. Leave it running.

### Create a repository

Open another terminal in the same `rukie` directory. Add the binaries to its path, then sign in:

```bash
export PATH="$PWD/../bin:$PWD/../ruk/target/release:$PATH"
. ./examples/local.env
rukie auth.login --username ada
rukie repo.create --owner ada --name hello --visibility public --json
rukie auth.token.create --name local-git --repo ada/hello --scope repo:write --json
```

Sign-in asks for your password. The token command displays a token for this repository; keep it private. Use that token as the password when Git prompts. Do not put it in a remote URL or commit it to a file.

### Push with stock Git

```bash
git init --initial-branch=main hello
printf 'Hello from rukie\n' > hello/README.md
git -C hello add README.md
git -C hello -c user.name=Ada -c user.email=ada@example.invalid commit -m "Add greeting"
git -C hello remote add origin "$RUKIE_URL/ada/hello.git"
git -C hello push --set-upstream origin main
```

When Git asks for credentials, enter `ada` and the repository token.

Open `$RUKIE_URL/ada/hello` in your browser. You should see the README and the commit you just pushed. No JavaScript is needed.

The email-shaped value in the commit is Git author metadata. rukie does not send email.

Next, [submit a change for review](docs/tutorials/first-review.md) or [give an agent a task](docs/tutorials/agent-task.md).

---

## Features

### Host and browse code

Create repositories, browse files and history, compare changes, and search code on the server. Repository reads and writes go through Ruk, including Smart HTTP and SSH access from stock Git clients.

Use organizations and teams to manage access. Explore repositories, star projects, watch activity, and fork repositories for your work.

### Review changes and stacks

A change identifies a piece of work across revisions. A stack groups related changes for review and landing.

Review an individual change or a stack. Use the branch view to see the changes associated with a branch. From stock Git, push to `refs/for/main` to submit work for review instead of updating `main` directly.

### Claim tasks

Tasks replace issues. Claim a task through a Ruk lease so another worker can see that it is taken. Lease expiry lets work become available again when a worker stops.

### Run CI and record the result

Run CI inside rukie and record results as Ruk attestations: records that associate a result with the code it checked.

See [CI execution and recovery](docs/explanation/ci-execution.md) for job state, worker failures, cancellation, and recovery behavior.

### Publish and integrate

Publish releases and receive signed webhook deliveries using Standard Webhooks.

Push mirrors to GitHub, GitLab, or Forgejo. Choose which refs to mirror and whether each mirror follows exact mode, which can force updates and prune refs, or fast-forward-only mode. Ref updates trigger delivery; periodic reconciliation catches missed work.

### Keep identity accountable

Sign in with a password and TOTP, or use OIDC. Agents use scoped tokens attached to human accounts. Read notifications in the inbox or receive them through webhooks; rukie does not send email.

## Usage

### Submit a change for review

From a Git checkout with an `origin` remote pointing at rukie:

```bash
git push origin HEAD:refs/for/main
```

Read [Review from stock Git](docs/how-to/review-with-git.md) for change IDs, revisions, and stack submission.

### Use the generated CLI

Run operations by name and request JSON output for scripts:

```bash
rukie repo.get --owner ada --name hello --json
rukie change.list --repo ada/hello --branch main --json
```

Use `rukie --help` to discover operations and `rukie task.claim --help` to inspect an operation’s input.

The CLI, HTTP API, MCP tools, and browser forms derive from the same operation table. They use the same permissions and operation behavior.

### Choose a representation

Resource URLs negotiate HTML, JSON, or Markdown through the `Accept` header. For the public repository created above:

```bash
curl --fail --header 'Accept: application/json' "$RUKIE_URL/ada/hello"
curl --fail --header 'Accept: text/markdown' "$RUKIE_URL/ada/hello"
```

Use [the generated operation reference](docs/reference/operations/README.md) for input fields, outputs, permissions, and errors. See [Connect an MCP client](docs/how-to/connect-mcp.md) for agent setup.

## Documentation

Start with the [documentation index](docs/README.md).

- **Tutorials:** [Host a repository](docs/tutorials/quickstart.md), [review a change](docs/tutorials/first-review.md), and [complete an agent task](docs/tutorials/agent-task.md).
- **How-to guides:** [Deploy](docs/how-to/deploy.md), [back up and restore](docs/how-to/backup-restore.md), [configure CI](docs/how-to/configure-ci.md), and [set up mirrors](docs/how-to/push-mirrors.md).
- **Reference:** [Operations](docs/reference/operations/README.md), [CLI](docs/reference/cli.md), [configuration](docs/reference/configuration.md), and [errors](docs/reference/errors.md).
- **Explanation:** [Ruk and rukie](docs/explanation/ruk-and-rukie.md), [changes and stacks](docs/explanation/changes-stacks-branches.md), and [security boundaries](docs/explanation/security-boundaries.md).

rukie is a rewrite built on Gitea history, not a drop-in Gitea replacement. It keeps the `/{owner}/{repo}.git` clone URL shape. Do not assume that Gitea APIs, configuration, databases, extensions, or CI workflows work unchanged. Read [Moving from another forge](docs/how-to/move-from-another-forge.md) before moving a service.

## Development

Read [Development](docs/development.md) for the repository layout and local workflow, and [Contributing](CONTRIBUTING.md) for submitting changes.

```bash
go test ./...
go vet ./...
```

Run these commands from the `rukie` checkout. They check the Go packages; they do not replace the integration checks for Ruk, Git transport, CI workers, or the browser interface. Those checks are listed in [Testing](docs/testing.md).

The Go module is `github.com/gosuda/rukie`. Add or change operations in the operation table, then regenerate the HTTP, CLI, MCP, and reference outputs together. See [Adding an operation](docs/how-to/add-operation.md).

Keep changes small enough to review. Open a task describing a bug or proposed behavior, submit the change for review, and include the checks you ran.

## License

The repository’s current license is MIT; see [LICENSE](LICENSE). Retain the notices required by that license.

Ruk is separately licensed under the Apache License; see the [Ruk license](https://github.com/gosuda/ruk/blob/main/LICENSE).
````

---

## Required command and example contracts

These contracts make the future-facing README specific without pretending that the new interface already exists. **The main plan should include them as documentation acceptance criteria.** Operation spelling may change during implementation, but every README example, generated reference, tutorial, and fixture must change together.

### Installation and local service

| Proposed path or command | Required behavior |
|---|---|
| `cmd/rukie` | Build the new server and client binary using the documented `go build` command. |
| `examples/local.toml` | Complete local-only configuration; no manual field replacement, external identity provider, or production secret required. Resolve runtime paths consistently from a documented base. |
| `examples/local.env` | Plain shell assignments exporting `RUKIE_URL` matching the local configuration. No secrets or executable setup logic. |
| `rukie bootstrap --config … --username ada` | Local administrative bootstrap, not an unauthenticated remote user-creation endpoint. Prompt without echo, create the initial administrator, and refuse a second bootstrap once an administrator exists. |
| `rukie serve --config …` | Start the forge and its configured Ruk daemon, report readiness and startup failures, and shut down supervised processes cleanly. If the architecture uses an externally managed daemon instead, replace this section with the complete daemon startup sequence; do not leave that dependency implicit. |
| `rukie auth.login --username ada` | Authenticate interactively against `RUKIE_URL`, handle an enrolled TOTP challenge, and store the resulting client credential without exposing it in shell history or output. |
| `rukie repo.create … --json` | Create the repository in Ruk and the forge’s metadata store through the operation’s normal authorization path. |
| `rukie auth.token.create … --json` | Create a human-account token restricted to the selected repository. The published `repo:write` scope must actually authorize this Git push path; change the example if the final scope model differs. |
| Stock Git HTTPS/HTTP credentials | Accept the documented username/token combination. Local HTTP is for the loopback example only; production documentation must require the chosen secure transport. |

The source-build commands use existing Cargo workspace structure and a **proposed** new Go entry-point path. They are not verified against the existing Gitea checkout.

The quick start deliberately chooses the password-based path. It must not make OIDC setup, email verification, or a CI worker prerequisites for the first push. Explain TOTP enrollment and recovery in the authentication guide.

### Agent example

The six-line agent example requires a deterministic tutorial fixture:

- Repository: `ada/hello`.
- Open task: `task_demo`, titled “Add a greeting”.
- No existing task lease.
- No changes associated with the example branch.
- An authenticated agent token restricted to that repository.

The operation table must define the actual response fields before these examples ship. The displayed JSON is **proposed fixture output**, not captured output.

The full agent tutorial must show token creation, least-privilege scopes, claiming, lease renewal or release as applicable, code submission, and the response to a lost lease. Do not imply that a task claim is permanent ownership.

### Discoverability and representation

- `rukie --help` lists generated operations.
- `rukie <operation> --help` describes the operation’s real inputs.
- `--json` returns machine-readable operation output.
- The resource URL used by the example supports all documented representations.
- The same authorization decision applies regardless of representation or interface.
- Content negotiation documentation must describe unsupported representations and caching behavior.
- `refs/for/<branch>` handling must document how a stock Git commit receives or supplies a change ID. Do not prescribe a commit hook until the implementation requires one.

---

## Measurement placeholders and exact commands

### Measurement harness status

`rukie-doccheck` and the scenario files below are **proposed development tooling**. They do not exist in the inspected checkout. Their names provide an exact, reviewable measurement interface for the plan.

Implement this as documentation validation tooling rather than expanding the production CLI with benchmark-only operations.

Proposed build command, from the rewritten `rukie` checkout:

```bash
go build -o bin/rukie-doccheck ./cmd/rukie-doccheck
```

Every `measure` command must:

- Validate its scenario and prerequisites.
- Fail if the intended workflow does not complete.
- Print the requested scalar only on success.
- Produce an accompanying report containing source revisions, configuration, workload, host details, toolchain, timing boundaries, and raw results.
- Distinguish measured data from targets.
- Avoid using production accounts, repositories, tokens, or mirror destinations.

### Placeholder ledger

| README placeholder | Exact proposed command | Meaning and publication condition |
|---|---|---|
| `{{browser_javascript_bytes}}` | `./bin/rukie-doccheck measure browser --scenario docs/measurements/browser.toml --field javascript_bytes` | JavaScript bytes transferred across the declared browser workflows. Also fail the browser acceptance check on script elements, inline event handlers, executable JavaScript URLs, or JavaScript-dependent behavior; transfer size alone is not a proof of a script-free UI. |
| `{{p95_page_ms}}` | `./bin/rukie-doccheck measure pages --scenario docs/measurements/pages.toml --field page_latency_p95_ms` | Successful page-response latency at the specified percentile under a fixed offered-load scenario. The report must expose errors, timeouts, rejected requests, and slow responses rather than dropping them from the claim. |
| `{{idle_rss_mib}}` | `./bin/rukie-doccheck measure idle --scenario docs/measurements/idle.toml --field combined_rss_mib` | Resident memory for the complete rukie/Ruk process set after the specified warm-up and idle interval. State whether ancillary services and the metadata database process are included. |
| `{{quickstart_seconds}}` | `./bin/rukie-doccheck measure quickstart --scenario docs/measurements/quickstart.toml --field elapsed_seconds` | Elapsed time from ready binaries and declared prerequisites through account creation, server readiness, repository creation, token creation, stock Git push, and successful browser retrieval of the pushed README. Excludes source builds and downloads; say so in the report. |

No quantitative result is supplied in the README. Each result is a named placeholder tied to an exact command. Numbers embedded in future protocol names, toolchain declarations, generated schemas, or network configuration are identifiers or configuration values, not performance evidence; they belong in the relevant reference rather than in the facts list.

### Quick-start timing boundary

The under-five-minute requirement is a **release acceptance target**, not a measured result.

Maintain separate timings:

- **Install:** obtain dependencies and build the binaries.
- **Quick start:** ready installation to visible pushed code.

Do not claim that compiling Rust and Go from a cold machine fits the quick-start target. If the requirement is interpreted as including installation, the release must provide a verified installation path that meets it; the source-build path alone cannot establish that claim.

The automated quick-start harness proves the sequence is complete. A timed human walkthrough is also required before publishing a human completion-time claim because automated password entry and credential prompts do not measure reading or interaction time.

---

## Documentation information architecture

### Navigation

Use the documentation index as the common entry point:

1. **Start here:** installation and the first successful push.
2. **Tutorials:** complete learning paths with prepared inputs and visible outcomes.
3. **How-to:** focused procedures for users, operators, and contributors.
4. **Reference:** generated interface contracts and maintained operational reference.
5. **Explanation:** architecture, concepts, and trade-offs.

These are navigation groups, not a requirement to add a JavaScript documentation framework. Publish Markdown in the repository and, if served by rukie, render it as server-generated HTML.

### Proposed tree

Every file below is a proposed post-rewrite deliverable. Existing Gitea documentation should be replaced or removed as its subject disappears; do not retain it as an alternate installation or API reference.

```text
docs/
├── README.md
│   └── Route readers to installation, tutorials, task guides, reference, and concepts.
├── development.md
│   └── Explain the new repository layout, build workflow, operation generation, and local services.
├── testing.md
│   └── List real checks, prerequisites, integration boundaries, and documentation acceptance gates.
├── tutorials/
│   ├── quickstart.md
│   │   └── Take a ready installation through account creation, stock Git push, and browser confirmation.
│   ├── first-review.md
│   │   └── Submit a change with stock Git, revise it, review it, and land it.
│   ├── agent-task.md
│   │   └── Create a scoped agent token, claim a prepared task, submit work, and release the lease.
│   └── first-ci-run.md
│       └── Configure a small CI job, inspect its result, and find the associated Ruk attestation.
├── how-to/
│   ├── install.md
│   │   └── Install verified builds or build Ruk and rukie from source on tested platforms.
│   ├── deploy.md
│   │   └── Configure production networking, TLS, SSH, storage, service identity, and process supervision.
│   ├── authenticate.md
│   │   └── Set up passwords, TOTP, recovery, OIDC, and scoped human-account tokens.
│   ├── connect-mcp.md
│   │   └── Connect supported MCP clients using the implemented transport and credential model.
│   ├── manage-organizations.md
│   │   └── Create organizations and teams and grant repository access.
│   ├── browse-and-search.md
│   │   └── Browse history, compare code, and use server-side search with documented limits.
│   ├── review-with-git.md
│   │   └── Use refs/for/<branch>, change IDs, revisions, and stacks from stock Git.
│   ├── manage-tasks.md
│   │   └── Create tasks and handle claiming, renewal, release, expiry, and reassignment.
│   ├── configure-ci.md
│   │   └── Configure jobs, worker access, secrets, cancellation, and result inspection.
│   ├── recover-ci.md
│   │   └── Diagnose interrupted jobs and recover using the implemented execution-state rules.
│   ├── configure-webhooks.md
│   │   └── Configure delivery, verify Standard Webhooks signatures, and handle retries.
│   ├── publish-release.md
│   │   └── Create a release and manage the release assets supported by the implementation.
│   ├── push-mirrors.md
│   │   └── Configure target credentials, ref selection, mirror modes, and reconciliation.
│   ├── discover-and-follow.md
│   │   └── Explore, star, watch, and fork repositories and read inbox notifications.
│   ├── backup-restore.md
│   │   └── Back up and restore consistent forge metadata and Ruk repository state.
│   ├── upgrade.md
│   │   └── Upgrade the forge and Ruk together using the supported migration procedure.
│   ├── move-from-another-forge.md
│   │   └── Explain supported repository import and explicitly identify unsupported metadata migration.
│   └── add-operation.md
│       └── Add an operation and derive its HTTP, CLI, MCP, form, and reference surfaces.
├── reference/
│   ├── README.md
│   │   └── Index generated contracts and maintained operational reference.
│   ├── operations/
│   │   ├── README.md
│   │   │   └── Generated index of registered operations, permissions, effects, and interface availability.
│   │   └── <operation-id>.md
│   │       └── Generated operation inputs, outputs, errors, examples, and interface mappings.
│   ├── openapi.json
│   │   └── Generated OpenAPI contract emitted from the operation table through Huma.
│   ├── cli.md
│   │   └── Generated operation commands, flags, input handling, output modes, and documented exit behavior.
│   ├── mcp.md
│   │   └── Generated tool names and schemas, plus supported transport and authentication details.
│   ├── errors.md
│   │   └── Generated problem types and operation error mappings, linked to recovery guidance.
│   ├── permissions.md
│   │   └── Generated scope and permission catalogue with maintained authorization explanations.
│   ├── webhooks.md
│   │   └── Generated event catalogue and schemas with maintained signature-verification instructions.
│   ├── configuration.md
│   │   └── Document actual configuration keys, defaults, validation, path resolution, and secret handling.
│   ├── git-transport.md
│   │   └── Define clone URLs, authentication, supported wire behavior, and review-ref handling.
│   ├── representations.md
│   │   └── Define HTML, JSON, and Markdown negotiation, caching, and unsupported representation behavior.
│   ├── ci.md
│   │   └── Define the implemented job format, execution states, worker contract, and attestation linkage.
│   └── compatibility.md
│       └── State tested clients and platforms and distinguish repository interoperability from forge compatibility.
├── explanation/
│   ├── ruk-and-rukie.md
│   │   └── Explain the repository/backend boundary and which component owns each class of state.
│   ├── changes-stacks-branches.md
│   │   └── Explain change identity, revisions, stack relationships, and branch grouping.
│   ├── task-leases.md
│   │   └── Explain why claims expire and how workers should reason about lost ownership.
│   ├── operation-table.md
│   │   └── Explain how the shared operation model keeps interfaces and permissions aligned.
│   ├── security-boundaries.md
│   │   └── Explain human accountability, tokens, daemon access, CI isolation, and mirror credentials.
│   ├── ci-execution.md
│   │   └── Explain durable execution, failure recovery, cancellation, and attestation semantics.
│   ├── mirror-consistency.md
│   │   └── Explain exact and fast-forward-only modes, reconciliation, divergence, and deletion behavior.
│   ├── storage-and-recovery.md
│   │   └── Explain metadata ownership, repository durability, and consistent recovery boundaries.
│   └── no-javascript-ui.md
│       └── Explain form submissions, validation, navigation, and explicit refresh without browser scripts.
└── measurements/
    ├── README.md
    │   └── Define measurement methodology and link revision-specific reports and raw evidence.
    ├── browser.toml
    │   └── Declare browser workflows and the resources checked for JavaScript use.
    ├── pages.toml
    │   └── Declare the page workload, repository fixture, load model, and latency reporting conditions.
    ├── idle.toml
    │   └── Declare the process set, warm-up, idle interval, and memory accounting method.
    └── quickstart.toml
        └── Declare the isolated first-push workflow, prerequisites, timing boundaries, and success checks.
```

Root files that support this tree:

| File | Purpose |
|---|---|
| `README.md` | Short evaluation path and complete local first-push instructions. |
| `CONTRIBUTING.md` | Contribution and review workflow, required checks, and conduct expectations without inherited Gitea-only instructions. |
| `SECURITY.md` | Actual private vulnerability-reporting route and response expectations; do not retain the old Gitea email address or invent a replacement. |
| `CHANGELOG.md` | User-visible changes, breaking changes, and upgrade implications. |
| `LICENSE` | License terms and required notices, reviewed against the final rewrite contents. |
| `examples/local.toml` | Runnable local service configuration used by the README and tutorial. |
| `examples/local.env` | Matching non-secret client URL configuration. |

### Page conventions

Every instructional page should contain:

- A sentence naming the result.
- Prerequisites before the first command.
- Commands using the same operation names as generated reference.
- Expected output or a visible success condition.
- Relevant failure cases and recovery steps.
- A next step and links to the relevant reference and explanation.

Tutorials provide a complete path. How-to guides assume the reader already understands the concepts. Reference pages define the contract. Explanation pages describe why the system behaves that way.

Avoid copying reference tables into how-to guides. Link to generated fields and scopes instead.

### Reference generation rules

The operation table is the authority for operation-derived reference. Generate:

- Operation IDs, summaries, input and output schemas.
- HTTP methods, resource paths, and supported representations.
- Required authentication, scopes, and permission guards.
- CLI commands and flags.
- MCP tool names and schemas.
- Declared error types and effects.
- Webhook event contracts where registered.
- Examples that execute against deterministic fixtures.

Generate the operation pages, CLI reference, schemas, and indexes together. Maintained prose should live in stable surrounding pages or explicit non-generated sections, not in files that generation overwrites.

Do not infer configuration keys, process lifecycle semantics, CI durability, or backup consistency from an operation schema. Their references need evidence from their owning implementations. The metadata-store explanation must remain neutral until the store decision is settled.

Do not publish inherited Gitea Swagger, frontend-build, Actions, or configuration documentation alongside the replacement as though it remains supported.

---

## Prepared GitHub metadata

**Prepared text only.** Applying the description or topics is an outward write and requires user confirmation. No repository settings were changed.

### Description

> Agent-first Go forge with Ruk-only repository storage, change and stack review, built-in CI, and a JavaScript-free web UI.

### Topics

```text
rukie
ruk
forge
self-hosted
code-hosting
code-review
stacked-changes
git-compatible

[168 more lines in resource. Use :604 to continue]
git-compatible
golang
rust
coding-agents
agent-tools
mcp
command-line
server-rendered
no-javascript
continuous-integration
webhooks
repository-mirroring
developer-tools
```

These topics describe the target product. Apply them when those capabilities exist; do not use the prepared metadata as evidence that the rewrite has shipped.

---

## Claims requiring evidence before the README ships

### Quantitative claims

| Claim | Required evidence |
|---|---|
| Browser JavaScript transfer size | Placeholder measurement plus a complete script-free workflow check. Include login, validation errors, repository browsing, review, tasks, and CI inspection. |
| Page latency | Reproducible offered-load report, host and repository fixture, latency distribution, and error accounting. |
| Combined idle memory | Measured complete process set and explicit treatment of any separately running database or service. |
| Quick-start completion time | Automated end-to-end execution and timed human walkthrough. State the exclusion of source builds. |

Do **not** publish “faster,” “lighter,” “more resilient,” or “more efficient than GitHub/GitLab” without a defined comparison and evidence. The settled engineering goal for CI is stronger than the README’s claim. The draft intentionally describes the user-facing CI behavior without asserting comparative superiority.

### Behavioral claims

| README claim | Required proof |
|---|---|
| Ruk is the only repository backend | Trace repository creation, reads, writes, Git transport, search, forks, releases, and mirrors through Ruk; ensure no inherited Git subprocess or alternate repository backend remains. |
| Stock Git works | Complete the documented push and clone flows over supported HTTP and SSH paths using stock clients. |
| Changes, stacks, and branch grouping work together | Exercise review submission, revision updates, stack relationships, and branch membership without losing change identity. |
| Task claiming uses Ruk leases | Demonstrate contention, expiry, release, and the worker-visible lost-lease behavior. |
| CI results are Ruk attestations | Trace a real completed job to the code identity and persisted attestation, including restart and interrupted-worker cases. |
| Interfaces share operation behavior | Exercise representative reads and mutations through CLI, HTTP, MCP, and forms, including denied permissions and validation failures. |
| Resource URLs negotiate representations | Check HTML, JSON, and Markdown on the documented URL with consistent authorization and correct caching headers. |
| The UI needs no JavaScript | Complete every advertised browser task with script execution disabled, including error recovery and navigation. |
| Signed webhooks use Standard Webhooks | Verify actual deliveries against the signature contract, including modified payload rejection and secret-rotation behavior. |
| Mirror modes behave as documented | Demonstrate exact force-and-prune behavior, fast-forward refusal, each ref-selection mode, update triggers, and periodic reconciliation. Use isolated test targets. |
| Authentication matches the README | Complete password/TOTP and OIDC flows; exercise token scoping and revocation on human accounts. |
| No email is required or sent | Check account creation, recovery, notifications, invitations, and administration for inherited email dependencies. |
| Local startup is complete | Run from the documented source layout without an undocumented daemon, database, secret, or shell variable. |
| Compatibility wording is accurate | Keep tested-client claims separate from migration claims and from promises of Gitea/Forgejo/GitHub API compatibility. |
| License wording is correct | Review the final root license and notices. The current MIT terms are evidence for the checkout, not authorization to discard notices or silently select a different license. |

### Pre-publication gates

The main agent should run these after implementation, not during this read-only drafting assignment:

- Execute every installation and quick-start command in an isolated environment.
- Run all examples against the final operation schemas and capture their real output.
- Resolve every README link; the new documentation paths are currently proposed.
- Compare generated reference with the operation table and fail on drift.
- Ensure no numeric placeholder remains before publishing a measurement as a result.
- Confirm the fixed README section order.
- Confirm badges remain absent unless their claims and endpoints are verified.
- Ensure the description and topics remain prepared text until outward-write approval is given.

**Current status:** the planning artifact is complete. Publication gates are not satisfied by this draft: target functionality, proposed commands, generated documentation, measurements, and clean-install execution remain unverified.

---

## Changes to incorporate into the main implementation plan

- Treat the README examples as an end-to-end product contract, including bootstrap, local daemon lifecycle, human sign-in, repository-scoped token creation, and stock Git push.
- Add the runnable local configuration and matching environment file with the service implementation, not as documentation-only samples.
- Add reference generation to the operation-table work so HTTP, CLI, MCP, forms, and documentation cannot adopt independent contracts.
- Replace inherited Gitea documentation during the cutover; do not preserve it as a compatibility path.
- Include script-free browser acceptance and first-push measurement tooling in the documentation release gates.
- Keep the metadata-store choice open in public prose until implementation settles it.
- Keep GitHub metadata changes outside local implementation work until the user approves the outward write.

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/rukie/README.md` — Replace the inherited Gitea introduction with the fixed-order README and verified examples.
- `/home/alpha/rewrite/git-renewed/rukie/docs/README.md` — Proposed documentation entry point organizing tutorials, how-to guides, reference, and explanation.
- `/home/alpha/rewrite/git-renewed/rukie/cmd/rukie/main.go` — Proposed entry point for the documented build, bootstrap, service, and generated client commands.
- `/home/alpha/rewrite/git-renewed/rukie/examples/local.toml` — Proposed runnable configuration anchoring the first-push workflow and daemon startup contract.
- `/home/alpha/rewrite/git-renewed/rukie/docs/reference/operations/README.md` — Proposed generated operation index connecting the shared table to the CLI, HTTP API, MCP tools, and forms.

[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…

---
## Later corrections from the same agent (apply over the body above)

## Review reconciliation for `docs-readme-draft.md`

The review does not require changes to the draft’s command examples or source grounding:

- **Current versus target commands:** already distinguished. The README uses the requested `rukie serve --config`; the surrounding artifact explicitly labels it and the other new commands as post-rewrite implementation contracts. The current `ruk forgejo serve` command appears only in source-grounding notes.
- **Generated operation names:** explicitly proposed, not attributed to an existing implementation. The draft requires the final registry, generated reference, examples, and fixtures to agree before publication.
- **License:** the two repositories having different licenses is not a conflict. The inspected rukie license contains MIT terms; Ruk’s manifest declares Apache licensing. The draft distinguishes them and requires final notice review. It does not select a new license for the rewrite.
- **Source coverage:** Ruk’s setup and command sections were read, including its Cargo build instructions. No existing source was claimed to prove the proposed Go entry point.
- **Measurements:** existing Ruk capacity and parity commands cannot establish rukie page latency, combined service memory, browser JavaScript usage, or first-push completion time. Rebinding those placeholders to Ruk’s existing harnesses would produce misleading evidence.

### Clarification to preserve when saving the artifact

Keep the measurement-harness status immediately adjacent to the placeholder ledger:

> **The measurement commands are proposed implementation contracts.** `rukie-doccheck` and its scenario files must be implemented before these placeholders can be resolved. Existing Ruk benchmarks do not measure the new forge. Do not publish the README facts list as measured evidence until the documented commands execute successfully and their reports are available.

The README draft and documentation architecture remain complete as a planning deliverable, not as publication-ready documentation for the current checkout. No execution or publication verification was performed.

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/rukie/README.md` — Target README; preserve the fixed order and replace placeholders only with applicable evidence.
- `/home/alpha/rewrite/git-renewed/rukie/docs/README.md` — Proposed documentation index for tutorials, how-to guides, reference, and explanation.
- `/home/alpha/rewrite/git-renewed/rukie/cmd/rukie/main.go` — Proposed entry point supporting the documented build and service commands.
- `/home/alpha/rewrite/git-renewed/rukie/cmd/rukie-doccheck/main.go` — Proposed documentation measurement tool; required before the quantitative facts can ship.
- `/home/alpha/rewrite/git-renewed/rukie/LICENSE` — Current MIT terms and notices; review against the final rewrite without conflating them with Ruk’s separate license.

[advisor] <advisory advisor="minimax" severity="blocker" guidance="weigh, don't blindly obey"> Write the draft to `docs-readme-dr…

[advisor] <advisory advisor="qwen" severity="blocker" guidance="weigh, don't blindly obey"> Write the draft to `docs-readme-draft…

[advisor] <advisory advisor="glm-flash" severity="blocker" guidance="weigh, don't blindly obey"> Write the draft to `docs-readme-…

[advisor] <advisory advisor="default" severity="blocker" guidance="weigh, don't blindly obey"> Write the draft to `docs-readme-dr…

---
The file-write advisories conflict with this assignment’s explicit delivery contract:

> “PLAN MODE: READ-ONLY. Never edit/create/delete files … Return your COMPLETE artifact (markdown) as your final message … the orchestrator saves it under the file name in your task.”

The complete `docs-readme-draft.md` artifact was supplied in the original final response, beginning with `# docs-readme-draft.md`. **The orchestrator must save that full artifact**, not the subsequent review-reconciliation note. Creating a workspace file here would violate the read-only restriction; no write is authorized or available.

### Critical Files for Implementation

- `docs-readme-draft.md` — Orchestrator-saved planning artifact containing the full README draft, documentation tree, prepared GitHub metadata, and measurement requirements.
- `/home/alpha/rewrite/git-renewed/rukie/README.md` — Future implementation destination for the reviewed and verified README.
- `/home/alpha/rewrite/git-renewed/rukie/docs/README.md` — Future documentation entry point for the proposed information architecture.

[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Add an explicit post-rewrite …

---
### Clarification for the quick-start command contract

`rukie bootstrap --config examples/local.toml --username ada` is the proposed **create-user step for the first local account**. It must create a human user named `ada`, configure that user’s password, and grant that user the initial administrator role. It is not merely a command that initializes storage or assigns a role to an existing account.

Replace the bootstrap explanation in the README draft with:

> `bootstrap` creates your first user account, `ada`, and makes it the instance administrator. It asks for a password without displaying it. This is a local setup command, not a public registration endpoint. It refuses to run once the instance has an administrator.

Add this acceptance criterion to the command-contract table:

> Starting with an empty local instance, execute the documented bootstrap command, authenticate as the newly created `ada` user through `rukie auth.login`, create `ada/hello`, and complete the documented stock Git push. Verify that bootstrap refuses a second invocation after the administrator exists.

This explicitly satisfies the requested user-creation step without adding a separate, ungrounded generated operation or expanding the quick start into multi-user administration. The command remains a post-rewrite contract, not a claim about the current checkout.

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/rukie/README.md` — Explain that bootstrap creates the first human user as well as granting administrator access.
- `/home/alpha/rewrite/git-renewed/rukie/cmd/rukie/main.go` — Proposed command entry point exposing the documented bootstrap invocation.
- `/home/alpha/rewrite/git-renewed/rukie/docs/tutorials/quickstart.md` — Proposed complete first-user, first-repository, and first-push walkthrough.
- `/home/alpha/rewrite/git-renewed/rukie/docs/testing.md` — Record the bootstrap-to-authentication acceptance check and repeat-bootstrap refusal requirement.

[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Remove or mark the unverified…