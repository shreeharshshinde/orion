# Phase 11 — Frontend Completion

**Status:** Planned
**Prerequisite:** Phase 10 (Observability Hardening)
**Location:** `frontend/` (Next.js 15, TypeScript, Tailwind, shadcn primitives)

---

## Current State

The frontend is a **polished static mockup**. Every page renders correctly but all data comes from `lib/api.ts` — a hardcoded file with 4 jobs, 2 pipelines, 3 queues, and 3 workers. No `fetch()` call exists anywhere. Interactive controls (Submit Job, Save config, Cancel) are visual dummies.

### What is built ✅

| File | State |
|---|---|
| App shell — sidebar, header, health chip | Static, hardcoded "API ready" |
| Home page | Complete, mock metrics |
| Dashboard overview | Complete layout, mock data |
| `/dashboard/jobs` | Table with filter UI shells, mock data |
| `/dashboard/queues` | Queue cards, depth bars, Save button (no-op), mock data |
| `/dashboard/workers` | Worker cards, capacity bars, mock data |
| `/dashboard/pipelines` | Pipeline cards, linear DAG preview, mock data |
| `/docs` | Link cards pointing to `../docs/*.md` (all `href="#"`) |
| `StatusBadge`, `MetricCard`, `Card`, `Button`, `Badge`, `PageHeader` | Done |

### What is missing ❌

| Gap | Impact |
|---|---|
| No API client — zero `fetch` calls | Every page shows stale mock data |
| No TanStack Query | No polling, caching, or loading/error states |
| No Job Detail page `/dashboard/jobs/[id]` | Can't inspect a job |
| No Submit Job form | Core user action is broken |
| No job cancel / replay actions | Can't operate jobs |
| No Pipeline Detail page `/dashboard/pipelines/[id]` | No DAG graph view |
| No Create Pipeline form | Can't submit pipelines |
| No Observability page | No links to Grafana/Jaeger/Prometheus |
| Docs page links are all `href="#"` | Docs section is non-functional |
| Health chip always shows "API ready" | No real liveness check |
| Queue "Save live config" is a no-op | Can't change queue config |
| No loading / empty / error states on any page | Bad UX on slow or unavailable API |
| No toast notifications | No feedback on mutations |
| `package.json` missing TanStack Query, React Flow, Recharts, Sonner | Key stack not installed |

---

## Installed Stack (today)

```
next, react, react-dom
@radix-ui/react-slot
tailwindcss, tailwind-merge, clsx, class-variance-authority
lucide-react
typescript
```

## Stack to Add

```bash
npm install @tanstack/react-query
npm install sonner                          # toasts
npm install @tanstack/react-table           # jobs table
npm install reactflow                       # pipeline DAG
npm install recharts                        # charts
npm install react-hook-form zod @hookform/resolvers  # forms
```

---

## Implementation Plan

### Step 1 — API Client + TanStack Query setup

**Files:** `frontend/lib/client.ts`, `frontend/lib/hooks.ts`, `frontend/app/providers.tsx`

Replace the mock data file with a typed fetch client. All hooks use TanStack Query for polling, caching, and error handling.

```ts
// lib/client.ts
const BASE = process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";

async function get<T>(path: string): Promise<T> {
  const res = await fetch(`${BASE}${path}`);
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
  return res.json();
}

async function post<T>(path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
  return res.json();
}

async function put<T>(path: string, body: unknown): Promise<T> { ... }
async function del(path: string): Promise<void> { ... }

export const api = { get, post, put, del };
```

```ts
// lib/hooks.ts
export function useJobs(filters?: JobFilter) {
  return useQuery({ queryKey: ["jobs", filters], queryFn: () => api.get<Job[]>("/jobs"), refetchInterval: 5000 });
}
export function useJob(id: string) {
  return useQuery({ queryKey: ["job", id], queryFn: () => api.get<Job>(`/jobs/${id}`), refetchInterval: 3000 });
}
export function useJobExecutions(id: string) {
  return useQuery({ queryKey: ["executions", id], queryFn: () => api.get(`/jobs/${id}/executions`) });
}
export function useWorkers() { ... }
export function usePipelines() { ... }
export function usePipeline(id: string) { ... }
export function useQueues() { ... }
export function useQueueStats(name: string) { ... }
export function useHealth() {
  return useQuery({ queryKey: ["health"], queryFn: () => api.get("/readyz"), refetchInterval: 5000, retry: false });
}
```

Wrap the app in `QueryClientProvider` in `app/providers.tsx`, imported in `app/layout.tsx`.

---

### Step 2 — Wire existing pages to live data

Update each existing page to use hooks instead of the mock import.

**Dashboard overview** — replace `import { jobs, overview, ... } from "@/lib/api"` with `useJobs()`, `useWorkers()`, `useQueues()`, `usePipelines()`. Compute `overview` values client-side from live data.

**Jobs page** — replace mock array with `useJobs(filters)`. Add working filter state (status, queue, type dropdowns using `<select>` or shadcn `Select`). Wire search input to a `name` query param.

**Queues page** — replace mock with `useQueues()` + `useQueueStats(name)` per card. Wire "Save live config" to a `useMutation` calling `PUT /queues/{name}` with a success toast.

**Workers page** — replace mock with `useWorkers()`.

**Pipelines page** — replace mock with `usePipelines()`.

**Health chip** — replace hardcoded `"API ready"` with `useHealth()`. Show `StatusDot` tone based on response: success = ready, danger = down.

Add a shared `<LoadingRows>` skeleton and `<EmptyState>` component used by all table/list pages.

---

### Step 3 — Job Detail page

**File:** `frontend/app/dashboard/jobs/[id]/page.tsx`

Make the jobs table rows `<Link href={/dashboard/jobs/${job.id}}>`. The detail page uses `useJob(id)` and `useJobExecutions(id)`.

Layout: tabs (Overview, Payload, Executions).

**Overview tab:**
- Status badge + large status label
- Timing fields: created, scheduled, started, completed, next_retry_at, deadline
- Queue, priority, attempt / max_retries, worker_id
- Error message panel (shown only when present)
- Cancel button (calls `POST /jobs/{id}/cancel`, disabled on terminal states)
- Replay button (calls `POST /jobs/{id}/replay`, shown only on `dead`/`failed`)

**Payload tab:**
- `<pre>` block with JSON.stringify of `payload`, copy button

**Executions tab:**
- Table: attempt, worker_id, status, started_at, finished_at, exit_code, error
- Sourced from `GET /jobs/{id}/executions`

---

### Step 4 — Submit Job form

**File:** `frontend/components/submit-job-dialog.tsx`

A `<Dialog>` (Radix) opened from the "Submit Job" button in the header and jobs page.

Form sections using `react-hook-form` + `zod`:

```ts
const schema = z.object({
  name: z.string().min(1),
  type: z.enum(["inline", "k8s_job"]),
  queue_name: z.enum(["high", "default", "low"]),
  priority: z.number().int().min(0).max(10).default(5),
  max_retries: z.number().int().min(0).max(10).default(3),
  idempotency_key: z.string().optional(),
  payload: z.object({
    handler_name: z.string().optional(),  // inline
    kubernetes_spec: z.object({ ... }).optional(),  // k8s_job
  }),
});
```

On submit: `POST /jobs`, show success toast with job ID, close dialog, invalidate `["jobs"]` query.

---

### Step 5 — Pipeline Detail + DAG view

**File:** `frontend/app/dashboard/pipelines/[id]/page.tsx`

Uses `usePipeline(id)` and `GET /pipelines/{id}/jobs`.

Layout: React Flow canvas (left) + selected node panel (right).

Node rendering:
- Map each `dag_spec.nodes` entry to a React Flow node
- Map `depends_on` edges to React Flow edges
- Color nodes by job status: pending=muted, running=aqua pulse, completed=green, dead/failed=red

Node click: shows job template, linked job ID (links to Job Detail), status.

Include a "Cancel Pipeline" button (`POST /pipelines/{id}/cancel`) that is disabled on terminal pipelines.

The pipelines list page rows become `<Link href={/dashboard/pipelines/${p.id}}>`.

---

### Step 6 — Create Pipeline form

**File:** `frontend/components/create-pipeline-dialog.tsx`

Two modes toggled by a tab: **Visual builder** and **JSON editor**.

Visual builder: add nodes (id + job template), draw edges by selecting source/target from dropdowns. Live validation: no duplicate IDs, no cycles.

JSON editor: `<textarea>` with syntax hint showing the expected DAG spec shape. Validate with zod on submit.

On submit: `POST /pipelines`, toast, invalidate `["pipelines"]`.

---

### Step 7 — Observability page

**File:** `frontend/app/dashboard/observability/page.tsx`

Static links panel + live health status. No charts (those are Phase 12).

Panels:
- API health: `/healthz` and `/readyz` status chips (live via `useHealth`)
- Grafana: external link to `http://localhost:3000`
- Prometheus: external link to `http://localhost:9090`
- Jaeger: external link to `http://localhost:16686`
- Service env/version from a `GET /healthz` response field (if available)

Configurable via `NEXT_PUBLIC_GRAFANA_URL`, `NEXT_PUBLIC_JAEGER_URL`, `NEXT_PUBLIC_PROMETHEUS_URL` env vars with localhost defaults.

Add "Observability" to the sidebar `navItems` in `app-shell.tsx`.

---

### Step 8 — Docs section

**Files:** `frontend/app/docs/[slug]/page.tsx`, install `next-mdx-remote`

```bash
npm install next-mdx-remote gray-matter
```

Map URL slugs to existing markdown files from the repo. In development, read files from `../../docs/`. In production, copy doc files to `public/docs/` during build.

Slug map:
```ts
const slugMap: Record<string, string> = {
  "architecture":    "docs/architecture/overview.md",
  "runbook":         "docs/RUNBOOK.md",
  "deployment":      "docs/DEPLOYMENT_PLAN.md",
  "adr-001":         "docs/adr/ADR-001-queue-design.md",
  // ...
};
```

Docs layout: left nav with all doc links, main content, auto-generated table of contents from headings. Add `rehype-highlight` for code block syntax.

---

### Step 9 — Loading, empty, and error states

Add to `frontend/components/ui.tsx`:

```tsx
export function SkeletonRow({ cols }: { cols: number }) { ... }   // animated shimmer rows
export function EmptyState({ message, action }: ...) { ... }      // empty table/list state
export function ErrorState({ message, onRetry }: ...) { ... }     // error + retry button
```

Apply to every page:
- While loading: skeleton rows matching the final table column count
- Empty result: `EmptyState` with context-specific message
- Fetch error: `ErrorState` with retry button that calls `refetch()`

---

### Step 10 — Toast notifications

Install `sonner`. Add `<Toaster />` to `app/layout.tsx`.

Call `toast.success(...)` / `toast.error(...)` on every mutation:
- Job submitted → `"Job {name} submitted — {id}"`
- Job cancelled → `"Job cancelled"`
- Job replayed → `"Job re-queued"`
- Queue config saved → `"Queue {name} config updated (live)"`
- Pipeline created → `"Pipeline {name} created"`
- Pipeline cancelled → `"Pipeline cancelled"`
- Any API error → `toast.error(err.message)`

---

---

## Solutions

### Gap 1 — No API client / no fetch calls (Step 1)

**Status:** ✅ Complete
**Date:** 2026-06-15

#### What was done

**Packages installed** (`frontend/package.json`):
```
@tanstack/react-query@^5.62.0
sonner@^1.7.0
@tanstack/react-table@^8.20.0
reactflow@^11.11.4
recharts@^2.14.1
react-hook-form@^7.54.0
zod@^3.24.0
@hookform/resolvers@^3.9.0
```

**`frontend/lib/client.ts`** — Typed fetch client with four methods:
- `api.get<T>(path)` — GET, throws on non-ok
- `api.post<T>(path, body?)` — POST with JSON body
- `api.put<T>(path, body)` — PUT with JSON body
- `api.del(path)` — DELETE, returns void
- Base URL read from `NEXT_PUBLIC_API_URL` env var, defaults to `http://localhost:8080`
- Returns `undefined` on 204 No Content responses

**`frontend/lib/hooks.ts`** — TanStack Query hooks covering all API resources:
- `useJobs(filters?)` — polls every 5 s, supports status/queue/type filter params
- `useJob(id)` — polls every 3 s
- `useJobExecutions(id)` — one-shot
- `useCancelJob()`, `useReplayJob()`, `useSubmitJob()` — mutations, auto-invalidate query cache
- `usePipelines()` — polls every 5 s
- `usePipeline(id)`, `usePipelineJobs(id)` — poll every 3 s
- `useCreatePipeline()`, `useCancelPipeline()` — mutations
- `useQueues()`, `useQueueStats(name)` — poll every 5 s
- `useUpdateQueue()` — mutation
- `useWorkers()` — polls every 5 s
- `useHealth()` — polls `/readyz` every 5 s, no retry on error

**`frontend/app/providers.tsx`** — `"use client"` wrapper that creates a `QueryClient` with `staleTime: 0` and `gcTime: 5 min`, then renders `<QueryClientProvider>`.

**`frontend/app/layout.tsx`** — Updated to wrap children with `<Providers>` and render `<Toaster richColors position="bottom-right" />` (sonner).

**Bonus fixes** — Two pre-existing syntax errors addressed:
- `app/dashboard/workers/page.tsx` had JSX outside of a function body; rewritten clean
- `app/dashboard/jobs/page.tsx` used `useState` without `"use client"`; directive added

#### Files changed
- `frontend/lib/client.ts` (new)
- `frontend/lib/hooks.ts` (new)
- `frontend/app/providers.tsx` (new)
- `frontend/app/layout.tsx` (updated)
- `frontend/app/dashboard/workers/page.tsx` (fixed)
- `frontend/app/dashboard/jobs/page.tsx` (fixed)

---

---

### Gap 2 — All pages show mock data / health chip hardcoded / queue Save no-op (Step 2)

**Status:** ✅ Complete
**Date:** 2026-06-18

#### What was done

All six existing dashboard surfaces were rewritten to pull from live TanStack Query hooks instead of the `lib/api.ts` mock import. Every page gained loading, empty, and error+retry states.

**`app/dashboard/page.tsx`** — Dashboard overview
- Replaced `import { jobs, overview, pipelines, queues, workers } from "@/lib/api"` with `useJobs()`, `useWorkers()`, `useQueues()`, `usePipelines()`
- Computed overview metrics (`runningJobs`, `queuedJobs`, `failedJobs`, `activeWorkers`, `usedSlots`) client-side from live arrays
- Loading spinner while any hook is fetching; empty states on empty arrays

**`app/dashboard/jobs/page.tsx`** — Jobs table
- Replaced mock array with `useJobs(filters)` where `filters` is `{ status?, queue?, type? }` React state
- Status, Queue, and Type dropdowns are functional `<select>` elements that update filter state and re-query
- Loading spinner, "No jobs found" empty state, error panel with Retry button

**`app/dashboard/queues/page.tsx`** — Queue cards
- Replaced mock with `useQueues()`
- "Save live config" button calls `useUpdateQueue().mutate({ name, body })` — fires `PUT /queues/{name}`
- `toast.success("Queue {name} config updated (live)")` on success; `toast.error(err.message)` on failure
- Button shows spinner while mutation is pending

**`app/dashboard/workers/page.tsx`** — Worker cards
- Replaced mock with `useWorkers()`
- Summary tiles (`activeWorkers`, `totalConcurrency`, `availableSlots`) computed from live data
- Loading spinner, "No workers registered" empty state, error+retry

**`app/dashboard/pipelines/page.tsx`** — Pipeline cards
- Replaced mock with `usePipelines()`
- Loading spinner, "No pipelines yet" empty state, error+retry

**`components/app-shell.tsx`** — Sidebar health chip
- Removed `import { jobs, overview, workers } from "@/lib/api"`
- Added `useHealth()`, `useJobs()`, `useWorkers()` hooks
- Health chip: warning `"Checking API"` while loading, success `"API ready"` when `useHealth()` succeeds, danger `"API down"` when it errors
- Sidebar footer stats (`workers`, `running`, `queued`, `failed`) computed from live hook data
- Sidebar nav badge counts for Jobs (running+queued) and Workers are live

#### Files changed
- `frontend/app/dashboard/page.tsx` (rewritten)
- `frontend/app/dashboard/jobs/page.tsx` (rewritten)
- `frontend/app/dashboard/queues/page.tsx` (rewritten)
- `frontend/app/dashboard/workers/page.tsx` (rewritten)
- `frontend/app/dashboard/pipelines/page.tsx` (rewritten)
- `frontend/components/app-shell.tsx` (rewritten)

---

---

### Gap 3 — No Job Detail page / no cancel-replay actions / table rows not linked (Step 3)

**Status:** ✅ Complete
**Date:** 2026-06-20

#### What was done

**`frontend/lib/api.ts`** — `Job` type extended with optional fields needed by the detail view:
- `idempotency_key?`, `payload?` (arbitrary JSON object)
- `scheduled_at?`, `started_at?`, `completed_at?`, `next_retry_at?`, `deadline_at?`

**`frontend/app/dashboard/jobs/[id]/page.tsx`** (new) — Server-routed dynamic page using `use(params)` for the async params unwrap (Next.js 15 style). Pulls data from `useJob(id)` (3 s poll) and `useJobExecutions(id)`.

Three tabs:

| Tab | Content |
|---|---|
| **Overview** | Two-column grid: Details card (type, queue, priority, attempt/max_retries, worker_id, idempotency_key) + Timing card (created, scheduled, started, completed, next_retry_at, deadline_at via `formatRelativeTime`). Error panel shown only when `error_message` is present. |
| **Payload** | `<pre>` block with `JSON.stringify(job.payload, null, 2)`. Copy button writes to clipboard and fires `toast.success("Copied to clipboard")`. |
| **Executions** | Table over `GET /jobs/{id}/executions`: attempt, worker_id, status, started_at, finished_at, exit_code, error. "No executions recorded yet" empty state when array is empty. |

Action buttons (top-right):
- **Cancel** — `POST /jobs/{id}/cancel` via `useCancelJob()`. Disabled on terminal states (`completed`, `failed`, `dead`, `cancelled`). Shows spinner while pending.
- **Replay** — `POST /jobs/{id}/replay` via `useReplayJob()`. Shown only when status is `dead` or `failed`. Shows spinner while pending.
- Both use `toast.success` / `toast.error` callbacks.

**`frontend/app/dashboard/jobs/page.tsx`** — Jobs table wired with `<Link href="/dashboard/jobs/{id}">`:
- Name/ID cell wrapped in a `<Link>` block — clicking the job name/id navigates to detail.
- Chevron button replaced with a `<Link>` so the whole interaction surface is a real anchor.

#### Files changed
- `frontend/lib/api.ts` (Job type extended)
- `frontend/app/dashboard/jobs/[id]/page.tsx` (new)
- `frontend/app/dashboard/jobs/page.tsx` (table rows linked)

---

### Gap 4 — No Submit Job form (Step 4)

**Status:** ✅ Complete
**Date:** 2026-06-20

#### What was done

**`frontend/components/submit-job-dialog.tsx`** (new) — CSS-backdrop modal (no extra Radix dependency; Radix Dialog not installed). Built with `react-hook-form` + `zod` + `@hookform/resolvers`.

Schema covers both job types in a flat object:

| Field | Applies to |
|---|---|
| name, type, queue_name, priority, max_retries, idempotency_key | all |
| handler_name, handler_args (JSON textarea) | inline |
| image, command (space-separated), namespace, cpu, memory, gpu | k8s_job |

Type toggle is a styled radio group that conditionally shows/hides the relevant section. On submit the flat form values are reshaped into the correct nested `payload` structure (`payload.handler_name` or `payload.kubernetes_spec`).

Mutation uses `useSubmitJob()` → `POST /jobs`:
- Success: `toast.success("Job "{name}" submitted — {id}")`, reset form, close dialog
- Error: `toast.error(err.message)`

**`frontend/components/app-shell.tsx`** — Added `useState(false)` for `dialogOpen`; `Submit Job` button in the sticky header sets it `true`; `<SubmitJobDialog>` rendered at the end of the component.

**`frontend/app/dashboard/jobs/page.tsx`** — Same pattern: `dialogOpen` state, `Submit Job` page-header button opens dialog, `<SubmitJobDialog>` rendered at bottom of the fragment.

#### Files changed
- `frontend/components/submit-job-dialog.tsx` (new)
- `frontend/components/app-shell.tsx` (Submit Job button wired)
- `frontend/app/dashboard/jobs/page.tsx` (Submit Job button wired)

---

### Gap 5 — No Pipeline Detail / DAG view (Step 5)

**Status:** ✅ Complete
**Date:** 2026-06-20

#### What was done

**`frontend/lib/api.ts`** — Extracted `PipelineNode` as a named export (`id`, `job_id?`, `depends_on?`, `job_template?`). `Pipeline.dag_spec.nodes` now typed as `PipelineNode[]`.

**`frontend/app/dashboard/pipelines/[id]/page.tsx`** (new) — Full-page React Flow DAG canvas with side panel and cancel action.

Layout: header row → canvas+panel row → meta strip.

DAG layout (`buildGraph`):
- Nodes are assigned to layers via a forward-pass: `layer[n] = max(layer[dep]) + 1`. Within each layer nodes are stacked vertically with 90 px spacing; layers are 200 px apart horizontally.
- Edges are derived from `node.depends_on[]` when present, falling back to `dag_spec.edges`.
- Node background/border/color are driven by the linked job's status: running = aqua glow, completed = green, failed/dead = red, pending/default = muted blue.

Canvas:
- `ReactFlow` with `fitView`, `nodesDraggable=false`, `nodesConnectable=false`. Imports `reactflow/dist/style.css` at the top of the file.
- `<Background>` grid and `<Controls>` (without interactive toggle).
- `proOptions={{ hideAttribution: true }}` suppresses the React Flow watermark.

Selected node panel (right, 288 px wide, appears on node click):
- If the node has a linked job: status badge, job ID as a `<Link>` to `/dashboard/jobs/{id}`, attempt, worker, started, completed, error message.
- If no linked job yet: shows `job_template.name` / `job_template.type` from the DAG spec and "No job spawned yet" note.
- Clicking the same node again deselects/closes the panel.

Cancel Pipeline button — `POST /pipelines/{id}/cancel` via `useCancelPipeline()`. Disabled on terminal pipelines (`completed`, `failed`, `dead`, `cancelled`). `toast.success` / `toast.error` callbacks.

**`frontend/app/dashboard/pipelines/page.tsx`** — Pipeline card header areas wrapped with `<Link href="/dashboard/pipelines/{id}">` so clicking the name/status navigates to the detail page.

#### Files changed
- `frontend/lib/api.ts` (PipelineNode type exported)
- `frontend/app/dashboard/pipelines/[id]/page.tsx` (new)
- `frontend/app/dashboard/pipelines/page.tsx` (cards linked)

---

### Gap 6 — No Create Pipeline form (Step 6)

**Status:** ✅ Complete
**Date:** 2026-06-20

#### What was done

**`frontend/components/create-pipeline-dialog.tsx`** (new) — CSS-backdrop modal with two authoring modes toggled by a tab strip.

**Visual builder:**
- Each node row has: editable ID input, type select (`inline` / `k8s_job`), delete button.
- "Depends on" section shows checkboxes for every other node ID — checking one adds it to `depends_on[]`, unchecking removes it.
- "Add node" button appends a new blank row.
- Deleting a node removes it from all other nodes' `depends_on` arrays.

**JSON editor:**
- Raw `<textarea>` with a placeholder showing the expected `dag_spec` shape.
- `JSON.parse` attempted on submit; parse error surfaced inline.

**Validation (both modes):**
- Name required
- At least one node
- No duplicate node IDs
- Cycle detection via DFS (`hasCycle`) — rejects submissions where `depends_on` forms a cycle

On submit: `POST /pipelines` via `useCreatePipeline()`. Success → `toast.success("Pipeline "{name}" created — {id}")`, form reset, dialog closed. Error → `toast.error`.

**`frontend/app/dashboard/pipelines/page.tsx`** — Added `dialogOpen` state; "Create Pipeline" button opens dialog; `<CreatePipelineDialog>` rendered at end of fragment.

#### Files changed
- `frontend/components/create-pipeline-dialog.tsx` (new)
- `frontend/app/dashboard/pipelines/page.tsx` (button wired)

---

## Gap Summary

| # | Gap | Step | Effort |
|---|---|---|---|
| 1 | No API client / no fetch calls | 1 | Medium |
| 2 | All pages show mock data | 2 | Medium |
| 3 | Health chip hardcoded | 2 | Small |
| 4 | Queue "Save" is a no-op | 2 | Small |
| 5 | No loading/empty/error states | 9 | Small |
| 6 | No Job Detail page | 3 | Medium |
| 7 | No Submit Job form | 4 | Medium |
| 8 | No job cancel/replay actions | 3 | Small |
| 9 | No Pipeline Detail / DAG view | 5 | Large |
| 10 | No Create Pipeline form | 6 | Medium |
| 11 | No Observability page | 7 | Small |
| 12 | Docs links are all `href="#"` | 8 | Medium |
| 13 | No toast notifications | 10 | Small |
| 14 | Missing npm packages | 1 | Small |
| 15 | Dashboard sparklines are hardcoded mock arrays | 11-UX | Small |
| 16 | Home page still uses mock `overview` import | 11-UX | Small |
| 17 | No time-series throughput / error-rate chart | 11-UX | Medium |
| 18 | Job status distribution has no visual weight | 11-UX | Small |
| 19 | Dashboard overview job rows not linked to detail | 11-UX | Small |
| 20 | Dashboard overview pipeline rows not linked to detail | 11-UX | Small |
| 21 | Worker metric card shows no fleet-wide utilization % | 11-UX | Small |

---

## Dashboard UX Audit (Phase 11 additions)

The existing dashboard foundation is strong: coherent dark neon theme, Z-pattern layout, incident strip, status badges, progress bars, live polling, and loading/error states. The gaps below are the delta between the current state and a professional operational dashboard per standard design principles.

### Gap 15 — Dashboard sparklines are hardcoded mock arrays

**Status:** ⬜ Pending

#### Problem
`throughputSpark` and `errorSpark` in `app/dashboard/page.tsx` are compile-time constants (`[2, 5, 3, 8, ...]`). They never change and bear no relation to real job data. A sparkline that never moves is worse than no sparkline — it signals "fake data" to the user.

#### Fix
Derive sparkline buckets from the live `jobs` array already fetched by the page. Bucket `completed` jobs by `updated_at` into N time slots (last hour, 12 × 5-minute buckets). Bucket `failed`/`dead`/`retrying` jobs the same way for the error sparkline. This requires no new API call — it is pure client-side computation on existing data.

```ts
function buildSparkBuckets(jobs: Job[], status: string[], buckets = 12, windowMs = 3600_000): number[] {
  const now = Date.now();
  const bucketMs = windowMs / buckets;
  const counts = Array(buckets).fill(0);
  for (const j of jobs) {
    if (!status.includes(j.status)) continue;
    const age = now - new Date(j.updated_at).getTime();
    if (age < 0 || age >= windowMs) continue;
    counts[Math.floor(age / bucketMs)]++;
  }
  return counts.reverse(); // oldest → newest
}
```

Replace the two hardcoded arrays with calls to this function, passing the live `jobs` array.

#### Files to change
- `frontend/app/dashboard/page.tsx`

---

### Gap 16 — Home page still uses mock `overview` import

**Status:** ⬜ Pending

#### Problem
`app/page.tsx` imports `overview` from `@/lib/api` — the static mock object. The four metric cards on the landing page (`Workers`, `Running`, `Queued`, `Pipelines`) always show the mock values (3 workers, 1 running, 1 queued, 1 pipeline), ignoring the real API.

#### Fix
Convert `app/page.tsx` to `"use client"` and replace the mock import with `useJobs()`, `useWorkers()`, `usePipelines()` hooks. Compute the same four values client-side. Show `—` skeleton while loading.

#### Files to change
- `frontend/app/page.tsx`

---

### Gap 17 — No time-series throughput / error-rate chart

**Status:** ⬜ Pending

#### Problem
The dashboard has no "trends over time" visualization. Per standard operational dashboard design, monitoring real-time performance requires a line or area chart. Without it, users cannot tell if the system is accelerating, degrading, or stable over the last hour.

#### Fix
Add a Recharts `<AreaChart>` (already installed) to the dashboard overview showing job completions and failures over the last 60 minutes, bucketed into 12 × 5-minute intervals. Data is derived client-side from the live `jobs` array (same bucketing logic as Gap 15).

Layout: replace the standalone sparklines in the "Recent jobs" card header with a dedicated chart strip above the table — or as a separate card in the 2-column section.

Axes: x = time label (`−55m`, `−50m`, … `now`), y = job count. Two area series: `completed` (primary/aqua, semi-transparent fill) and `failed+dead` (danger, semi-transparent fill).

#### Files to change
- `frontend/app/dashboard/page.tsx`

---

### Gap 18 — Job status distribution has no visual weight

**Status:** ⬜ Pending

#### Problem
The status distribution section at the bottom of the dashboard shows 8 equal-sized tiles each containing only a number and a badge. All tiles look identical regardless of count — a job with 0 occurrences looks the same as one with 50. This violates the "data storytelling" principle: the visual should encode the value.

#### Fix
Add a proportional horizontal bar inside each tile whose width is `count / totalJobs * 100%`. Use the status badge color for the bar. This makes the distribution scannable at a glance without needing to read each number.

#### Files to change
- `frontend/app/dashboard/page.tsx`

---

### Gap 19 — Dashboard overview job rows not linked to detail

**Status:** ⬜ Pending

#### Problem
The "Recent jobs" table in the dashboard overview has `cursor-pointer` styling but clicking a row does nothing — there is no `<Link>` or `onClick` navigation. Users who spot a problem job must manually navigate to `/dashboard/jobs` and search for it.

#### Fix
Wrap job name cells with `<Link href="/dashboard/jobs/{job.id}">`. The row already has `hover:bg-muted/20` styling; the link makes it functional.

#### Files to change
- `frontend/app/dashboard/page.tsx`

---

### Gap 20 — Dashboard overview pipeline rows not linked to detail

**Status:** ⬜ Pending

#### Problem
Same issue as Gap 19 but for the "Active pipelines" list in the dashboard overview. Pipeline rows have `cursor-pointer` but no navigation.

#### Fix
Wrap each pipeline row div with `<Link href="/dashboard/pipelines/{p.id}">`.

#### Files to change
- `frontend/app/dashboard/page.tsx`

---

### Gap 21 — Worker metric card shows no fleet utilization %

**Status:** ⬜ Pending

#### Problem
The "Workers" metric card shows `{usedSlots}/{totalSlots} slots used` as detail text but the main `value` prop is just the worker count. The most operationally useful single number is fleet utilization percentage — it immediately tells an operator whether the cluster is idle, healthy, or saturated.

#### Fix
Change the "Workers" MetricCard `value` to `{Math.round((usedSlots / Math.max(totalSlots, 1)) * 100)}%` and `detail` to `{usedSlots}/{totalSlots} slots · {activeWorkers} workers`. Add `tone` logic: `> 90%` → danger, `> 70%` → warning, else aqua.

#### Files to change
- `frontend/app/dashboard/page.tsx`

---

## Dependencies Not in scope for Phase 11

- SSE/WebSocket bridge for live gRPC `WatchJob` streams (Phase 12)
- Embedded Prometheus charts in Observability page (Phase 12)
- Auth / session / role-aware actions (Phase 13)
- Mobile sidebar sheet (Phase 13)
- Command palette `⌘K` (Phase 13)
- E2E tests (Phase 13)
- Dockerfile + Helm wiring for frontend (Phase 13)
