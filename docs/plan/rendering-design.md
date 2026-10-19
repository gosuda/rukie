# rendering-design.md (revision 2)

Plan mode has no write tool, so this full revision is returned inline. It replaces the earlier draft. Persist it verbatim as `rendering-design.md`.

**Labels:** **V** = verified in source this pass or cited from a planning artifact whose source I checked; **P** = probable; **U** = unverified; **I** = design choice or inference, not measured.

## 0. Review disposition

I checked each review item against source. Accepted items are reflected in the body. Rejected or partly rejected items are listed with the evidence.

| Item | Disposition | Evidence |
|---|---|---|
| goldmark `GlobalAttributeFilter` includes `style` | **Accepted.** The filter is not a guard | `renderer/html/html.go:302` (V) |
| `rel` "may not render" | **Rejected, with a test added.** `rel` is in `LinkAttributeFilter` (line 504), which `renderAutoLink` (523) and `renderLink` (601) use. `RenderAttributes` iterates node attributes (731–734). `BaseNode.SetAttributeString` exists (`ast.go:453`) | V on master; re-confirm on the v1.8.6 tag |
| Reject `style` and `on*` before `templ.Raw` | **Accepted.** `htmlcheck` runs fail-closed on goldmark output | §6.2, §11 |
| Gate is tokenizer plus fuzz; never set style via node attributes | **Accepted** | §6.2, §11 |
| Weak-pointer cache | **Not in this design.** Count-bounded caches only, and only if measured miss cost fails budget | §7.4, §12.3 |
| Streamed responses must stay on one Ruk page | **Accepted** | §8.2, §10 |
| Huma not in `go.mod` | **Fact accepted; removal rejected.** Huma v2.39.1 is a settled decision and is added by the rewrite. Current `go.mod` is the Gitea base; `go-chi/chi/v5 v5.3.2` is the router being replaced | `go.mod` (V); settled decisions |
| Reuse existing pins | **Accepted.** goldmark v1.8.6 is kept (`go.mod`; used at `markdown.go:140,162,169`). goldmark-highlighting is dropped | §6, §15 |
| Resolve `at` to one Oid | **Accepted** | §7.2, §9.2, §8.1 |
| Pin blame and file to one `ViewInfo` | **Accepted.** Retry once, then fail closed | §9.2 |
| Split as server transform | **Accepted.** Already the design; now explicit | §8.4–8.5 |
| Explicit `http.NewResponseController` | **Accepted** | §10.2 |
| Cite one research copy | **Accepted.** Cite the "consolidated final" copy by section | §15 |
| One content-hash algorithm | **Accepted** | §4.2 |
| p95 benchmark definition and pass/fail | **Accepted** | §12 |
| `#L10-L20` scrolls | **Rejected as a claim in this design; accepted as a fact.** Only `#L{n}` scrolls. The nojs doc line is false | nojs-interaction-patterns.md §1 (V) |
| Test helper parses HTML, tag names and attribute keys only | **Accepted.** Byte substring check removed | §11 |
| p95 index | **Accepted.** Nearest-rank, `ceil(0.95n)-1` | §12.2 |
| `data:image/svg+xml` identicons | **Accepted.** Added to the allowlist for `img[src]` only | §5, §11 |
| "Cheaper hot path" in §1 | **Accepted** | §1 |
| Read existing diff and sanitizer code | **Accepted.** Read; code is deleted, not reused | §8.3, §14 |
| Existing line anchors | **Accepted.** `id="L{n}"` scheme kept | `templates/repo/view_file.tmpl:113` (V) |
| Shell fields make ETags wrong | **Accepted.** HTML ETags removed | §13 |
| Markdown cache key lacks repo, ref, directory | **Accepted** (also needed for link resolution) | §6.2 |
| Blame base one-based | **Partly accepted.** `/tmp/ruk-model.json` `$defs.LineRange` description says one-based (V). The Rust doc comment was not confirmed. `BlameHunk.start_line` base is U | §9.2 |
| `transform_codespan.go` imports bluemonday | **Accepted** (line 12, `css.HexRGB` at 55) | §14 |
| Chroma lexers are stateful | **Accepted.** Tokenize from file start, and per diff side | §7.4, §8.1 |
| Diff budget arithmetic | **Accepted.** Budgets recomputed | §12.1 |
| Relative links need Ruk | **Accepted.** Rendering resolves links with pure path logic; resolution happens at click | §6.2 |
| Write deadline ordering | **Accepted** | §10.2 |
| Template inventory unverified | **Accepted.** Top-level families read; exact count left to phase 0 `find` | §14.1 |
| `TableCellAlignStyle` emits `style=` | **Accepted.** Keep `TableCellAlignAttribute`, which rukie already uses (`markdown.go:140`) | §6.1 |
| nojs `§…` placeholders | **Accepted.** Real section numbers used | throughout |
| "Keep artifact; budgets pending measure" | **Noted.** Budgets are stated as pass/fail targets pending phase 0 | §12 |

## 1. Decisions summary

| Concern | Pick | Reason |
|---|---|---|
| Template engine | templ, pinned via `go tool` | Compile-time typed views. Performance is a phase-0 gate, not a claim (§2) |
| CSS | `app.css` plus generated `chroma.css`, embedded, content-hashed | No build tool; immutable URLs |
| Headers | Brief's CSP, plus three baseline headers | Zero-JS is browser-enforced |
| Markdown | goldmark v1.8.6, no unsafe, no attribute syntax, `htmlcheck` fail-closed on output | Raw HTML killed by renderer default; attribute syntax killed by not enabling it |
| Highlighting | chroma v2.27.0, class mode, one `highlight` package for code, diff sides, and fences | One path, stateful-correct |
| Diff | Own hunk parser; full-side tokenization; token-level Myers under explicit caps | Deterministic; no time-based limits |
| Blame | `/blame/…` route reusing the code table with a gutter | Bounded cost; linkable |
| Streaming | One Ruk page per response; finite; flush explicit via `ResponseController` | Errors after commit are in-document only |
| Zero-JS gate | `htmlcheck` (runtime, fail-closed) plus tests plus fuzzing | Same code at runtime and in tests |
| Budgets | Cold p95 per route, measured by a repeatable harness, pass/fail | §12 |

## 2. Template engine: templ vs html/template

| Criterion | templ | html/template | Edge |
|---|---|---|---|
| Type safety | Typed Go parameters; errors at `go build` (research-go-stack.md §2, V) | Runtime; `Execute(any)`; typos surface at execution | templ |
| Escaping | Text and attributes escaped; URL attributes sanitized (research §2, V) | Contextual autoescape; `ZgotmplZ` | Tie |
| Trusted-HTML escape hatch | `templ.Raw`, `templ.Safe*` | `template.HTML`, `template.URL` | Tie; both confined (§11) |
| Speed | Vendor mean, 2023, self-published: 369 ns/op vs 2,475 ns/op (research §2). **P, and not used for budgets.** Mean only; no percentile | Runtime reflection per field | Unproven; phase-0 gate |
| Streaming | Writes to `io.Writer`; `templ.WithStreaming` exists but changes error semantics | Writes to `io.Writer` | Tie; we use our own explicit flush (§10) |
| Toolchain | Codegen step; tool pinned; generated files committed; CI drift check; Go ≥ 1.26 (we are on 1.27) | None | html/template |
| Supply risk | Pre-1.0 (v0.3.1070, 2026-10-04); single maintainer (research §2, P) | Stdlib, patched via Go releases we track (research §1) | html/template |
| Composition | Typed children; `@Comp(args)` | `{{template}}`; no compile check | templ |
| Ban enforcement | Banned constructs visible in `.templ` source | Same, in `.tmpl` text | Tie |

**Pick: templ.** The failures html/template defers to runtime (typos, drift between view models and about 20 legacy families, agent-driven edits) move to `go build` and CI. The cost is one pinned tool, committed generated output, and a drift check. The view layer is the only part that would move if templ stalls, because `vm` is pure Go.

**Phase-0 gate:** implement the file view (1,000 lines) and split diff (25 files) in both engines on identical `vm` input. Keep templ unless it is no faster at p95 **and** allocates no less on both. **I.**

## 3. Layout and component structure

### 3.1 Packages

```
internal/web/
  assets/      app.css, chroma.css (generated), favicon.svg; go:embed; gen/ (chroma generator)
  asset/       hashing at startup, precomputed gzip, handler for /static/
  secheaders/  CSP and baseline headers; every wrapper implements Unwrap()
  trusted/     the only package that calls templ.Raw or templ.Safe*
  htmlcheck/   runtime tokenizer gate; used by trusted/markdown and by zerojs tests
  shell/       document frame, skip link, header, primary nav, main, footer, flash
  ui/          tabs, breadcrumb, pager, details, badge, form field, error summary, empty state
  view/        *.templ page views by family
  vm/          pure Go view models; imports no templ
  highlight/   chroma wrapper: lexer selection, prefix cap, token output
  markdown/    goldmark pipeline and AST transformers
  diff/        hunk parser, unified and split builders, token Myers with caps
  negotiate/   Op-to-HTML adapter, error pages, explicit flush helper
  zerojs/      test helpers and self-tests (test-only)
```

### 3.2 Document frame

Order: `<html lang="en">` → `<head>` (charset, viewport, `color-scheme`, title, two stylesheet links, favicon) → Boundary flush (§10) → `<body>` → skip link → `<header>` (brand, `<nav aria-label="Primary">`, identity area with a `<form method="post" action="/logout">` carrying the CSRF field) → `<main id="main" tabindex="-1">` → `<footer>` (build id; store and view from the last read's `ViewInfo`).

Shell model:

```go
type Shell struct {
	Title     string
	Crumbs    []Crumb
	Nav       []NavItem
	Principal string
	CSRF      string
	Flash     []Flash
	Assets    Assets
}
```

`Assets` is computed once at startup (§4.2).

### 3.3 Components

| Component | Inputs | Output | Notes |
|---|---|---|---|
| `ui.RepoTabs` | repo, active tab | `<nav aria-label="Repository">` of links | Links, not ARIA tabs |
| `ui.Breadcrumb` | `[]Crumb` | `<nav aria-label="Breadcrumb"><ol>` | Last item `aria-current="page"` |
| `ui.Pager` | prev, next | `<nav aria-label="Pagination">` | Same targets as `<link rel>` in head and `Link` headers |
| `ui.Details` | summary, open | `<details><summary>` | Only disclosure mechanism (nojs §2) |
| `ui.FormField` | label, name, input, hint, error | `<label for>`, `aria-describedby`, `aria-invalid` | GOV.UK pattern (nojs §6) |
| `ui.ErrorSummary` | `[]FieldErr` | `<div role="alert" tabindex="-1">` with links to `#field` | On 422 re-render |
| `ui.Badge` | kind, text | `<span class="badge badge-…">` | Never color-only |
| `view.TreePage` | `vm.Tree` | `<table>` | §7 |
| `view.BlobPage`, `ui.CodeTable` | `vm.Code` | `<table class="code">` | §7.5 |
| `view.BlamePage` | `vm.Blame` | `ui.CodeTable` with gutter | §9 |
| `view.DiffPage` | `vm.Diff`, mode | Unified or split table per file | §8 |
| `view.MarkdownBlock` | trusted HTML | `<article class="md">` | §6 |
| `shell.Flash` | `[]Flash` | `role="status"` or `role="alert"` | |

### 3.4 Rules

- `.templ` files contain markup, `for`, and one level of `if`. Deeper branching lives in `vm`. Cyclomatic complexity below 10 applies to `vm` and hand-written Go; generated files are excluded.
- `class` values come from a closed enum. Repository data never reaches a class or attribute name.
- No `style`, no `on*`, no `<script>`, no `templ.JS*` or `templ.Once*` (§11).
- No comments in templ or Go implementation code; exported doc comments only.
- `templ.Raw` and `templ.Safe*` appear only in `trusted/` (AST lint, §11).

### 3.5 Op-to-HTML integration

Each Op `Out` has one registered view, `func(Out) templ.Component`. `negotiate` sits in front of the Huma operation. When the highest-q acceptable type is `text/html`, it calls the same handler and passes `Out` to the view. Otherwise the JSON and markdown paths handle the request (out of scope here).

Huma v2.39.1 is a settled decision and a **new** module. It is absent from the current `go.mod` (V). It is added in commit C1 (§14). The router changes from `go-chi/chi/v5` to `net/http` ServeMux at cutover.

`Out` holds exactly one Ruk page. The view reads file sides and patch streams through a `Source` interface, pinned to the oids already resolved for the response (§8.1, §9.2). A streamed response never fetches another cursor (§10.1).

## 4. CSS delivery

### 4.1 Files

- `assets/app.css`: hand-written. First statement: `@layer reset, tokens, base, layout, components, code, diff;`. Every rule sits inside a named layer.
- `assets/chroma.css`: generated by `assets/gen/main.go` through `//go:generate`. Contains the light style, and the dark style inside `@media (prefers-color-scheme: dark)`, all inside `@layer code`. Committed. CI runs `go generate ./internal/web/assets/... && git diff --exit-code`. The generator produces bytes at development time; nothing runs at deploy time.
- `assets/favicon.svg`.

No web fonts. `default-src 'none'` blocks them. Typography uses system stacks.

### 4.2 One hash algorithm

1. `//go:embed` supplies the bytes of each file.
2. At process start, `asset.New` computes `sum := sha256.Sum256(b)` and `h := hex.EncodeToString(sum[:8])` (16 hex characters).
3. Public name is `<stem>.<h>.<ext>`, for example `app.0123456789abcdef.css`. The URL is `/static/<public name>`.
4. `Assets.AppCSS` and `Assets.ChromaCSS` are the final URL strings. Templates read them. No runtime lookup happens on the render path.
5. The handler accepts only names in the startup map. Anything else is 404.
6. Headers: `Cache-Control: public, max-age=31536000, immutable`, `ETag: "<h>"`, `Content-Type: text/css; charset=utf-8`, `X-Content-Type-Options: nosniff`, `Vary: Accept-Encoding`. `Content-Encoding: gzip` from a precomputed `compress/gzip` body when accepted. `If-None-Match` returns 304. No `Vary: Cookie`.

No inline styles exist, so no path to unhashed or inline CSS exists.

### 4.3 Cascade layers and tokens

Order: `reset < tokens < base < layout < components < code < diff`. Later layers win regardless of specificity, so nothing needs `!important`. chroma's rules live in `code`.

Sample token block (**I**: placeholder values; contrast is tested):

```css
@layer tokens {
  :root {
    color-scheme: light dark;
    --color-canvas: #ffffff;
    --color-fg: #1f2328;
    --color-muted: #59636e;
    --color-border: #d1d9e0;
    --color-focus: #0969da;
    --color-add-bg: #dafbe1;
    --color-add-intra: #aceebb;
    --color-del-bg: #ffebe9;
    --color-del-intra: #ffc1ba;
    --font-sans: system-ui, -apple-system, "Segoe UI", sans-serif;
    --font-mono: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    --space-1: 0.25rem;
    --space-2: 0.5rem;
    --space-3: 1rem;
    --header-h: 3rem;
    --tab-size: 4;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --color-canvas: #0d1117;
      --color-fg: #e6edf3;
      --color-muted: #9198a1;
      --color-border: #3d444d;
      --color-focus: #4493f8;
      --color-add-bg: #12261e;
      --color-add-intra: #1f5a35;
      --color-del-bg: #2d1517;
      --color-del-intra: #6e2226;
    }
  }
}
```

Theme follows the OS preference. A manual toggle would need JS or a cookie round trip, so none is offered. Diff rows keep text sigils, a left border, and underlined intra-line changes, so meaning survives forced colors (**I**).

### 4.4 Size budget and tests

- `app.css` ≤ 32 KiB raw; `chroma.css` ≤ 24 KiB raw; gzip total ≤ 14 KiB (**I**).
- `TestCSSLayered`: brace-depth scan; any top-level rule outside an `@layer` block fails.
- `TestTokenContrast`: parse `--color-*` pairs per theme. Text ≥ 4.5:1; borders and focus ≥ 3:1 (WCAG 1.4.3, 1.4.11).
- `TestAssetHash`: same bytes give the same URL; one changed byte changes it.
- `TestStaticHeaders`: header set in §4.2.

## 5. Security headers and CSP

On every HTML response:

```
Content-Security-Policy: default-src 'none'; style-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'
```

- `default-src 'none'` covers `script-src` (omitted, so it falls back), `connect-src` (no XHR, fetch, WebSocket, EventSource), fonts, media, `object`, `embed`, workers, and frames. **`<script>` cannot execute even if injected.** This is the zero-JS guarantee. The tests in §11 detect regressions.
- `style-src 'self'`: only hashed stylesheets. Without `'unsafe-inline'`, the browser also refuses `<style>` elements and `style=` attributes.
- `img-src 'self' data:`: same-origin raw and image routes, and identicons as `data:image/svg+xml`. An SVG loaded through `<img>` cannot run script. External markdown images are demoted (§6.2).
- `form-action 'self'`: forms post only to us. Chrome also applies it to redirects after a form POST (**P**); PRG redirects are same-origin.
- `frame-ancestors 'none'`: header only; `<meta>` cannot carry it.
- `base-uri 'none'`: blocks injected `<base>`.

Other headers on all responses: `X-Content-Type-Options: nosniff`, `Referrer-Policy: strict-origin-when-cross-origin`. On `/static/*` and raw responses: `Cross-Origin-Resource-Policy: same-origin` (**I**).

Raw responses (`/{o}/{r}/raw/{oid}/{path}`):

- png, gif, jpeg, webp: inline with detected type.
- Everything else, including SVG and HTML: `Content-Disposition: attachment` and `Content-Security-Policy: default-src 'none'; sandbox`.
- Unknown types default to `text/plain; charset=utf-8`.

`<meta http-equiv="refresh">` is not governed by CSP and remains allowed (nojs §4).

Every wrapper implements `Unwrap() http.ResponseWriter`. `http.ResponseController` (§10) finds the underlying writer through it.

## 6. Markdown

### 6.1 Pipeline

Pinned goldmark v1.8.6 (rukie `go.mod`). The existing pipeline already uses the transformer, renderer-override, table-alignment, and parser-option APIs (rukie `modules/markup/markdown/markdown.go:121–162, 169`). Those APIs are confirmed in the existing code. Shape:

```go
goldmark.New(
	goldmark.WithExtensions(
		extension.NewTable(extension.WithTableCellAlignMethod(extension.TableCellAlignAttribute)),
		extension.Strikethrough,
		extension.Linkify,
		extension.TaskList,
	),
	goldmark.WithParserOptions(
		parser.WithASTTransformers(
			util.Prioritized(&links{repo: repo, commit: commit, dir: dir}, 100),
			util.Prioritized(&headings{prefix: "md-"}, 90),
		),
	),
	goldmark.WithRendererOptions(
		renderer.WithNodeRenderers(util.Prioritized(&codeBlocks{}, 100)),
	),
)
```

Not enabled: `html.WithUnsafe()` (rukie currently sets it, `markdown.go:164`) and `parser.WithAttribute()` (currently set, `markdown.go:161`). Both are removed. Footnotes and math are cut: math needs client JS.

`extension.TableCellAlignAttribute` writes `align=`. `TableCellAlignStyle` writes `style=` and is forbidden (**V**: table code as cited in review; rukie already uses the attribute method).

### 6.2 Rules

1. **Raw HTML is killed by the renderer default.** goldmark emits `<!-- raw HTML omitted -->` when `Unsafe` is off (`html.go:389–398`, `656`). Neither `extension.GFM` nor the attribute filter does this. Research §5 notes GFM does not filter raw HTML tags.
2. **Attribute syntax is killed by not enabling it.** `WithAttribute` is off, so `{style=…}` in source is never parsed. goldmark's `GlobalAttributeFilter` admits `style` (`html.go:302`), so the filter is not the guard.
3. **Transformers and renderers set only enum-valued attributes**: `rel`, `id`, `class`, `align`. No source text is ever copied into an attribute name or value, and `style` is never set through node attributes.
4. **Destinations.** goldmark drops `javascript:`, `vbscript:`, `file:`, and non-image `data:` (`html.go:938–951`). The transformer adds an allowlist: `http`, `https`, `mailto`, relative paths, `#fragments`. Anything else renders as plain text.
5. **Relative links use pure path resolution.** The transformer joins the containing directory with the link path, cleans `.` and `..`, and rejects escapes above the root. It emits `/{o}/{r}/-/resolve/{commit-oid}/{cleaned}`. That route decides file versus directory at click time, returns 303 to `/blob/` or `/tree/`, or 404. **Render makes no Ruk calls for links.**
6. **Images.** Relative paths emit `/{o}/{r}/raw/{commit-oid}/{cleaned}`. `https:` images demote to a link with text `image: alt` and `rel="nofollow ugc noopener noreferrer"`, because `img-src 'self'` would block them and they leak client IPs. `data:image/{png,gif,jpeg,webp}` is kept.
7. **External links** get `rel="nofollow ugc noopener noreferrer"`. `rel` is in `LinkAttributeFilter` (`html.go:504`), rendered at `html.go:523` and `601`. Test `TestMarkdownRelRenders` asserts it appears in output. If it does not, the transformer's node attribute path is wrong and the test fails first.
8. **Heading IDs** are slugged, deduplicated, and prefixed `md-`, so they cannot clobber `#L12`, `#main`, or `#f-…`.
9. **Fenced code** uses the `highlight` package through the `codeBlocks` node renderer. Unknown languages render escaped plain. No second highlighter.
10. **Tables** wrap in `<div class="table-wrap" tabindex="0" aria-label="Scrollable table">`.
11. **Limits.** Input ≤ 512 KiB; larger renders as escaped plain text with a notice.
12. **Runtime gate, fail-closed.** `trusted.Markdown` runs `htmlcheck.Check` on goldmark's output before wrapping it in `templ.Raw`. On violation it emits escaped plain source, logs the violation, and increments a counter. Cost is in §12.
13. **Trust boundary.** `trusted.Markdown` is the only `templ.Raw` call for markdown.

### 6.3 Sanitizer decision: none in the render path

- The things a sanitizer removes (raw HTML, attribute injection, dangerous URLs) are removed at source (rules 1, 2, 4). `htmlcheck` verifies the result.
- rukie's current `modules/markup/sanitizer_default.go` is bluemonday's UGC policy plus `AllowStyles("color","background-color")` on `div`, `span`, `p`, `tr`, `th`, `td` (lines 19, 58), class regexes, SVG, MathML, and video. That policy is exactly the surface the zero-JS rule forbids. **It is deleted, not ported.**
- bluemonday's last release is v1.0.27 (2024-07-04) and last commit 2025-04-04 (research §5, V in research, not re-fetched). Removing it removes the second sanitizer and its `bluemonday/css` user.
- If raw HTML ever becomes an input, write a strict allowlist on the `htmlcheck` tokenizer (a few hundred lines) and make the `zerojs` assertion its acceptance test (**I**).
- Fuzz target `FuzzMarkdownZeroJS` (§11).

## 7. Code view

### 7.1 Routes

- Files: `GET /{o}/{r}/blob/{ref}/{path...}`
- Directories: `GET /{o}/{r}/tree/{ref}/{path...}`
- Windows: `?lines=A-B`. Highlight: `?hl=A-B` (nojs §1).

`ref` resolves once to a commit oid before any Ruk read: `RevisionRef.head` for oids, `bookmark.resolve` for branch names (semantics **U**). The response URL is reissued with the oid for permalinks (nojs §1).

### 7.2 Data path

1. `file{at: head(oid), path}` returns `FileBody`:
   - `blob{size, content}`: **`size` is known before reading.** If `size` > 8 MiB, render "too large" with a raw link and open no stream.
   - `directory`: tree page.
   - `symlink`: escaped target; never followed.
   - `gitlink`: "submodule at {oid}", no link.
   - `conflict`: conflict view with terms.
2. Content: `ByteSource.inline` (≤ 1 MiB) or `streamed`. Streamed content reads through `io.LimitReader(stream, cap+1)`. On cap, close the stream (**U**: confirm Cancel stops a read; otherwise stop granting credit).
3. Binary check on decoded bytes: a NUL in the first 8 KiB, or invalid UTF-8 (including the `{hex}` form), gives a binary notice with a raw link.
4. Lines: split on `\n`. Drop the final empty segment when the file ends in `\n`. Remove a trailing `\r` for display.

### 7.3 Caps

| Cap | Value | Past the cap |
|---|---|---|
| Blob size | 8 MiB | "Too large"; no stream opened |
| Default window | lines 1–1000 | Pager |
| Max window | 2,000 lines; larger is rejected, not clamped | 400 |
| Highlight prefix | 256 KiB from file start through the window's end (complete lines only) | Lines beyond are escaped plain |
| Line length | 8 KiB displayed | Prefix plus "(N more bytes, view raw)" |

The prefix cap is on bytes tokenized from file start, not window bytes. Reason in §7.4.

### 7.4 Highlighting and lexer state

- **Lexers are stateful.** A window that starts inside a block comment or string would be coloured wrong. So highlighting always tokenizes from byte 0 of the file, through the end of the requested window, capped at the prefix limit. Lines past the cap are plain. The result is correct for every line it colours.
- Lexer: `lexers.Match(path)`, else `lexers.Analyse(first 4 KiB)`, else `lexers.Fallback`.
- Tokenize: `lexer.Tokenise` over the prefix. `chroma.SplitTokensIntoLines` yields per-line tokens. Each line formats with `html.New(html.WithClasses(true), html.PreventSurroundingPre(true))` through `chroma.Literator` (**P**: confirm names in v2.27.0). Formatter output is chroma-escaped spans, passed to `trusted.Code(line)`.
- **Caching is not in v1.** Budgets are cold. A cache of per-blob tokens (key: blob oid, lexer, theme version) is added only if phase 0 measures cold p95 over budget. Its bound is an entry count (`golang-lru/v2` is count-based, so a per-entry size cap is part of the bound). No weak-pointer caches, because they free on GC, not on a limit.

### 7.5 Markup

```html
<div class="code-scroll" tabindex="0" role="region" aria-label="Source code">
<table class="code">
<caption class="visually-hidden">Source of path at 8-char oid, N lines, lexer</caption>
<tbody>
<tr id="L1" class="line"><td class="ln"><a href="#L1">1</a></td><td class="code">…</td></tr>
</tbody></table></div>
```

- `id="L{n}"` on each row. This keeps the existing scheme from `templates/repo/view_file.tmpl:113`, where the id sits on a span. The Gitea `rel="L{n}"` and `data-line-number` hooks were JS selection hooks and are dropped.
- **Only `#L{n}` scrolls.** Range fragments such as `#L10-L20` match no element id and do not scroll. The nojs §1 claim that they "still scroll" is false and must be corrected in that doc. Ranges use `?hl=A-B`, which adds `class="hl"` to rows in range.
- Two-click range selection, no JS: after `?hl=N`, each number links to `?hl=N-M#L{M}`.
- `scroll-margin-top: calc(var(--header-h) + var(--space-3))` on rows and headings (WCAG 2.4.11).
- `td.ln { user-select: none; position: sticky; left: 0 }`, so copying code does not copy numbers.
- `white-space: pre; tab-size: var(--tab-size)`. The scroll container is focusable and named (WCAG 2.1.1). WCAG 1.4.10 exempts code (**I**).
- `?plain=1` emits one `<pre>` for copying (nojs §9). `?raw=1` with `download` for raw.

### 7.6 Caching and ETags

HTML pages carry per-request shell state, so no HTML ETag is issued (§13). Raw bytes are the only ETag'd content (§8.9, §13).

## 8. Diff rendering

### 8.1 Source and side texts

- `diff{left, right, paths, page}`. `left` and `right` resolve **once** to commit oids before any read. Change and workspace refs resolve to heads; divergent heads fail `E_DIVERGENT` into an error page.
- Page default 100 files, max 1000 (V).
- Per file, for highlighting: read the old side with `file{at: left-oid, path: old_path ?? path}` and the new side with `file{at: right-oid, path}`. Both are pinned to the same oids as the diff, so no ref movement can interleave. Each side goes through §7.2 with the 128 KiB side cap. Sides over the cap render plain, with a notice.
- Row line numbers map to side line numbers. Each patch line's bytes must equal the side line's bytes. A mismatch renders that row plain and counts a metric. This catches stale or mismatched reads.
- Cost: up to two `file` reads per highlighted file. Batching is **U** (open item).

Per `FileDiff`:

- `body = "binary"`: row "Binary file changed". If `old.executable != new.executable`, show the mode change from the booleans.
- `kind = renamed`, empty patch: "Renamed from {old_path}".
- `kind = added` or `deleted`: "New file" or "Deleted file".
- `conflicted = true`: banner linking to the conflict view.
- `old` and `new` `DiffBlob.oid`: blob permalinks.
- `unified.patch`: inline or streamed.

### 8.2 Caps

| Cap | Value | Past the cap |
|---|---|---|
| Patch per file (displayed) | 1 MiB | Truncated at a row boundary, with a notice and raw-patch link (`/compare/{l}...{r}.diff?path=`, `text/plain` attachment) |
| Patch bytes per page | 16 MiB | Remaining files are listed by path, each with `?paths=` link |
| Rows per file | 20,000 | Truncated as above |
| Line length | 4 KiB displayed | Prefix plus notice |
| Tokenized side bytes per page | 256 KiB total, both sides | Remaining rows plain |
| Side size per file | 128 KiB each | Plain |

**One Ruk page per response.** Cap overflow lists remaining files with links; it does not fetch later cursors. Cursors are used only for "next page" links, which open new requests with new leases (§10.1).

### 8.3 Parsing, and what is deleted

`diff.ParseUnified` is line-based, about 200 lines, with no dependency. It reads `@@ -a[,b] +c[,d] @@ section`, body lines (`' '`, `'+'`, `'-'`), and `\ No newline at end of file`. It accepts optional `---`/`+++` headers. Whether Ruk emits them is **U**; a fixture decides.

**Existing code is deleted, not reused:**

- `services/gitdiff` parses `git` output and is coupled to Gitea's `DiffFile` model.
- `highlightdiff.go` runs `diffmatchpatch.DiffMain` over chroma HTML with private-use-rune placeholders. Its own comment says it "totally depends on Chroma's valid HTML output". It imports `sergi/go-diff` (V).
- Full-file highlighting in that code is gated by a **wall-clock** limit (`MaxFullFileHighlightTimeLimit`, 2 s) next to a 256 KiB size limit (V, in rukie gitdiff code; exact file **U**). Time-gated output depends on machine speed, which breaks reproducibility.

The rewrite keeps the ideas that were sound (size caps, placeholder-free tokens) and replaces the rest.

### 8.4 Unified layout

`<table class="diff unified">` with `<colgroup>`. Columns: old number, new number, sign, code.

[345 more lines in resource. Use :1207 to continue]
`<table class="diff unified">` with `<colgroup>`. Columns: old number, new number, sign, code.

- Hunk row: `<tr class="hunk"><th scope="rowgroup" colspan="4">@@ … @@ section</th></tr>`.
- Row classes: `ctx`, `add`, `del`, `meta`.
- Sign is text (`+`, `-`, space), with a visually-hidden word `added:` or `removed:` (nojs §2 a11y notes).

### 8.5 Split layout: server transform

Split is built **server-side from the same parsed hunk model** as unified. There is no browser-side rearrangement. Within each change block (a maximal run of `-` then `+` lines between context lines): pair the i-th `-` with the i-th `+`; leftover `-` rows are left-only; leftover `+` rows are right-only; context rows appear on both sides.

`<table class="diff split">` with `<thead>` `<th scope="col">Before</th><th scope="col">After</th>`, two columns of (line number, sign, code), `table-layout: fixed`.

Toggle: `?diff=unified` (default) and `?diff=split` as links, `aria-current="true"` on the active one (nojs §2).

### 8.6 Intra-line highlighting

Applies to paired rows only, under four caps:

| Cap | Value |
|---|---|
| Paired lines per page | 2,000 |
| Paired bytes per page | 512 KiB |
| Line size for a pair | ≤ 2 KiB and ≤ 512 tokens each |
| Token comparisons per page | 1,000,000 (counter, not time) |

Past any cap, the remaining pairs are coarse row highlighting only.

- Tokens: maximal runs of letters, digits, `_`; maximal whitespace runs; single other code points (UTF-8 aware).
- Myers O(ND) on tokens. Max edit distance D = 64; beyond, the pair is row-level.
- Output: each changed token run is `<ins>` (new side) or `<del>` (old side). Each run is emitted as a self-contained `<ins>…</ins>` around chroma spans for that token slice, so nesting is well-formed by construction. Chroma spans come from the side tokenization in §8.1.
- Visibility: background tint plus underline, so the change is not colour-only.

Counter-based caps make output deterministic. Time-based caps do not.

### 8.7 File sections

- `<section class="file" id="f-{h8}" aria-labelledby="fh-{h8}"><details open>`. `h8` is the first 8 hex of `sha256(path)`, so anchors are stable.
- `<summary>` holds `<h2 id="fh-{h8}">path</h2>`, kind badge, and text counts (`+12 −3`).
- Open by default for the first 20 files per page, closed beyond. Server-side and deterministic (**I**).
- `hidden="until-found"` on collapsed bodies is optional (nojs §1 "Optional"; bonus only).

### 8.8 Streaming

Diff pages stream per file (§10). Each file's patch streams only when that file renders, so back-pressure comes from the Ruk credit window.

### 8.9 Caching

Diff HTML carries no ETag (§13). Deterministic output means a cache could be added later, keyed by `(left oid, right oid, path, mode, renderer version)`, if phase 0 requires it.

## 9. Blame view

### 9.1 Route

`GET /{o}/{r}/blame/{ref}/{path...}?lines=A-B`, default lines 1–1000. This replaces nojs §1's `?blame=1` column (deviation, §15). A separate URL keeps the file page cheap and makes blame linkable.

### 9.2 Data path: one revision, one view

1. Resolve `ref` **once** to a commit oid (same rule as §7.1).
2. `blame{at: head(oid), path, lines: window, page}` and `file{at: head(oid), path}`. Both reads use the same oid, so a moving ref cannot split them.
3. Consistency check: compare `ViewInfo.view` from both replies (both carry it, V). If they differ, re-issue both once. If they still differ, return an error page ("repository changed while reading; retry"). **Fail closed, no banner**, because a joined view would pair lines with the wrong authors.
4. Pages of blame hunks until hunks cover the window or the cursor ends. Cap: 20 pages, then error.
5. Join: a hunk covers `[start_line, start_line + line_count − 1]`. The one-based base is documented for `LineRange` (`/tmp/ruk-model.json` `$defs.LineRange`, V). `BlameHunk.start_line` has no documented base (**U**). The implementation assumes one-based and a fixture verifies against `git blame --line-porcelain` before merge. Rows clamp to the window.

### 9.3 Markup

- Each hunk is `<tbody class="hunk">`.
- The first row carries `<td class="blame" rowspan="N">`:
  - Head: 8-character oid, or "conflicted" for `Head.type = conflicted`.
  - Link: `/{o}/{r}/changes/{id}` when `change` is present, else `/{o}/{r}/commit/{oid}`.
  - Author: name only, decoded from `ByteString` (hex escaped).
  - `<time datetime="…">` in UTC from `Signature.timestamp` and `offset`.
- Other rows in the hunk have no blame cell.
- Alternating shading: `tbody.hunk:nth-child(even)`.
- **`Signature.email` is never rendered.** Identity is name plus identicon (`data:image/svg+xml`, generated in `vm`).

### 9.4 Budget

Window ≤ 1,000 lines with ≤ 150 hunks (§12.1, F5).

## 10. Streaming HTML

### 10.1 Scope and the one-page rule

Finite, bounded streaming only, for: diff, change and branch lists over 100 rows, tree listings over 300 entries, and search results.

**Each streamed response renders exactly one Ruk page.** It never fetches a later cursor mid-response, because a lease can expire mid-stream (V: `PageCursor` says "a cursor stops working when its lease expires") and the 200 status is already sent. Next-page links are separate requests with fresh leases.

nojs §4 rejects streaming for **live** tails. Finite streams bounded by page caps are a different case, and this design is consistent with nojs §4.

### 10.2 Mechanism

1. Authorization, the single Ruk page, and shell data resolve before headers. Failures there are real status codes.
2. The response writer is wrapped explicitly:

```go
func newStream(w http.ResponseWriter, r *http.Request) *stream {
	rc := http.NewResponseController(w)
	return &stream{ctx: r.Context(), rc: rc, bw: bufio.NewWriterSize(w, 16<<10)}
}

func (s *stream) Boundary() error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if err := s.rc.SetWriteDeadline(time.Now().Add(stallTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if err := s.bw.Flush(); err != nil {
		return err
	}
	if err := s.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

func (s *stream) Write(p []byte) (int, error) { return s.bw.Write(p) }
```

Order inside `Boundary`: **the write deadline is set before any buffered bytes are written**, so the stall timeout covers the network write. `stallTimeout` is 30 s (**I**).

3. **Unsupported writer:** `ErrNotSupported` from `Flush` or `SetWriteDeadline` means the response still completes, but unflushed. A metric records it. Streaming tests run against a real `net/http` server, where flush is supported.
4. **Flush errors:** any non-`ErrNotSupported` error from `Flush`, `SetWriteDeadline`, or a buffered write means the client is gone. Stop, close streams, return.
5. **Headers before the first flush:** `Content-Type`, `Cache-Control: private, no-cache`, security headers, `X-Accel-Buffering: no`, and no `Content-Length` are set before the first `Write`. The first flush happens after `<head>`, so headers and head go out together.
6. **Templates** call `negotiate.Boundary()` as a templ component. Its `Render` type-asserts the writer for `Boundary() error`. It is a no-op for other writers. `templ.Flush()` and `templ.WithStreaming()` are not used.
7. **Cancellation cleanup:** the loop checks `ctx.Err()` between files. On cancel it closes the open Ruk stream readers (Cancel, or stop granting credit, per §7.2), writes nothing further, and returns without an error notice. A deferred close guarantees cleanup on every path.
8. **Errors after commit:** `<div role="alert">Stopped after N files: {code}</div>`, then `</main></body></html>`. The status stays 200. This trade-off is logged with the request id.
9. **Server:** `ReadHeaderTimeout` set; `WriteTimeout` unset for these routes, because `SetWriteDeadline` is the per-flush control (**I**).
10. No in-app gzip for HTML, because it would buffer flushes. Compression belongs at the proxy with flush passthrough (**I**).

### 10.3 Tests

- `TestStreamFirstFlush`: real server; fake Ruk stream blocks after file 1. The client must read `</head>` and file 1's `</section>` before the block releases. Timing via `httptrace`.
- `TestStreamCancelClosesStreams`: client disconnects mid-stream. The fake records Close. No writes after disconnect.
- `TestStreamErrorAfterCommit`: stream error after file 1 yields the alert and a closed document.
- `TestStreamNoCursorFetch`: the fake records calls; a streamed response makes exactly one page call.

## 11. Zero-JS enforcement and htmlcheck

### 11.1 htmlcheck: one implementation, two uses

`internal/web/htmlcheck` (runtime package) exports `Check(src []byte) error`. It tokenizes with `golang.org/x/net/html` and inspects **tag names and attribute keys only**. It does not do byte-substring matching on the document. Escaped text such as `&lt;script` is text, not a tag, and `onload=` inside text is not an attribute.

```go
func Check(src []byte) error {
	z := html.NewTokenizer(bytes.NewReader(src))
	for {
		switch z.Next() {
		case html.ErrorToken:
			if err := z.Err(); err != io.EOF {
				return fmt.Errorf("htmlcheck: %w", err)
			}
			return nil
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			if err := checkTag(string(name), z, hasAttr); err != nil {
				return err
			}
		}
	}
}
```

`checkTag` loops `z.TagAttr()` (keys are lower-cased by the tokenizer) and fails on:

- tag `script`;
- any attribute key beginning with `on` followed by at least one letter;
- attribute `style`;
- URL-bearing attributes (`href`, `src`, `action`, `formaction`, `poster`, `data`, `srcset`, `xlink:href`) whose value, after lowercasing and removing ASCII whitespace and control characters, begins with `javascript:` or `vbscript:`;
- any `data:` URL except `img[src]` with `data:image/png`, `gif`, `jpeg`, `webp`, or `svg+xml`. The `svg+xml` allowance is for identicons, which cannot run script through `<img>`.

Uses:

- **Runtime:** `trusted.Markdown` runs it on goldmark output and fails closed (§6.2 rule 12).
- **Tests:** `zerojs.Assert(tb, body)` calls `Check` and reports the error.

`golang.org/x/net v0.60.0` is already pinned (V). It moves from test-only to runtime.

### 11.2 Layers

1. **Source lint, `TestTemplSourcesZeroJS`.** `.templ` sources and non-generated Go. Static markup only. Fails on `<script`, `style=`, `style={`, `on[a-z]+=` in attribute position, `templ.JS`, `templ.Once`, and `javascript:` literals. This runs on source, not on rendered output, so it can never see repository content.
2. **Confinement, `TestRawConfined`.** `go/parser` and `go/ast` walk non-generated Go. `templ.Raw` and `templ.Safe*` selectors appear only under `internal/web/trusted`.
3. **Runtime assertion,** via `htmlcheck`, on every HTML response in tests.
4. **Self-tests, `TestCheckRejects` and `TestCheckAccepts`.**
   - Must fail: `<script>`, `<a onclick="x">`, `<div style="color:red">`, `<a href="javascript:alert(1)">`, `<img src=x onerror=…>`, and `<img src="data:text/html,…">`.
   - Must pass: `<pre>&lt;script&gt;onclick=style=</pre>`, and `<img src="data:image/svg+xml;base64,…">`.
5. **Route coverage, `TestAllHTMLRoutesZeroJS`.** Every registered HTML route is rendered with fixtures and checked with `Assert` and the CSP header check.
6. **Fuzzing.** `FuzzMarkdownZeroJS` (random bytes through the markdown pipeline and `trusted.Markdown`), `FuzzHighlightZeroJS` (random source and filename through code view), `FuzzDiffZeroJS` (random unified patch through diff view). Seed corpus from `testdata`. CI runs `-fuzztime=30s`; nightly runs longer.

Layers 3–6 are regression detectors. The CSP header is the runtime guarantee.

## 12. Performance budgets and measurement

### 12.1 Workloads and budgets

All budgets are **cold**: no cache, process warm (§7.4). "Render p95" is handler time from in-memory typed replies to the last byte written. It **excludes Ruk** (fixtures replay recorded replies with zero latency). First-byte budgets for streaming pages are gated separately, and include the first file's render but not Ruk.

| ID | Route and fixture | Render p95 budget | First flush p95 (streaming only) |
|---|---|---|---|
| F1 | Tree, 300 entries | 3 ms | n/a |
| F2 | Repo home, 64 KiB README (40 headings, 20 links, 8 KiB of fenced Go) | 25 ms | n/a |
| F3 | Blob, 1,000-line Go file (~40 KB), lines 1–1000 | 25 ms | n/a |
| F4 | Blob, 10,000-line Go file, window ending at the 256 KiB cap | 100 ms | n/a |
| F5 | Blame, 1,000 lines, 150 hunks, file as F3 | 35 ms | n/a |
| F6 | Diff, 25 files, 120 KB patch, unified, sides totalling ≤ 256 KiB | 120 ms | 40 ms |
| F7 | Diff, 25 files, 300 KB patch, unified | 130 ms | 40 ms |
| F8 | Diff, 100 files, 2 MB patch, unified (single page, 16 MiB cap) | 260 ms | 60 ms |
| F9 | Change list, 100 rows | 5 ms | n/a |
| F10 | `/static/app.<h>.css`, in-memory | 0.2 ms | n/a |
| F11 | Raw blob, conditional request, after authorization | 0.2 ms | n/a |

Derivations (**I**; the 3 MB/s tokenize rate is a phase-0 calibration target, not a measurement):

- F3: 40 KB ÷ 3 MB/s ≈ 13 ms, plus ~5 ms emit, plus ~2 ms misc ≈ 20 ms; budget 25 ms.
- F4: 256 KiB ÷ 3 MB/s ≈ 85 ms, plus ~10 ms emit ≈ 95 ms; budget 100 ms.
- F6: 85 ms tokenize, plus ~3 ms parse, plus ~10 ms emit, plus ≤ 10 ms intra ≈ 108 ms; budget 120 ms.
- F7: 85 ms tokenize, plus ~6 ms parse, plus ~15 ms emit, plus ≤ 10 ms intra ≈ 116 ms; budget 130 ms.
- F8: 85 ms tokenize, plus ~40 ms parse at 50 MB/s, plus ~40 ms emit, plus ≤ 10 ms intra ≈ 175 ms; budget 260 ms.
- Tokenized side bytes are capped at 256 KiB per page, so the tokenize term is bounded for every diff size.

**Discipline:** if a measured p95 exceeds its budget, lower the cap that drives it. Do not raise the budget without recording why. Caps are the tuning knobs.

### 12.2 Measurement harness

1. **Fixtures.** `internal/web/testdata/perf/`: Ruk replies recorded once from a real `ruk` daemon run against a fixture repo (JSON plus patch bytes). An in-process fake implements the typed Ruk client and replays them with zero latency.
2. **Latency samples.** `TestPerfBudgets`, behind `//go:build perf`. Per route: 50 warm-up requests, then N = 500 measured, sequential (concurrency 1).
3. **Timer.** `time.Now()` before `handler.ServeHTTP` and after it returns. For streaming pages, a recording writer wrapper also records the time of the first `Boundary()` flush. First-flush and complete times are reported and gated separately.
4. **Percentile.** Sort the N durations ascending. p95 = `d[ceil(0.95·N) − 1]` (nearest rank, zero-based index). p50 and p99 are reported for context. Gate on p95 only.
5. **Pass/fail.** Each F-row's p95 must be ≤ its budget on the reference runner (4 vCPU, one fixed class, CPU model recorded). Failure fails the perf job. Shared runners are noisy, so the perf job runs on the reference runner, not on every PR.
6. **Concurrency.** A separate throughput run at concurrency 8 reports requests per second and p95. It is informational, not gated, until the reference runner is measured.
7. **Micro regressions (PR gate).** `BenchmarkRender*` with `b.Loop()` (Go 1.24+, V) and `-benchmem`, `-count=10 | benchstat`. A mean regression above 10% with p < 0.05 blocks merge. This gates change, not absolute speed; `go test -bench` gives means only, so it never gates p95.
8. **Profile attribution.** `go test -bench=… -cpuprofile` and `go tool pprof -top`. Buckets: chroma, templ emit, goldmark, `htmlcheck`, diff parse, intra-line. Investigate when templ emit exceeds 30% of blob CPU (**I**).
9. **End to end (reported, not gated).** `oha -z 30s -c 8` against the daemon with real Ruk. Results go in the PR description. First-byte including Ruk is reported here.
10. **Phase-0 spike.** The templ-vs-html/template pair (§2) runs on F3 and F6 fixtures.

### 12.3 Caches

No cache in v1. Caches are added only when phase 0 shows a cold budget fails. Any cache then gets its own warm budget, its bound (entry count plus per-entry size cap), and a measured miss cost. `hashicorp/golang-lru/v2 v2.0.7` is available (V) and is count-based. No weak-pointer caches.

## 13. Header and cache matrix

| Response | Cache-Control | ETag | Vary | Notes |
|---|---|---|---|---|
| Hashed static assets | `public, max-age=31536000, immutable` | content hash | `Accept-Encoding` | §4.2 |
| HTML pages (all) | `private, no-cache` | none | `Accept, Cookie` | Shell holds CSRF, principal, flash: no 304 can be correct |
| Raw bytes `/raw/{oid}/…` | `private, no-cache` | `"raw-{oid}"` | `Accept` | Blob oid is content-addressed; authorize before 304 |
| Raw patch `/compare/…diff` | `private, no-cache` | none | `Accept` | Ref-dependent |
| POST responses | `no-store` | none | `Cookie` | 303 to GET |
| Error pages | `no-store` | none | `Accept, Cookie` | |

Security headers (§5) go on every response. `Vary: Accept` is set on negotiated routes.

## 14. Migration: legacy inventory and commit order

### 14.1 Inventory

Top-level entries of `templates/` (V from a directory read in this pass). Child listings are truncated by the tool, so an exact `.tmpl` count needs `find templates -name '*.tmpl' | wc -l` at phase 0. That number is a checklist input, not a claim.

| Family | Observed contents (abridged) | Decision |
|---|---|---|
| `repo/` | Large: `view_file`, `view_content`, `view_list`, branch, tag, wiki subtree, upload, unicode-escape, file tree | **Replace** (§7–9, §8). Wiki: cut unless settled |
| `shared/` | `issuelist`, search, secrets, variables, webhook, user, repo, icons | **Replace.** Issue views become tasks (settled). Secrets and variables become CI settings (decision) |
| `user/` | settings, overview, profile, dashboard, auth, notifications, heatmap | **Replace** |
| `org/` | home, settings, team, member, projects, worktime, menu, create | **Replace** for orgs and teams (settled). Worktime and projects: **cut** unless settled (decision) |
| `explore/` | repos, users, code, search, navbar | **Replace** |
| `admin/` | system status, queues, runners, users, stacktrace, actions | **Replace** a minimal operator subset (decision) |
| `webhook/` | new | **Replace.** Signed webhooks (settled) |
| `projects/` | list, view, new | **Cut** unless tasks need boards (decision) |
| `package/`, `api/packages/` | packages registry | **Cut.** Not in settled features |
| `mail/` | repo, user, org, base | **Cut.** Settled: no email |
| `swagger/` | ~1 MB generated JSON plus viewer | **Cut.** Huma serves OpenAPI |
| `devtest/` | UI test pages | **Cut** |
| `custom/` | empty hook files (0 B) | **Cut** |
| `install.tmpl`, `post-install.tmpl` | installer | **Cut or replace** with config file (decision) |
| `status/` | 404, 500, 503 | **Replace** (§6 errors) |
| `base/` | head, navbar, footer, paginate, modal pieces | **Replace** with `shell` and `ui` |
| `home.tmpl` | home | **Replace** |

Every "Replace" maps to a `view/` family. Every "Cut" is deleted in the commit that lands the replacement for its neighbours, so nothing is left behind.

### 14.2 Commit order (every commit builds; clean cutover)

- **C0 (phase 0, no routes):** templ vs html/template spike (F3, F6). Lexer throughput calibration (§12.1). Exact `.tmpl` count. Ruk fixture recording (§12.2 step 1).
- **C1:** `htmlcheck` with self-tests; `trusted` with AST confinement test; `secheaders` with the exact CSP on the new router only; Huma v2.39.1 added (settled); `goldmark` v1.8.6 reconfigured in the new `markdown/` package (no unsafe, no attribute syntax).
- **C2:** `assets`, `asset`, `app.css` (tokens and base), `shell`, `/static/*`.
- **C3 onward, one family per commit:** add `view`, `vm`, and tests. Delete the legacy templates and handlers for that family in the same commit. Suggested order: blob and tree (exercises highlight and shell); repo README (markdown); commit log; diff unified then split; blame; changes and branches; tasks; orgs and teams; explore; admin; status pages.
- **Deletions that must land with their replacements:** `modules/markup/sanitizer_default.go` and `modules/markup/markdown/transform_codespan.go` (bluemonday and `css.HexRGB`, line 12); `services/gitdiff/highlightdiff.go` and `services/gitdiff` parser; the `goldmark-highlighting` extension wiring; `chi` routes at their families.
- **Final commit:** delete `web_src`, Vite, TypeScript, Fomantic, `templates/base/head_script.tmpl` (named in nojs §… gap list; existence **V** in directory read: `base/head_script.tmpl`, 793 B). Enable `TestAllHTMLRoutesZeroJS` across every route. Delete the `templates/` tree.

No route is ever served under the CSP while legacy script still ships, because a family moves from the legacy handler to the new router in the commit that deletes its templates.

## 15. Dependencies, deviations, risks, open items

### 15.1 Dependencies (rukie `go.mod`, V where pinned)

| Module | Current pin | Rendering design | Action |
|---|---|---|---|
| `github.com/yuin/goldmark` | v1.8.6 | Kept, reconfigured (no unsafe, no attribute syntax) | Reuse |
| `github.com/yuin/goldmark-highlighting/v2` | pinned | Replaced by `highlight` package | Drop after `markdown.go` rewrite |
| `github.com/alecthomas/chroma/v2` | v2.27.0 | Kept, class mode | Reuse |
| `github.com/microcosm-cc/bluemonday` | v1.0.27 | Removed (sanitizer and `codespan` users deleted) | Drop |
| `github.com/sergi/go-diff` | v1.4.0 | Removed (`highlightdiff.go` deleted) | Drop |
| `golang.org/x/net` | v0.60.0 | Kept; now runtime (`htmlcheck`) | Reuse |
| `github.com/hashicorp/golang-lru/v2` | v2.0.7 | Unused in v1 (§12.3) | Keep only if a cache is added |
| `github.com/go-chi/chi/v5` | v5.3.2 | Replaced by ServeMux at cutover | Drop at final commit |
| `github.com/danielgtaylor/huma/v2` | absent | Settled: v2.39.1 | Add in C1 |
| `github.com/a-h/templ` | absent | v0.3.x via `tool` directive | Add in C0 gate, C1 |
| `github.com/PuerkitoBio/goquery` | v1.13.0 | Not used by this design | Out of scope |

### 15.2 Deviations from planning docs

1. nojs §1 `?blame=1` column becomes `/blame/…` (§9.1).
2. nojs §1 says a pasted `#L10-L20` "still scrolls". **False.** Only `#L{n}` scrolls. The nojs doc needs correcting.
3. nojs §4 rejects streaming for live tails. This design allows finite, single-page streaming (§10.1).
4. research §5 picked goldmark v2.1.6. This design keeps the pinned v1.8.6. Reason: no v2-only API is needed, v2 is young (research §5, V), and the pin already exists. Revisit only if a required API is missing.
5. research §5 kept bluemonday as an option. This design drops it (§6.3).
6. Huma is a settled addition, not an existing dependency (§3.5). The review's "rewrite on chi" alternative contradicts settled decisions and is not adopted.

### 15.3 Risks

- **templ pre-1.0 churn.** Mitigation: pin; phase-0 gate; generated output committed.
- **Lexer throughput.** The 3 MB/s figure is a target. Mitigation: calibrate in C0; lower the caps, not the budgets, if measured throughput is lower.
- **Ruk stream and Cancel semantics.** The Go client must implement the daemon's frame and credit protocol (`daemon/mod.rs`, V for frame handling). Whether Cancel stops a read's outbound stream is **U**.
- **Lease expiry.** Handled by the one-page rule (§10.1). The design does not depend on lease lifetime after the first reply. Whether a stream read survives its page's lease is **U**; if not, the in-document error path (§10.2 step 8) handles it.
- **Blame base.** `BlameHunk.start_line` one-based? **U.** Fixture decides (§9.2).
- **Diff patch headers.** `---`/`+++` present? **U.** Parser accepts both.
- **Two side reads per highlighted diff file.** Batching **U**. Cost is inside F6–F8 budgets only if Ruk round trips are small; they are excluded from render budgets and reported end-to-end.
- **goldmark v1.8.6 API names.** The transformer and renderer-override APIs are used in rukie today (V). `SetAttributeString` and `extension.GFM` were verified on master, not the v1.8.6 tag. Confirm in C1.
- **chroma names.** `Literator`, `Coalesce`, `SplitTokensIntoLines` (V for the last), and whether the formatter escapes text (**P**). Confirm in C0.
- **Baseline.** `@layer`, `:target`, `<details>`, `scroll-margin` are assumed Baseline (**I**; MDN check pending).
- **Chrome form-action redirects** (**P**).

### 15.4 Open items for implementers

- Ruk `Page7` ordering for directory listings (**U**).
- `bookmark.resolve` semantics for branch-to-oid (**U**).
- Whether the Huma form decoder covers every POST form in §3.5 (research-agent-api-codegen; **not re-checked here**).
- Whether a `file` read at a commit oid returns identical content across operation views (should; **U**). It is the basis of §9.2 step 2.

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/rukie/go.mod` — drop `bluemonday v1.0.27`, `sergi/go-diff v1.4.0`, `goldmark-highlighting/v2`, and `go-chi/chi/v5` at cutover; keep `goldmark v1.8.6`, `chroma/v2 v2.27.0`, and `golang.org/x/net v0.60.0`; add Huma v2.39.1 and templ as tool.
- `/home/alpha/rewrite/git-renewed/rukie/modules/markup/markdown/markdown.go` — current pipeline with `WithUnsafe` (line 164) and `WithAttribute` (line 161). The reconfiguration in §6.1 starts here.
- `/home/alpha/rewrite/git-renewed/rukie/modules/markup/sanitizer_default.go` — bluemonday policy with `AllowStyles` (line 58). Deleted, not ported.
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/protocol.rs` — `LineRange`, `BlameHunk.start_line` base (the §9.2 open question), `ByteSource::Streamed`, `StreamId`.
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/mod.rs` — stream frames, credit windows, and `FrameKind::Cancel` semantics the Go client must honour (§7.2, §10.2).