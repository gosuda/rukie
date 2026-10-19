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
