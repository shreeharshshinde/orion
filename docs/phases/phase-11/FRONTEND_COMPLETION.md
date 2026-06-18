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

## Dependencies Not in scope for Phase 11

- SSE/WebSocket bridge for live gRPC `WatchJob` streams (Phase 12)
- Embedded Prometheus charts in Observability page (Phase 12)
- Auth / session / role-aware actions (Phase 13)
- Mobile sidebar sheet (Phase 13)
- Command palette `⌘K` (Phase 13)
- E2E tests (Phase 13)
- Dockerfile + Helm wiring for frontend (Phase 13)
