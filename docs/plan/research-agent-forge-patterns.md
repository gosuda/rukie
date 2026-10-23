Artifact not written: no write tool (`read`, `grep`, `web_search`, `ast_grep` only). The file below is one merged block with the header fix, the rukie baselines and both edits already applied. Save it as `local://research-agent-forge-patterns.md`.

I re-ran targeted greps on the pages the last advisory disputed, and they match the report.
- **GitHub blog:** it says "40 built-in tools down to 13", success rates up "2-5 percentage points" and latency down "400 milliseconds". It also gives 94.5% / 87.5% / 69.0% tool-use coverage.
- **SourceHut ToS:** it says "(i.e. September 10th)" and prohibits "original content written with or which facilitates the use of LLMs".
- **GitLab MCP Authentication:** it lists an OAuth token with the `mcp` scope or a fine-grained PAT. It says legacy, project and group tokens get `403 insufficient_scope`.
- **Not re-verified:** a grep on the `api.github.com` repo URLs returned nothing, which looks like a tool quirk. The `pushed_at`, `archived` and licence fields came from earlier full reads of that JSON. Re-check them with `gh api repos/<owner>/<repo> --jq '.pushed_at,.archived,.license.spdx_id'` before relying on them.

**Top findings per section**
1. **MCP servers for forges**
   - GitHub, GitLab and Gitea all use toolsets or scopes to shrink the tool list, with read-only modes and tools that take a `method` argument.
   - GitHub's own data shows fewer tools help.
   - **Gap:** Ruk's MCP server is `2024-11-05`, stdio only, 12 tools, with no toolsets, read-only mode or HTTP transport. `claim`, `attest` and `land` are not MCP tools yet.
2. **Agent-facing HTTP standards**
   - Standard: RFC 9457 problem+json, RFC 8288 `Link`, RFC 9110 `If-Match`.
   - Not standard: llms.txt is a community proposal and the `Idempotency-Key` draft expired.
   - Rukie already has `Link`/`X-Total-Count` and blob ETags.
   - **Gap:** `If-Match` on mutable resources, `Idempotency-Key`, problem+json, `llms.txt` and Markdown pages.
3. **Zero-JS forge UIs**
   - sourcehut, cgit, stagit and Fossil show it works.
   - **Gap:** 58 template files use `form-fetch-action`, and their handlers answer with JSON, not a redirect. Fomantic, tippy, the Vue trees, WebAuthn and captchas all need replacing. The CSP is already nonce-based and tightens last.
   - **Mirror risk:** Codeberg and SourceHut policies on LLM-written content may block agent-authored repos.
4. **Many concurrent agents on one repo**
   - Ruk already has claims, attestations and two-step landing.
   - **Gap:** none of them has an HTTP route, web page or MCP tool yet.
5. **OAuth and token scoping**
   - Rukie already has an OIDC/OAuth2 provider with PKCE and category-level token scopes.
   - **Gap:** RFC 9728/8414 well-known documents and RFC 8707 audience checks. Extend the existing scopes with agent scopes and an agent identity.

```markdown
# Research: agent-first forge interfaces and zero-JS forge UIs (for rukie)

Date: 2026-10-20. Confidence: **H** = read in a primary source or the local tree this session; **M** = partial or secondary; **INF** = inference or unverified.
Local trees: rukie = /home/alpha/rewrite/git-renewed/rukie (Gitea fork, Go module prefix `gitea.dev/...`). A second copy with an identical top-level layout exists at /home/alpha/rewrite/git-renewed/ruk/rukie; only the first was read. Ruk = /home/alpha/rewrite/git-renewed/ruk.
Repo-field caveat: stars, license, pushed_at and archived were read from full api.github.com JSON responses; a later grep on the same URLs returned nothing (tool quirk). Re-check with `gh api repos/<owner>/<repo> --jq '.pushed_at,.archived,.license.spdx_id'` before relying on them.

## 0. Candidate repos

| Candidate | Stars | License | pushed_at | Archived | Fit |
|---|---|---|---|---|---|
| github/github-mcp-server (Go) | 33,766 | MIT | 2026-10-19T15:23:31Z | no (378 open issues) | Best pattern donor (toolsets, read-only, consolidation). Coupled to GitHub API: copy patterns, do not fork. |
| gitea.com/gitea/gitea-mcp (Go) | not on GitHub API; unverified | unverified | README current, MCP up to 2026-07-28 | n/a | Closest forge analog (61 tools by README row count). |
| GitLab built-in MCP | n/a (part of GitLab 19.6 docs) | n/a | GA in 19.5 | n/a | Best-documented toolset header, prefix header, OAuth and fine-grained PAT story. |
| raohwork/forgejo-mcp (Go) | 70 | MPL-2.0 | 2025-10-28T07:59:05Z | no | Low fit: stale, no toolsets, no read-only mode. |
| gastownhall/beads (was steveyegge/beads) | 28,022 | MIT | 2026-10-19T17:49:42Z | no | Best agent work-claim prior art. |
| jj-vcs/jj (Rust) | 32,154 | Apache-2.0 | 2026-10-19T16:05:50Z | no | Change-ID, stack and conflict model. |
| alecthomas/chroma (Go) | 5,057 | API says NOASSERTION; COPYING text is MIT (+ OFL for one font) | 2026-10-18T10:41:08Z | no | Syntax highlighting; rukie's view_file.tmpl already emits `chroma` classes (use not traced). |
| charmbracelet/soft-serve (Go) | 7,266 | MIT | 2026-10-05T15:23:49Z | no | SSH TUI only; agent-friendly SSH commands pattern. |
| bors-ng (Elixir) | 1,528 | Apache-2.0 | 2024-04-04T12:24:44Z | **yes** | Do not depend on it; concept only. |
| cgit, stagit, Fossil, sourcehut | not GitHub-hosted | cgit GPLv2 [H], stagit MIT [H], Fossil BSD-2 (page links COPYRIGHT-BSD2.txt) [M], sourcehut unverified | n/a | n/a | Zero-JS UI pattern donors. |

## 1. MCP servers for forges

### GitHub MCP [H]
Sources: https://github.com/github/github-mcp-server (README; docs/server-configuration.md; docs/remote-server.md; docs/tool-renaming.md; docs/insiders-features.md on main).
- About 20 toolsets. Default set = `context, repos, issues, pull_requests, users`. `all` and `default` are meta-sets.
- Remote config via headers `X-MCP-Toolsets`, `X-MCP-Tools`, `X-MCP-Exclude-Tools`, `X-MCP-Readonly`, `X-MCP-Lockdown`, `X-MCP-Insiders`, `X-MCP-Features`, and URL forms `/x/{toolset}`, `/readonly`, `/x/all/readonly`.
- Precedence: read-only beats everything, even explicitly named tools. Exclude beats toolsets and tools. Server-side lockdown is an upper bound the header cannot relax.
- Unknown toolsets are silently ignored; unknown tool names fail at startup.
- Naming: snake_case, `issue_read`, `get_file_contents`, `actions_list`. The alias table (`DeprecatedToolAliases`) lists 25 retired names that now resolve to 5 `method`-discriminated tools (`actions_list/get/run_trigger`, `projects_list/get/write`).
- Each tool lists "OAuth Challenge Scopes"; the docs say scope filtering is always enabled.
- Pagination: `page`/`perPage` (default 30, max 100) on REST-style tools, cursor `after` on newer ones (`get_file_blame`); CSV mode emits `#pageInfo.*` preamble lines.
- Output: compact DTOs without API URLs; `structuredContent` and output schemas only when MCP >= 2026-07-28 is negotiated.
- Dynamic toolsets: documented as beta in the v0.20.0 README ("avoid situations where the model gets confused by the sheer number of tools", `--dynamic-toolsets`); no match for "dynamic" in main's README [M], so treat as de-emphasised [INF].
- Conformance lesson: open issue #3453, v2.0.1 omitted the required `resultType` under 2026-07-28 and Claude Code loaded no tools.

### GitLab MCP [H]
Source: https://docs.gitlab.com/user/model_context_protocol/mcp_server/
- `POST /api/v4/mcp`; experiment in 18.3, GA in 19.5.
- `X-Gitlab-Enabled-Mcp-Server-Toolsets`: `meta` always; default `core, merge_requests, work_items, repository, ci` (+ `duo_agent_platform` when available); opt-in `wikis, code_security, orbit`.
- Unknown toolset returns 400; toolsets and tools combine as a union; listing never grants access; licence-gated tools are hidden. `X-Gitlab-Mcp-Server-Tool-Name-Prefix` (max 32 chars) avoids collisions.
- Rate limit 60/min (Free) or 600/min (Premium/Ultimate) per user, shared across all of that user's clients.

### Gitea MCP (official) [H]
Source: https://gitea.com/gitea/gitea-mcp (README)
- 61 tools by README row count. Consolidated `*_read`/`*_write` tools with a `method` parameter; read-only mode (`-r`) hides the `*_write` tools.
- Filters `-S/--scope` and `-O/--tools` combine as a union. Stateless POST-only `/mcp`; page size capped by server `MAX_RESPONSE_ITEMS` (default 50). OAuth optional (`-oauth`), no DCR.

### Forgejo community server (raohwork) [H]
Flat tool set, token via flag or env, stale since 2025-10.

### Evidence that tool count hurts agents [H]
- GitHub blog, 2025-11-19, https://github.blog/ai-and-ml/github-copilot/how-were-making-github-copilot-smarter-with-fewer-tools : trimming VS Code's default 40 built-in tools to 13 improved success rates by 2-5 percentage points (SWE-Lancer, SWE-bench Verified) and cut online response latency by an average of 400 milliseconds. Embedding-based selection reached 94.5% tool-use coverage, versus 87.5% LLM-based and 69.0% for the static list.
- Anthropic, https://www.anthropic.com/engineering/writing-tools-for-agents : "More tools don't always lead to better outcomes"; consolidate chained operations; namespace by service and resource; `response_format` concise/detailed; cap responses (25,000 tokens in Claude Code); actionable errors.
- MCP 2026-07-28 changelog, https://modelcontextprotocol.io/specification/2026-07-28/changelog.md : `tools/list` SHOULD be deterministic; `ttlMs` and `cacheScope` are required on list/read results (CacheableResult). Tool annotations must be treated as untrusted unless the server is trusted (server/tools.md).

### Baseline in Ruk today [H, read from source]
- `ruk/crates/ruk/src/mcp/{mod,server,tools}.rs` exists: newline-delimited JSON-RPC 2.0 over **stdio only**; methods `initialize`, `tools/list`, `tools/call`, `ping`; `MCP_PROTOCOL_VERSION = "2024-11-05"` (server.rs:36).
- 12 tools (tools.rs): nine change operations (`describe_change`, `new_commit`, `edit`, `split`, `squash`, `rebase`, `abandon`, `resolve`, `revert`) plus `revset_eval`, `refs_list`, `attestation_query`.
- Three reads degrade locally (mod.rs docs): `revset_eval` returns `E_UNSUPPORTED_OPERATION`; `refs_list` does not enforce cursor binding; `attestation_query` uses an allow-all checkpoint.
- Tool schemas reuse existing serde request types, with unknown-field checks.
- No toolsets, read-only mode, annotations, HTTP transport or OAuth found (grep of server.rs for `toolsets|annotations|readOnlyHint` returned nothing).
- Discrepancy to check: mod.rs names `E_UNSUPPORTED_OPERATION`, but the 30-code enum in /tmp/ruk-model.json does not list it [M].
- The protocol schema has `claim.acquire/release/list`, `attest.add/list`, `land.prepare/execute` (/tmp/ruk-model.json); none is an MCP tool yet.

### Gaps and recommendation (only what is missing)
1. Move the Ruk MCP server from `2024-11-05` to `2026-07-28`: stateless, no `initialize`, `server/discover`, required `resultType`, `Mcp-Method` / `Mcp-Name` headers, `ttlMs` / `cacheScope`. Add a Streamable HTTP transport next to stdio, fronted by rukie over the hosted multi-repo daemon.
2. Add the missing coordination tools: `claim` (acquire/release/list), `attest` (add/list), `land` (prepare/execute), and a `mirror_admin` toolset.
3. Add toolset gating with GitHub's precedence (read-only > exclude > include) and GitLab's union and 400-on-unknown semantics. Add a tool-name prefix header and an alias table. Add `readOnlyHint` / `destructiveHint` annotations, and enforce read-only server-side as well.
4. Keep the default tool list at or below about 16. Consolidate the read side behind `method` enums (`repo_read`, `history_read`, `change_read`, `issue_read`, `code_search`). The nine write tools are distinct typed requests, so keeping them separate is acceptable while the total stays small.
5. Add a conformance test against 2026-07-28 (the `resultType` bug above).

## 2. HTTP-level agent standards

### Standards evidence
- llms.txt [H], https://llmstxt.org/ (v2, modified 2026-08-10): required H1, optional blockquote, H2 file lists, an `## Optional` section; may live at any path and covers that subtree; `.md` page variants; `Link: <...>; rel="alternate"; type="text/markdown"` and `rel="describedby"`. Published by OpenAI, Anthropic and Gemini docs. It is a community proposal, not an IETF or W3C standard.
- Accept: text/markdown [H], https://developers.cloudflare.com/fundamentals/reference/markdown-for-agents/ : `Content-Type: text/markdown`, `Vary: Accept`, `Content-Signal`. Cloudflare drops `ETag` and `Last-Modified` on converted output because conditional requests cannot be honoured; conversion is HTML to Markdown with a 6 MiB cap.
- RFC 9457 [H], https://www.rfc-editor.org/rfc/rfc9457 : `application/problem+json`; `type` URI is the primary identifier; consumers SHOULD NOT auto-dereference it; extension members allowed; one problem per response is recommended when types differ.
- Idempotency-Key [H], https://datatracker.ietf.org/doc/draft-ietf-httpapi-idempotency-key-header/ : `-07` (latest revision 2025-10-15) is expired and archived, never an RFC.
- RFC 8288 [H], https://www.rfc-editor.org/rfc/rfc8288.html , and GitHub REST pagination docs (https://docs.github.com/rest/guides/using-pagination-in-the-rest-api): `Link` header with `rel=next|prev|first|last`, omitted when everything fits one page.
- RFC 9110 section 13.1.1 [H], https://www.rfc-editor.org/rfc/rfc9110 : `If-Match` uses strong comparison; failing it means the method MUST NOT run and the server may answer 412. 428 comes from RFC 6585 [INF: not re-read].

### Baseline in rukie today [H, read from tree]
- Link pagination exists: `APIContext.SetLinkHeader(total, pageSize)` (services/context/api.go:219) writes `Link` and exposes it via CORS; `SetTotalCountHeader` writes `X-Total-Count` (services/context/base.go:73). Used e.g. in routers/api/v1/utils/hook.go.
- ETag/304 exists for blobs, LFS objects and attachments through `httpcache.HandleGenericETag{Private,Public}Cache` (routers/api/v1/repo/file.go:141,182; routers/web/repo/download.go; routers/common/serve.go). A `Link: rel="canonical"` header is already emitted (services/context/repo.go:1051).
- API errors are `APIError{message,url}` (services/context/api.go:63-67). `application/problem+json` appears only in the Swift package API (routers/api/packages/swift/swift.go:84).
- Grep over routers/, services/context/ and modules/web/ found **no** `If-Match`, `Idempotency`, `llms` or `text/markdown`. Not checked: templates/ and public/ contents for a static llms.txt.
- A swagger UI exists (templates/swagger, web_src/js/swagger.ts).

### Gaps and recommendation (only what is missing)
- Keep the existing Link and X-Total-Count mechanism. Add opaque cursors only for Ruk-backed lists (Ruk pages with cursors; page_default 100, page_max 1000).
- Add `If-Match` / ETag on mutable resources (issues, changes, claims, mirrors), not just blobs. Use the Ruk stamp or op id as the ETag. Return 412 on mismatch and 428 when the header is required but missing.
- Add an `Idempotency-Key` header mapped to Ruk's per-mutation `request_id` UUID (the digest covers `args`, not the id). A replay returns the stored outcome; the same key with different args returns `E_REQUEST_REUSED` in an error body.
- Replace `APIError{message,url}` with RFC 9457 on the new API surface. `type` = `https://<host>/problems/<code>`, mapped from Ruk's `E_*` registry (30 codes in /tmp/ruk-model.json). Add `retryable` and `next_action` extension members.
- Add `/llms.txt` (and per-repo `llms.txt`), `Accept: text/markdown` on repo, issue and change pages with `Vary: Accept` and per-variant ETags, and `Link: rel="alternate"; type="text/markdown"`.
- Drop the swagger UI (JS). Serve the OpenAPI document only.

## 3. Zero-JS forge UIs

### Prior art [H]
- sourcehut: sourcehut.org states "All features work without JavaScript". Design post https://drewdevault.com/blog/sourcehut-design (2019): content first, minimal colour, buttons labelled with their action ("Delete $reponame"), icons only alongside text. GraphQL APIs on every service (https://sourcehut.org/blog/2022-11-15-four-years). Patch review is email-first with statuses set by an `X-Sourcehut-Patchset-Update` reply header and mbox export for `git am` (https://man.sr.ht/lists.sr.ht/).
- cgit (https://git.zx2c4.com/cgit/about/): CGI in C, HTML caching, side-by-side diffs, atom feeds, archives, GPLv2.
- stagit (https://codemadness.org/git/stagit/file/README.html): static pages via libgit2, usable in text browsers; README lists limits for 2000+ commits, many files, many branches.
- Fossil (https://fossil-scm.org/home/doc/trunk/www/javascript.md): JS as enhancement with graceful fallback; the timeline graph column collapses without JS; its doc suggests `<noscript>` checkboxes plus submit buttons for diff selection.
- Soft Serve (README): SSH TUI plus `ssh host repo tree|blob` commands; no web UI.
- Browser primitives that need no JS (MDN): `<details name>` exclusive groups (https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/details); `:target` (https://developer.mozilla.org/en-US/docs/Web/CSS/Reference/Selectors/:target); `popover` + `popovertarget` and `commandfor` (https://developer.mozilla.org/en-US/docs/Web/API/Popover_API/Using); Post/Redirect/Get with 303 (https://en.wikipedia.org/wiki/Post/Redirect/Get). PRG cannot cover a refresh before the submission completes, so a per-render hidden token is needed [INF: map it to Ruk `request_id`].

### Baseline in rukie today [H, read from tree]
Ordering: rukie is a Gitea fork with a Vite/TS/Vue frontend, Fomantic, tippy, sortable and CodeMirror, not a greenfield web layer. Setting `script-src 'none'` is the last step, not the first. Delete the JS stack, replace the templates, convert the forms, and only then tighten the CSP. Until then the CI "no `<script`" crawl fails on every page (head_script.tmpl loads iife.ts on all of them).

Scripts on every page and inline:
- `templates/base/head_script.tmpl`: inline error-collector `<script>`, a `global-window-config` JSON block, and `ctx.ScriptImport "web_src/js/iife.ts"`.
- `templates/base/footer.tmpl`: a module script that `alert`s when the frontend failed to initialise.
- A grep of templates/ for `<script` found these files: `base/footer.tmpl`, `base/head_script.tmpl`, `repo/diff/box.tmpl` (diff file-tree toggle reads `window.localUserSettings`), `shared/combomarkdowneditor.tmpl`, `status/500.tmpl`, `user/auth/captcha.tmpl` (loads reCAPTCHA, hCaptcha and Turnstile scripts), `user/dashboard/repolist.tmpl`.

Already server-rendered:
- `repo/view_file.tmpl` renders a code table with `id="L{{$line}}"` anchors and `chroma` classes; the source/rendered toggle (`?display=source`), raw, blame, history, permalink and edit are plain links.
- Pagination links, issue lists and diffs are templated server-side [INF: not each read].

JS-only today:
- Fomantic behaviours in `web_src/js/modules/fomantic/` (`dropdown.ts` 18 KB, `modal.ts`, `tab.ts`, `transition.ts`, `dimmer.ts`). Templates use `ui dropdown`, `ui modal` and `g-modal-confirm` (e.g. `repo/issue/sidebar/issue_dependencies.tmpl`, `repo/issue/labels/label_edit_modal.tmpl`).
- Line menu in view_file.tmpl: `code-line-menu tippy-target` with new-issue reference, blame, and copy-permalink; plus `data-global-click="onCopyContentButtonClick"` and the unicode-escape toggle buttons.
- Vue components in `web_src/js/components/`: `ViewFileTree`, `RepoFileSearch`, `RepoRecentCommits`, `RepoActionView` (20 KB, Actions log), `WorkflowGraph` (21 KB).
- `web_src/js/modules/`: tippy tooltips, sortable (project boards), toast; `webcomponents/relative-time.ts` and `overflow-menu.ts`; `features/tribute.ts` (mention autocomplete); `features/user-auth-webauthn.ts`; `markup/mermaid.ts` and `math.ts`.
- Live updates: websocket route `/-/ws` (routers/web/web.go:609), `user-events.sharedworker.ts`, and the `/stopwatches` endpoint.

Forms:
- `form-fetch-action` is the class on issue creation, comment, label edit, due date, dependency, pin, lock and time-tracking forms (`templates/repo/issue/*`).
- Handlers answer those POSTs with `ctx.JSONRedirect(redirect)` → `{"redirect": ...}` (services/context/context.go:239) or `JSONError*` (HTTP 400 + JSON), e.g. routers/web/repo/issue_comment.go:147.

CSP:
- Already nonce-based: `script-src * 'nonce-…'; style-src * 'unsafe-inline'` with `default-src * data: blob:` (services/context/context_template.go:116-152; services/context/base.go:181-199; test at context_test.go:72).

### Gap list (replacements needed before a no-`<script>` rule can pass CI)
1. **Delete** the Vite/TS/Vue pipeline (web_src/js, package.json, vite.config.ts, pnpm-lock.yaml, vitest and eslint configs, Playwright e2e) and `ScriptImport`. Keep any CSS-only build (Tailwind config, `web_src/css`) if wanted, but remove the Fomantic JS modules.
2. **Replace in templates** (each needs a no-JS design):
   - Dropdowns and selections (`ui dropdown`): `<select>`, `<details>`, or a dedicated GET page with a search form.
   - Modals (`ui modal`, `g-modal-confirm`): a confirmation page, or an inline `<form>` plus `popover`.
   - Tabs (`ui tab`): `<details name>`, or one URL per tab.
   - Tooltips (`data-tooltip-content`): `title`.
   - Code line menu and copy buttons: plain per-line links with `:target` highlight; drop copy and use the raw link.
   - Diff file tree (`repo/diff/box.tmpl`): always-visible server-rendered list in `<details open>`.
   - Markdown editor (`combomarkdowneditor.tmpl`) and mention autocomplete (tribute): plain `<textarea>` plus a separate preview POST.
   - Live updates (`/-/ws`, shared worker, stopwatches, Actions log view): reload links, or `<meta http-equiv="refresh">` for CI pages [INF].
   - Mermaid and math rendering: server-side, or show source.
   - Project boards (sortable): move buttons.
   - Relative time and overflow menus: absolute `<time datetime>`; wrapping toolbars.
   - WebAuthn login: cannot work without JS. Decide: drop it, or keep password, TOTP and OAuth only.
   - Captcha (`captcha.tmpl`: reCAPTCHA, hCaptcha, mCaptcha, Turnstile are all JS): choose a non-JS alternative such as rate limits or invitation.
3. **Forms (the main no-JS gap).**
   - Count: a grep for `form-fetch-action` in rukie `templates/` paged as "files 1-20 of 58", so 58 template files carry the class. The first page shows admin (`admin/badge`, `admin/packages`, `admin/repo`, `user/view_orgs`), `org/settings` and `org/teams`, the stopwatch buttons in `base/head_navbar_icons.tmpl`, and `repo/editor/{edit,delete,upload,patch,cherry_pick,fork}`. It also shows `repo/branch/list.tmpl` (delete-branch modal), `repo/diff/new_review.tmpl` and a `devtest/fetch-action.tmpl` demo page. Pages 2 and 3 were not read, but the earlier issue-template grep found about 10 more under `repo/issue/*`. Counting by file is a lower bound on forms.
   - Cause [H]: `JSONRedirect` returns `200 {"redirect": ...}` and `JSONOK` returns `{"ok": true}` (services/context/context.go:239,243). `JSONError*` returns 400 + JSON (context.go:257-283). The forms are real `<form method="post" action=...>` [H], but without JS the browser would show that JSON [INF: not executed].
   - Recommendation: under the clean cutover the JS client is deleted, so there is no need for a `303`-when-no-`X-Requested-With` shim. Make `JSONRedirect` / `JSONOK` become `303 See Other` with a flash message, and make `JSONError*` re-render the form with the error. The shim only matters if the JS frontend is kept during a transition.
   - Many of the 58 are modal forms (`ui small modal form-fetch-action`, e.g. branch delete, repo delete, package delete, member remove). Each also needs the confirmation-page redesign from item 2, not only the 303.
   - Handler call sites of `JSONRedirect` / `JSONOK` / `JSONError*` were not counted.
   - Add a hidden per-render `request_id` token so a refresh or double-submit is idempotent.
4. **CSP (last step)**: change to `default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; script-src 'none'` (the existing mechanism only needs new values). Add a CI crawl that fails on any `<script`, after steps 1-3 so every page can pass.
5. **Docs for agents**: serve `.md` twins (section 2).

### Mirror-target policy risk [H]
- Codeberg: https://blog.codeberg.org/protecting-our-floss-commons-from-llms.html (2026-07-23). Members passed (358 to 144) a Terms of Use change prohibiting "vibe-coded projects". Its informal guidance lists as likely unwelcome: "Projects that are created by LLM 'agents' in autonomous ways", "Projects written and maintained with heavy use of LLMs", and projects whose resource use far exceeds what a few people could produce by hand. The ToU commit text itself was not read.
- SourceHut: https://sourcehut.org/blog/2026-08-27-tos-changes-and-llms . New ToS prohibit "original content written with or which facilitates the use of LLMs", taking effect for new projects after the two-week notice period (i.e. September 10th).
- Implication: pushing agent-authored repositories to Codeberg may breach its ToU. Recommendation: per-target policy preflight in mirror setup, Codeberg disabled until the owner opts in.

## 4. Many concurrent agents on one repo

### Evidence [H]
- Kubernetes Lease (https://kubernetes.io/docs/concepts/architecture/leases/): `holderIdentity`, `leaseDurationSeconds`, `renewTime`; expired leases garbage-collected after 1 hour in the apiserver example.
- Beads (https://github.com/steveyegge/beads): `bd ready`, `bd update <id> --claim` (atomic assign + in_progress), hash IDs (`bd-a1b2`) to avoid merge collisions, Dolt-backed dependency graph.
- jj (https://github.com/jj-vcs/jj): conflicts stored as first-class objects in commits, automatic rebase of descendants, operation log and undo.
- GitHub merge queue (https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue): temporary `gh-readonly-queue/...` branches, `merge_group` events, build concurrency 1-100, a failing entry is removed and the rest rebuilt, and "jump the queue" forces a full rebuild.
- Zuul (https://zuul-ci.org/docs/zuul/latest/gating.html): dependent pipeline with speculative parallel testing; on failure builds are discarded and restarted without the failing change; frequent failures can waste resources; shared queues across projects.
- in-toto Statement v1 (https://github.com/in-toto/attestation/blob/main/spec/v1/statement.md): `_type`, `subject[]` matched by digest, `predicateType`, `predicate`. SLSA Provenance (https://slsa.dev/spec/v1.1/provenance, current spec v1.2): `externalParameters` are untrusted and must be verified; digests may use a `gitCommit` key.

### Baseline in Ruk [H, /tmp/ruk-model.json]
- Claims: path-prefix, TTL 1-3600 s, `enforce` (overlap fails `E_CLAIMED`) or `advise` (warn only); `E_LEASE_LOST` exists.
- Attestations: `{subject: Oid, kind, result, signed_at, supersedes}`, signed in namespace `ruk-attest`, private keys never reach the coordinator; `signed_at` is "a claim, never an ordering authority".
- `land.prepare` returns `candidate`, `base`, `plan` (expires after one hour), `policy_generation` and `requirements[]`; then `land.execute`.
- Operations: `op.log`, `undo`, `resolve`, `rebase`, `workcopy` journal (ruk-core/src/workcopy_journal/).
- None of these has an HTTP route, a web page or an MCP tool yet (section 1 baseline).

### Recommendation
1. Expose claims, attestations and landing through the new MCP tools and HTTP endpoints. Add an issue-level claim on top (Beads-style `ready` + atomic `claim`) that creates a Ruk path claim with the same TTL and releases with it.
2. Default agents to `enforce` and humans to `advise`. Show claim holder and expiry in the file tree and on the change page.
3. Model a change as a stable change ID plus a stack of parents (jj). Build a Zuul-style dependent pipeline over `land.prepare` candidates: bound speculative depth, and make queue-jumping explicit and costly.
4. Export attestations on demand as in-toto Statement v1: `subject = {name: repo, digest: {gitCommit: oid}}`, `predicateType = https://<host>/attest/<kind>/v1`. Keep Ruk's native format as the stored form.
5. Agent flow: `land.prepare` → check `requirements[]` → `attest.add` for each → `land.execute`. Use the MCP tasks extension for long waits.

## 5. OAuth for MCP and token scoping

### Evidence [H]
- MCP authorization 2026-07-28 (https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization.md; changelog above):
  - Servers MUST implement RFC 9728 Protected Resource Metadata and answer 401 with `WWW-Authenticate: Bearer resource_metadata="https://<host>/.well-known/oauth-protected-resource", scope="..."` (the `scope` parameter is a SHOULD).
  - Authorization servers MUST provide RFC 8414 or OIDC discovery.
  - Client registration: Client ID Metadata Documents (SHOULD), pre-registration, or DCR (now deprecated).
  - Clients MUST send the RFC 8707 `resource` parameter; servers MUST validate token audience and MUST NOT pass tokens through. PKCE is required.
  - Insufficient scope returns 403 `insufficient_scope` with the required scopes in one challenge; clients step up using the union of scopes.
  - stdio servers SHOULD NOT use OAuth. The 2026-07-28 transport is stateless.
- GitLab MCP (https://docs.gitlab.com/user/model_context_protocol/mcp_server/, "Authentication"): accepts an OAuth access token with the `mcp` scope or a fine-grained PAT with the MCP tool resource and Execute permission; legacy PATs, project tokens and group tokens get `403 Forbidden` with `insufficient_scope`.
- GitLab fine-grained PATs (https://docs.gitlab.com/auth/tokens/fine_grained_access_tokens/): both the token's permission and the owner's own access must allow the operation; 365-day default and maximum expiry; admin `sudo` tokens still enforce their own permissions; groups can require fine-grained tokens after a set date.
- GitHub fine-grained PATs (https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens) [M, text-in-image read]: one resource owner per token, optional per-repository limit, per-permission levels, org approval, default expiry 30 days (1-366 or none), auto-removal after 1 year unused.
- GitHub MCP: PAT takes precedence over OAuth locally; tools are filtered by token scope.

### Baseline in rukie today [H, read from tree]
- An OIDC/OAuth2 provider exists: `/.well-known/openid-configuration` (routers/web/web.go:526) with `code_challenge_methods_supported: ["plain","S256"]` (routers/web/auth/oauth2_wellknown.go:59), issuer logic in services/oauth2_provider/access_token.go.
- PAT scopes (models/auth/access_token_scope.go): `all`, `public-only`, `no-token-scope`, and read/write pairs for `activitypub, admin, misc, notification, organization, package, issue, repository, user`. These are categories, not per-repository, per-path or per-tool.
- Grep of routers/web/web.go, routers/web/auth and services/oauth2_provider found no `oauth-authorization-server`, `oauth-protected-resource` or `resource_metadata`.

### Gaps and recommendation (only what is missing)
1. Add RFC 9728 `/.well-known/oauth-protected-resource` and the `WWW-Authenticate` challenge above; add RFC 8414 `/.well-known/oauth-authorization-server`; validate the RFC 8707 `resource` audience; add `iss` (RFC 9207); add Client ID Metadata Document support (DCR optional).
2. Extend the existing scope set, do not rebuild it. models/auth/access_token_scope.go already has `read:`/`write:` pairs per category (`issue`, `repository`, `package`, `admin`, ...) plus `all`, `public-only` and `no-token-scope`. Add agent scopes (`mcp:read`, `mcp:write`, `claim`, `attest`, `land`, `mirror:admin`) to that same bitmap.
   - What I did not find in the constants read: restriction to specific repositories or paths. I also did not check token expiry [INF]; confirm both before building.
   - Annotate each MCP tool with its required scopes and hide tools the token cannot use. Emit all required scopes in one 403 challenge.
3. Add agent credentials on top: a service-account identity, a short default TTL, and the both-checks rule (token permission AND owner's access, as in GitLab). Write the agent identity into Ruk's `EffectiveIdentity` on every mutation.
4. Rate-limit per agent identity, not per user (GitLab's shared per-user limit is a known pain point for many clients [INF]).

## Unverified items
- gitea-mcp stars and license; Zuul and sourcehut licenses; the Codeberg ToU commit text.
- RFC 6585 428 text; `<meta refresh>` for CI pages; no-JS behaviour of `form-fetch-action` forms (inferred from the JSON-only handlers, not executed).
- Whether rukie already uses chroma (class names suggest it; not traced).
- Contents of rukie `public/.well-known` and any static llms.txt.
- Count of `JSONRedirect` / `JSONOK` / `JSONError*` call sites; pages 2-3 of the `form-fetch-action` grep.
- Rukie token expiry and per-repository token limits (not checked).
- GitHub fine-grained PAT details came from a text-in-image read (M).
- Repo fields (stars, license, pushed_at, archived): re-check with `gh api` as noted at the top.

## Critical files for implementation
Ruk (real, listed in the tree):
- /home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/protocol.rs: 184 KB protocol types (claims, attest, land, errors, pages).
- /home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/coord/: `prepare.rs` (land plans), `policy.rs`, `lock.rs`, `hosted.rs`, `oplog.rs`.
- /home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/mcp/{mod,server,tools}.rs: current 2024-11-05 stdio MCP server to upgrade.
- /home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/{mod,ipc}.rs: hosted daemon framing the Go forge calls.
- /tmp/ruk-model.json: typed v1 schema (47 methods, 30-code error registry).

Rukie (Gitea fork at /home/alpha/rewrite/git-renewed/rukie; a second copy exists at /home/alpha/rewrite/git-renewed/ruk/rukie, unread):
- templates/base/head_script.tmpl, templates/base/footer.tmpl, templates/repo/diff/box.tmpl, templates/repo/view_file.tmpl: JS loaders and JS-only UI to replace.
- services/context/{api.go,base.go,context.go,context_template.go}: Link/ETag helpers, API error envelope, JSONRedirect, CSP.
- routers/web/web.go, routers/web/auth/oauth2_wellknown.go, models/auth/access_token_scope.go: OIDC provider, websocket route and PAT scopes to extend.
```

[advisor] <advisory advisor="default" severity="nit" turns_ago="1" guidance="weigh, don't blindly obey"> Stop re-checking GitHub …

---
## Later corrections from the same agent (apply over the body above)

→ read(/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/loca…) ⇒ ok · 1 line

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Grep each API URL for pushed_at, …

---
I could not write the artifact. I have no file-write tool (`read`, `grep`, `web_search` and `ast_grep` only). The orchestrator directory exists and is empty, so nothing was copied there.

- **Intended path:** `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/research-agent-forge-patterns.md`
- **Content:** The complete merged document is the last fenced markdown block in my previous message, starting at `# Research: agent-first forge interfaces and zero-JS forge UIs (for rukie)`. Please have the orchestrator, or any agent with a write tool, save that block to the path above verbatim. The summary bullets sit just above it.

**Re-check on your `ttlMs` / `cacheScope` challenge:** I re-ran a grep on the 2026-07-28 changelog. Item 5 reads "Require `ttlMs` and `cacheScope` fields on results returned by `tools/list`, `prompts/list`, `resources/list`, `resources/read`, and `resources/templates/list`".

**Not re-checked:** the repo fields (`pushed_at`, `open_issues_count`, `license`, `archived`). A grep on the `api.github.com` URLs returns nothing, a tool quirk. I read those values earlier from full JSON responses, and the file tells readers to confirm them with `gh api`.

[advisor] <advisory advisor="glm-flash" severity="concern" guidance="weigh, don't blindly obey"> You cannot write the file, so Ma…

[advisor] <advisory advisor="default" severity="concern" turns_ago="2" guidance="weigh, don't blindly obey"> Reply to Main by IRC…

---
I checked the tool schema, not just memory. I have four tools: `_read`, `_grep`, `web_search` and `_ast_grep`. None can create or edit a file or run a shell command. `_ast_grep` takes only `pat`, `path`, `lang`, `skip` and `i`, so it has no rewrite parameter. I also have no IRC or send tool, so this reply is the only channel to Main.

**Request to Main:** please write the file yourself. Save the last fenced markdown block from my earlier "Artifact not written" message, from `# Research: agent-first forge interfaces and zero-JS forge UIs (for rukie)` to the end of "Critical files for implementation", to:

`/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/research-agent-forge-patterns.md`

That block already has every edit applied. Do not use the earlier code block plus Edit 1 and Edit 2; the merged block supersedes them.

I checked that the target directory exists and is empty. The file is not written, and I can give you the path only as a request.

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Mark pushed_at, archived and lice…

---
I can't re-confirm those fields. A re-fetch of `https://api.github.com/repos/bors-ng/bors-ng` just now failed with HTTP 403, probably rate limiting. My earlier full reads of that JSON showed:

| Repo | `pushed_at` | `archived` | License | Other |
|---|---|---|---|---|
| github/github-mcp-server | 2026-10-19T15:23:31Z | false | MIT | `open_issues_count` 378 |
| bors-ng | 2024-04-04T12:24:44Z | true | Apache-2.0 | — |

The elided-range markers you saw did not appear in my own tool results. I still can't prove those lines to you, so please treat the repo fields as **M** (unconfirmed on re-fetch) until someone re-runs the `gh api` check.

**Edit for the file:** in section 0, replace the "Repo-field caveat" line with:

```markdown
Repo-field caveat [M]: stars, license, pushed_at, archived and open-issue counts were read from api.github.com JSON earlier in the session. A re-fetch on 2026-10-20 returned HTTP 403, so they are not re-confirmed. Verify with `gh api repos/<owner>/<repo> --jq '.pushed_at,.archived,.license.spdx_id,.open_issues_count'` before relying on them.
```

In the section 0 table, read every stars, license, `pushed_at` and archived cell as **M**, not **H**. The "378 open issues" note in the github-mcp-server row is also **M**.

The artifact is still not written, as covered in my last reply.

[advisor] <advisory advisor="minimax" severity="nit" turns_ago="1" guidance="weigh, don't blindly obey"> Stop hitting api.github.…

[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Stop re-pulling the GitHub AP…

[advisor] <advisory advisor="muse" severity="nit" guidance="weigh, don't blindly obey"> You stop re-reading GitHub API. You saw H…

[advisor] <advisory advisor="overlycritic" severity="nit" guidance="weigh, don't blindly obey"> Keep those cells [H]; do not down…