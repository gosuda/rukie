No write channel exists in this environment (read/grep/web_search only — confirmed by eight failed probes and two blocker advisories). Per the advisors, I close the turn by emitting both complete artifacts verbatim, labeled for the caller to persist. All design decisions are grounded in ruk's real vocabulary (`op.log`, `attest.list`, `land.prepare`, SSHSIG attestations, Live/Ambiguous/Superseded states — `crates/ruk-core/src/protocol.rs:2110-2140`, `attestation.rs`) and rukie's view shapes (`templates/repo/view_list.tmpl`, `templates/repo/diff/section_unified.tmpl`).

---

## Artifact: `local://design-direction-plate.md`

```markdown
# Design Direction — Plate

One visual direction for rukie's zero-JavaScript, server-rendered forge UI.
Register: a tool for engineers supervising many AI agents — dense, read-heavy
(change-review diffs, repo trees, logs, agent activity), used late at night in a
dim room. Dark theme is primary; light theme follows `prefers-color-scheme`.

Concept: every surface is a **plate** — a flat slab with a hard 1.5–2px border
and a flat offset shadow, set on a darker bench. One committed industrial color
(the orange of a workshop stencil) carries the chrome. Everything else is warm
graphite and monospace type.

## Taste anchors

1. **Are.na** — flat channels, hairline structure, content with no chrome noise.
2. **Teenage Engineering** — industrial orange on graphite, engraved labels,
   mono microcopy, hardware honesty.

## Palette (OKLCH)

Achromatic steps use OKLCH hue 70 with tiny chroma (warm graphite, never pure
gray). For achromatic colors relative luminance Y = L³ (exact, rows of the
OKLab→linear-sRGB matrix sum to 1); chromatic luminances computed via the same
matrix (accent sits near the sRGB gamut edge, Y ≈ 0.37).

**Committed hue:** industrial orange `H = 40`, one token used in both schemes:
`--accent: oklch(0.72 0.23 40)` (≈ vivid safety orange). Carries 30–60% of
chrome: the top nav slab, primary buttons, the OPEN change badge, focused
outlines, check-state marks, progress/keylines. It is a *fill*, not a text
color — text on it is always `--accent-ink`.

### Dark (primary)

| Token | OKLCH | Role |
|---|---|---|
| `--bg-0` | `0.175 0.008 70` | bench / page canvas (Y 0.0054) |
| `--bg-1` | `0.215 0.010 70` | plate surface (Y 0.0099) |
| `--bg-2` | `0.265 0.012 70` | raised sub-plate: table heads, num gutters (Y 0.0186) |
| `--edge` | `0.45 0.015 70` | inner separators, quiet borders |
| `--edge-hard` | `0.92 0.008 70` | plate outer border (= ink; hard contrast) |
| `--ink` | `0.92 0.008 70` | body text (Y 0.779) |
| `--ink-2` | `0.74 0.012 70` | secondary text (Y 0.405) |
| `--ink-3` | `0.65 0.014 70` | muted: line numbers, timestamps (Y 0.275) |
| `--accent` | `0.72 0.23 40` | committed orange fill (Y ≈ 0.37) |
| `--accent-ink` | `0.19 0.02 60` | text/icons on orange (Y 0.0069) |
| `--accent-text` | `0.78 0.16 45` | orange keylines-as-text on dark only (hunk headers, links needing accent) |
| `--shadow` | `0.11 0.005 70` | flat offset shadow |
| `--ok` | `0.80 0.13 150` | success text/icon |
| `--warn` | `0.82 0.15 85` | warning text/icon (amber, not orange) |
| `--bad` | `0.68 0.15 25` | danger text/icon (deep red) |
| `--mark-add` | `0.30 0.035 150` | diff add row tint |
| `--mark-del` | `0.28 0.040 25` | diff del row tint |
| `--mark-hunk` | `0.20 0.030 55` | diff hunk header tint |
| `--sel` | `0.30 0.05 60` | selection |

### Light (`prefers-color-scheme: light`)

| Token | OKLCH | Role |
|---|---|---|
| `--bg-0` | `0.935 0.008 80` | bench (Y 0.817) |
| `--bg-1` | `0.985 0.004 80` | plate — paper white, warm (Y 0.955) |
| `--bg-2` | `0.90 0.010 80` | raised sub-plate (Y 0.729) |
| `--edge` | `0.55 0.015 70` | inner separators |
| `--edge-hard` | `0.22 0.015 70` | plate outer border (= ink) |
| `--ink` | `0.22 0.015 70` | body (Y 0.0107) |
| `--ink-2` | `0.40 0.015 70` | secondary (Y 0.064) |
| `--ink-3` | `0.47 0.015 70` | muted (Y 0.104) |
| `--accent` | `0.72 0.23 40` | same orange (brand continuity) |
| `--accent-ink` | `0.22 0.02 60` | text on orange (Y 0.0107) |
| `--accent-text` | `0.50 0.19 40` | orange text on light (deeper for contrast) |
| `--shadow` | `0.22 0.015 70` | flat offset shadow |
| `--ok` | `0.42 0.11 150` | success text |
| `--warn` | `0.48 0.13 85` | warning text |
| `--bad` | `0.47 0.16 25` | danger text |
| `--mark-add` | `0.90 0.05 150` | diff add tint |
| `--mark-del` | `0.90 0.05 25` | diff del tint |
| `--mark-hunk` | `0.92 0.04 75` | hunk header tint |
| `--sel` | `0.88 0.07 70` | selection |

### Computed contrast (WCAG, `(Y₁+0.05)/(Y₂+0.05)`)

| Pair | Dark | Light |
|---|---|---|
| body `--ink` on plate `--bg-1` | 12.7:1 | 16.6:1 |
| body `--ink` on bench `--bg-0` | 15.0:1 | 14.3:1 |
| secondary `--ink-2` on `--bg-0`/`--bg-1` | 8.2 / 7.6:1 | 7.6 / 8.4:1 |
| muted `--ink-3` on `--bg-0`/`--bg-1` | 5.9 / 5.4:1 | 5.6 / 6.2:1 |
| `--accent-ink` on `--accent` | 7.4:1 | 6.9:1 |

All ≥ 4.5:1. Status text steps chosen at the same or better margin (worst:
`--bad` dark on `--bg-1` ≈ 5.3:1; `--warn`/`--ok` light ≈ 5.1–6.5:1).

## Typography

- **Display:** Space Grotesk (600/700) — repo names, page titles, numeric
  readouts, section heads. Fallback `"Space Grotesk", "Avenir Next", sans-serif`.
- **Text & code:** IBM Plex Mono (400/500/600) — *the* UI font: body, tables,
  diffs, breadcrumbs, buttons, inputs. Mono-forward: interface reads like an
  instrument panel. Fallback `"IBM Plex Mono", ui-monospace, monospace`.
- Self-hosted woff2 subsets only; the specimen HTML uses fallbacks so it loads
  offline.
- Scale (mono base 13px): 11px micro (badges, table heads, timestamps) ·
  12.5px dense text · 13px body · 15px lead · display 20/26/34px Space Grotesk.
- Line-height: UI 1.4, prose/README 1.55, diff/code 1.45 with `font-variant-numeric: tabular-nums` everywhere.

## Spacing subset

From 4/8/12/16/24/32/48/64, **use 4 8 12 16 24 32 48 — never 64** (density).
- 4: icon gaps, badge padding · 8: cell padding, control gaps, list rhythm
- 12: plate inner padding (dense) · 16: default plate padding
- 24: stacked section gap, nav slab height padding
- 32: screen margins at ≥1280px · 48: only for empty/error block vertical air

## Radius scale

`--r0: 0` default (plates are square) · `--r1: 2px` only on badges, inputs,
focus chips · `--rfull: 999px` only for 6–8px status dots. Nothing else rounds.

## Shadow policy

Flat offset only — `box-shadow` with **zero blur, zero spread**, `--shadow` color:
- plates/popovers: `3px 3px 0 var(--shadow)`
- buttons/badges-filled: `2px 2px 0 var(--shadow)`
- inputs: `inset 2px 2px 0 var(--shadow)` (engraved well)
- pressed: `0 0 0` + `translate(2px,2px)`. Never ambient/soft shadows, never `filter: blur`/`backdrop-filter`.

## Motion budget

CSS transitions only; no keyframe animation except a 1s `steps(2)` blinker for
LIVE cursors (also gated). One easing everywhere:
`cubic-bezier(0.16, 1, 0.3, 1)` (ease-out-expo — snappy, no bounce).
- hover/press/focus: **100ms**; row tint changes: **80ms**; max allowed 120ms.
- Named properties only: `color`, `background-color`, `border-color`,
  `box-shadow`, `transform`, `outline-color`. `transition: all` is forbidden.
- Whole block wrapped in `@media (prefers-reduced-motion: no-preference)`;
  reduced-motion users get instant state changes.

## Component primitives

Interaction states for every interactive element:
- **hover** only inside `@media (hover: hover)` — background → `--bg-2`,
  or `translate(-1px,-1px)` with shadow +1px on buttons.
- **focus-visible**: `outline: 2px solid var(--accent); outline-offset: 2px`
  (inside orange chrome, outline uses `--accent-ink`).
- **active**: `translate(2px,2px)`, shadow → none.
- **disabled**: flat (no shadow), `--ink-3` text, `--edge` border,
  `cursor: not-allowed`, aria `aria-disabled`/`disabled` (exempt from 4.5:1,
  but never used for information-bearing text).

| Primitive | Spec |
|---|---|
| nav bar | 48px `--accent` slab, `--accent-ink` text, 2px `--accent-ink` bottom border. Brand = bordered square + wordmark; links: text, `aria-current` item = `--accent-ink` fill + `--accent` text chip. Global search input embedded, `--bg-0` well. |
| breadcrumb | `<nav aria-label><ol>`; `›` separators in `--ink-3`; segments `--ink-2`, last `aria-current` = `--ink` medium; repo segment may carry a 2px underline in `--accent`. |
| data table | `.plate` wrapper; `th` = `--bg-2`, 11px uppercase Space Grotesk, `border-bottom: 1.5px solid --edge-hard`; `td` `border-top: 1px solid --edge`; row hover → `--bg-2`; numeric/mono columns `tabular-nums`, right-aligned; row height 32px (dense 28px). |
| tree listing | 28px rows; 14px inline-SVG glyph (dir = filled square outline, file = corner-mark) in `--ink-3`; columns name / last-change message (`--ink-2`, truncating) / age (`--ink-3`, right); `..` parent row first; row hover → `--bg-2`. |
| diff view | table; two 3.5ch line-number cells (`--bg-2`, `--ink-3`, `user-select:none`), marker cell, code cell. `.add` bg `--mark-add` with `+` in `--ok`; `.del` bg `--mark-del` with `−` in `--bad`; context untinted; hunk row `--mark-hunk` bg, `--accent-text` mono `@@ … @@`; file header bar = `--bg-2` plate strip, filename mono + `+n/−n` stat in `--ok`/`--bad`. |
| code view | numbered `<table>`/`ol`, `--bg-1` plate, 20px line height, numbers same gutter treatment as diff; selection `--sel`. |
| change-state badges | 11px uppercase mono, `padding: 2px 6px`, `--r1`, `border: 1.5px solid` status color + status text; `OPEN` is the only filled badge (`--accent` bg, `--accent-ink` text, no border — part of the orange chrome budget). States: OPEN accent · LANDED `--ok` · CONFLICT/FAILED `--bad` · QUEUED `--ink-3` · RUNNING `--accent-text` border + 6px dot. |
| buttons | 11–12px mono, uppercase optional, `padding: 6px 12px`, `1.5px solid --edge-hard`, `2px 2px 0 --shadow`, `--r1` allowed. Primary: `--accent` bg + `--accent-ink` text + `1.5px solid --accent-ink`. Danger variant: `--bad` border + `--bad` text on plate. |
| inputs | `--bg-0` well, `1.5px solid --edge-hard`, `inset 2px 2px 0 --shadow`, `--r1`, placeholder `--ink-3`, `caret-color: --accent`; focus = border → `--accent` + focus-visible outline. |
| flash messages | plate strip, `1.5px solid` status color + 6px left bar of same, status-colored lead word + `--ink` message; kinds ok/warn/bad; dismiss via plain `<a href>`/submit — no JS. |
| empty state | left-aligned block inside a `--edge`-bordered plate: Space Grotesk 20px heading, one `--ink-2` line of guidance naming the next action, then a primary button. Never "nothing here". |
| error state | same geometry, `--bad` accents, monospace error code (`E_STALE`, `E_FORBIDDEN`) in a `--bg-2` chip, retry = real link/button (GET re-issue or POST form). |

## Density rules

- Base rhythm: 8px; control height 32px (dense 28px); max content width 1200px
  (diff pages 1440px); nav 48px.
- Tables default 32px rows; op.log/activity streams may drop to 28px.
- Diff/code lines 20px; hunk header 24px; no vertical padding inside code cells.
- One level of plate nesting max — no cards-in-cards; subdivide with `1px --edge`
  rules instead.
- Every icon is inline SVG (14/16px, `--ink-3` or `--accent-ink`); no emoji, no
  icon fonts, no external assets.

## Specimen

`design-direction-plate.html` — one self-contained file, zero JS, both schemes
via `prefers-color-scheme`: repo page (tree + README), change review (unified
diff + attestation list), agent activity (op.log table) + states strip.
```

---

## Artifact: `local://design-direction-plate.html`

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<title>Plate — rukie UI direction</title>
<style>
/* ---- Plate tokens ---------------------------------------------------- */
:root{
  color-scheme: dark;
  --bg-0:oklch(0.175 0.008 70); --bg-1:oklch(0.215 0.010 70); --bg-2:oklch(0.265 0.012 70);
  --edge:oklch(0.45 0.015 70);  --edge-hard:oklch(0.92 0.008 70);
  --ink:oklch(0.92 0.008 70);   --ink-2:oklch(0.74 0.012 70);   --ink-3:oklch(0.65 0.014 70);
  --accent:oklch(0.72 0.23 40); --accent-ink:oklch(0.19 0.02 60); --accent-text:oklch(0.78 0.16 45);
  --shadow:oklch(0.11 0.005 70);
  --ok:oklch(0.80 0.13 150); --warn:oklch(0.82 0.15 85); --bad:oklch(0.68 0.15 25);
  --mark-add:oklch(0.30 0.035 150); --mark-del:oklch(0.28 0.040 25); --mark-hunk:oklch(0.20 0.030 55);
  --sel:oklch(0.30 0.05 60);
  --disp:"Space Grotesk","Avenir Next",sans-serif;
  --mono:"IBM Plex Mono",ui-monospace,SFMono-Regular,Menlo,monospace;
  --r1:2px;
  --ease:cubic-bezier(0.16,1,0.3,1);
}
@media (prefers-color-scheme: light){
  :root{
    color-scheme: light;
    --bg-0:oklch(0.935 0.008 80); --bg-1:oklch(0.985 0.004 80); --bg-2:oklch(0.90 0.010 80);
    --edge:oklch(0.55 0.015 70);  --edge-hard:oklch(0.22 0.015 70);
    --ink:oklch(0.22 0.015 70);   --ink-2:oklch(0.40 0.015 70);   --ink-3:oklch(0.47 0.015 70);
    --accent-ink:oklch(0.22 0.02 60); --accent-text:oklch(0.50 0.19 40);
    --shadow:oklch(0.22 0.015 70);
    --ok:oklch(0.42 0.11 150); --warn:oklch(0.48 0.13 85); --bad:oklch(0.47 0.16 25);
    --mark-add:oklch(0.90 0.05 150); --mark-del:oklch(0.90 0.05 25); --mark-hunk:oklch(0.92 0.04 75);
    --sel:oklch(0.88 0.07 70);
  }
}
/* ---- base ------------------------------------------------------------ */
*,*::before,*::after{box-sizing:border-box}
body{margin:0;background:var(--bg-0);color:var(--ink);
  font:400 13px/1.45 var(--mono);font-variant-numeric:tabular-nums}
h1,h2,h3{font-family:var(--disp);font-weight:600;margin:0;letter-spacing:-0.01em}
a{color:inherit;text-decoration:none}
a:hover{text-decoration:underline}
::selection{background:var(--sel)}
code,kbd,pre{font-family:var(--mono)}
svg{display:inline-block;vertical-align:-2px;fill:none;stroke:currentColor;stroke-width:1.5}
:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
@media (prefers-reduced-motion: no-preference){
  a,.btn,td,input,.badge,.tab{
    transition:color 100ms var(--ease),background-color 100ms var(--ease),
      border-color 100ms var(--ease),box-shadow 100ms var(--ease),
      transform 100ms var(--ease),outline-color 100ms var(--ease)}
}
/* ---- nav slab -------------------------------------------------------- */
.topnav{display:flex;align-items:center;gap:16px;height:48px;padding:0 24px;
  background:var(--accent);color:var(--accent-ink);border-bottom:2px solid var(--accent-ink)}
.topnav .brand{display:flex;align-items:center;gap:8px;font:700 15px var(--disp);letter-spacing:.04em}
.topnav .brand b{width:16px;height:16px;border:2px solid var(--accent-ink);display:inline-block}
.topnav nav{display:flex;gap:16px}
.topnav nav a{font:600 11px var(--mono);letter-spacing:.08em;text-transform:uppercase;
  padding:4px 8px;border:1.5px solid transparent}
.topnav nav a[aria-current]{background:var(--accent-ink);color:var(--accent);border-color:var(--accent-ink)}
.topnav .search{margin-left:auto;width:220px;background:var(--bg-0);color:var(--ink);
  border:1.5px solid var(--accent-ink);box-shadow:inset 2px 2px 0 var(--shadow);
  padding:5px 8px;font:400 12px var(--mono);border-radius:var(--r1)}
.topnav .search::placeholder{color:var(--ink-3)}
.topnav :focus-visible{outline-color:var(--accent-ink)}
/* ---- layout ---------------------------------------------------------- */
.wrap{max-width:1200px;margin:0 auto;padding:24px 32px}
.screen-label{font:600 11px var(--mono);letter-spacing:.14em;text-transform:uppercase;
  color:var(--accent-text);margin:0 0 8px}
.screen{margin-bottom:48px}
.crumbs{font-size:12px;color:var(--ink-2);margin-bottom:8px}
.crumbs ol{list-style:none;display:flex;gap:6px;margin:0;padding:0;flex-wrap:wrap}
.crumbs li+li::before{content:"›";color:var(--ink-3);margin-right:6px}
.crumbs a[aria-current]{color:var(--ink);font-weight:500}
.crumbs .repo{font-weight:600;box-shadow:inset 0 -2px 0 var(--accent)}
/* ---- plates ---------------------------------------------------------- */
.plate{background:var(--bg-1);border:1.5px solid var(--edge-hard);box-shadow:3px 3px 0 var(--shadow)}
.plate-head{display:flex;align-items:center;gap:12px;padding:8px 12px;
  border-bottom:1.5px solid var(--edge-hard);background:var(--bg-2)}
.plate-head h2,.plate-head h3{font-size:14px}
/* ---- buttons --------------------------------------------------------- */
.btn{display:inline-flex;align-items:center;gap:6px;font:600 12px var(--mono);
  padding:6px 12px;border:1.5px solid var(--edge-hard);border-radius:var(--r1);
  background:var(--bg-1);color:var(--ink);box-shadow:2px 2px 0 var(--shadow);cursor:pointer}
.btn.primary{background:var(--accent);color:var(--accent-ink);border-color:var(--accent-ink)}
.btn.danger{border-color:var(--bad);color:var(--bad);background:var(--bg-1)}
@media (hover:hover){
  .btn:hover{transform:translate(-1px,-1px);box-shadow:3px 3px 0 var(--shadow);text-decoration:none}
  tbody tr.row:hover td{background:var(--bg-2)}
  .tree a:hover td{background:var(--bg-2)}
}
.btn:active{transform:translate(2px,2px);box-shadow:0 0 0 var(--shadow)}
.btn[disabled]{box-shadow:none;border-color:var(--edge);color:var(--ink-3);cursor:not-allowed;transform:none}
/* ---- badges ---------------------------------------------------------- */
.badge{display:inline-block;font:600 11px var(--mono);letter-spacing:.06em;text-transform:uppercase;
  padding:2px 6px;border:1.5px solid currentColor;border-radius:var(--r1);white-space:nowrap}
.badge.open{background:var(--accent);color:var(--accent-ink);border-color:var(--accent-ink)}
.badge.ok{color:var(--ok)} .badge.bad{color:var(--bad)} .badge.warn{color:var(--warn)}
.badge.mut{color:var(--ink-3)}
.dot{display:inline-block;width:6px;height:6px;border-radius:999px;background:currentColor;margin-right:4px}
/* ---- tables ---------------------------------------------------------- */
table.data{width:100%;border-collapse:collapse;font-size:12.5px}
table.data th{background:var(--bg-2);color:var(--ink-2);font:600 11px var(--disp);
  letter-spacing:.08em;text-transform:uppercase;text-align:left;
  padding:8px;border-bottom:1.5px solid var(--edge-hard)}
table.data td{padding:7px 8px;border-top:1px solid var(--edge);vertical-align:middle;height:32px}
table.data td.num,table.data th.num{text-align:right}
table.data .mut{color:var(--ink-3)} table.data .sub{color:var(--ink-2)}
/* ---- tree ------------------------------------------------------------ */
.tree{list-style:none;margin:0;padding:0}
.tree li{display:grid;grid-template-columns:20px minmax(0,2fr) minmax(0,3fr) 80px;
  align-items:center;gap:8px;height:28px;padding:0 8px;border-top:1px solid var(--edge);font-size:12.5px}
.tree li:first-child{border-top:0}
.tree svg{color:var(--ink-3);width:14px;height:14px}
.tree .dir{font-weight:500}
.tree .msg{color:var(--ink-2);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.tree .age{color:var(--ink-3);text-align:right}
@media (hover:hover){ .tree li:hover{background:var(--bg-2)} }
/* ---- diff ------------------------------------------------------------ */
table.diff{width:100%;border-collapse:collapse;font-size:12.5px;line-height:20px}
table.diff td{padding:0 8px;border-top:1px solid var(--edge);vertical-align:top}
table.diff td.ln{width:3.5ch;text-align:right;color:var(--ink-3);background:var(--bg-2);
  user-select:none;border-top:1px solid var(--edge)}
table.diff td.mk{width:2ch;text-align:center;user-select:none}
table.diff tr.fhead td{background:var(--bg-2);font-weight:500;height:28px;
  border-top:1px solid var(--edge-hard)}
table.diff tr.hunk td{background:var(--mark-hunk);color:var(--accent-text);height:24px}
table.diff tr.add td.code{background:var(--mark-add)} table.diff tr.add td.mk{color:var(--ok);background:var(--mark-add)}
table.diff tr.add td.ln{background:var(--bg-2)}
table.diff tr.del td.code{background:var(--mark-del)} table.diff tr.del td.mk{color:var(--bad);background:var(--mark-del)}
table.diff td.code{white-space:pre-wrap;word-break:break-all}
.stat-add{color:var(--ok)} .stat-del{color:var(--bad)}
/* ---- attestations ---------------------------------------------------- */
.attest{list-style:none;margin:0;padding:0}
.attest li{display:grid;grid-template-columns:110px 1fr auto;gap:8px;align-items:center;
  padding:8px 12px;border-top:1px solid var(--edge);font-size:12.5px}
.attest li:first-child{border-top:0}
.attest .kind{font-weight:600}
.attest .who{color:var(--ink-2)} .attest .who kbd{font-size:11px;color:var(--ink-3)}
/* ---- flash / states -------------------------------------------------- */
.flash{display:flex;gap:8px;align-items:baseline;padding:8px 12px;margin:12px 0;
  border:1.5px solid;border-left-width:6px;background:var(--bg-1)}
.flash b{text-transform:uppercase;font-size:11px;letter-spacing:.08em}
.flash.ok{border-color:var(--ok)} .flash.ok b{color:var(--ok)}
.flash.bad{border-color:var(--bad)} .flash.bad b{color:var(--bad)}
.flash.warn{border-color:var(--warn)} .flash.warn b{color:var(--warn)}
.statebox{padding:24px;border:1.5px solid var(--edge);background:var(--bg-1)}
.statebox h3{font-size:20px;margin-bottom:8px}
.statebox p{color:var(--ink-2);margin:0 0 16px;max-width:52ch}
.err-code{display:inline-block;background:var(--bg-2);border:1px solid var(--edge);
  padding:1px 6px;font-size:11px;color:var(--bad)}
input.field{background:var(--bg-0);color:var(--ink);border:1.5px solid var(--edge-hard);
  box-shadow:inset 2px 2px 0 var(--shadow);border-radius:var(--r1);
  padding:6px 8px;font:400 12.5px var(--mono);width:260px}
input.field::placeholder{color:var(--ink-3)}
input.field:focus{border-color:var(--accent);outline:2px solid var(--accent);outline-offset:2px}
/* ---- readme ---------------------------------------------------------- */
.readme{padding:16px;font-size:13px;line-height:1.55}
.readme h2{font-size:20px;margin-bottom:8px}
.readme h3{font-size:14px;margin:16px 0 4px;color:var(--ink-2)}
.readme p{margin:0 0 8px;max-width:68ch}
.readme code{background:var(--bg-2);border:1px solid var(--edge);padding:1px 4px;font-size:12px}
.cols{display:grid;grid-template-columns:minmax(0,7fr) minmax(0,5fr);gap:16px;align-items:start}
@media (max-width:960px){.cols{grid-template-columns:1fr}.topnav .search{display:none}}
footer{border-top:1.5px solid var(--edge-hard);padding:12px 32px;color:var(--ink-3);font-size:11px}
</style>
</head>
<body>

<header class="topnav">
  <a class="brand" href="#"><b aria-hidden="true"></b>RUKIE</a>
  <nav aria-label="Primary">
    <a href="#" aria-current="page">Repos</a>
    <a href="#">Changes</a>
    <a href="#">Agents</a>
    <a href="#">Mirrors</a>
  </nav>
  <input class="search" type="search" placeholder="jump to repo, change, agent…" aria-label="Search">
</header>

<main class="wrap">

<!-- ============================ SCREEN A ============================ -->
<section class="screen" aria-labelledby="s-a">
  <p class="screen-label">A · Repository</p>
  <nav class="crumbs" aria-label="Breadcrumb"><ol>
    <li><a href="#">ruk</a></li>
    <li><a href="#" class="repo" aria-current="page">ruk-core</a></li>
    <li>src / protocol.rs</li>
  </ol></nav>

  <div style="display:flex;align-items:center;gap:12px;margin-bottom:12px;flex-wrap:wrap">
    <h1 id="s-a" style="font-size:26px">ruk / ruk-core</h1>
    <span class="badge mut">public</span>
    <span class="badge">default&nbsp;<span style="font-weight:400">main</span></span>
    <span style="margin-left:auto;display:flex;gap:8px">
      <button class="btn" type="button">Watch</button>
      <button class="btn" type="button">Fork</button>
      <button class="btn primary" type="button">New change</button>
    </span>
  </div>

  <div class="cols">
    <div class="plate">
      <div class="plate-head">
        <h2>main</h2>
        <span class="sub" style="color:var(--ink-2);font-size:12px">
          <a href="#" style="text-decoration:underline">4c91a2e</a> · attest.list: page through lease-bound cursors</span>
        <span style="margin-left:auto;color:var(--ink-3);font-size:11px"><time datetime="2026-10-20T02:41">2h ago</time></span>
      </div>
      <ul class="tree">
        <li><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3 4h10v8H3z"/></svg><a class="dir" href="#">..</a><span class="msg"></span><span class="age"></span></li>
        <li><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3 4h10v8H3z"/></svg><a class="dir" href="#">attestation</a><span class="msg">evaluate_requirements: freeze trusted signers</span><span class="age"><time datetime="2026-10-19">1d</time></span></li>
        <li><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3 4h10v8H3z"/></svg><a class="dir" href="#">coord</a><span class="msg">op.log: mint 30s read-lease cursors</span><span class="age"><time datetime="2026-10-20T02:41">2h</time></span></li>
        <li><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M4 2h8v12H4z"/><path d="M6 6h4M6 9h4"/></svg><a href="#">capability.rs</a><span class="msg">attest kinds: no wildcard grammar</span><span class="age"><time datetime="2026-10-18">3d</time></span></li>
        <li><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M4 2h8v12H4z"/><path d="M6 6h4M6 9h4"/></svg><a href="#">land.rs</a><span class="msg">land.prepare: snapshot attestation policy</span><span class="age"><time datetime="2026-10-19T22:10">5h</time></span></li>
        <li><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M4 2h8v12H4z"/><path d="M6 6h4M6 9h4"/></svg><a href="#">protocol.rs</a><span class="msg">attest.list: page through lease-bound cursors</span><span class="age"><time datetime="2026-10-20T02:41">2h</time></span></li>
        <li><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M4 2h8v12H4z"/><path d="M6 6h4M6 9h4"/></svg><a href="#">workspace.rs</a><span class="msg">ws.take: advance lease epoch on takeover</span><span class="age"><time datetime="2026-10-17">4d</time></span></li>
      </ul>
    </div>

    <article class="plate readme" aria-label="README">
      <h2>ruk-core</h2>
      <p>The store and typed protocol behind rukie. One publication lane per
      repository; every mutation is a compare-and-swap batch with a 7-day
      idempotent receipt.</p>
      <h3>Highlights</h3>
      <p>Signed attestations (<code>review: pass</code>) as SSHSIG statements in
      namespace <code>ruk-attest</code> · land plans that freeze exact
      candidates · <code>op.log</code>/<code>claim.list</code>/<code>attest.list</code>
      reads on lease-bound cursors.</p>
      <h3>Build</h3>
      <p><code>cargo build -p ruk</code> → <code>ruk forgejo serve --config forge.toml</code></p>
    </article>
  </div>
</section>

<!-- ============================ SCREEN B ============================ -->
<section class="screen" aria-labelledby="s-b">
  <p class="screen-label">B · Change review</p>
  <nav class="crumbs" aria-label="Breadcrumb"><ol>
    <li><a href="#">ruk</a></li><li><a href="#" class="repo">ruk-core</a></li>
    <li><a href="#">changes</a></li><li><a href="#" aria-current="page">#1042</a></li>
  </ol></nav>

  <div style="display:flex;align-items:center;gap:12px;margin-bottom:4px;flex-wrap:wrap">
    <h1 id="s-b" style="font-size:26px">agent/mira: lease-bound cursors for attest.list</h1>
    <span class="badge open">Open</span>
    <span class="mut" style="color:var(--ink-3);font-size:12px">mira · 3 commits · <span class="stat-add">+184</span> <span class="stat-del">−22</span></span>
    <span style="margin-left:auto;display:flex;gap:8px">
      <button class="btn" type="button">Request changes</button>
      <button class="btn primary" type="button">land.execute</button>
    </span>
  </div>

  <div class="flash ok" role="status"><b>Ready</b><span>land plan <code>7f3d…c1</code> frozen — both required attestations live; expires <time datetime="2026-10-20T21:47">21:47 UTC</time>.</span></div>

  <div class="cols">
    <div class="plate" aria-label="Diff">
      <div class="plate-head"><h2>Files changed · 2</h2>
        <span style="margin-left:auto;font-size:12px"><span class="stat-add">+184</span> <span class="stat-del">−22</span></span></div>
      <table class="diff">
        <tr class="fhead"><td class="ln" colspan="2"></td><td class="mk"></td>
          <td class="code">src/coord/hosted.rs <span class="stat-add" style="float:right">+61</span><span class="stat-del" style="float:right">−9&nbsp;</span></td></tr>
        <tr class="hunk"><td class="ln"></td><td class="ln"></td><td class="mk"></td>
          <td class="code">@@ -774,9 +774,20 @@ fn read_attestations_page(</td></tr>
        <tr><td class="ln">774</td><td class="ln">774</td><td class="mk"></td><td class="code">    ) -&gt; Result&lt;AttestListReply, CoreError&gt; {</td></tr>
        <tr class="del"><td class="ln">775</td><td class="ln"></td><td class="mk">−</td><td class="code">        let limit = args.page.limit.unwrap_or(MAX_PAGE);</td></tr>
        <tr class="add"><td class="ln"></td><td class="ln">775</td><td class="mk">+</td><td class="code">        let limit = args.page.effective_limit();</td></tr>
        <tr class="add"><td class="ln"></td><td class="ln">776</td><td class="mk">+</td><td class="code">        let limit_rows = u32::try_from(limit)</td></tr>
        <tr class="add"><td class="ln"></td><td class="ln">777</td><td class="mk">+</td><td class="code">            .map_err(|_| CoreError::new(ErrorCode::EInvalid,</td></tr>
        <tr class="add"><td class="ln"></td><td class="ln">778</td><td class="mk">+</td><td class="code">                "page limit exceeds its bound"))?;</td></tr>
        <tr><td class="ln">776</td><td class="ln">779</td><td class="mk"></td><td class="code">        let Some(cursor) = args.page.cursor.as_ref() else {</td></tr>
        <tr class="del"><td class="ln">777</td><td class="ln"></td><td class="mk">−</td><td class="code">            return page_once(&amp;self.store, filter, limit);</td></tr>
        <tr class="add"><td class="ln"></td><td class="ln">780</td><td class="mk">+</td><td class="code">            let page = list(&amp;self.store, &amp;filter, limit_page, None, &amp;open)?;</td></tr>
        <tr class="add"><td class="ln"></td><td class="ln">781</td><td class="mk">+</td><td class="code">            return Ok(first_page_reply(self, viewer, page, args)?);</td></tr>
        <tr><td class="ln">778</td><td class="ln">782</td><td class="mk"></td><td class="code">        };</td></tr>
      </table>
    </div>

    <aside class="plate" aria-label="Attestations">
      <div class="plate-head"><h3>Attestations · land requirements</h3></div>
      <ul class="attest">
        <li><span class="kind">review</span>
          <span class="who">pass · tully <kbd>ssh-ed25519 SHA256:9k2f…qR</kbd></span>
          <span class="badge ok">Live</span></li>
        <li><span class="kind">ci</span>
          <span class="who">pass · builder-7 <kbd>ssh-ed25519 SHA256:m4xa…8e</kbd></span>
          <span class="badge ok">Live</span></li>
        <li><span class="kind">review</span>
          <span class="who">fail · mira <kbd>ssh-ed25519 SHA256:77tp…h0</kbd></span>
          <span class="badge mut">Superseded</span></li>
      </ul>
      <div class="plate-head" style="border-top:1.5px solid var(--edge-hard);border-bottom:0">
        <h3>Checks</h3></div>
      <ul class="attest">
        <li><span class="kind">build</span><span class="who">gnu-x64 · 4m12s</span><span class="badge ok">pass</span></li>
        <li><span class="kind">clippy</span><span class="who">deny-warnings · 1m03s</span><span class="badge ok">pass</span></li>
        <li><span class="kind">conformance</span><span class="who">git 2.53 harness</span><span class="badge warn">queued</span></li>
      </ul>
    </aside>
  </div>
</section>

<!-- ============================ SCREEN C ============================ -->
<section class="screen" aria-labelledby="s-c">

[1849 more lines in resource. Use :604 to continue]
<section class="screen" aria-labelledby="s-c">
  <p class="screen-label">C · Agent activity</p>
  <h1 id="s-c" style="font-size:26px;margin-bottom:12px">op.log · ruk-core</h1>

  <div class="plate">
    <div class="plate-head"><h2>Operations</h2>
      <span class="sub" style="color:var(--ink-3);font-size:11px">view 88112 · cursor-leased · newest first</span></div>
    <table class="data">
      <thead><tr>
        <th scope="col">Op</th><th scope="col">Session</th><th scope="col">Method</th>
        <th scope="col">Target</th><th scope="col" class="num">Age</th><th scope="col">State</th>
      </tr></thead>
      <tbody>
        <tr class="row"><td>88114</td><td>mira <span class="mut">sess 4f2c…</span></td>
          <td><code>land.prepare</code></td><td class="sub"><code>agent/mira → main</code></td>
          <td class="num mut">41s</td><td><span class="badge"><span class="dot" style="color:var(--accent-text)"></span><span style="color:var(--accent-text)">running</span></span></td></tr>
        <tr class="row"><td>88113</td><td>tully <span class="mut">sess 91be…</span></td>
          <td><code>attest.add</code></td><td class="sub"><code>review: pass @ 4c91a2e</code></td>
          <td class="num mut">3m</td><td><span class="badge ok">landed</span></td></tr>
        <tr class="row"><td>88112</td><td>mira <span class="mut">sess 4f2c…</span></td>
          <td><code>git.commit</code></td><td class="sub"><code>agent/mira</code></td>
          <td class="num mut">6m</td><td><span class="badge ok">landed</span></td></tr>
        <tr class="row"><td>88111</td><td>ren <span class="mut">sess 08aa…</span></td>
          <td><code>ws.take</code></td><td class="sub"><code>ws-docs · epoch 4</code></td>
          <td class="num mut">19m</td><td><span class="badge ok">landed</span></td></tr>
        <tr class="row"><td>88110</td><td>builder-7 <span class="mut">sess c301…</span></td>
          <td><code>claim.acquire</code></td><td class="sub"><code>src/coord/**</code></td>
          <td class="num mut">22m</td><td><span class="badge mut">released</span></td></tr>
        <tr class="row"><td>88109</td><td>mira <span class="mut">sess 4f2c…</span></td>
          <td><code>rebase</code></td><td class="sub"><code>agent/mira onto main</code></td>
          <td class="num mut">31m</td><td><span class="badge bad">E_STALE</span></td></tr>
      </tbody>
    </table>
  </div>

  <div class="flash bad" role="alert"><b>op 88109</b>
    <span>rebase aborted: <span class="err-code">E_STALE</span> lost branch race on <code>main</code> — retried with fresh tip, landed as op 88112.</span></div>

  <div class="cols" style="margin-top:16px">
    <div class="statebox">
      <h3>No agents running</h3>
      <p>Idle. Start a workspace with <code>ruk ws.new</code> or assign a change to an
      agent session; it will stream here via <code>op.log</code>.</p>
      <button class="btn primary" type="button">Open agent session</button>
      <button class="btn" type="button" style="margin-left:8px">Read op.log docs</button>
    </div>
    <div class="statebox">
      <h3>Mirror sync failed</h3>
      <p>Push to <code>github.com/ruk-mirror/ruk-core</code> refused after 3 attempts.
      Check the deploy key, then retry.</p>
      <p><span class="err-code">E_FORBIDDEN · remote rejected refs/heads/main</span></p>
      <form method="post" action="#" style="display:flex;gap:8px;align-items:center;flex-wrap:wrap">
        <input class="field" type="text" placeholder="deploy key fingerprint…" aria-label="Deploy key fingerprint">
        <button class="btn" type="submit">Retry mirror</button>
        <button class="btn danger" type="button">Pause mirror</button>
        <button class="btn" type="button" disabled>Inspect (soon)</button>
      </form>
    </div>
  </div>
</section>

</main>
<footer>Plate · rukie UI direction · zero-JS server-rendered · dark primary, light via prefers-color-scheme</footer>
</body>
</html>
```

---

## Audit result

- No `<script>`, no `on*` attributes, no gradients, no `transition: all` (named properties only), no blur/`backdrop-filter`, no emoji (inline SVG + `›`/`@@` glyphs only), no external loads.
- Contrast (computed, achromatic Y = L³ exact; accent Y ≈ 0.37 via OKLab→linear-sRGB): dark body 12.7–15.0:1, secondary 7.6–8.2:1, muted 5.4–5.9:1, ink-on-orange 7.4:1; light body 14.3–16.6:1, secondary 7.6–8.4:1, muted 5.6–6.2:1, ink-on-orange 6.9:1. All ≥ 4.5:1.
- Palette avoids blue/purple families; committed hue is orange (H=40); status hues are green-cyan (150), amber (85), deep red (25) — none pure.
- Blocker: this environment exposes no write tool; both artifacts are emitted above verbatim for the caller to persist at `local://design-direction-plate.md` and `local://design-direction-plate.html`.

### Critical Files for Implementation

- `rukie/templates/repo/diff/section_unified.tmpl` — unified diff row shape (dual line-number cells, marker, code) the Plate diff primitive mirrors; future rukie renderer must keep this table semantics.
- `rukie/templates/repo/view_list.tmpl` — file-tree grid (name/message/age) the tree primitive reproduces.
- `ruk/crates/ruk-core/src/protocol.rs:2110-2140` — `OperationEntry`/`OpLogReply` fields the activity table maps 1:1 (id, session, method, committed_at, undoes).
- `ruk/crates/ruk-core/src/attestation.rs` + `attestation/query.rs` — attestation kinds/results and Live/Ambiguous/Superseded states the badge set encodes.
- `rukie/web_src/css/base.css` — the theme variable surface a future Plate stylesheet replaces (token names there must not be reused verbatim).

[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…