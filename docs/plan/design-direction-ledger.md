Write definitively confirmed impossible from this seat: `read` is my only file tool, `local://` is read-only for me, `ssh://` has no daemon, and `proc://`/`agent://` writes would corrupt sibling agents (explicitly forbidden). The corrected **v3 artifacts** below incorporate every valid advisory fix (button-underline scoping, checkbox sizing, transform removal, spacing-subset compliance, §4/§9 line-number agreement, conservative on-wash ratios, neutral danger-hover border, corrected sRGB column). Persist verbatim to:

- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/design-direction-ledger.md`
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/design-direction-ledger.html`

## Artifact 1 (v3) — `design-direction-ledger.md`

```markdown
# Design Direction — Ledger

A zero-JavaScript, server-rendered UI direction for rukie: a forge for engineers
supervising many concurrent AI agents. Read-heavy surfaces (change-review diffs,
repo trees, logs, agent activity). Primary scene: bright office, mid-afternoon,
fifty reviews in a row — so light theme leads, dark follows via
`prefers-color-scheme`. Post-minimalism: the interface is typeset, not decorated.
One accent, warm tinted neutrals, hairlines instead of shadows, serif display for
identity, sans for work, mono for truth.

## 1. Name & paradigm

**Ledger** — post-minimalist, text-forward. A forge UI that reads like a
well-kept ledger: entries, rulings, attestations. Restrained strategy: exactly
one accent at ≤10% coverage; status colors are functional ink, never decoration.

## 2. Taste anchors

- **sourcehut** — utility, text-forwardness, refusal of decorative chrome.
- **Linear** — density discipline, precise hover/focus states, restrained color.

What Ledger takes: sourcehut's flatness and typographic confidence; Linear's
information density and state polish. What it rejects: sourcehut's austerity
(lack of status affordances) and Linear's sans-only sameness.

## 3. Palette (OKLCH)

Custom warm-tinted ramp — no Tailwind steps. Accent hue 200 (deep teal, "ink
teal"); no purple-blue/purple-pink anywhere; no pure red/green/blue.
sRGB columns are computed approximations for reference; `oklch()` is canonical.

### Light scheme

| Role | Token | Value | ≈ sRGB |
|---|---|---|---|
| page background | `--paper` | `oklch(0.975 0.006 85)` | #F9F6F3 |
| raised surface | `--surface` | `oklch(0.995 0.003 85)` | #FDFCFA |
| sunken / table head / hunk header | `--inset` | `oklch(0.945 0.008 85)` | #EFEDE7 |
| hairline borders | `--line` | `oklch(0.905 0.008 85)` | #E5E2DC |
| control borders (≥3:1) | `--line-strong` | `oklch(0.62 0.02 85)` | #8B867A |
| primary text | `--text-1` | `oklch(0.30 0.015 80)` | #322D25 |
| secondary text | `--text-2` | `oklch(0.45 0.015 80)` | #5A544C |
| tertiary / meta / table-head labels | `--text-3` | `oklch(0.52 0.012 80)` | #6C6861 |
| accent — links, primary action, active nav | `--accent` | `oklch(0.475 0.085 200)` | #006A6F |
| accent hover/active | `--accent-strong` | `oklch(0.42 0.09 200)` | #005B60 |
| accent wash — selected rows, open badge | `--accent-wash` | `oklch(0.93 0.02 200)` | #D9ECED |
| text on accent fills | `--on-accent` | `oklch(0.99 0.003 85)` | #FBFAF7 |

### Dark scheme (`prefers-color-scheme: dark`)

| Role | Token | Value | ≈ sRGB |
|---|---|---|---|
| `--paper` | `oklch(0.235 0.008 80)` | #201E1A |
| `--surface` | `oklch(0.27 0.009 80)` | #292622 |
| `--inset` | `oklch(0.205 0.008 80)` | #1B1A16 |
| `--line` | `oklch(0.36 0.01 80)` | #403D37 |
| `--line-strong` | `oklch(0.55 0.015 80)` | #736E62 |
| `--text-1` | `oklch(0.90 0.012 80)` | #E2DDD5 |
| `--text-2` | `oklch(0.76 0.012 80)` | #B5B0A9 |
| `--text-3` | `oklch(0.68 0.012 80)` | #9C9890 |
| `--accent` | `oklch(0.78 0.09 195)` | #6BCAC9 |
| `--accent-strong` | `oklch(0.84 0.08 195)` | #8ADBDB |
| `--accent-wash` | `oklch(0.30 0.03 200)` | #1B3234 |
| `--on-accent` | `oklch(0.235 0.008 80)` | #201E1A |

### Status colors (functional roles only)

Roles: **success** = checks passed / attested / merged / mirror synced;
**warning** = queued / rate-limited / needs attention, non-blocking;
**danger** = failed checks / rework requested / mirror error. Status color
appears as: text, an 8px status dot, or a ≤10% wash background. Never as a
card border, never as an icon, never decoratively.

| Role | Light text | Dark text | Wash light | Wash dark |
|---|---|---|---|---|
| `--ok` | `oklch(0.45 0.10 155)` ≈#17653C | `oklch(0.77 0.11 160)` ≈#70CA9C | `oklch(0.945 0.025 155)` ≈#E1F2E6 | `oklch(0.33 0.05 160)` ≈#1C3D2C |
| `--warn` | `oklch(0.47 0.09 90)` ≈#6E580F | `oklch(0.80 0.11 90)` ≈#D9BB66 | `oklch(0.945 0.04 95)` ≈#F5EDCF | `oklch(0.33 0.05 95)` ≈#3D3515 |
| `--danger` | `oklch(0.51 0.135 32)` ≈#A54231 | `oklch(0.74 0.13 30)` ≈#F28979 | `oklch(0.945 0.025 35)` ≈#FDE7E2 | `oklch(0.33 0.05 35)` ≈#4C2C24 |

Diff washes: light `--diff-add` `oklch(0.94 0.03 155)` ≈#DCF2E3, `--diff-del`
`oklch(0.94 0.025 30)` ≈#FCE5E2; dark `oklch(0.32 0.05 155)` ≈#1C3A28 /
`oklch(0.32 0.05 30)` ≈#492923.
`color-scheme: light dark` is declared on `:root` so native controls and
scrollbars follow the scheme.

Coverage rule: `--accent` pixels stay ≤10% of any viewport — links, primary
button, active-nav underline, focus outlines, open-badge. Everything else is
neutral ink, hairline, or a functional status color.

## 4. Type

Named families only; no Inter, no `system-ui` shorthand, no webfont loading in
v1 — families resolve locally and fall back gracefully.

| Slot | Family stack | Usage |
|---|---|---|
| Display | `"Source Serif 4", "Source Serif Pro", Georgia, "Times New Roman", serif` | Page titles, repo names, change titles, brand — 600 weight |
| Text | `"Source Sans 3", "Source Sans Pro", "Segoe UI", "Helvetica Neue", Arial, sans-serif` | All UI text — 400/600 |
| Mono | `"Source Code Pro", "SF Mono", ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace` | Diffs, code, IDs, SHAs, durations, all numerals in tables (`tabular-nums`) |

Scale (px): **11** caps labels (`uppercase`, `letter-spacing .05em`, text-3) ·
**12** meta/breadcrumbs/badges · **12.5** diff & code body incl. line numbers ·
**13** controls, table cells, check rows · **14** base/body · **16** lede ·
**22** h1/display · headings inside panels 14–16. Line-height 1.45 body, 1.5
mono. Serif display at 22 gives the ledger identity; everything that works is
sans; everything that is true (diffs, hashes, numbers) is mono.

## 5. Spacing subset

From the allowed set {4,8,12,16,24,32,48,64} Ledger uses **4, 8, 12, 16, 24,
32, 48** — 64 is excluded (dense tool; nothing earns it).

| Step | Used for |
|---|---|
| 4 | inline gaps, badge padding, cell padding-y, dot-to-text gap |
| 8 | control padding-x inside forms, check-row padding-y, flash gap, panel-head padding-y |
| 12 | cell/panel header padding-x, form gaps |
| 16 | panel body padding, section gaps, meta-row gaps |
| 24 | screen padding, block separation, empty-state padding |
| 32 | between major blocks (tree → README → footer of screen) |
| 48 | between specimen screens, page footer offset, line-number column width |

Any other padding/gap/margin value is a defect. (1px/2px lines and outlines
are borders, not spacing; component geometry like the 8px status dot and
32px control height is measured, not spaced.)

## 6. Radius scale

| Token | Value | Used for |
|---|---|---|
| `--r1` | 2px | badges, code chips, log/error detail blocks (status dot is a true circle — a shape, not a radius) |
| `--r2` | 4px | buttons, inputs, selects, textareas, tables, panels, flashes, diff containers |
| `--r3` | 8px | screen-level containers only — reserved, rare |

No `rounded-lg`-class uniform rounding: small radii everywhere, the largest
only at page-section boundaries.

## 7. Shadow policy

**Zero `box-shadow`, zero `drop-shadow`, zero elevation.** Depth is expressed
exclusively by the three-step surface stack (`--inset` < `--paper` <
`--surface`) plus `--line` hairlines. Focus is an `outline`, not a glow.
Popovers/menus, if they ever exist, get a 1px `--line-strong` border — still
no shadow.

## 8. Motion budget

- Standard duration **90ms**; hard cap **120ms** anywhere.
- One easing: `--ease: cubic-bezier(0.25, 1, 0.5, 1)` (ease-out-quart).
- Property whitelist (CSS transitions only): `color`, `background-color`,
  `border-color`, `outline-color`. No transforms anywhere in the stylesheet
  (alignment uses flex/grid, never `transform`), no opacity fades, no
  keyframes, no `transition: all`.
- `@media (prefers-reduced-motion: reduce)` → `transition: none !important`
  on `*`, `*::before`, `*::after`; animations none.

## 9. Component primitives

All hover rules gated `@media (hover: hover)`; link-underline hover is scoped
to `a:not(.btn)` so button-styled anchors never underline. `:focus-visible` =
`outline: 2px solid var(--accent); outline-offset: 1px` (inputs additionally
switch `border-color` to `--accent`). `active` = one step darker background
(no transform). `disabled` = `text-3` ink on `--inset` with `--line` border
and `cursor: not-allowed` (native `disabled`; no opacity wash).

### Nav bar
48px, `--surface` + bottom `--line` hairline. Brand serif 17px text-1. Links
13px text-2; `aria-current="page"` gets text-1 + 2px `--accent` bottom rule.
Right side: search input (form GET, zero-JS) + identity label.

### Breadcrumb
12px, separators `/` in text-3, segments text-2 links, final segment text-1
(non-link). Hover: underline.

### Data table
`border-collapse`, header row `--inset` bg, 11px caps labels, `--line` row
hairlines, cell padding 4/12 (row ≈32px), numeric columns mono +
right-aligned + `tabular-nums`. Hover (hover:hover): row `--inset`. No zebra
striping — hairlines carry the rhythm.

### Tree listing
Table variant (rows ≈28px). Directory names 600 with trailing `/`, files 400,
both text-1 links (no icons). Columns: name / last change (text-2, ellipsis)
/ change id (mono accent link) / size (mono text-3 right).

### Diff view (unified, line-numbered)
Per-file container: `--line` border, r2, head row = mono path + stat badge
(padding 8/12). Table: `48px` old-lineno | `48px` new-lineno | 1fr code;
line numbers right-aligned **`--text-2`** (contrast floor: `--text-3` dips to
4.31:1 on dark `--diff-add`, so line numbers take the stronger tone) with
right hairline, `user-select: none`. Rows: `.hunk` `--inset` + text-3 `@@`;
`.add` `--diff-add`; `.del` `--diff-del`; context unstyled. `+`/`−`/` `
sigils are literal first characters. Horizontal scroll inside container
(`overflow-x: auto`), never wraps.

### Code view
Same table machinery as diff with a single line-number column (`--text-2`);
code on `--paper` (code is content, not furniture). 12.5px mono / 1.5.

### Badges — change state
`r1`, 11px 600, 1px `--line` border, `--surface` bg, text-2, padding 4px.
State fills (status text on matching wash, border stays neutral): `draft`
muted · `open` accent/accent-wash · `queued` warn/warn-wash · `attested`
ok/ok-wash · `rework` danger/danger-wash · `merged` ok/ok-wash ·
`failed` danger/danger-wash. Text-on-wash pairs audited below (5.1–6.5:1).

### Buttons
32px height, r2, 13px 600, padding 4/12.
- **primary**: `--accent` bg + `--on-accent` text; hover/active `--accent-strong`.
- **secondary** (default): `--surface` bg, `--line-strong` border, text-1;
  hover `--inset`; active `--line` bg.
- **danger**: `--danger` bg + `--on-accent`; hover degrades to `--inset` bg +
  `--danger` text with neutral `--line-strong` border (5.2:1, signals caution
  without a colored border).
- disabled: `--inset` bg, text-3, `--line` border.

### Form inputs
32px, r2, `--surface` bg, `--line-strong` border (3.4–3.5:1 vs paper, both
schemes), 13px, placeholder text-3. Checkbox/radio inputs are exempt from the
32px box (`height:auto`). `textarea` auto-height via `rows` attribute,
padding 8. `select` same box. Labels 12px text-2. Submit via real `<form>`
GET/POST — zero-JS by contract.

### Flash messages
Full-width band, r2, 1px `--line` border, wash bg of its status, 8px status
dot (`align-self:center` — no transform) + bold status-colored title +
text-2 detail. Neutral border — the wash and dot carry the meaning.

### Empty / error states
- **Empty**: 1px dashed `--line-strong`, r2, 24px padding, left-aligned;
  h3 text-1 16px, body text-2, one action link. Never "nothing here".
- **Error**: `--danger-wash` panel, r2, bold `--danger` title, text-2 body,
  mono detail block for logs/IDs.

## 10. Density rules

- Content max-width **1180px**, left-aligned single column; no sidebar cards.
- Row heights: nav 48 · data rows ≈32 · tree rows ≈28 · check rows ≈36 ·
  diff lines 19 (12.5px × 1.5).
- Header/meta rows are 12px text-2 with mono IDs; one meta row per entity,
  never a card stack.
- Tables fill container width; panels are single-bordered — no card-in-card,
  no nested borders beyond one level (diff file inside review column is the
  allowed exception).
- ≥40 diff/context lines per viewport at 1080p is the density target.

## 11. Audit

- **Gradients**: none. All fills are flat `oklch()` solids or flat washes.
- **`transition: all`**: absent; whitelisted properties only; no `transform`
  declarations anywhere.
- **Uniform rounded-lg / shadow-md**: absent; radius scale 2/4/8 with usage
  rules; zero box-shadow.
- **Emoji icons**: none. Status = CSS dots; sigils are literal diff
  characters; no icon font.
- **Colored card borders**: none. Every container border is `--line` (or
  `--line-strong` for interactive controls, dashed for empty states). Button
  borders may match their own fill (invisible by construction); the danger
  hover keeps a neutral border.
- **Tailwind ramp / purple-blue / pure primaries**: absent; custom OKLCH,
  accent hue 200, status hues 32/90/155.

### Contrast (WCAG 2.x)

Method: linear-sRGB Y per color — for achromatic values `Y = L³` is exact
(OKLab `l′` coefficients sum to 1.0); for chromatic values the full
OKLab→linear-sRGB transform (l′m′s′ cubes → M2⁻¹, gamut-clamped) was
evaluated. Ratio `(L1+0.05)/(L2+0.05)`. Thresholds: text ≥4.5:1; non-text
UI borders ≥3:1. The first draft overstated several chromatic pairs and
mis-mapped the hex approximations; this table supersedes it and is
internally consistent (darker inks always out-contrast lighter ones on the
same background).

Light scheme:
- body `--text-1` on `--paper` **12.7:1** · on `--surface` **13.5:1**
- `--text-2` on `--paper` **6.9:1** · on `--surface` **7.3:1**
- `--text-3` on `--paper` **5.1:1** · on `--surface` **5.4:1** ·
  on `--inset` (table heads, hunk rows, disabled) **4.7:1**
- accent on `--surface` **6.3:1** · on `--paper` **5.9:1** ·
  `--accent-strong` on `--surface` **7.8:1** · on `--paper` **7.4:1**
- accent on `--accent-wash` **5.2:1**
- `--on-accent` on `--accent` (primary button) **6.2:1** ·
  `--on-accent` on `--danger` (danger button) **6.0:1**
- status text on `--surface`: `--ok` **7.0:1**, `--warn` **6.8:1**,
  `--danger` **6.1:1**
- status text on own wash (badges, flash titles): `--ok` **6.1:1**,
  `--warn` **5.9:1**, `--danger` **5.2:1**
- `--text-2` detail text on status washes (flash bodies) **≥5.9:1**
- `--text-1` on `--diff-add` **11.6:1** · on `--diff-del` **11.4:1**;
  line-number `--text-2` on diff rows **6.3:1**
- non-text `--line-strong` on `--paper` **3.4:1**

Dark scheme:
- body `--text-1` on `--paper` **12.4:1** · on `--surface` **11.2:1**
- `--text-2` on `--paper` **7.8:1** · on `--surface` **7.0:1**
- `--text-3` on `--paper` **5.8:1** · on `--surface` **5.2:1** ·
  on `--inset` **6.2:1** · on `--accent-wash` **4.5:1** (floor; use text-2
  for anything smaller than 12px on dark accent-wash)
- accent on `--surface` **7.8:1** · on `--paper` **8.6:1** ·
  `--accent-strong` on `--surface` **9.5:1**
- accent on `--accent-wash` **7.0:1**
- `--on-accent` on `--accent` (primary button) **8.6:1** ·
  `--on-accent` on `--danger` **6.9:1**
- status text on `--paper`: `--ok` **8.4:1**, `--warn` **8.9:1**,
  `--danger` **6.9:1**
- status text on own wash: `--ok` **6.0:1**, `--warn` **6.5:1**,
  `--danger` **5.1:1**
- `--text-1` on `--diff-add` **9.2:1** · on `--diff-del` **9.6:1**;
  line-number `--text-2` on diff rows **5.6–5.7:1**
- non-text `--line-strong` on `--paper` **3.5:1**

All text pairs ≥4.5:1, control borders ≥3:1, both schemes.
```

## Artifact 2 (v3) — `design-direction-ledger.html`

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>rukie — Ledger direction</title>
<style>
:root{
  color-scheme: light dark;
  --paper: oklch(0.975 0.006 85);
  --surface: oklch(0.995 0.003 85);
  --inset: oklch(0.945 0.008 85);
  --line: oklch(0.905 0.008 85);
  --line-strong: oklch(0.62 0.02 85);
  --text-1: oklch(0.30 0.015 80);
  --text-2: oklch(0.45 0.015 80);
  --text-3: oklch(0.52 0.012 80);
  --accent: oklch(0.475 0.085 200);
  --accent-strong: oklch(0.42 0.09 200);
  --accent-wash: oklch(0.93 0.02 200);
  --on-accent: oklch(0.99 0.003 85);
  --ok: oklch(0.45 0.10 155);
  --ok-wash: oklch(0.945 0.025 155);
  --warn: oklch(0.47 0.09 90);
  --warn-wash: oklch(0.945 0.04 95);
  --danger: oklch(0.51 0.135 32);
  --danger-wash: oklch(0.945 0.025 35);
  --diff-add: oklch(0.94 0.03 155);
  --diff-del: oklch(0.94 0.025 30);
  --ease: cubic-bezier(0.25, 1, 0.5, 1);
  --r1: 2px; --r2: 4px; --r3: 8px;
  --font-display: "Source Serif 4", "Source Serif Pro", Georgia, "Times New Roman", serif;
  --font-text: "Source Sans 3", "Source Sans Pro", "Segoe UI", "Helvetica Neue", Arial, sans-serif;
  --font-mono: "Source Code Pro", "SF Mono", ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
}
@media (prefers-color-scheme: dark){
  :root{
    --paper: oklch(0.235 0.008 80);
    --surface: oklch(0.27 0.009 80);
    --inset: oklch(0.205 0.008 80);
    --line: oklch(0.36 0.01 80);
    --line-strong: oklch(0.55 0.015 80);
    --text-1: oklch(0.90 0.012 80);
    --text-2: oklch(0.76 0.012 80);
    --text-3: oklch(0.68 0.012 80);
    --accent: oklch(0.78 0.09 195);
    --accent-strong: oklch(0.84 0.08 195);
    --accent-wash: oklch(0.30 0.03 200);
    --on-accent: oklch(0.235 0.008 80);
    --ok: oklch(0.77 0.11 160);
    --ok-wash: oklch(0.33 0.05 160);
    --warn: oklch(0.80 0.11 90);
    --warn-wash: oklch(0.33 0.05 95);
    --danger: oklch(0.74 0.13 30);
    --danger-wash: oklch(0.33 0.05 35);
    --diff-add: oklch(0.32 0.05 155);
    --diff-del: oklch(0.32 0.05 30);
  }
}
*{box-sizing:border-box}
body{margin:0;background:var(--paper);color:var(--text-1);
  font:400 14px/1.45 var(--font-text);-webkit-font-smoothing:antialiased}
h1,h2,h3,p,ul,figure{margin:0}
a{color:var(--accent);text-decoration:none}
.mono{font-family:var(--font-mono)}
.visually-hidden{position:absolute;width:1px;height:1px;overflow:hidden;
  clip:rect(0 0 0 0);white-space:nowrap}

/* ---------- chrome ---------- */
.topbar{display:flex;align-items:center;gap:16px;height:48px;padding:0 24px;
  background:var(--surface);border-bottom:1px solid var(--line)}
.brand{font:600 17px var(--font-display);color:var(--text-1)}
.topnav{display:flex;align-items:stretch;height:48px;gap:4px}
.topnav a{display:flex;align-items:center;padding:0 12px;color:var(--text-2);
  font-size:13px;border-bottom:2px solid transparent}
.topnav a[aria-current="page"]{color:var(--text-1);border-bottom-color:var(--accent)}
.search{margin-left:auto}
.topbar .who{color:var(--text-3);font-size:12px}

/* ---------- primitives ---------- */
.btn{display:inline-flex;align-items:center;justify-content:center;height:32px;
  padding:4px 12px;border:1px solid var(--line-strong);border-radius:var(--r2);
  background:var(--surface);color:var(--text-1);font:600 13px var(--font-text);
  cursor:pointer;text-decoration:none}
.btn.primary{background:var(--accent);border-color:var(--accent);color:var(--on-accent)}
.btn.danger{background:var(--danger);border-color:var(--danger);color:var(--on-accent)}
.btn:disabled,.btn[disabled]{background:var(--inset);border-color:var(--line);
  color:var(--text-3);cursor:not-allowed}
input:not([type="checkbox"]):not([type="radio"]),select,textarea{
  height:32px;padding:0 8px;border:1px solid var(--line-strong);
  border-radius:var(--r2);background:var(--surface);color:var(--text-1);
  font:400 13px var(--font-text)}
input[type="checkbox"],input[type="radio"]{height:auto;width:auto}
textarea{height:auto;min-height:48px;padding:8px;width:100%;resize:vertical}
::placeholder{color:var(--text-3)}
input:disabled,select:disabled,textarea:disabled{background:var(--inset);
  border-color:var(--line);color:var(--text-3);cursor:not-allowed}
label{font-size:12px;color:var(--text-2)}

.badge{display:inline-block;padding:4px;border:1px solid var(--line);
  border-radius:var(--r1);background:var(--surface);color:var(--text-2);
  font:600 11px/1 var(--font-text);white-space:nowrap}
.badge.accent{color:var(--accent);background:var(--accent-wash)}
.badge.ok{color:var(--ok);background:var(--ok-wash)}
.badge.warn{color:var(--warn);background:var(--warn-wash)}
.badge.danger{color:var(--danger);background:var(--danger-wash)}

.dot{flex:none;width:8px;height:8px;border-radius:50%;background:var(--text-3)}
.dot.ok{background:var(--ok)} .dot.warn{background:var(--warn)}
.dot.danger{background:var(--danger)} .dot.accent{background:var(--accent)}

.flash{display:flex;gap:8px;align-items:baseline;padding:8px 12px;
  border:1px solid var(--line);border-radius:var(--r2);background:var(--surface);
  font-size:13px;color:var(--text-2)}
.flash .dot{align-self:center}
.flash strong{font-weight:600}
.flash.ok{background:var(--ok-wash)} .flash.ok strong{color:var(--ok)}
.flash.warn{background:var(--warn-wash)} .flash.warn strong{color:var(--warn)}
.flash.danger{background:var(--danger-wash)} .flash.danger strong{color:var(--danger)}

.panel{border:1px solid var(--line);border-radius:var(--r2);background:var(--surface)}
.panel-head{display:flex;align-items:center;gap:12px;padding:8px 12px;
  border-bottom:1px solid var(--line);font:400 12px var(--font-mono);color:var(--text-3)}

.crumbs{font-size:12px;color:var(--text-3)}
.crumbs a{color:var(--text-2)}
.crumbs .cur{color:var(--text-1)}

.table{width:100%;border-collapse:collapse}
.table th{font:600 11px var(--font-text);text-transform:uppercase;
  letter-spacing:.05em;color:var(--text-3);text-align:left;padding:4px 12px;
  background:var(--inset);border-bottom:1px solid var(--line)}
.table td{padding:4px 12px;border-bottom:1px solid var(--line);font-size:13px;
  vertical-align:middle}
.table tr:last-child td{border-bottom:0}
.num{font-family:var(--font-mono);text-align:right;font-variant-numeric:tabular-nums}
.meta{color:var(--text-2);font-size:12px}
.meta .id{font-family:var(--font-mono);color:var(--accent)}

.tree td.name a{color:var(--text-1);font-weight:400}
.tree tr.dir td.name a{font-weight:600}
.tree td.chg{color:var(--text-2);font-size:12px;max-width:340px;
  white-space:nowrap;overflow:hidden;text-overflow:ellipsis}

.checks{list-style:none;padding:0;border:1px solid var(--line);
  border-radius:var(--r2);background:var(--surface)}
.checks li{display:flex;gap:12px;align-items:center;padding:8px 12px;
  border-bottom:1px solid var(--line);font-size:13px}
.checks li:last-child{border-bottom:0}
.checks .t{color:var(--text-1);font-weight:600}
.checks .d{color:var(--text-2)}
.checks .m{margin-left:auto;color:var(--text-3);font:400 12px var(--font-mono)}

.diff-file{border:1px solid var(--line);border-radius:var(--r2);
  overflow:hidden;background:var(--surface)}
.diff-head{display:flex;align-items:center;gap:12px;padding:8px 12px;
  border-bottom:1px solid var(--line);font:400 12px var(--font-mono);
  color:var(--text-1)}
.diff-scroll{overflow-x:auto}
.diff{width:100%;border-collapse:collapse;font:400 12.5px/1.5 var(--font-mono)}
.diff td{padding:0 12px;white-space:pre;color:var(--text-1)}
.diff td.ln{width:1%;min-width:48px;text-align:right;color:var(--text-2);
  border-right:1px solid var(--line);padding:0 8px;user-select:none}
.diff tr.hunk td{background:var(--inset);color:var(--text-3)}
.diff tr.add td{background:var(--diff-add)}
.diff tr.del td{background:var(--diff-del)}

.codeview{width:100%;border-collapse:collapse;font:400 12.5px/1.5 var(--font-mono)}
.codeview td{padding:0 12px;white-space:pre}
.codeview td.ln{width:1%;min-width:48px;text-align:right;color:var(--text-2);
  border-right:1px solid var(--line);padding:0 8px;user-select:none}

.prose{padding:12px 16px;display:grid;gap:12px}
.prose h3{font:600 16px var(--font-display);color:var(--text-1)}
.prose p{color:var(--text-2);max-width:72ch}
.prose .codeview-wrap{border:1px solid var(--line);border-radius:var(--r2);
  overflow-x:auto;background:var(--paper)}

.empty{border:1px dashed var(--line-strong);border-radius:var(--r2);
  padding:24px;background:transparent}
.empty h3{font:600 16px var(--font-display);color:var(--text-1);margin:0 0 4px}
.empty p{color:var(--text-2);margin:0 0 12px;max-width:56ch}

.error-panel{border:1px solid var(--line);border-radius:var(--r2);
  background:var(--danger-wash);padding:12px 16px}
.error-panel h3{font:600 14px var(--font-text);color:var(--danger);margin:0 0 4px}
.error-panel p{color:var(--text-2);margin:0 0 8px}
.error-panel pre{margin:0;padding:8px 12px;border:1px solid var(--line);
  border-radius:var(--r1);background:var(--surface);
  font:400 12px/1.5 var(--font-mono);color:var(--text-1);overflow-x:auto}

/* ---------- layout ---------- */
.screen{max-width:1180px;margin:0 auto;padding:24px;display:grid;gap:16px;
  align-content:start}
.screen+.screen{border-top:1px solid var(--line);padding-top:32px}
.spec{font:600 11px var(--font-text);text-transform:uppercase;
  letter-spacing:.06em;color:var(--text-3)}
.repo-head{display:flex;align-items:baseline;gap:12px;flex-wrap:wrap}
.repo-head h1{font:600 22px var(--font-display)}
.lede{color:var(--text-2);font-size:14px;max-width:72ch}
.row{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
.meta-row{display:flex;gap:16px;flex-wrap:wrap;color:var(--text-2);font-size:12px}
.toolbar{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
.toolbar .spacer{margin-left:auto}
footer.page{max-width:1180px;margin:48px auto 0;padding:16px 24px;
  border-top:1px solid var(--line);color:var(--text-3);font-size:12px}
.signoff{display:grid;gap:8px;max-width:640px}
.signoff .row{gap:12px}
.check-inline{display:flex;gap:8px;align-items:center}

/* ---------- states ---------- */
@media (hover:hover){
  a:not(.btn):hover{text-decoration:underline}
  .topnav a:hover{color:var(--text-1);text-decoration:none}
  .btn:hover{background:var(--inset)}
  .btn.primary:hover{background:var(--accent-strong);border-color:var(--accent-strong)}
  .btn.danger:hover{background:var(--inset);color:var(--danger);
    border-color:var(--line-strong)}
  .table tbody tr:hover td{background:var(--inset)}
}
a:focus-visible,.btn:focus-visible,input:focus-visible,select:focus-visible,
textarea:focus-visible{outline:2px solid var(--accent);outline-offset:1px}
input:focus-visible,select:focus-visible,textarea:focus-visible{border-color:var(--accent)}
.btn:active{background:var(--line)}
.btn.primary:active{background:var(--accent-strong)}

.btn,a,input,select,textarea,.badge{
  transition-property:color,background-color,border-color,outline-color;
  transition-duration:90ms;transition-timing-function:var(--ease)}
@media (prefers-reduced-motion:reduce){
  *,*::before,*::after{transition:none!important;animation:none!important}
}
</style>
</head>
<body>

<header class="topbar">
  <a class="brand" href="#">rukie</a>
  <nav class="topnav" aria-label="Primary">
    <a href="#" aria-current="page">Changes</a>
    <a href="#">Repositories</a>
    <a href="#">Agents</a>
    <a href="#">Mirrors</a>
  </nav>
  <form class="search" role="search" action="#" method="get">
    <input type="search" name="q" placeholder="Search repositories, agents, changes" aria-label="Search">
  </form>
  <span class="who">m.okafor</span>
</header>

<main>

<!-- ============ Screen A — Repository ============ -->
<article class="screen" aria-labelledby="h-repo">
  <p class="spec">Specimen A — Repository</p>
  <nav class="crumbs" aria-label="Breadcrumb">
    <a href="#">rukie</a> / <a href="#">atelier</a> / <span class="cur">rukie</span>
  </nav>

  <div class="repo-head">
    <h1 id="h-repo">rukie</h1>
    <span class="badge accent">public</span>
    <span class="badge ok">mirrored · github</span>
  </div>
  <p class="lede">Zero-JavaScript forge for agentic engineering. Ruk-native

[6673 more lines in resource. Use :2413 to continue]
  <p class="lede">Zero-JavaScript forge for agentic engineering. Ruk-native
    repository backend; every change carries attestations.</p>
  <div class="row">
    <a class="btn primary" href="#">New change</a>
    <a class="btn" href="#">Watch</a>
    <a class="btn" href="#">Mirrors</a>
    <span class="meta">branch <span class="id">main</span> ·
      last change <span class="id">#1287</span> · 2m ago by
      <span class="mono">agent/forge-7</span></span>
  </div>

  <section class="panel" aria-labelledby="h-tree">
    <div class="panel-head" id="h-tree">main · 4,118 objects · 128.4 MiB</div>
    <table class="table tree">
      <caption class="visually-hidden">Repository file tree</caption>
      <thead>
        <tr><th scope="col">Name</th><th scope="col">Last change</th>
            <th scope="col">Change</th><th scope="col" class="num">Size</th></tr>
      </thead>
      <tbody>
        <tr class="dir"><td class="name"><a href="#">crates/</a></td>
          <td class="chg">Split InvocationOpen framing from CommandOpen</td>
          <td><a class="mono" href="#">#1287</a></td><td class="num">—</td></tr>
        <tr class="dir"><td class="name"><a href="#">services/</a></td>
          <td class="chg">Push-mirror reconcile loop for GitLab self-managed</td>
          <td><a class="mono" href="#">#1281</a></td><td class="num">—</td></tr>
        <tr class="dir"><td class="name"><a href="#">templates/</a></td>
          <td class="chg">Ledger: diff line numbers, hunk headers</td>
          <td><a class="mono" href="#">#1274</a></td><td class="num">—</td></tr>
        <tr><td class="name"><a href="#">go.mod</a></td>
          <td class="chg">Drop go-git; ruk is the only backend</td>
          <td><a class="mono" href="#">#1268</a></td><td class="num">10.4 KB</td></tr>
        <tr><td class="name"><a href="#">main.go</a></td>
          <td class="chg">Wire daemon client into web router</td>
          <td><a class="mono" href="#">#1266</a></td><td class="num">1.4 KB</td></tr>
        <tr><td class="name"><a href="#">README.md</a></td>
          <td class="chg">Describe attestation model</td>
          <td><a class="mono" href="#">#1255</a></td><td class="num">10.2 KB</td></tr>
      </tbody>
    </table>
  </section>

  <section class="panel" aria-labelledby="h-readme">
    <div class="panel-head" id="h-readme">README.md</div>
    <div class="prose">
      <h3>rukie</h3>
      <p>A forge for people who review machine work. Agents propose; humans
        attest. Every change page shows the diff, the check list, and the
        attestations side by side — no client-side JavaScript.</p>
      <div class="codeview-wrap">
        <table class="codeview">
          <caption class="visually-hidden">Code view</caption>
          <tbody>
            <tr><td class="ln">1</td><td>[server]</td></tr>
            <tr><td class="ln">2</td><td>APP_NAME = rukie</td></tr>
            <tr><td class="ln">3</td><td>REPO_BACKEND = ruk</td></tr>
            <tr><td class="ln">4</td><td>[mirror]</td></tr>
            <tr><td class="ln">5</td><td>TARGETS = github, gitlab, forgejo</td></tr>
          </tbody>
        </table>
      </div>
    </div>
  </section>
</article>

<!-- ============ Screen B — Change review ============ -->
<article class="screen" aria-labelledby="h-change">
  <p class="spec">Specimen B — Change review</p>
  <nav class="crumbs" aria-label="Breadcrumb">
    <a href="#">rukie</a> / <a href="#">rukie</a> / <a href="#">changes</a> /
    <span class="cur">#1287</span>
  </nav>

  <div class="repo-head">
    <h1 id="h-change">Split InvocationOpen framing from CommandOpen</h1>
    <span class="badge accent">open</span>
  </div>
  <div class="meta-row">
    <span>author <span class="mono">agent/forge-7</span></span>
    <span><span class="mono">+128 −41</span> in 4 files</span>
    <span>branch <span class="id">mirror/receipts</span> → <span class="id">main</span></span>
    <span>2 attestations · checks 4/5</span>
  </div>

  <div class="flash ok"><span class="dot ok"></span>
    <span><strong>Attested</strong> by m.okafor and j.reyes — change meets the
      review bar for merging.</span></div>
  <div class="flash warn"><span class="dot warn"></span>
    <span><strong>Mirror delayed</strong> — github.com rate-limited the push;
      reconcile retries at 15:20 UTC.</span></div>

  <section aria-labelledby="h-checks">
    <p class="spec" id="h-checks" style="margin-bottom:8px">Attestations &amp; checks</p>
    <ul class="checks">
      <li><span class="dot ok"></span>
        <span><span class="t">cargo test</span> <span class="d">— 214 passed, 0 failed in 41s</span></span>
        <span class="m">ci · ruk#4412</span></li>
      <li><span class="dot ok"></span>
        <span><span class="t">go vet + lint</span> <span class="d">— clean</span></span>
        <span class="m">ci · 8s</span></li>
      <li><span class="dot ok"></span>
        <span><span class="t">m.okafor</span> <span class="d">— human attestation, reviewed full diff</span></span>
        <span class="m">14:02</span></li>
      <li><span class="dot ok"></span>
        <span><span class="t">j.reyes</span> <span class="d">— human attestation, spot-checked services/</span></span>
        <span class="m">14:11</span></li>
      <li><span class="dot warn"></span>
        <span><span class="t">license headers</span> <span class="d">— missing year in services/mirror/gitlab.go</span></span>
        <span class="m">lint</span></li>
    </ul>
  </section>

  <section class="diff-file" aria-label="Diff for crates/ruk/src/daemon/ipc.rs">
    <div class="diff-head">crates/ruk/src/daemon/ipc.rs
      <span class="badge ok" style="margin-left:auto">+64 −28</span></div>
    <div class="diff-scroll">
      <table class="diff">
        <caption class="visually-hidden">Unified diff with line numbers</caption>
        <tbody>
          <tr class="hunk"><td class="ln"></td><td class="ln"></td>
            <td>@@ -88,7 +88,10 @@ pub fn open_invocation(</td></tr>
          <tr><td class="ln">88</td><td class="ln">88</td>
            <td> let frame = Frame::read(&amp;mut rx).await?;</td></tr>
          <tr><td class="ln">89</td><td class="ln">89</td>
            <td> match frame.kind {</td></tr>
          <tr class="del"><td class="ln">90</td><td class="ln"></td>
            <td>-    KIND_CMD =&gt; CommandOpen::parse(frame.body),</td></tr>
          <tr class="del"><td class="ln">91</td><td class="ln"></td>
            <td>-    KIND_INVOKE =&gt; CommandOpen::parse(frame.body),</td></tr>
          <tr class="add"><td class="ln"></td><td class="ln">90</td>
            <td>+    KIND_CMD =&gt; CommandOpen::parse(frame.body),</td></tr>
          <tr class="add"><td class="ln"></td><td class="ln">91</td>
            <td>+    KIND_INVOKE =&gt; InvocationOpen::parse(frame.body),</td></tr>
          <tr class="add"><td class="ln"></td><td class="ln">92</td>
            <td>+    other =&gt; Err(FramingError::UnknownKind(other)),</td></tr>
          <tr><td class="ln">92</td><td class="ln">93</td>
            <td> }</td></tr>
          <tr><td class="ln">93</td><td class="ln">94</td>
            <td> }</td></tr>
        </tbody>
      </table>
    </div>
  </section>
  <section class="diff-file" aria-label="Diff for crates/ruk/src/daemon/mod.rs">
    <div class="diff-head">crates/ruk/src/daemon/mod.rs
      <span class="badge ok" style="margin-left:auto">+12 −4</span></div>
    <div class="diff-scroll">
      <table class="diff">
        <tbody>
          <tr class="hunk"><td class="ln"></td><td class="ln"></td>
            <td>@@ -14,4 +14,7 @@ mod ipc;</td></tr>
          <tr><td class="ln">14</td><td class="ln">14</td>
            <td> pub use ipc::{CommandOpen, Frame};</td></tr>
          <tr class="add"><td class="ln"></td><td class="ln">15</td>
            <td>+pub use ipc::{InvocationOpen, FramingError};</td></tr>
          <tr><td class="ln">15</td><td class="ln">16</td>
            <td> pub mod serve;</td></tr>
        </tbody>
      </table>
    </div>
  </section>

  <form class="signoff" action="#" method="post">
    <label for="note">Review note <span class="meta">— recorded with your attestation</span></label>
    <textarea id="note" name="note" rows="4" placeholder="What you verified, what you skipped…"></textarea>
    <label class="check-inline"><input type="checkbox" name="attest" value="1">
      I attest the check list above reflects the state I reviewed.</label>
    <div class="row">
      <button class="btn primary" type="submit" name="do" value="approve">Approve</button>
      <button class="btn danger" type="submit" name="do" value="rework">Request rework</button>
      <button class="btn" type="submit" name="do" value="merge" disabled>Merge — awaiting checks</button>
    </div>
  </form>
</article>

<!-- ============ Screen C — Agent activity ============ -->
<article class="screen" aria-labelledby="h-agents">
  <p class="spec">Specimen C — Agent activity</p>
  <div class="repo-head"><h1 id="h-agents">Agent activity</h1></div>

  <form class="toolbar" action="#" method="get">
    <select name="agent" aria-label="Agent filter">
      <option>All agents</option><option>forge-7</option>
      <option>review-2</option><option>migrator-1</option>
    </select>
    <input type="text" name="q" placeholder="Filter changes…" aria-label="Filter changes">
    <button class="btn" type="submit">Apply</button>
    <span class="spacer"></span>
    <span class="meta">6 of 50 shown · updated 14:31</span>
  </form>

  <div class="flash danger"><span class="dot danger"></span>
    <span><strong>agent/migrator-1</strong> — 3 consecutive check failures on
      #1270..#1272; review before assigning more work.</span></div>

  <section class="panel" aria-labelledby="h-activity">
    <table class="table" id="h-activity">
      <caption class="visually-hidden">Agent activity</caption>
      <thead>
        <tr><th scope="col">Agent</th><th scope="col">Change</th>
            <th scope="col">Status</th><th scope="col" class="num">Lines ±</th>
            <th scope="col" class="num">Attest.</th><th scope="col" class="num">Duration</th>
            <th scope="col">Updated</th></tr>
      </thead>
      <tbody>
        <tr><td class="mono">forge-7</td>
          <td><a href="#">#1287 Split InvocationOpen framing</a></td>
          <td><span class="badge ok">attested</span></td>
          <td class="num">+128 −41</td><td class="num">2</td>
          <td class="num">6m 12s</td><td class="meta">2m ago</td></tr>
        <tr><td class="mono">forge-7</td>
          <td><a href="#">#1286 Mirror receipts for gitlab.com</a></td>
          <td><span class="badge accent">open</span></td>
          <td class="num">+96 −18</td><td class="num">0</td>
          <td class="num">4m 03s</td><td class="meta">11m ago</td></tr>
        <tr><td class="mono">review-2</td>
          <td><a href="#">#1285 Ledger badges: state coverage</a></td>
          <td><span class="badge ok">merged</span></td>
          <td class="num">+42 −9</td><td class="num">1</td>
          <td class="num">2m 48s</td><td class="meta">26m ago</td></tr>
        <tr><td class="mono">migrator-1</td>
          <td><a href="#">#1272 Drop go-git callers, wave 3</a></td>
          <td><span class="badge danger">rework</span></td>
          <td class="num">+210 −388</td><td class="num">0</td>
          <td class="num">9m 31s</td><td class="meta">40m ago</td></tr>
        <tr><td class="mono">migrator-1</td>
          <td><a href="#">#1271 Drop go-git callers, wave 2</a></td>
          <td><span class="badge danger">failed</span></td>
          <td class="num">+188 −355</td><td class="num">0</td>
          <td class="num">8m 02s</td><td class="meta">1h ago</td></tr>
        <tr><td class="mono">forge-7</td>
          <td><a href="#">#1284 Tree endpoint: zero-copy path lookup</a></td>
          <td><span class="badge warn">queued</span></td>
          <td class="num">+58 −12</td><td class="num">0</td>
          <td class="num">—</td><td class="meta">1h ago</td></tr>
      </tbody>
    </table>
  </section>

  <section class="empty">
    <h3>No changes match “docs”</h3>
    <p>Agent forge-7 has nothing in flight under that filter. Clear the filter
      or assign a docs sweep.</p>
    <a class="btn" href="#">Clear filter</a>
  </section>

  <section class="error-panel">
    <h3>Mirror push failed — codeberg.org</h3>
    <p>Change #1285 mirrored to github and gitlab but the forgejo target
      rejected the ref. The reconcile loop will retry; no local state was lost.</p>
    <pre>mirror/forgejo  codeberg.org/atelier/rukie
  ! [remote rejected] main -&gt; main (pre-receive hook declined)
  next retry: 14:45 UTC · attempt 3/5</pre>
  </section>
</article>

</main>

<footer class="page">
  rukie · Ledger direction · zero JavaScript, server-rendered ·
  both schemes via prefers-color-scheme
</footer>

</body>
</html>
```

---

## Final status

- **Write impossible from this seat** (verified): tools are `read`/`grep`/`web_search` only; `local://` read-only for me; `ssh://` no daemon; `proc://`/`agent://` writes forbidden (sibling corruption). Both v3 files are inline above, byte-ready for the absolute paths given by the orchestrator.
- **v3 fixes applied** per valid advisories: `a:not(.btn):hover` + `.btn{text-decoration:none}` (anchor-buttons no longer underline); `input:not([type=checkbox],[type=radio])` sizing split (checkbox no longer 32px); `.checks .dot` transform removed → `align-items:center` on rows; all padding/gap on-subset (cells 4/12, badge 4px, flash gap 8, diff-head 8/12, ln column 48); §4 line numbers moved to the 12.5px mono step matching §9; diff line numbers → `--text-2` (fixes real 4.31:1 dark-on-add failure); danger-button hover keeps neutral `--line-strong` border; corrected sRGB column.
- **Contrast (recomputed, internally consistent)**: light — body 12.7/13.5, secondary 6.9/7.3, tertiary 5.1–5.4, accent-on-surface 6.3, accent-strong 7.8, button pairs 6.0–6.2, status 5.2–7.0; dark — body 11.2/12.4, secondary 7.0/7.8, accent-on-surface 7.8–8.6, button pairs 6.9–8.6, status 5.1–8.9; borders 3.4–3.5. All ≥4.5.
- **HTML audit**: no `<script>`, no `on*` attributes, no transform, no gradients, no `transition: all`, no emoji, no colored card borders, semantic header/nav/main/article/table, both schemes via `prefers-color-scheme`.

### Critical Files for Implementation

- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/design-direction-ledger.md` — target for Artifact 1 above
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/design-direction-ledger.html` — target for Artifact 2 above
- `rukie/docs/guidelines-frontend.md` — legacy Vue/Fomantic/Tailwind stack Ledger replaces
- `rukie/templates/` — Go-template surface the zero-JS Ledger design targets
- `rukie/web_src/css/` — existing CSS-var theming layer Ledger's token model supersedes

[advisor] <advisory advisor="glm-flash" severity="concern" turns_ago="1" guidance="weigh, don't blindly obey"> Stop probing write…

[advisor] <advisory advisor="minimax" severity="blocker" guidance="weigh, don't blindly obey"> Five write paths are now exhausted…