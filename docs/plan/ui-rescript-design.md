# ui-rescript-design.md

Canonical source for the web application architecture (O41). This file owns the UI stack contract. Where `rendering-design.md` describes the zero-JS design, this file wins on stack and layering; `rendering-design.md` still owns budgets, CSP base, token plumbing, and the fallback render pipeline.

**Labels:** **V** = verified at release channel or official docs on 2026-10-21; **P** = probable; **U** = unverified; **I** = design choice.

Anchor: Christopher Alexander, Dieter Rams

## 1. Shape

Two renderers, one contract each.

```
rukie (one Go binary)
 ├─ Go + templ          → baseline HTML for every route (the no-JS fallback)
 ├─ go:embed app.*.js   → ReScript+Preact bundle, served at /-/app/app.<hash>.js
 └─ op table            → JSON ops; the app calls them exactly like the CLI does
```

The Go renderer is the fallback and the document: every route emits complete HTML, every mutation is a `<form action>` POST, and the page works end to end with JavaScript disabled. When the browser runs the bundle, Preact hydrates interactive surfaces in place. There is no second API: the app consumes `/-/api` JSON ops, the same contract the MCP server and the CLI use (Parnas: one boundary hides the change). An htmx-style fragment API is rejected for the same reason: it adds a second response contract beside the op table (I).

## 2. Language and component model

All application source is ReScript (`.res`, interfaces `.resi`). No TypeScript, no hand-written JavaScript, no React, no `@rescript/react`, no Next.js (user decision 2026-10-21). The only `.js` in the repo is toolchain configuration that ReScript cannot express; `.res.js` compiler output is a build artifact, never committed.

Preact 11.0.1 is the component model through the ReScript generic JSX transform (V: rescript-lang.org manual, "Generic JSX transform", since 11.1):

- `web/rescript.json`: `"jsx": { "version": 4, "module": "Preact" }`.
- `web/src/Preact.res`: the transform interface, `@module("preact/jsx-runtime")` externals for `jsx`, `jsxs`, the keyed variants, `jsxFragment`, and `Elements` with `type props = JsxDOM.domProps`. `Preact.string|int|float|array` lift values to `Jsx.element`.
- `web/src/PreactHooks.res`: externals to `preact/hooks` (`useState`, `useEffect`, `useMemo`, `useRef`, `useCallback`) typed per call site.
- `@jsx.component` / `@react.component` both work under the generic transform (V).

Component discipline (type-driven): domain state is a variant, transitions are exhaustive `switch`es with no wildcard arm; `option` at every boundary; fetch results decode through a parse function returning `Result`, never blind `Js.Json` walks.

## 3. Build

- Node 24 LTS is a build prerequisite only; production runs no Node.
- `web/package.json` pins: `rescript` 12.3.1, `preact` 11.0.1, `esbuild` 0.28.2 (npm dist-tags 2026-10-21, V). Lockfile committed; `npm ci` in CI.
- Pipeline: `rescript build` → `.res.js` → `esbuild --bundle --format=esm --minify` → `internal/web/assets/app.<content-hash>.js`, embedded via `go:embed` and served immutable.
- `make build-web` runs `npm ci`, `rescript build`, and the esbuild step; `make check` depends on it. `rukie dev` shells the same target so dev and prod ship the identical bundle path.
- Sourcemap off in the shipped bundle.

## 4. Hydration and the fallback contract

- The fallback page is the same DOM tree the app hydrates. A page section that the app owns carries `data-app="<Module>"`; the bundle maps the attribute to the ReScript module. Hydration replaces the marked subtree only; the rest of the document is static.
- Data flows through the op table. On hydrate, embedded `data-props` JSON seeds the first render; after that, the app calls ops. Props embedded in the document must round-trip through the same decoders the app uses.
- Fallback gates (replace `TestAllHTMLRoutesZeroJS`): `TestAllHTMLRoutesRenderWithoutJS` asserts every HTML route returns a complete rendered document; `TestNoJSFallbackMutationsPost` drives each mutation form with a plain POST and asserts the effect lands. Both run against the Go renderer only, no JS engine.
- `TestEmbeddedAppBundle` asserts the embedded asset exists, is served with the content-hash URL, and carries the immutable cache headers of `internal/web/assets`.
- `TestNoJSAppSource` walks `web/src` and fails on any `.ts`, `.tsx`, `.js`, `.mjs`, or `.jsx` file (ReScript-only, SC-03).
- CSP: `script-src 'self'` admits only the embedded same-origin bundle; `trusted`/`htmlcheck` keep guarding untrusted content (markdown, highlight, user text); they reject `script`, `on*`, `javascript:`, `vbscript:`, and non-allowlisted `data:` URLs inside rendered content. The CodeQL `data:` scheme gap at `internal/web/htmlcheck/check.go` is fixed in the same change.

## 5. Surfaces

Interactive-now (bundle owned): the ops console `site.home` (live inbox count, review queue refresh), the view composer at `/{o}/{r}/views/{id}`, the land-parade form, the change stack reorder, and live CI log tailing. Everything else reads as fallback HTML; the app attaches only where `data-app` is marked.

Per-step `UI:` rows in the plan stay the route inventory; the bundle takes over the marked subtrees as it lands. A step that ships a route with no `data-app` ships fallback-only and is done.

## 6. Non-goals

- No SSR of ReScript components. The fallback already renders; SSR would duplicate the DOM contract across two runtimes.
- No client-side router. Routes are server routes; navigation is ordinary links. A soft-navigation layer is deferred until measured HTML latency fails O30.
- No global state library. Component-local `useState` plus op calls; cross-component refresh uses DOM events on `data-app` boundaries.
- No htmx, Alpine, or other JS runtime: user decision 2026-10-21 ("ReScript instead of js/ts") plus the op-table boundary rule in §1.
