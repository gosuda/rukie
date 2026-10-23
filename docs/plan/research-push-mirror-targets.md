I could not write `local://research-push-mirror-targets.md`. My toolset here is `read`, `grep`, `web_search` and `ast_grep`, with no write or yield tool, and plan mode forbids writes anyway. The full artifact is below. Please persist it verbatim to `local://research-push-mirror-targets.md`.

Confidence labels: **[H]** read this session in a primary source, **[M]** inferred from primary sources, **[U]** not verified.

---

# Research: pushing a Git mirror over HTTPS to GitHub, GitLab and Forgejo/Codeberg

Researched 2026-10-20. Doc versions seen: GitLab docs 19.6, GitHub REST API version 2026-03-10, GHES 3.21/3.22, Forgejo docs v17 and the swagger on codeberg.org (Forgejo 16.0.0-dev).

## 0. Findings that change the design

1. **Do not run `git push --mirror` against these hosts.**
   - Git defines `--mirror` as pushing *all refs under `refs/`* and deleting remote refs that are gone locally [H] (<https://git-scm.com/docs/git-push>, the `--mirror` entry).
   - All three hosts reserve internal ref namespaces and reject writes to them. Each such ref is rejected individually, so the push exits non-zero even though branches and tags went through.
   - Push explicit refspecs instead: `+refs/heads/*:refs/heads/*` and `+refs/tags/*:refs/tags/*`. Compute deletions yourself from an `ls-remote` diff (§5.4).
2. **Gitea and Forgejo themselves run `git push --mirror` with force.**
   - Gitea/rukie: `PushToExternal(... Force: true, Mirror: true ...)` at `/home/alpha/rewrite/git-renewed/rukie/services/mirror/mirror_push.go:158-163`. It also sets `remote.<n>.push` to `+refs/heads/*:refs/heads/*` and `+refs/tags/*:refs/tags/*` (lines 34-39) [H].
   - Forgejo does the same: `/home/alpha/rewrite/git-renewed/ruk/forgejo/services/mirror/mirror_push.go:279-287`, and its docs say "This will force push to the remote repository" [H] (<https://forgejo.org/docs/latest/user/repo-mirror/>).
   - I did not find ref filtering in either push path. Whether this produces hidden-ref errors against GitHub is [U]; I found no Gitea issue confirming it.
3. **Codeberg's Terms of Use (§2(1)7) prohibit "projects that mostly consist of code written by 'generative AI'-tools (including … *Claude*, *OpenAI Codex*)"** [H]. This is a policy blocker for an agent-heavy forge pushing to Codeberg. See §6.
4. **GitHub needs the Workflows permission or `workflow` scope** for any push that adds or changes `.github/workflows/**` (§1.1).
5. **GitHub, GitLab and Codeberg all publish a recommended pushes-per-minute figure or an equivalent.** Coalesce pushes per target and debounce (§5.5).

---

## 1. GitHub (github.com and GHES)

### 1.1 HTTPS auth, token types, minimum permissions

| Item | Fact | Source | Conf. |
|---|---|---|---|
| Push over HTTPS | Remote is `https://github.com/OWNER/REPO.git`. Git prompts for username and password; the "password" is a PAT. Password auth is removed. | <https://docs.github.com/en/get-started/git-basics/about-remote-repositories> | H |
| GitHub App installation token | `git clone https://x-access-token:TOKEN@github.com/owner/repo.git`. The app needs the "Contents" repository permission. The token expires after 1 hour. | <https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation> | H |
| `x-access-token` is a convention only | A community report says any non-empty username works with a valid token. Not an official statement, and the same report claims it holds for PATs. | <https://github.com/orgs/community/discussions/173881> | M |
| Classic PAT scopes | `repo` covers public and private repos. `public_repo` covers public only. `workflow` is required to "add and update GitHub Actions workflow files". Workflow files can be committed without it only if the same path and contents already exist on another branch of the same repo. | <https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps> | H |
| Fine-grained PAT | Push needs `contents: write`. Changing workflow files also needs `workflows: write`. The permission list is `contents`, `workflows`, `administration`, `metadata`, and so on. | <https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens> | H |
| Fine-grained PAT gaps | No write to public repos the user is not a member of, and no write to repos where the user is only an outside or repository collaborator. A token covers one owner only. Max 50 fine-grained PATs per user. | same page | H |
| Fine-grained PAT, `workflows` permission | `PUT/DELETE contents`, `POST git/refs`, `PATCH git/refs/{ref}` and release endpoints list Workflows as an additional permission. | <https://docs.github.com/en/rest/authentication/permissions-required-for-fine-grained-personal-access-tokens> ("Workflows" section) | H |
| SSO | A classic PAT used on an SAML-SSO organisation must be authorised for it. | about-remote-repositories page above | H |
| Deploy keys (SSH alternative) | A per-repo SSH key, read-only by default and writable if "Allow write access" is set. A key cannot be reused across repos. It has no expiry and is not tied to a user. GitHub recommends a GitHub App instead. | <https://docs.github.com/en/authentication/connecting-to-github-with-ssh/managing-deploy-keys> | H |
| Machine accounts | The ToS permit machine accounts. | <https://docs.github.com/en/site-policy/github-terms/github-terms-of-service> | H |

Recommended credential for an automated mirror: a GitHub App installation token with Contents:write, Workflows:write (if needed) and Administration:write (only if the app creates repos). Rationale: 1-hour expiry, not tied to a person, higher rate limits (§1.3) [M]. Fallback: a fine-grained PAT scoped to the single repo.

### 1.2 Refs the host rejects or hides, protection

- **`refs/pull/*` is read-only.** Pushing gives `! [remote rejected] HEAD -> refs/pull/1/head (deny updating a hidden ref)` [H] (<https://docs.github.com/en/pull-requests/how-tos/review-pull-requests/checking-out-pull-requests-locally>). A forum report shows the same for `refs/pull/N/{head,merge}` during `--mirror` pushes: "The current action can only be performed by the system" [M] (<https://stackoverflow.com/questions/47776357/git-push-mirror-without-pull-refs>).
- **Branch protection**
  - Force pushes and deletions are blocked by default on any protected branch. Admins bypass unless "Do not allow bypassing" is on. Matching is by `fnmatch` pattern [H] (<https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches>).
  - A protected branch can still be updated by a force push if "Allow force pushes" is enabled for the pusher.
- **Rulesets** apply to branches and tags: "Restrict creations / updates / deletions" and "Block force pushes" (enabled by default). Only bypass actors, including GitHub Apps, can override [H] (<https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets>).
- **Tag protection** is therefore done through rulesets. I did not verify the status of the legacy tag-protection feature [U].
- **Push protection (secret scanning).** It blocks pushes containing detected secrets. It is enabled by default for public repos at the user level, and anyone with write access can bypass it with a reason [H] (<https://docs.github.com/en/code-security/concepts/secret-security/push-protection>). A mirror push can therefore fail with `GH013` on historical commits. [M]: surface this as a distinct error class.
- **"Block command line pushes that expose my email"** can fail mirror pushes. GitLab's mirror troubleshooting documents this as `RST_STREAM error code 2` [H] (<https://docs.gitlab.com/user/project/repository/mirror/troubleshooting/>).
- **Empty target repo.** With `auto_init` false (the default) the repo is empty and accepts the first push of any history [H] (create-repo API reference).

### 1.3 Limits

| Limit | Value | Source | Conf. |
|---|---|---|---|
| Push size | 2 GiB per push (`remote: fatal: pack exceeds maximum allowed size`). Workaround: push a series of commits, then a final `--mirror` or full refspec. | <https://docs.github.com/en/get-started/using-git/troubleshooting-the-2-gb-push-limit> | H |
| Single object | Recommended ≤1 MB, enforced at 100 MB. | <https://docs.github.com/en/repositories/creating-and-managing-repositories/repository-limits> | H |
| File warning | A warning at >50 MiB (push still succeeds). Blocked at >100 MiB. Larger files need Git LFS. | <https://docs.github.com/en/repositories/working-with-files/managing-large-files/about-large-files-on-github> | H |
| Repo size | Recommended <1 GB, strongly <5 GB. On-disk 10 GB. 5,000 branches. | repository-limits page | H |
| Git read ops | Recommended ≤15 per second per repo. | repository-limits page | H |
| Push rate | Recommended ≤6 pushes per minute per repo. | repository-limits page | H |
| GHES | The 100 MB limit is the default but "enterprise owners can change or remove" it (GHES 3.21). Git rate limiting is admin-configurable per repo network and off unless enabled. | <https://docs.github.com/en/enterprise-server@3.21/repositories/creating-and-managing-repositories/repository-limits>, <https://docs.github.com/en/enterprise-server@3.21/admin/configuring-settings/configuring-user-applications-for-your-enterprise/configuring-rate-limits> | H |
| LFS | LFS API requests have their own bucket: 3,000/min authenticated, 300/min unauthenticated. The mirroring recipe is `git lfs fetch --all`, `git push --mirror`, then `git lfs push --all <url>`. | <https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api>, <https://docs.github.com/en/repositories/creating-and-managing-repositories/duplicating-a-repository> | H |
| REST primary limit | 5,000/h per user. 15,000/h for Enterprise Cloud apps. App installations get 5,000/h, scaling up to 12,500/h (+50/h per repo above 20, +50/h per user above 20). | rate-limits page | H |
| REST secondary limit | ≤100 concurrent requests. ≤900 points/min per endpoint (a POST/PATCH/PUT/DELETE costs 5 points, a GET costs 1). ≤80 content-creating requests/min and ≤500/h. On 403/429 with `retry-after`, wait that long. Otherwise wait ≥1 minute, then back off exponentially. | rate-limits page | H |
| Git protocol rate limit | A separate git-protocol throttle exists but no numbers are published. | repository-limits page ("throttled performance") | M |

### 1.4 REST API: create, check, set mirror/archived metadata

Base URL: `https://api.github.com`. GHES: `http(s)://HOSTNAME/api/v3` [H] (<https://docs.github.com/en/enterprise-server@3.22/rest/quickstart>). Send `Accept: application/vnd.github+json` and `X-GitHub-Api-Version: 2026-03-10`.

| Op | Endpoint | Permission | Conf. |
|---|---|---|---|
| Check existence | `GET /repos/{owner}/{repo}`: 200, 301 (renamed or moved), 403, or 404. A private repo the token cannot see also returns 404. | fine-grained: Metadata:read; or any `repo`-scoped classic token | H |
| Create under a user | `POST /user/repos` with `name`, `private`, `description`, `homepage`, `has_issues`, `has_wiki`, `has_projects`, `auto_init` (default false). Returns 201 or 422 (validation, or "endpoint has been spammed"). | classic: `public_repo` or `repo` for public, `repo` for private. Fine-grained: Administration:write (marked "Additional permissions ✓" in the PAT table). | H |
| Create under an org | `POST /orgs/{org}/repos`, same body plus `team_id`. The caller must be an org member. | same scopes. Fine-grained: Administration:write. | H |
| Install-token availability | Both endpoints appear in the "endpoints available for GitHub App installation tokens" list. Whether a token can create a repo under a *user* account is [U]. | <https://docs.github.com/en/rest/authentication/endpoints-available-for-github-app-installation-access-tokens> | M |
| Update metadata, archive | `PATCH /repos/{owner}/{repo}` with `description`, `homepage`, `archived` (true archives, false unarchives), `has_issues`, `has_wiki`, `has_projects`, `has_pull_requests`, `pull_request_creation_policy` (`all` or `collaborators_only`), `private`/`visibility`. Fine-grained: Administration:write. | same page | H |

- The repo object has `mirror_url` (read-only) and `pushed_at`. GitHub has no native push-mirror API [H, by absence in the repo reference].
- Archiving makes the repo read-only, so **do not archive a repo you keep pushing to** [M]. A mirror-notice `description` is fine.
- **Suggested creation payload:** `{"name":..., "description":"Mirror of <forge URL> (read-only)", "private":..., "has_issues":false, "has_wiki":false, "has_projects":false, "auto_init":false}`. [M]: consider `pull_request_creation_policy:"collaborators_only"` or `has_pull_requests:false` on the mirror. The fields appear in the repo response schema and the PATCH parameters in the docs.

### 1.5 Terms of service

- Abuse or excessively frequent API requests "may result in the temporary or permanent suspension of your Account's access to the API"; GitHub decides what is excessive and "will make a reasonable attempt to warn you via email" [H] (ToS §H). "You may not share API tokens to exceed GitHub's rate limitations" [H].
- The AUP forbids "using our servers for any form of excessive automated bulk activity, to place undue burden on our servers through automated means" [H] (<https://docs.github.com/en/site-policy/acceptable-use-policies/github-acceptable-use-policies>).
- Accounts created by bots or automation are not permitted. One human-owned machine account for automated tasks is allowed [H] (ToS §B).
- No GitHub rule found against AI-generated content [M, by absence].

---

## 2. GitLab (gitlab.com and self-managed)

### 2.1 HTTPS auth

| Item | Fact | Source | Conf. |
|---|---|---|---|
| Username convention | Gitea/Forgejo docs: username `oauth2`, password = PAT with `write_repository`. GitLab's own doc for pushing to another GitLab with 2FA says the same. | <https://docs.gitlab.com/user/project/repository/mirror/push/>, <https://forgejo.org/docs/latest/user/repo-mirror/> | H |
| Project or group access token | "Use any non-blank value as a username and the project access token as the password." Bot username format is `project_{id}_bot_{random}`. | <https://docs.gitlab.com/user/project/settings/project_access_tokens/> | H |
| Project access token on GitLab.com | Requires Premium or Ultimate. Needs the Maintainer or Owner role to create. | same page | H |
| Classic PAT scopes | `write_repository`: pull and push over Git-over-HTTP, "Does not support API authentication". `api` also grants Git-over-HTTP for PATs but not for group or project tokens. `read_repository`: pull only. | <https://docs.gitlab.com/security/tokens/access_token_scopes/> | H |
| Fine-grained PAT | GA in GitLab 19.2. For Git over HTTPS: Code → Push (project access); LFS upload also needs Code → Push. | <https://docs.gitlab.com/auth/tokens/fine_grained_access_tokens_other/> | H |
| Deploy tokens | The scope table lists `read_repository` and registry scopes only. No `write_repository`, so deploy tokens cannot push. Not usable over SSH. | <https://docs.gitlab.com/user/project/deploy_tokens/> | H |
| Deploy keys (SSH) | Write-capable project deploy keys exist. They can be allowed to create protected tags. GitLab's own push mirrors support "SSH public key" auth with `ssh://` URLs (not SCP-style). | <https://docs.gitlab.com/user/project/protected_tags/>, <https://docs.gitlab.com/user/project/repository/mirror/> | H |
| Token lifecycle | GitLab ties a mirror to its creating user. Deleting that user, or revoking the bot token, disables the mirror. | <https://docs.gitlab.com/user/project/repository/mirror/troubleshooting/> | H |

Minimum to push is a PAT with `write_repository`, or a project/group access token with `write_repository` and the Developer role or higher. The default-branch constraint below makes Maintainer practical for pushing to protected default branches [M].

### 2.2 Refs hidden or rejected, protection

- **Internal namespaces:** `refs/merge-requests/<iid>/{head,merge,train}`, `refs/keep-around/<sha>`, `refs/pipelines/<iid>` and `refs/environments/<slug>` are GitLab-specific [H] (<https://docs.gitlab.com/development/gitaly/>, "GitLab-specific references"; keep-around refs: <https://docs.gitlab.com/development/merge_request_concepts/keep_around_refs/>).
- Gitaly "Reject pushes into internal ref namespaces" is a security fix [H] (MR !3426, title only). [M]: a `--mirror` push to GitLab would be rejected for each of those refs.
- **Default branch protection** is "Fully protected" by default: Developers cannot push, Maintainers can, **no one can force-push** [H] (<https://docs.gitlab.com/user/project/repository/branches/default/>).
  - Force push on a protected branch is enabled with the "Allowed to force push" toggle on a branch rule [H] (<https://docs.gitlab.com/user/project/repository/branches/protected/>).
  - A mirror that force-updates a protected default branch therefore needs that toggle on the target [M].
- **Protected tags:** "Allowed to create" gating; tags can only be deleted via UI or API, not via `git push` [H] (<https://docs.gitlab.com/user/project/protected_tags/>).
- **Push rules** (Premium/Ultimate) can reject mirror pushes. They include "Reject unsigned commits", "Check whether the commit author is a GitLab user", "Prohibited filenames", "Maximum file size" (LFS exempt) and "Prevent pushing secret files". "Reject unverified users" is skipped for pushes arriving via mirroring, and in 19.3 and later the committer check is skipped for GitLab-signed commits [H] (<https://docs.gitlab.com/user/project/repository/push_rules/>).
- Object-format mismatch (SHA-1 vs SHA-256) is unsupported for mirroring [H] (mirror page).

### 2.3 Limits

| Limit | Value | Source | Conf. |
|---|---|---|---|
| GitLab.com repo size (incl. LFS) | 10 GB | <https://docs.gitlab.com/user/gitlab_com/> | H |
| GitLab.com max push size | 5 GiB per request (Cloudflare) | same | H |
| Self-managed max push size | admin setting; LFS pushes are exempt | <https://docs.gitlab.com/administration/settings/account_and_limit_settings/> | H |
| File size cap | no GitLab.com per-file cap found. Only the optional push-rule "Maximum file size" and the 100 MB attachment limit (not repo content). | same | M |
| Concurrent git ops (GitLab.com) | 60 HTTP and 30 SSH fetches/clones per repo; load shed as `GitLab is currently unable to handle this request due to load` | gitlab_com page | H |
| Auth throttles (GitLab.com) | proposed plan limits: Free 5,000/h, Premium 15,000/h, Ultimate 25,000/h per user, applied to API, web and authenticated Git-over-HTTPS (each clone/fetch/push = 1 request) | <https://docs.gitlab.com/user/gitlab_com/rate_limits/> | H |
| Current limits (GitLab.com) | authenticated Git HTTPS 10,000/min per user; Git SSH 600 ops/min per user, project and command; project creation 200/day per user; single-project GET 400/min | rate_limits page | H |
| Auth-failure ban | 403 for 15 minutes after 300 failed Git or registry auth requests from one IP in one minute | rate_limits page | H |
| 429 behaviour | `Retry-After` and `RateLimit-ResetTime` headers. Back off exponentially. | rate_limits page | H |
| Push mirror limits | 10 enabled push mirrors per project | mirror/push page | H |
| LFS | Push mirroring of LFS objects over SSH is unsupported ("LFS objects are missing"). Use an HTTPS URL. | mirror troubleshooting page | H |
| Self-managed | limits are operator-defined | gitlab_com page | H |

### 2.4 REST API

Base URL: `https://HOST/api/v4` (confirmed by every curl example) [H].

| Op | Endpoint | Token | Conf. |
|---|---|---|---|
| Check existence | `GET /projects/:id`. `id` is the integer ID or the URL-encoded `namespace/path`. It returns `archived`, `empty_repo`, `http_url_to_repo`, `default_branch`, `visibility`, `repository_object_format`. A 404 means missing or invisible. | `read_api` or `api` | H |
| Create | `POST /projects` with `name`, `path`, `namespace_id` (group or subgroup, else the user's personal namespace), `visibility`, `description`, `initialize_with_readme`, `lfs_enabled`, `default_branch`. Create under a user with `POST /projects/user/:user_id`. | `api` (`write_repository` explicitly "does not support API authentication") | H |
| Archive | `POST /projects/:id/archive`, `POST /projects/:id/unarchive`. | `api`; Maintainer or Owner | H |
| Per-feature visibility | `issues_access_level`, `merge_requests_access_level`, `wiki_access_level`, `builds_access_level`, etc. via `PUT /projects/:id`. | `api`; Maintainer or Owner | H |
| Project creation rate | 200 per day per user on GitLab.com | rate_limits page | H |

Do not set `import_url` or `mirror=true`; those make a pull mirror, a Premium feature.

### 2.5 Terms of service

- The AUP bars using services such that "usage significantly exceeds that of other users of similar features", and anything that would "compromise, overburden, or otherwise impair our services"; GitLab reserves the right to act "against the spirit of this policy" [H] (<https://handbook.gitlab.com/handbook/legal/acceptable-use-policy/>).
- No GitLab rule found against AI-generated content in repositories. The AI clauses cover GitLab's own AI services [H, by reading].

---

## 3. Forgejo (self-hosted) and Codeberg

Codeberg runs Forgejo. Its swagger reports `This documentation describes the Forgejo API`, basePath `/api/v1` [H] (<https://codeberg.org/swagger.v1.json>).

### 3.1 HTTPS auth

| Item | Fact | Source | Conf. |
|---|---|---|---|
| Username/password convention | In push-mirror docs: username plus access token as password: "the requested **password** can also be your access token". | <https://forgejo.org/docs/latest/user/repo-mirror/> | H |
| Server behaviour | A Basic header's password is the token. "if the password is empty or `x-oauth-basic`, we'll use the username as a token". | `/home/alpha/rewrite/git-renewed/ruk/forgejo/services/auth/util.go:60-66` | H |
| Git HTTP scope check | `CheckRepoScopedToken(ctx, repo, GetScopeLevelFromAccessMode(accessMode))` is applied in the git HTTP handler. | `/home/alpha/rewrite/git-renewed/ruk/forgejo/routers/web/repo/githttp.go:156` | H |
| Scopes | `write:repository` is the repo write scope. A scope is required even for public repos. A "Specific repositories" token can only carry `read/write:repository` and `read/write:issue`. | <https://forgejo.org/docs/latest/user/authentication/token-scope/> | H |
| Specific-repo token limits | It cannot transfer the repo, add collaborators or change visibility. | same | H |
| SSH push mirrors | Forgejo generates an Ed25519 key. The public key is added as a deploy key on the target. LFS over SSH is not implemented. | repo-mirror doc | H |

Minimum to push a target repo: a token with `write:repository`, owned by a user who can write to the repo. For a specific-repo token, the repo must be in its list.

### 3.2 Refs rejected or hidden, protection

- **`refs/pull/N/head` rejected for non-AGit PRs:** `Only AGit pull-requests can be pushed via refs/pull/N/head` plus a hint to push to the head branch. A nonexistent PR index gives `Unable to get pull request N`; an index ≤0 gives `Invalid ref` [H] (`/home/alpha/rewrite/git-renewed/ruk/forgejo/routers/private/hook_pre_receive.go:233-237` dispatch, `:579-...` `preReceivePull`).
- **`refs/for/<branch>/<topic>` is AGit:** pushing it creates or updates a pull request instead of a ref. It requires permission to create PRs, a non-empty repo, a non-wiki repo, and an existing base branch, else `Unexpected ref:` [H] (`hook_pre_receive.go` `preReceiveFor`; <https://forgejo.org/docs/latest/user/agit-support/>).
- **Codeberg's own advice** is that `git push --mirror` to Codeberg works "by adding multiple remotes". Its FAQ adds that disabling PR/issues units "will skip the deletion of internal references" [H] (<https://docs.codeberg.org/getting-started/faq>). Exact semantics [U]; send explicit refspecs.
- **Protected branches and tags:** globs on `/`-separated names, `**` spans separators; a rule without glob wins over one with. Force push is blocked on protected branches unless allowed. Protected tags use glob or `/regex/` patterns; empty allowed lists mean nobody can create or modify [H] (<https://forgejo.org/docs/latest/user/repository/protection/>). The pre-receive hook rejects a non-allowed pusher with `Not allowed to push to protected branch X` (`hook_pre_receive.go:414-416`).
- **Quota:** the pre-receive hook rejects over-quota pushes (HTTP 413 `quotaExceeded`); deletions are still allowed [H] (`hook_pre_receive.go` `preReceiveTag` and `isOverQuota` branches; swagger 413 responses).

### 3.3 Limits

| Limit | Value | Source | Conf. |
|---|---|---|---|
| Codeberg git storage | 750 MiB git, +1.5 GiB for LFS, packages, releases and attachments per user or org. Private or personal "non-promoted" content ≤100 MiB. | <https://blog.codeberg.org/new-storage-limits-on-codeberg-what-you-need-to-know.html>, <https://docs.codeberg.org/getting-started/faq> | H |
| Codeberg exceptions | Request via `Codeberg-e.V./requests` before exceeding. | same | H |
| Codeberg repo count | default 100 per user or org | FAQ | H |
| Codeberg API/web rate limits | no numeric limits published. Basic limits are exposed via HTTP headers (July 2026). Some limits are heuristic spam-protection and deliberately undisclosed. A user hit one with a handful of web PR creations. An older mention cites "2000 requests/300s". | <https://codeberg.org/Codeberg/Community/issues/2721> | M |
| Self-hosted Forgejo | no per-file or push-size cap found in the source I read. Only quota and `[git.timeout] MIRROR=300s` for the mirror runner. | `modules/setting/git.go:61` | M |
| Forgejo push-mirror intervals | min interval 10 min (hard floor 1 min), default 8 h; `interval` 0 is allowed (sync-on-commit only) | `/home/alpha/rewrite/git-renewed/ruk/forgejo/modules/setting/mirror.go:12-24`, `routers/api/v1/repo/mirror.go:365-366` | H |

### 3.4 REST API (`/api/v1`)

| Op | Endpoint | Scope | Conf. |
|---|---|---|---|
| Check existence | `GET /repos/{owner}/{repo}` | `read:repository` | H |
| Create under a user | `POST /user/repos` with `CreateRepoOption`: `name` (required), `description`, `private`, `auto_init`, `default_branch`, `readme`, `license`, `gitignores`, `object_format_name` (sha1 or sha256), `template`, `trust_model`. Responses: 201, 400, 401, 403, 409 (exists), 413 (quota), 422. | `write:repository` | H |
| Create under an org | `POST /orgs/{org}/repos`. Exact scope combination (organization and/or repository) is [U]. | | M |
| Edit, archive | `PATCH /repos/{owner}/{repo}` with `EditRepoOption`: `archived`, `description`, `private`, `mirror_interval` (e.g. `8h30m0s`), `internal_tracker` and others. | `write:repository` | H |
| Push-to-create | Pushing to a nonexistent repo creates it if `ENABLE_PUSH_CREATE_USER` or `ENABLE_PUSH_CREATE_ORG` is on. Default off. Push options `-o repo.private=true|false` set visibility. Do not rely on it. | <https://forgejo.org/docs/v17.0/user/git-cli/push-to-create/> | H |
| Forgejo as source | `POST /repos/{owner}/{repo}/push_mirrors` with `CreatePushMirrorOption {remote_address, remote_username, remote_password, interval, sync_on_commit, use_ssh, branch_filter}`. `GET` lists; `GET/DELETE /push_mirrors/{name}`; `POST /push_mirrors-sync`. Response `PushMirror {remote_name, repo_name, remote_address, interval, sync_on_commit, last_error, last_update, created, public_key, branch_filter}`. The push-mirror-sync route requires repo admin (`hook`/`api.go:1010`). | swagger | H |

On Codeberg itself, pull mirrors are disabled (§6); push mirrors from Codeberg and pushes into Codeberg are unaffected by that.

---

## 4. Summary matrix

| | GitHub | GitLab | Forgejo / Codeberg |
|---|---|---|---|
| HTTPS user | `x-access-token` (apps), any (PAT) | `oauth2` (PAT), any non-blank (project/group token) | any (token as password), or token as username |
| Min write cred | `contents:write` (+ `workflows:write`), or `repo` (+ `workflow`) | `write_repository` (+ Developer, or Maintainer for protected defaults) | `write:repository` |
| Push hidden refs | `refs/pull/*` | `refs/merge-requests/*`, `refs/keep-around/*`, `refs/pipelines/*`, `refs/environments/*` | `refs/pull/*` (non-AGit), `refs/for/*` (PR creation) |
| Force push on protected | blocked by default | no force push on default branch unless toggled | blocked unless allowed |
| Max push | 2 GiB | 5 GiB (GitLab.com) | quota-bound (Codeberg 750 MiB git) |
| Max file | 100 MiB hard, warns at 50 MiB | push-rule dependent | none found |
| Rate guidance | ≤6 pushes/min/repo | 10,000 Git-HTTPS req/min/user | undisclosed; use headers |
| Repo create | `POST /user/repos` or `/orgs/{org}/repos` | `POST /projects` | `POST /user/repos` or `/orgs/{org}/repos` |
| Archive | `PATCH /repos` `archived` | `POST /projects/:id/archive` | `PATCH /repos` `archived` |
| Native push-mirror | none | yes (project settings or `POST /projects/:id/remote_mirrors`) | yes (`POST /repos/{o}/{r}/push_mirrors`) |

---

## 5. How existing forges implement push mirrors, and recommended semantics

### 5.1 Gitea (rukie checkout) and Forgejo

- **Config:** per mirror, a git remote `remote_mirror_<suffix>` is created with `--mirror=push`. Push refspecs are `+refs/heads/*:refs/heads/*` and `+refs/tags/*:refs/tags/*`. With a `branch_filter` (Forgejo only), the heads refspec is replaced by one `+refs/heads/<b>:refs/heads/<b>` per comma-separated glob, tags are always pushed (`ruk/forgejo/services/mirror/mirror_push.go:36-97`) [H].
- **Command:** `git push --mirror -f <remote>`, run with `GIT_TERMINAL_PROMPT=0` and `LC_ALL=C` (`modules/git/gitcmd/command.go:259-260` in rukie) [H].
- **Triggers:** an interval (default 8 h) and optional `sync_on_commit`. `syncPushMirrorWithSyncOnCommit` enqueues all `sync_on_commit` mirrors of the repo for each push [H].
- **Pre-sync checks:** LFS objects are pushed first via the LFS API when the LFS server is on, except for SSH auth; then the git push. The wiki repo is pushed separately; a missing wiki remote is tolerated [H].
- **Error surfacing:**
  - `LastError` is stored on the mirror row, with `exit status N - ` stripped.
  - Credentials are sanitised from errors: `SanitizeErrorCredentialURLs` in rukie, `util.SanitizeErrorCredentialURLs` in Forgejo [H].
- **SSRF defences in Forgejo's runner**
  - The URL allow/block list is re-checked every sync, because DNS may now resolve to an internal address or the list may have changed (`recheckPushPermitted`, lines 183-188, 215-221).
  - HTTP redirects are disabled via `-c http.followRedirects=false` (lines 284-286) [H].
- **Overwrite semantics:** the docs warn "This will force push to the remote repository. This will overwrite any changes in the remote repository!" [H]. Gitea has an open request for an option to disable force-push [M] (<https://github.com/go-gitea/gitea/issues/21105>; search result only, not read).
- **Prune:** this comes from `--mirror` (git deletes remote refs without a local counterpart), not from a separate option [H] (git-push `--mirror` doc).

### 5.2 GitLab

- **Triggers:** a push to the source updates the mirror within 5 minutes, or 1 minute when "Only mirror protected branches" is on. An admin or Maintainer can force an update. Silent Mode disables mirroring [H] (<https://docs.gitlab.com/user/project/repository/mirror/push/>).
- **Overwrite semantics:** by default, a ref that diverged on the mirror is overwritten from upstream. With **Keep divergent refs**:
  - updates to the diverged ref are skipped and the mirror keeps its commit;
  - refs that exist only on the mirror are left untouched;
  - the update is marked **failed**.
  The flag can only be changed after creation via the API [H].
- **Deletion semantics:** "When a branch is merged into the default branch and deleted in the source project, it is deleted from the remote mirror on the next push. Branches with unmerged changes are kept." A diverged branch shows an error [H]. This is more conservative than `--mirror`.
- **Filters:** "Only mirror protected branches"; "Mirror specific branches" (RE2 regex, Premium/Ultimate) [H].
- **Status API:** `update_status` is one of `none|scheduled|started|finished|failed`, plus `last_error`, `last_successful_update_at`, `last_update_at`, `last_update_started_at`, `keep_divergent_refs`, `only_protected_branches`, `auth_method` (`password` or `ssh_public_key`), `host_keys`. The stored URL is returned scrubbed of credentials [H] (<https://docs.gitlab.com/api/remote_mirrors/>).
- **Ownership coupling:** a mirror is tied to its creating user; deleting that user disables it [H].

### 5.3 GitHub

No native push-mirror feature. GitHub documents the manual recipes [H] (<https://docs.github.com/en/repositories/creating-and-managing-repositories/duplicating-a-repository>):

- one-off: `git clone --bare`, then `git push --mirror <new>`;
- ongoing: `git clone --mirror`, `git remote set-url --push origin <mirror>`, then `git fetch -p origin && git push --mirror`;
- LFS: `git lfs fetch --all`, then `git lfs push --all`.


[Showing lines 1-599 of 1962 (50.0KB limit). Use :600 to continue]

It also documents "push each of these commits one at a time" to stay under 2 GiB.

### 5.4 Recommended semantics for rukie

These are synthesized [M] from the sources above.

| Topic | Recommendation | Evidence |
|---|---|---|
| Refs to push | Only `refs/heads/*` and `refs/tags/*`. Never `refs/pull/*`, `refs/for/*`, `refs/merge-requests/*`, `refs/keep-around/*`, `refs/pipelines/*`, `refs/environments/*`, or rukie's own agent and PR namespaces. | §1.2, §2.2, §3.2 |
| Force vs non-force | Default per-ref **force** (`+src:dst`) for a faithful mirror, as Gitea, Forgejo and GitLab's default do. Offer a "keep divergent refs" mode: non-forced pushes, record the per-ref failure and continue (GitLab's model). Use per-ref `--force-with-lease=<ref>:<expect>` only when rukie tracks the last pushed value; the bare form is explicitly risky with background fetches. | §5.1, §5.2, git-push `--force-with-lease` doc [H] |
| Atomicity | Prefer per-ref results (`--porcelain`) over `--atomic` so that one rejected ref (protected branch, push rule) does not block the others. `--atomic` is available if all-or-nothing is wanted. | git-push `--atomic`, `--porcelain` [H] |
| Prune | Compute deletes yourself: `ls-remote` the target, diff against the source's heads and tags, send `:refs/heads/x` for refs rukie previously pushed and that no longer exist. Delete only refs that rukie itself pushed (record them). Never touch other namespaces. `--prune` with explicit refspecs also works (git doc: "`--prune remote refs/heads/*:refs/tmp/*`"), but removes any remote ref matching the destination pattern, including refs a human added on the mirror [H]. | §5.2 conservative deletion semantics |
| Default-branch deletion | Never delete the target's default branch automatically; repoint it first. GitHub, GitLab and Forgejo each protect default branches (§1.2, §2.2, §3.2). | [M] |
| Protected target branches | Surface "protected branch" errors verbatim and tell the user which toggle to flip. GitHub: `GH006` messages; GitLab: "Allowed to force push"; Forgejo: `Not allowed to push to protected branch`. | §1.2, §2.2, §3.2 |
| Tags | Pushed with `+refs/tags/*`. On GitLab, a protected-tag rule blocks tag updates silently from the target's point of view. | §2.2 |
| LFS | Push LFS objects over the host's LFS HTTPS API before refs (as Gitea does; GitLab push mirrors need an HTTPS URL for LFS). GitHub LFS has its own 3,000 requests/min bucket. | §5.1, §2.3, §1.3 |
| Size guards | Before pushing, check the source's largest blob against GitHub's 100 MiB; fall back to LFS or fail with a clear error. Split pushes over 2 GiB (GitHub) or 5 GiB (GitLab.com) into batches of commits, as GitHub documents. | §1.3, §2.3 |
| Triggers | Debounced on-push trigger (coalesce for ≥10 s, minimum interval between pushes per target) plus a slow periodic reconcile. Cap per-target pushes below GitHub's 6 per minute per repo and GitLab's 1-minute cadence. | §1.3, §2.2 |
| Retry and backoff | Honour `Retry-After` and `x-ratelimit-reset` (GitHub and GitLab). For other failures use exponential backoff with jitter and a ceiling. On repeated 401/403, stop retrying and mark the mirror `needs-attention`; GitLab bans an IP for 15 minutes after 300 failed auth requests per minute, so never hammer a bad credential. | §1.3, §2.3 |
| Error classes | At minimum: `auth` (401/403 or `Authentication failed`), `not_found` (repo missing; offer create), `rate_limited`, `rejected_ref` (hidden or protected), `too_large` (push or file), `push_protection` (secret scanning, push rules), `network`, `quota` (Codeberg/Forgejo 413). | §1–§3 |
| Surfacing | Store `last_error`, `last_attempt`, `last_success`, `update_status` per target (GitLab and Gitea models). Always scrub credentials from URLs and stderr. | §5.1, §5.2 |
| Credentials | Never embed tokens in stored remote URLs. Pass them through an `http.extraheader` or credential helper at run time, and disable prompting. Prefer short-lived GitHub App installation tokens (1 h). | [M] |
| URL safety | Re-validate the target host on every sync, disable HTTP redirects, block internal addresses. | §5.1 (Forgejo SSRF checks) |
| Create-if-missing | Optional per target: `GET` the repo, on 404 call the create endpoint with `auto_init=false` and an empty initial state (GitHub, GitLab) or no `auto_init` (Forgejo), then push. Needs the extra scopes listed in §1.4, §2.4, §3.4. Make it opt-in. | §1.4, §2.4, §3.4 |
| Mark as mirror | Set `description` and, optionally, disable issues and PRs on the target; do **not** archive while still pushing. | §1.4, §2.4 |
| Divergence | A target that changed independently will be overwritten in force mode, and will fail in keep-divergent mode. Say this in the UI as GitLab and Forgejo do. | §5.1, §5.2 |

### 5.5 Request budgeting

GitHub's secondary limits count each `POST`/`PATCH`/`PUT`/`DELETE` as 5 points and cap content-creating requests at 80/min and 500/h. A "check exists → create → patch metadata" sequence per repo is about three calls; budget them separately from git pushes [M].

---

## 6. Policy and terms by host

- **GitHub**
  - Bot-created accounts are not allowed; one human-owned machine account is. Abusive API use can suspend API access [H] (ToS §B, §H).
  - AUP: no "excessive automated bulk activity" [H].
  - Nothing found that bars mirroring or AI-written code.
- **GitLab**
  - AUP: don't exceed other users' usage "significantly" or impair stability [H].
  - Nothing found that bars push mirroring or AI-written code.
  - Self-managed: operator policies; GitLab's Silent Mode disables mirrors [H].
- **Codeberg (Forgejo)**
  - **Pull mirrors are disabled.** New mirrors were disabled in March 2020 because they "consum[ed] resources forever" [H] (<https://blog.codeberg.org/mirror-repos-easily-created-consuming-resources-forever.html>). The FAQ says "If you need a mirror, you can create manual mirrors by adding multiple remotes to your local repository and using `git push --mirror`" [H].
  - **Pushing into Codeberg:** the blog says new manual mirroring "can of course still be done" via `git push --mirror`. No Codeberg document I read bans push mirrors from other forges [M].
  - **Terms of Use** (<https://codeberg.org/codeberg/org/src/branch/main/TermsOfUse.md>, commit dated 2026-07-21) [H]:
    - §2(1)1: public content must carry a FLOSS licence.
    - §2(1)2: private repos only for things required for FLOSS projects, plus small personal items.
    - §2(1)4: forks, migrations and testing repos with no unique contributions, inactive over a month, "might be removed after notifying maintainers and providing a 90 days period".
    - §2(1)7: "You must not share projects that mostly consist of code written by 'generative AI'-tools (including services such as *Claude*, *OpenAI Codex*)."
    - §2(5): excessive traffic or resource use "will normally" earn a warning first.
  - **Blog guidance** (<https://blog.codeberg.org/protecting-our-floss-commons-from-llms.html>, 2026-07-23) [H]:
    - Unlikely to be affected: active communities, projects with significant pre-LLM history, maintainers who accepted some LLM contributions.
    - Tolerated in practice: small LLM-generated side projects using few resources.
    - **Unwelcome:** "Projects that are created by LLM 'agents' in autonomous ways", "Projects written and maintained with heavy use of LLMs", "Projects where the amount of resources … is significantly larger than what the involved amount of people could have created by hand", and "Projects heavily tied to the LLM ecosystem". Moderators will not scan automatically.
  - **Implication for rukie:** a mirror of an agent-authored repo to Codeberg may violate §2(1)7. Make Codeberg an explicit opt-in with a visible warning and a link to the ToU. Do not enable it by default or in bulk, and cap its push rate. This is [M]: it rests on the ToU wording and on moderation being human-led.

---

## 7. Gaps

- Whether Gitea's or Forgejo's `--mirror` pushes produce `hidden ref` errors on GitHub, and how users cope. [U]
- Forgejo's exact token-scope combination for `POST /orgs/{org}/repos`. [U]
- Whether GitHub App installation tokens may create repos under user accounts. [U]
- GitHub tag-protection (legacy) status versus rulesets. [U]
- GitLab per-file size limits on GitLab.com. Not documented anywhere I read. [U]
- GitLab behaviour when a push mirror force-updates a protected default branch. [U]
- Numeric Codeberg git/API rate limits. Not public. [M]
- GHES per-version differences in limits beyond the 3.21 pages read. [U]

## Sources (primary)

- GitHub: `docs.github.com` pages for remote repositories, PAT management, OAuth scopes, App-installation auth, deploy keys, large files, repository limits, the 2 GiB push limit, REST rate limits, repos REST API, fine-grained permissions, protected branches, rulesets, push protection, ToS, AUP, GHES 3.21 limits and rate-limit configuration, and the GHES 3.22 REST quickstart.
- GitLab: `docs.gitlab.com` pages for mirroring, push mirroring, mirror troubleshooting, remote-mirrors API, projects API, access-token scopes, fine-grained tokens, project access tokens, deploy tokens, protected branches, default branch, protected tags, push rules, GitLab.com settings and rate limits, instance account/limit settings, Gitaly refs and keep-around refs, plus the GitLab AUP at `handbook.gitlab.com`.
- Forgejo and Codeberg: `forgejo.org/docs` pages for repo mirror, token scope, protection, AGit and push-to-create, the Codeberg swagger at `codeberg.org/swagger.v1.json`, `docs.codeberg.org` FAQ, the Codeberg blog posts on mirrors, storage and LLMs, the Codeberg ToU, and Codeberg/Community issue #2721.
- Git: `git-scm.com/docs/git-push`.
- Local source read: `rukie/services/mirror/mirror_push.go`, `rukie/modules/git/*`, `ruk/forgejo/services/mirror/mirror_push.go`, `ruk/forgejo/routers/private/hook_pre_receive.go`, `ruk/forgejo/routers/web/repo/githttp.go`, `ruk/forgejo/services/auth/util.go`, `ruk/forgejo/modules/setting/mirror.go`.

---

## Summary (for the main agent)

I answered all six points per host in the document above, with primary-source URLs and local file citations. Items I could not confirm are marked [U] in §7.

Headline answers:
1. **Auth:**
   - GitHub: classic PAT with `repo` (+`workflow`) or fine-grained `contents:write` (+`workflows:write`); App installation tokens use `x-access-token`.
   - GitLab: `write_repository`, username `oauth2` (any non-blank for project/group tokens); deploy tokens cannot push.
   - Forgejo/Codeberg: token as password with `write:repository`; token as username also works.
2. **Refs:** GitHub rejects `refs/pull/*`; GitLab rejects `refs/merge-requests`, `refs/keep-around`, `refs/pipelines` and `refs/environments`; Forgejo rejects non-AGit `refs/pull/*` and turns `refs/for/*` into a PR. Push only `refs/heads/*` and `refs/tags/*`, and compute deletes yourself.
3. **Limits:** GitHub 2 GiB per push and 100 MiB per file (warning at 50), with a recommended 6 pushes/min/repo. GitLab.com 5 GiB per push and 10 GB per repo, with `Retry-After` on 429. Codeberg has a 750 MiB git quota and undisclosed rate limits.
4. **API:** create with `POST /user/repos` or `/orgs/{org}/repos` (GitHub, Forgejo) and `POST /projects` (GitLab, needs `api`); check with `GET`; archive via `PATCH` (GitHub, Forgejo) or `POST /projects/:id/archive` (GitLab).
5. **Prior art:** Gitea and Forgejo run `git push --mirror -f` (interval, `sync_on_commit`, optional branch filter). GitLab force-overwrites by default or "keeps divergent refs"; it deletes only branches merged and deleted at the source. GitHub has no native push mirror.
6. **Policy:** Codeberg's ToU §2(1)7 prohibits projects mostly written by generative-AI tools (it names Claude and Codex), and its blog calls autonomous LLM-agent projects unwelcome. Codeberg's pull mirrors are disabled, but manual `git push --mirror` into it is endorsed. GitHub and GitLab only forbid excessive or abusive automation.

I did not verify Gitea/Forgejo `--mirror` pushes producing hidden-ref errors on GitHub, the Forgejo org-repo scope combination, or GitLab push-mirror behaviour against a protected default branch.

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/rukie/services/mirror/mirror_push.go` — Gitea push-mirror runner (remote config at 32-45, force/mirror push at 158-163) to replace.
- `/home/alpha/rewrite/git-renewed/ruk/forgejo/services/mirror/mirror_push.go` — Forgejo variant with branch filter, SSH key auth and the SSRF re-check at sync time (183-221, 279-287).
- `/home/alpha/rewrite/git-renewed/ruk/forgejo/routers/private/hook_pre_receive.go` — how Forgejo rejects `refs/pull/*` and `refs/for/*` (233-237, 579+), as target-side behaviour to design around.
- `/home/alpha/rewrite/git-renewed/ruk/forgejo/modules/setting/mirror.go` — mirror interval defaults (10 min minimum, 8 h default) and the `DisableNewPush` and `DisableNewPull` switches.
- `/home/alpha/rewrite/git-renewed/ruk/forgejo/services/auth/util.go` — Basic-auth token handling (60-66), the contract for the Forgejo/Codeberg credential format.

[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…