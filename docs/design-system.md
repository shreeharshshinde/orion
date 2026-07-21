# Orion Design System v2
## Constellation — a design language for a system named after stars

---

## 1. Why this exists

The current dashboard works but reads as a generic dark-mode Tailwind template with a teal accent swapped in. This document defines a distinct visual language for Orion, grounded in the one thing that's actually specific to this product: it is named after a constellation, and the existing mascot already draws star-points across the bear. Every decision below either uses that motif deliberately or explains why it doesn't apply.

This is not a re-skin. It changes what the dashboard *is for*: proving, at a glance, that a real distributed system is alive and healthy — not just displaying numbers in boxes.

---

## 2. Color system

Six named values. Not "dark background + accent" — each color has one job and never does a second one.

```css
:root {
  /* Surfaces */
  --void:        #0A0E17;  /* page background — true near-black navy */
  --panel-solid: #131826;  /* card/panel background */
  --panel-edge:  #1E2536;  /* card border, hairline */
  --panel-raised:#161C2C;  /* hover/active surface, one step up */

  /* The one interactive accent — "a star is active" */
  --star:        #22D3EE;
  --star-dim:     rgba(34, 211, 238, 0.14);
  --star-glow:    rgba(34, 211, 238, 0.35);

  /* Category accents — used ONLY for their named concept, never interchangeably */
  --nebula:      #8B7FE8;  /* pipelines / DAGs / orchestration only */
  --nebula-dim:   rgba(139, 127, 232, 0.14);

  --flare:       #FBBF66;  /* retrying / warning / scheduled only */
  --flare-dim:    rgba(251, 191, 102, 0.14);

  --collapse:    #FB7185;  /* failed / dead / error only */
  --collapse-dim: rgba(251, 113, 133, 0.14);

  --drift:       #34D399;  /* completed / success / healthy only */
  --drift-dim:    rgba(52, 211, 153, 0.14);

  /* Text */
  --text-bright:  #F1F5F9;
  --text-body:    #94A3B8;
  --text-faint:   #4B5567;

  /* Environment badge — the ONE place color means "danger level," not concept */
  --env-local:   #64748B;
  --env-staging: #FBBF66;
  --env-prod:    #FB7185;
}
```

**Status color mapping (fixed, never remixed):**

| Job/pipeline status | Color | Rationale |
|---|---|---|
| `queued` | `--text-faint` (gray) | Not happening yet — no color earned |
| `scheduled` | `--flare` dim | About to happen |
| `running` | `--star` | Active — the one thing that gets the accent |
| `completed` | `--drift` | Done, healthy |
| `retrying` | `--flare` | Warning-adjacent, recovering |
| `failed` | `--collapse` dim | Error, but recoverable |
| `dead` | `--collapse` solid | Error, terminal — needs a human |
| `cancelled` | `--text-faint` (gray) | Intentional, not a failure |

**Never:** use `--nebula` for anything except pipeline/DAG UI. Never use `--star` for anything except "this is currently active." If you catch yourself reaching for `--star` to mean "this is important," stop — that's what makes an accent color turn into wallpaper.

---

## 3. Typography

Two typefaces, each with exactly one job.

```css
--font-ui:   'Inter', -apple-system, sans-serif;      /* all chrome: nav, labels, buttons, prose */
--font-data: 'JetBrains Mono', 'SF Mono', monospace;   /* every number, ID, timestamp, status string */
```

**The rule:** if a piece of text is *system output* (a job ID, a duration, a queue depth, a status string, a timestamp, a metric value) it is `--font-data`. If it's *UI chrome* (a nav label, a button, a heading, a description sentence) it is `--font-ui`. This single consistent split is what makes the dashboard read as "real telemetry from a real system" rather than a mockup — the visual distinction between "the system is talking" and "the interface is talking" is the whole point.

Type scale:
```css
--text-xs:   11px;  /* metadata, timestamps */
--text-sm:   13px;  /* body, table cells */
--text-base: 14px;  /* default UI text */
--text-lg:   16px;  /* card titles */
--text-xl:   20px;  /* section headings */
--text-2xl:  28px;  /* hero metric numbers — always --font-data */
--text-3xl:  40px;  /* landing page hero only */
```

---

## 4. Motion principles

One orchestrated moment (the orbit), everything else quiet and functional:

- **Skeleton shimmer**: 1.5s linear, opacity 0.4 → 0.7 → 0.4, `--panel-raised` colored — never a spinning circle
- **Status pulse**: only on `running` status dots, 2s ease-in-out scale(1) → scale(1.15) → scale(1), respects reduced-motion
- **Page transitions**: none — instant, dashboards are for speed not delight
- **Hover**: 120ms background transition only, no scale/shadow theatrics on data rows

---

## 5. Copy principles (applies to empty/error states, badges, buttons)

- Buttons are verb-first: "Submit job", "Retry connection", "Create pipeline" — never "OK" or "Submit"
- Empty states are an invitation: "No jobs yet — submit your first job" with a CTA, never "No data"
- Errors say what happened and what to do, no "Error:" prefix, no apology: "Can't reach the API. Check that orion-api is running on :8080. Retry"
- Never use exclamation points in system copy
- Status words are lowercase to match the actual enum values from the API (`queued`, `running`, not "Queued", "Running") — reinforces that this is real system state, not a designer's paraphrase
