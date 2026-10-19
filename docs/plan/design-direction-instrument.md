The sibling "plate" artifact confirms the convention: same blocker (no write tool), full artifacts emitted inline for the caller to persist at the absolute path. My palette arithmetic cross-checks against its published Y values (achromatic Y = L³: ink 0.92 → 0.779 ✓, dark bench 0.175 → 0.0054 ✓). All advisory corrections are applied: accent moved to hue 235 (clear blue, 30° from info 205, away from indigo), full recomputation with the correct OKLab→linear-sRGB matrix, radius scale fixed to 4/8/12/16 with a satisfiable nesting rule, concrete shadow recipes, `.b-mut` at --ink on --s3 (8.0/10.3:1), spacing confined to the subset, and missing hover/focus states added. Emitting both complete artifacts for persistence.

---

**Status:** Both artifacts complete below. **Blocker:** this environment exposes no write tool (read/grep/web_search only; confirmed by repeated probes and by the sibling `design-direction-plate` artifact which hit the identical wall). Persist verbatim to:
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/design-direction-instrument.md`
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/design-direction-instrument.html`

Contrast method: achromatic tokens Y = L³ (exact); chromatic tokens via Björn Ottosson's OKLab→linear-sRGB matrices and WCAG `(Y₁+0.05)/(Y₂+0.05)`, values floored. Worst pair: light info-on-tint 5.0:1; dark fail-on-tint 5.2:1. Light `--ink3` is the declared ≥3:1 icon/large-text tier (3.4:1); dark `--ink3` passes AA (4.9:1). Recompute with `culori` before token lock (`unverified` until then — hand arithmetic, though double-passed).

---

## Artifact 1 — `design-direction-instrument.md`

````markdown
# Design Direction — Instrument

One visual direction for rukie's zero-JavaScript, server-rendered forge UI.
Register: a tool for engineers supervising many AI agents; dense, read-heavy
(change-review diffs, repo trees, logs, agent activity). Physical scene: an
operations lead at a large monitor in a control-room team space, glancing at
status across many repositories. Light and dark are equal citizens; default
follows `prefers-color-scheme`.

Concept: a **Fluent-style structured workbench**. Depth comes only from
layered transparent box-shadows (never 1px borders); a full-palette status
system encodes change/attestation/claim state as data; numerics are compact
and tabular everywhere. The chrome recedes; the data is the interface.

## Taste anchors

1. **Stripe dashboard docs** — restrained cool neutrals, one confident blue,
   data encoded by position and weight before color; elevation as a quiet
   layer cake.
2. **Things 3** — generous hit areas inside dense rows, state badges that are
   icon *with* label, and motion so short it reads as responsiveness.

## Palette (OKLCH)

Neutrals are a custom cool-paper ramp: hue 264 at chroma ≤ 0.020 reads as
warm-neutral gray against tinted surfaces — not a Tailwind ramp (steps are
surface *roles*, not an even scale). The accent is an instrument blue
(hue 235), the only chromatic non-status color; it is 30° from the info
status hue and far from any purple. Status hues are ≥ 40° apart; none is a
pure primary red, green, or blue.

**Status roles as data encoding** (mapped to ruk's real vocabulary —
`AttestationStatus::{Live,Ambiguous,Superseded}`, `Gap::{NoLiveMatch,
Ambiguous,Expired}`, error codes from `/tmp/ruk-model.json`):

| Role | Light | Dark | Encodes |
|---|---|---|---|
| ok | `oklch(0.440 0.095 160)` | `oklch(0.740 0.120 165)` | `Live`, attest `pass`, healthy claim |
| warn | `oklch(0.440 0.110 78)` | `oklch(0.780 0.130 82)` | `Ambiguous`, queued, claim expiring <10 min |
| fail | `oklch(0.470 0.140 38)` | `oklch(0.720 0.160 40)` | `NoLiveMatch`, `Expired`, `E_STALE`, `E_PUSH_REJECTED`, land blocked |
| info | `oklch(0.460 0.120 205)` | `oklch(0.780 0.110 200)` | lease held & healthy, session live, mirror in flight |
| neutral | `--ink` on `--s3` | `--ink` on `--s3` | `Superseded`, claim released, none (struck icon) |

### Light scheme

| Token | OKLCH | Role (Y) |
|---|---|---|
| `--paper` | `oklch(0.985 0.004 264)` | app canvas (0.956) |
| `--s1` | `oklch(0.950 0.006 264)` | raised surface: cards, panels (0.860) |
| `--s2` | `oklch(0.925 0.007 264)` | sunken well: diff body, code, stripes (0.791) |
| `--s3` | `oklch(0.900 0.008 264)` | inset floor, hunk headers, disabled fills (0.729) |
| `--ink` | `oklch(0.360 0.020 264)` | primary text (0.0466) |
| `--ink2` | `oklch(0.500 0.022 264)` | secondary text, line numbers (0.125) |
| `--ink3` | `oklch(0.600 0.020 264)` | icons, ≥18 px/bold tertiary — 3:1 tier (0.216) |
| `--accent` | `oklch(0.500 0.130 235)` | links, focus ring, selection (0.134) |
| `--accent-strong` | `oklch(0.450 0.150 235)` | primary button fill (0.103) |
| `--accent-soft` | `oklch(0.920 0.030 235)` | row-hover / selected wash |
| `--ok-tint` | `oklch(0.920 0.030 160)` | status chip/row fill (0.788) |
| `--warn-tint` | `oklch(0.920 0.030 78)` | status chip/row fill (0.777) |
| `--fail-tint` | `oklch(0.920 0.030 38)` | status chip/row fill (0.771) |
| `--info-tint` | `oklch(0.920 0.030 205)` | status chip/row fill (0.788) |

### Dark scheme

| Token | OKLCH | Role (Y) |
|---|---|---|
| `--paper` | `oklch(0.185 0.010 264)` | app canvas (0.0063) |
| `--s1` | `oklch(0.235 0.012 264)` | raised surface (0.0130) |
| `--s2` | `oklch(0.275 0.013 264)` | sunken well (0.0208) |
| `--s3` | `oklch(0.310 0.014 264)` | inset floor, disabled (0.0300) |
| `--ink` | `oklch(0.920 0.008 264)` | primary text (0.779) |
| `--ink2` | `oklch(0.740 0.014 264)` | secondary text (0.405) |
| `--ink3` | `oklch(0.640 0.016 264)` | icons/tertiary — passes AA (0.262) |
| `--accent` | `oklch(0.760 0.140 235)` | links, focus ring (0.450) |
| `--accent-strong` | `oklch(0.780 0.130 235)` | primary button fill (0.486) |
| `--accent-soft` | `oklch(0.310 0.040 235)` | row-hover / selected wash |
| `--ok-tint` | `oklch(0.300 0.045 165)` | status fill (0.0285) |
| `--warn-tint` | `oklch(0.300 0.045 82)` | status fill (0.0267) |
| `--fail-tint` | `oklch(0.300 0.045 40)` | status fill (0.0258) |
| `--info-tint` | `oklch(0.300 0.045 200)` | status fill (0.0283) |

**Rules:** status-colored text always sits on its own `--*-tint` chip or row
(it is a data glyph, not body copy). Reading text uses only `--ink`/`--ink2`;
`--ink3` is reserved for icons, gutters, and ≥18 px or ≥600-weight text.
Status text hues: light 38/78/160/205, dark 40/82/165/200 — adjacent gaps
≥ 40°. Neutrals share hue 264 with chroma ≤ 0.020 (near-achromatic).

### Computed contrast (WCAG, `(Y₁+0.05)/(Y₂+0.05)`, floored)

| Pair | Light | Dark |
|---|---|---|
| `--ink` on `--paper` / `--s1` | 10.4 / 9.4 | 14.7 / 13.1 |
| `--ink2` on `--paper` / `--s1` / `--s2` | 5.7 / 5.2 / 4.8 | 8.0 / 7.2 / 6.4 |
| `--ink3` on `--s1` (tier: icons/large) | 3.4 | 4.9 |
| `--accent` on `--paper` / `--s1` | 5.4 / 4.9 | 8.8 / 7.9 |
| `--ok` on `--ok-tint` | 5.9 | 6.1 |
| `--warn` on `--warn-tint` | 6.2 | 6.7 |
| `--fail` on `--fail-tint` | 5.7 | 5.2 |
| `--info` on `--info-tint` | 5.0 | 6.9 |
| button label `--paper` on `--accent-strong` | 6.5 | 9.5 |
| neutral badge `--ink` on `--s3` | 8.0 | 10.3 |

All reading and status pairs ≥ 4.5:1 except the declared `--ink3` 3:1 tier
(light). Method: achromatic Y = L³ exactly; chromatic Y via the
OKLab→linear-sRGB matrices. Recompute with `culori` before token lock.

## Typography

- **Display:** `Space Grotesk` 500/700 — page titles, repo names, change IDs.
  `letter-spacing: -0.01em` only at ≥ 20 px.
- **Text:** `Source Sans 3` 400/600 — everything else; stays legible at 13 px.
- **Mono:** `IBM Plex Mono` 400/500/600 — code, diffs, OIDs, and every
  numeric in data tables (`font-feature-settings: "tnum" 1`, right-aligned).
- Bare stacks only, self-hosted in production, zero network loads in the
  specimen: `"Space Grotesk","Avenir Next",sans-serif` /
  `"Source Sans 3","Segoe UI",Arial,sans-serif` /
  `"IBM Plex Mono","SF Mono",Consolas,monospace`. No `system-ui`, no Inter.

## Spacing (subset of 4/8/12/16/24/32/48/64)

Use `{4, 8, 12, 16, 24, 32, 48}`. 64 is omitted: if 48 is not enough the
layout is wrong, not the scale. Cell insets 4/8; row gaps 8/12; component
padding 12/16; section gaps 24; region breaks 32/48. Heights (24/28/32/48 px
rows, nav, controls) are density constants, not spacing rhythm.

## Radius

`--r1: 4px` (badges, chips) · `--r2: 8px` (buttons, inputs, code) ·
`--r3: 12px` (cards, tables, panels) · `--r4: 16px` (page-level regions).
**Nesting rule: inner radius = outer radius − padding.** Valid pairs with
subset paddings: `--r4` ⊃ `--r3` at 4 · `--r4` ⊃ `--r2` at 8 · `--r3` ⊃
`--r2` at 4 · `--r3` ⊃ `--r1` at 8 · `--r2` ⊃ `--r1` at 4. Full-bleed children
(tables, wells) are exempt — they clip to the parent's radius via
`overflow: hidden` and take no radius of their own.

## Shadow recipes (elevation is the depth system; borders are never used for depth)

All shadows are two-layer transparent stacks. Light uses cool near-black at
low alpha; dark uses black at high alpha plus a 1 px inset top highlight —
the Fluent fake-ambient-light trick, still not a border.

| Token | Light | Dark |
|---|---|---|
| `--e0` well | `inset 0 1px 3px oklch(0.25 0.015 264 / 0.10)` | `inset 0 1px 3px oklch(0.10 0.010 264 / 0.30)` |
| `--e1` raised | `0 1px 2px oklch(0.25 0.015 264 / 0.08), 0 2px 8px -2px oklch(0.25 0.015 264 / 0.10)` | `0 1px 2px oklch(0.10 0.010 264 / 0.28), 0 2px 8px -2px oklch(0.10 0.010 264 / 0.34)` |
| `--e2` overlay | `0 2px 4px oklch(0.25 0.015 264 / 0.10), 0 8px 24px -8px oklch(0.25 0.015 264 / 0.14)` | `0 2px 4px oklch(0.10 0.010 264 / 0.32), 0 8px 24px -8px oklch(0.10 0.010 264 / 0.40)` |
| `--hi` highlight | `inset 0 1px 0 oklch(1 0 0 / 0)` | `inset 0 1px 0 oklch(1 0 0 / 0.07)` |

Cards and panels compose `box-shadow: var(--e1), var(--hi)`. A uniform
`shadow-md` is banned; every surface names its recipe.

## Motion budget

One easing for everything: `cubic-bezier(0.22, 1, 0.36, 1)` (ease-out-quint —
fast attack, long settle; never bounce/elastic). Durations: **90 ms** micro
(hover fills, badge presses) · **140 ms** standard (elevation, focus ring,
flash slide ≤ 8 px) · **200 ms** ceiling (height/opacity on `<details>`
indicators). Only named properties ever transition: `background-color`,
`color`, `box-shadow`, `opacity`, `outline-color`, `transform`.
`transition: all` is banned. Every transition lives inside
`@media (prefers-reduced-motion: no-preference)`; reduced motion = zero
transitions, instant state.

## Component primitives

Shared interaction contract: hover **only** inside `@media (hover: hover)`;
`:focus-visible` = 2 px `--accent` outline, 2 px offset, never suppressed;
`:active` = `translateY(1px)` + one shade deeper fill; `[disabled]` /
`aria-disabled` = `--s3` fill, `--ink3` text, no shadow, `cursor:
not-allowed`, never information-bearing.

- **Top nav** — 48 px, `--s1` + `--e1`, product mark left, breadcrumb center,
  agent-count and user badges right; sticky (server-rendered
  `position: sticky`, no JS).
- **Breadcrumb** — `nav[aria-label=breadcrumb] > ol`; `/` separators via
  `li+li::before`; parents `--ink2` → `--ink` on hover; current page
  `aria-current="page"`, `--ink`, 600.
- **Data table** — `<table class=data>`, 28 px rows, cells `4px 12px`, header
  11 px caps `--ink2` `+0.06em` tracking; numerics mono `tnum` right-aligned;
  row hover `--accent-soft`; selected row `--accent-soft` + `inset 2px 0 0
  var(--accent)` indicator (a shadow, not a border).
- **Tree listing** — rows in a data table: type icon (inline SVG `--ink2`),
  dirs `--ink` 600, files `--ink` 400, last-commit `--ink2` truncated, age
  mono `--ink2` right.
- **Diff view** — `<table class=diff>` per file: 3.5 ch mono gutter `--ink2`
  on `--s2` (`user-select: none`), `+`/− marker column, code 13 px Plex Mono;
  add rows `--ok-tint`, del rows `--fail-tint`, context `--s2`, hunk headers
  `--s3` with `--ink` `@@` range; changed tokens get 600 weight, not a second
  color.
- **Code view** — line-numbered rows over the `--s2` well (`--e0`), line
  hover `--s3`, blame column mono `--ink2`.
- **State badge** — `<span class="badge">` = 14 px inline SVG icon
  (`aria-hidden`) + 11 px/600 label on `--*-tint`, radius `--r1`, padding
  `4px 8px`, gap 4 px. Icon+label+fill triple encoding — never color alone.
  Icons: ok=check, warn=warning-diamond, fail=octagon, info=pulse, neutral=
  struck seal, claim=lock, mirror=loop.
- **Buttons** — heights 28 px (form) / 24 px (in-table); primary `--accent-
  strong` fill + `--paper` label, `--r2`, `--e1`; secondary `--s2` fill +
  `--ink`, hover lifts `--e1`→`--e2` (shadow change only, no color shift).
- **Inputs** — `--s2` well + `--e0` inset, `--r2`, 32 px, `4px`-inner padding;
  `:focus-visible` ring + well lightens to `--s1`; `::placeholder` `--ink2`;
  invalid = `--fail` text + `aria-invalid`.
- **Flash** — `--e2` bar, role-tinted fill + role icon + `--ink` text +
  `<button>` dismiss; `role="status"` / `"alert"`.
- **Empty state** — `--s1` card, 32 px `--ink3` icon, `--ink` 600 title,
  `--ink2` body naming *what creates content*, one primary button.
- **Error state** — `--fail-tint` icon chip, `E_*` code in mono
  (`E_STALE`, `E_LEASE_LOST`) + plain-language line + retry `<form>` POST.

## Density

Base 14 px / 1.45; tables and tree 13 px; diff/code 13 px mono. Rows 28 px,
table header 24 px, badge 24 px. Content columns: code/diff full width of a
max-1440 px canvas; README/prose 72 ch; forms 52 ch. Control-room rule:
every numeric column is mono + `tnum` + right-aligned; every time column is
relative mono (`4m`, `2h`) with absolute `title`; every state is an
icon+label badge — glancable at 2 m.

## Compliance audit

- No gradients (zero `gradient` anywhere); no `transition: all`; no uniform
  `shadow-md` (four named recipes); no emoji icons (inline SVG only); no
  external network loads; no purple/blue-pink families; accent hue 235 blue,
  30° from info 205; status hues ≥ 40° apart; no pure red/green/blue.
- Contrast: see the computed table above — every reading/status pair ≥ 4.5:1
  except the declared `--ink3` 3:1 tier (light 3.4).
- Blocker note: this artifact and its HTML specimen were emitted inline
  (no write tool in the authoring environment); persist at the paths given
  by the orchestrator.
````

---

## Artifact 2 — `design-direction-instrument.html`

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>rukie — Instrument direction specimen</title>
<style>
/* ---- tokens -------------------------------------------------------- */
:root{
  color-scheme: light dark;
  --r1:4px; --r2:8px; --r3:12px; --r4:16px;
  --ease:cubic-bezier(0.22,1,0.36,1);
  --paper:oklch(0.985 0.004 264); --s1:oklch(0.950 0.006 264);
  --s2:oklch(0.925 0.007 264);   --s3:oklch(0.900 0.008 264);
  --ink:oklch(0.360 0.020 264);  --ink2:oklch(0.500 0.022 264);
  --ink3:oklch(0.600 0.020 264);
  --accent:oklch(0.500 0.130 235); --accent-strong:oklch(0.450 0.150 235);
  --accent-soft:oklch(0.920 0.030 235);
  --ok:oklch(0.440 0.095 160);   --ok-tint:oklch(0.920 0.030 160);
  --warn:oklch(0.440 0.110 78);  --warn-tint:oklch(0.920 0.030 78);
  --fail:oklch(0.470 0.140 38);  --fail-tint:oklch(0.920 0.030 38);
  --info:oklch(0.460 0.120 205); --info-tint:oklch(0.920 0.030 205);
  --e0:inset 0 1px 3px oklch(0.25 0.015 264 / 0.10);
  --e1:0 1px 2px oklch(0.25 0.015 264 / 0.08),0 2px 8px -2px oklch(0.25 0.015 264 / 0.10);
  --e2:0 2px 4px oklch(0.25 0.015 264 / 0.10),0 8px 24px -8px oklch(0.25 0.015 264 / 0.14);
  --hi:inset 0 1px 0 oklch(1 0 0 / 0);
}
@media (prefers-color-scheme: dark){
  :root{
    --paper:oklch(0.185 0.010 264); --s1:oklch(0.235 0.012 264);
    --s2:oklch(0.275 0.013 264);   --s3:oklch(0.310 0.014 264);
    --ink:oklch(0.920 0.008 264);  --ink2:oklch(0.740 0.014 264);
    --ink3:oklch(0.640 0.016 264);
    --accent:oklch(0.760 0.140 235); --accent-strong:oklch(0.780 0.130 235);
    --accent-soft:oklch(0.310 0.040 235);
    --ok:oklch(0.740 0.120 165);   --ok-tint:oklch(0.300 0.045 165);
    --warn:oklch(0.780 0.130 82);  --warn-tint:oklch(0.300 0.045 82);
    --fail:oklch(0.720 0.160 40);  --fail-tint:oklch(0.300 0.045 40);
    --info:oklch(0.780 0.110 200); --info-tint:oklch(0.300 0.045 200);
    --e0:inset 0 1px 3px oklch(0.10 0.010 264 / 0.30);
    --e1:0 1px 2px oklch(0.10 0.010 264 / 0.28),0 2px 8px -2px oklch(0.10 0.010 264 / 0.34);
    --e2:0 2px 4px oklch(0.10 0.010 264 / 0.32),0 8px 24px -8px oklch(0.10 0.010 264 / 0.40);
    --hi:inset 0 1px 0 oklch(1 0 0 / 0.07);
  }
}
/* ---- base ------------------------------------------------------------ */
*{box-sizing:border-box}
body{margin:0;background:var(--paper);color:var(--ink);
  font:400 14px/1.45 "Source Sans 3","Segoe UI",Arial,sans-serif}
h1,h2,h3,.disp{font-family:"Space Grotesk","Avenir Next",sans-serif;
  font-weight:600;margin:0;letter-spacing:-0.01em}
.mono,code,pre{font-family:"IBM Plex Mono","SF Mono",Consolas,monospace}
.num{font-feature-settings:"tnum" 1;font-variant-numeric:tabular-nums;text-align:right}
a{color:var(--accent);text-decoration:none}
@media (hover:hover){a:hover{text-decoration:underline}}
:focus-visible{outline:2px solid var(--accent);outline-offset:2px;border-radius:var(--r1)}
svg.ic{width:14px;height:14px;stroke:currentColor;fill:none;stroke-width:2;
  stroke-linecap:round;stroke-linejoin:round;flex:none}
main{max-width:1440px;margin:0 auto;padding:24px 32px 48px}
section{margin-bottom:48px}
.mut{color:var(--ink2)}
.card{background:var(--s1);border-radius:var(--r3);box-shadow:var(--e1),var(--hi)}
.well{background:var(--s2);border-radius:var(--r2);box-shadow:var(--e0)}
.scene{font:600 11px/1 "IBM Plex Mono",monospace;letter-spacing:.14em;
  text-transform:uppercase;color:var(--ink2);margin:0 0 12px}
hr{border:0;margin:0}
/* ---- nav + breadcrumb ------------------------------------------------- */
.topnav{position:sticky;top:0;z-index:10;height:48px;display:flex;align-items:center;
  gap:16px;padding:0 24px;background:var(--s1);box-shadow:var(--e2)}
.brand{display:flex;align-items:center;gap:8px;font:700 15px "Space Grotesk",sans-serif;
  color:var(--ink);letter-spacing:.02em}
.crumbs ol{list-style:none;display:flex;gap:8px;margin:0;padding:0;font-size:13px}
.crumbs li{display:flex;gap:8px;align-items:center}
.crumbs li+li::before{content:"/";color:var(--ink3)}
.crumbs a{color:var(--ink2)}
@media (hover:hover){.crumbs a:hover{color:var(--ink)}}
.crumbs [aria-current]{color:var(--ink);font-weight:600}
/* ---- badges: icon + label + tint, never color alone ------------------- */
.badge{display:inline-flex;align-items:center;gap:4px;padding:4px 8px;border-radius:var(--r1);
  font-size:11px;font-weight:600;line-height:16px;white-space:nowrap}
.b-ok{background:var(--ok-tint);color:var(--ok)}
.b-warn{background:var(--warn-tint);color:var(--warn)}
.b-fail{background:var(--fail-tint);color:var(--fail)}
.b-info{background:var(--info-tint);color:var(--info)}
.b-mut{background:var(--s3);color:var(--ink)}
/* ---- buttons / inputs -------------------------------------------------- */
.btn{display:inline-flex;align-items:center;gap:6px;height:28px;padding:0 12px;
  border:0;border-radius:var(--r2);font:600 13px "Source Sans 3",sans-serif;
  cursor:pointer;box-shadow:var(--e1)}
.btn-sm{height:24px;padding:0 8px;font-size:12px}
.btn-p{background:var(--accent-strong);color:var(--paper)}
.btn-s{background:var(--s2);color:var(--ink)}
.btn:active{transform:translateY(1px)}
.btn[disabled],.btn[aria-disabled=true]{background:var(--s3);color:var(--ink3);
  box-shadow:none;cursor:not-allowed}
.input{height:32px;padding:0 12px;border:0;border-radius:var(--r2);
  background:var(--s2);color:var(--ink);font:inherit;width:280px;box-shadow:var(--e0)}
.input::placeholder{color:var(--ink2)}
/* ---- tables ------------------------------------------------------------ */
table.data{width:100%;border-collapse:collapse;font-size:13px}
table.data th{font:600 11px/24px "Source Sans 3",sans-serif;letter-spacing:.06em;
  text-transform:uppercase;color:var(--ink2);text-align:left;padding:0 12px}
table.data td{padding:4px 12px;height:28px;vertical-align:middle}
table.data td.num{font-size:12.5px}
/* ---- flash -------------------------------------------------------------- */
.flash{display:flex;gap:8px;align-items:center;padding:8px 16px;border-radius:var(--r2);
  margin-bottom:16px;box-shadow:var(--e2)}
.flash p{margin:0;font-size:13px}
/* ---- repo page ------------------------------------------------------------ */
.repo-head{display:flex;align-items:center;gap:12px;margin:8px 0 16px;flex-wrap:wrap}
.repo-head h1{font-size:24px}
.tree td:first-child{width:38%}
.tree .fn{display:flex;gap:8px;align-items:center}
.tree .dir{font-weight:600}
.tree svg{color:var(--ink2)}
.readme{padding:16px 24px;font-size:14px}
.readme h2{font-size:20px;margin-bottom:8px}
.readme p{margin:0 0 8px;max-width:72ch}
.readme code{background:var(--s2);border-radius:var(--r1);padding:1px 4px;font-size:12.5px}
/* ---- diff ------------------------------------------------------------------ */
.diff{width:100%;border-collapse:collapse;font-size:13px;line-height:20px}
.diff td{padding:0 8px;white-space:pre}
.diff .ln{width:1%;min-width:3.5ch;text-align:right;color:var(--ink2);
  background:var(--s2);user-select:none;font-size:11.5px}
.diff .mk{width:1%;text-align:center;color:var(--ink2);user-select:none}
.diff tr.add td.code{background:var(--ok-tint)} .diff tr.add td.mk{color:var(--ok);background:var(--ok-tint)}
.diff tr.del td.code{background:var(--fail-tint)} .diff tr.del td.mk{color:var(--fail);background:var(--fail-tint)}
.diff tr.ctx td.code{background:var(--s2)}
.diff tr.hunk td{background:var(--s3);color:var(--ink);font-size:11.5px;padding:4px 8px}
.diff .tok{font-weight:600}
/* ---- attestation list -------------------------------------------------------- */
.attest>div{display:grid;grid-template-columns:auto 1fr auto;gap:4px 16px;align-items:center;
  padding:8px 0;font-size:13px}
/* ---- motion: named props, one easing, gated ------------------------------------ */
@media (prefers-reduced-motion: no-preference){
  .btn{transition:box-shadow .14s var(--ease),transform .09s var(--ease),background-color .09s var(--ease)}
  table.data tbody tr{transition:background-color .09s var(--ease)}
  .badge{transition:background-color .14s var(--ease)}
  details .chev{transition:transform .14s var(--ease)}
}
@media (prefers-reduced-motion: reduce){*{transition:none !important}}
details summary{cursor:pointer;list-style:none;display:flex;gap:8px;align-items:center}
details summary::-webkit-details-marker{display:none}
details[open] .chev{transform:rotate(90deg)}
@media (hover:hover){
  table.data tbody tr:hover{background:var(--accent-soft)}
  .btn-s:hover{box-shadow:var(--e2)}
  .btn-p:hover{box-shadow:var(--e2)}
}
</style>
</head>
<body>

<header class="topnav">
  <span class="brand"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 6h16M4 12h10M4 18h16"/></svg>rukie</span>
  <nav class="crumbs" aria-label="breadcrumb"><ol>
    <li><a href="#">fleet</a></li>
    <li><a href="#">compiler-infra</a></li>
    <li><a href="#" aria-current="page">llrt</a></li>
  </ol></nav>
  <span style="flex:1"></span>
  <span class="badge b-info"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M3 12h4l2-6 4 12 2-6h6"/></svg>14 agents live</span>
  <span class="badge b-mut"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="8" r="4"/><path d="M4 21c1.5-4 5-6 8-6s6.5 2 8 6"/></svg>ops&#8209;lead</span>
</header>

<main>

<!-- ============ A · repository page: tree + README ============ -->
<section aria-labelledby="s-a">
<p class="scene" id="s-a">A · Repository — file tree + README</p>
<div class="flash" style="background:var(--info-tint)">
  <svg class="ic" viewBox="0 0 24 24" style="color:var(--info)" aria-hidden="true"><path d="M3 12h4l2-6 4 12 2-6h6"/></svg>
  <p>Mirror to <span class="mono">github.com/rukie-mirrors/llrt</span> synced
  <span class="mono num">38s</span> ago · next push batch in <span class="mono num">90s</span></p>
</div>

<div class="repo-head">
  <h1 class="disp">llrt</h1>
  <span class="badge b-ok"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M5 13l4 4 10-10"/></svg>attested · 3 signers</span>
  <span class="badge b-info"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><rect x="4" y="10" width="16" height="10" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3"/></svg>claim: merge-queue</span>
  <span class="mut mono" style="font-size:12px">main @ e9f2c1a · sha1</span>
  <span style="flex:1"></span>
  <button class="btn btn-s" type="button"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3v12m0 0l-4-4m4 4l4-4M4 21h16"/></svg>Clone</button>
  <button class="btn btn-p" type="button">Open workspace</button>
</div>

<div class="card" style="margin-bottom:24px">
<table class="data" aria-label="Files in llrt on branch main">
<thead><tr><th scope="col">Name</th><th scope="col">Last change</th><th scope="col" style="text-align:right">Age</th><th scope="col" style="text-align:right">Size</th></tr></thead>
<tbody>
<tr><td><span class="fn dir"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg><a href="#">src</a></span></td>
<td class="mut">agent heron-7: fold scheduler queue into lane</td><td class="num mut mono">6m</td><td class="num mut mono">—</td></tr>
<tr><td><span class="fn dir"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg><a href="#">crates/ruk-git</a></span></td>
<td class="mut">mira: pack v3 ingest backpressure</td><td class="num mut mono">41m</td><td class="num mut mono">—</td></tr>
<tr><td><span class="fn"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M6 3h8l4 4v14H6z"/><path d="M14 3v4h4"/></svg><a href="#">Cargo.toml</a></span></td>
<td class="mut">agent osprey-2: bump workspace deps</td><td class="num mut mono">2h</td><td class="num mut mono">3.1 KB</td></tr>
<tr><td><span class="fn"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M6 3h8l4 4v14H6z"/><path d="M14 3v4h4"/></svg><a href="#">README.md</a></span></td>
<td class="mut">ops-lead: revise agent quickstart</td><td class="num mut mono">1d</td><td class="num mut mono">8.4 KB</td></tr>
</tbody></table>
</div>

<div class="card readme" aria-label="README">
  <h2>llrt</h2>
  <p>A low-latency runtime for lane-parallel agent commits on ruk. Publishes
  staged objects and ref transactions through the per-repo publication lane;
  attestations gate <code>land.execute</code>.</p>
  <p class="mut">Quickstart: <code>ruk forgejo serve --config fleet.toml</code>,
  then point any agent session at <code>ws.take</code>.</p>
</div>
</section>

<hr aria-hidden="true">

<!-- ============ B · change review: diff + attestations ============ -->
<section aria-labelledby="s-b">
<p class="scene" id="s-b">B · Change review — unified diff + attestations</p>
<div class="repo-head">
  <h1 class="disp" style="font-size:20px">qvtrnksx — fold scheduler queue into lane</h1>
  <span class="badge b-warn"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3l10 18H2z"/><path d="M12 10v4m0 3v.5"/></svg>1 requirement unmet</span>
  <span class="mut mono" style="font-size:12px">e9f2c1a → 3b77d90</span>
  <span style="flex:1"></span>
  <button class="btn btn-s" type="button">Request changes</button>
  <button class="btn btn-p" type="button" disabled aria-disabled="true" title="attestation requirement unmet">Land</button>
</div>

<div class="card" style="padding:8px 24px;margin-bottom:16px" aria-label="Attestation requirements">
  <div class="attest">
    <div><span class="badge b-ok"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M5 13l4 4 10-10"/></svg>review:pass</span>
      <span>signed by <span class="mono" style="font-size:12px">reviewer@ed25519:7f3a…</span> · live</span>
      <span class="mut mono num">12m</span></div>
    <div><span class="badge b-ok"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M5 13l4 4 10-10"/></svg>test:pass</span>
      <span>signed by <span class="mono" style="font-size:12px">ci-runner@ed25519:0b1c…</span> · live · <span class="mono num">1,842</span> checks</span>
      <span class="mut mono num">9m</span></div>
    <div><span class="badge b-fail"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 8v5m0 3v.5"/><circle cx="12" cy="12" r="9"/></svg>land:require — gap: NoLiveMatch</span>
      <span class="mut">no trusted signer on exact subject <span class="mono" style="font-size:12px">3b77d90</span> · required by policy</span>
      <span class="mut mono num">—</span></div>
    <div><span class="badge b-mut"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M7 17L17 7M7 7h10v10"/></svg>review:fail</span>
      <span class="mut"><s>superseded by the 12m statement</s></span>
      <span class="mut mono num">2h</span></div>
  </div>
</div>

<div class="card" style="overflow:hidden">
<table class="diff" aria-label="Unified diff of src/lane/queue.rs">
<tbody>
<tr class="hunk"><td colspan="4" class="mono">src/lane/queue.rs @@ -84,7 +84,9 @@ fn drain(&amp;mut self, w: &amp;mut LaneWriter)</td></tr>
<tr class="ctx"><td class="ln mono">84</td><td class="ln mono">84</td><td class="mk"> </td><td class="code mono">    let batch = self.pending.split_off(0);</td></tr>
<tr class="del"><td class="ln mono">85</td><td class="ln mono">  </td><td class="mk">−</td><td class="code mono">    for cand in batch {</td></tr>
<tr class="del"><td class="ln mono">86</td><td class="ln mono">  </td><td class="mk">−</td><td class="code mono">        w.push(cand)<span class="tok">.map_err(QueueErr::Push)?</span>;</td></tr>
<tr class="del"><td class="ln mono">87</td><td class="ln mono">  </td><td class="mk">−</td><td class="code mono">    }</td></tr>
<tr class="add"><td class="ln mono">  </td><td class="ln mono">85</td><td class="mk">+</td><td class="code mono">    <span class="tok">w.push_grouped(&amp;batch, GroupKey::Ref)</span>?;</td></tr>
<tr class="add"><td class="ln mono">  </td><td class="ln mono">86</td><td class="mk">+</td><td class="code mono">    self.last_group = batch.group_key();</td></tr>
<tr class="ctx"><td class="ln mono">88</td><td class="ln mono">87</td><td class="mk"> </td><td class="code mono">    w.flush_linger(FLUSH_MS)</td></tr>
</tbody></table>
</div>
</section>

<hr aria-hidden="true">

<!-- ============ C · agent activity ============ -->
<section aria-labelledby="s-c">
<p class="scene" id="s-c">C · Agent activity — sessions, claims, lanes</p>
<div class="card" style="overflow-x:auto">
<table class="data" aria-label="Live agent sessions across the fleet">
<thead><tr><th scope="col">Agent / session</th><th scope="col">Workspace</th><th scope="col">Last op</th><th scope="col">Claim</th><th scope="col">Attestation</th><th scope="col" style="text-align:right">Idle</th><th scope="col" style="text-align:right">Lease</th><th scope="col"><span class="scene" style="position:absolute;left:-9999px">Actions</span></th></tr></thead>
<tbody>
<tr><td><span class="fn"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="5" width="14" height="14" rx="3"/><path d="M9 12h6"/></svg>heron-7 <span class="mut mono" style="font-size:11.5px">s-0x41f2</span></span></td>
<td class="mono mut" style="font-size:12.5px">ws/merge-queue</td><td class="mono" style="font-size:12.5px">edit</td>
<td><span class="badge b-info"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><rect x="4" y="10" width="16" height="10" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3"/></svg>lane·held</span></td>
<td><span class="badge b-warn"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3l10 18H2z"/><path d="M12 10v4m0 3v.5"/></svg>ambiguous</span></td>
<td class="num mono mut">41s</td><td class="num mono mut">58m</td>
<td><button class="btn btn-s btn-sm" type="button">Tail</button></td></tr>
<tr><td><span class="fn"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="5" width="14" height="14" rx="3"/><path d="M9 12h6"/></svg>osprey-2 <span class="mut mono" style="font-size:11.5px">s-0x41c8</span></span></td>
<td class="mono mut" style="font-size:12.5px">ws/deps-bump</td><td class="mono" style="font-size:12.5px">land.prepare</td>
<td><span class="badge b-mut"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="8"/></svg>none</span></td>
<td><span class="badge b-ok"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M5 13l4 4 10-10"/></svg>live</span></td>
<td class="num mono mut">3m</td><td class="num mono mut">12m</td>
<td><button class="btn btn-s btn-sm" type="button">Tail</button></td></tr>
<tr><td><span class="fn"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="5" width="14" height="14" rx="3"/><path d="M9 12h6"/></svg>kestrel-1 <span class="mut mono" style="font-size:11.5px">s-0x3ff0</span></span></td>
<td class="mono mut" style="font-size:12.5px">ws/docs-refresh</td><td class="mono" style="font-size:12.5px">push.reconcile</td>
<td><span class="badge b-fail"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 8v5m0 3v.5"/><circle cx="12" cy="12" r="9"/></svg>E_STALE</span></td>
<td><span class="badge b-mut"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M7 17L17 7M7 7h10v10"/></svg>superseded</span></td>
<td class="num mono mut">9m</td><td class="num mono mut">expired</td>
<td><button class="btn btn-s btn-sm" type="button">Retry</button></td></tr>
<tr><td><span class="fn"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="5" width="14" height="14" rx="3"/><path d="M9 12h6"/></svg>mira <span class="mut mono" style="font-size:11.5px">s-0x3fe1</span></span></td>
<td class="mono mut" style="font-size:12.5px">human</td><td class="mono" style="font-size:12.5px">attest.add</td>
<td><span class="badge b-mut"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="8"/></svg>none</span></td>
<td><span class="badge b-info"><svg class="ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M3 12h4l2-6 4 12 2-6h6"/></svg>signing</span></td>
<td class="num mono mut">now</td><td class="num mono mut">—</td>
<td><button class="btn btn-s btn-sm" type="button">Tail</button></td></tr>
</tbody></table>
</div>


[31 more lines in resource. Use :1207 to continue]

<div style="display:flex;gap:16px;margin-top:16px;align-items:center;flex-wrap:wrap">
  <input class="input" type="search" placeholder="filter sessions, workspaces, claims…" aria-label="Filter agent activity">
  <button class="btn btn-s" type="button">Filter</button>
  <details>
    <summary><svg class="ic chev" viewBox="0 0 24 24" aria-hidden="true"><path d="M9 6l6 6-6 6"/></svg><span class="mut" style="font-size:13px">show 3 reclaimed sessions</span></summary>
    <p class="mut" style="margin:8px 0 0 24px;font-size:13px">s-0x3fa0 wren-4 · expired lease, reclaimed 2h ago — replayed request id, no-op.</p>
  </details>
</div>

<div class="card" style="margin-top:24px;padding:24px;text-align:center" aria-label="Empty state example">
  <svg class="ic" viewBox="0 0 24 24" style="width:32px;height:32px;color:var(--ink3);margin-bottom:8px" aria-hidden="true"><circle cx="11" cy="11" r="7"/><path d="M21 21l-4.35-4.35"/></svg>
  <p style="margin:0;font-weight:600">No sessions match “kestrel-9”</p>
  <p class="mut" style="margin:4px 0 16px">Sessions appear here when an agent opens <span class="mono" style="font-size:12.5px">session.open</span> against this fleet.</p>
  <button class="btn btn-p" type="button">Open session docs</button>
</div>
</section>

</main>
</body>
</html>
```

---

### Critical Files for Implementation

- `design-direction-instrument.md` — token/spec contract (persist at the absolute path above); the future `web_src/css/` token file and templates consume it.
- `design-direction-instrument.html` — zero-JS reference specimen (persist alongside); becomes the visual-diff baseline.
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/attestation/status.rs` — `Live`/`Ambiguous`/`Superseded` semantics the badge roles encode.
- `/tmp/ruk-model.json` — 47-method protocol surface driving IA (`attest.*`, `claim.*`, `ws.*`, `land.*`, `op.log`, `push.reconcile`).
- `/home/alpha/rewrite/git-renewed/rukie/templates/repo/view_list.tmpl` + `templates/repo/diff/section_unified.tmpl` — the tree/diff shapes the primitives reproduce in the rewrite.