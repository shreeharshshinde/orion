# Orion Frontend Dashboard Design

## Purpose

Orion needs a polished control-plane frontend for operating the backend that already exists: jobs, pipelines, workers, queues, scheduler behavior, observability, and documentation. The frontend should feel like a professional infrastructure tool: fast to scan, calm under pressure, detailed when needed, and elegant without becoming decorative.

The recommended visual direction is an aqua, white, and blue system with restrained glass effects, clear status colors, dense operational layouts, and smooth interactions. The goal is not a marketing site first; the first screen should be a usable operations dashboard.

## Recommended Stack

| Area | Choice | Reason |
|---|---|---|
| Framework | Next.js App Router | Strong routing, docs pages, API proxy routes, SSR where useful |
| Language | TypeScript | Safer API contracts and UI state |
| UI system | shadcn/ui + Radix UI | Professional primitives, accessible dialogs/menus/tabs/forms |
| Styling | Tailwind CSS | Fast design iteration and theme tokens |
| Icons | lucide-react | Clean operational icon language |
| Data fetching | TanStack Query | Polling, caching, optimistic updates, retries |
| Tables | TanStack Table | Jobs, workers, queues need dense sortable/filterable tables |
| Charts | Recharts or Tremor | Operational time-series and status breakdowns |
| DAG view | React Flow | Pipeline graph visualization and interactive node states |
| Forms | React Hook Form + Zod | Submit job, create pipeline, queue config validation |
| Code editor | Monaco Editor or CodeMirror | JSON payload and DAG spec editing |
| Docs | MDX | Product docs, runbooks, ADR links, examples |

## Product Shape

The frontend should have three top-level experiences:

1. **Home**: project overview, architecture story, quick start, and links into the live system.
2. **Dashboard**: day-to-day control plane for jobs, pipelines, queues, workers, and health.
3. **Docs**: readable documentation hub for architecture, runbooks, deployment, API examples, and ADRs.

Use a persistent app shell for dashboard and docs:

- Left sidebar for primary navigation.
- Top command/search bar.
- Environment switcher in the header.
- Health indicator visible at all times.
- User/actions menu prepared for future auth.

## Information Architecture

```text
/
  Home

/dashboard
  Overview
  Jobs
  Job Detail
  Submit Job
  Pipelines
  Pipeline Detail
  Create Pipeline
  Queues
  Workers
  Observability
  Settings

/docs
  Overview
  Architecture
  API Reference
  Job Lifecycle
  Pipeline DAGs
  Queue Scheduling
  Deployment
  Runbook
  ADRs
```

## Navigation

Primary sidebar:

- Overview
- Jobs
- Pipelines
- Queues
- Workers
- Observability
- Docs
- Settings

Header:

- Global search / command palette
- Environment selector: Local, Staging, Production
- API health chip: Ready, Degraded, Down
- Refresh interval selector: Live, 5s, 30s, Paused
- Theme toggle: Light, Dark, System

## Visual Design System

### Theme

The theme should feel clean and cloud-native, with an aqua-blue identity.

| Token | Light Value | Usage |
|---|---:|---|
| Background | `#F7FCFF` | Main app background |
| Surface | `#FFFFFF` | Panels, tables, forms |
| Surface tinted | `#ECFAFF` | Subtle highlighted areas |
| Border | `#CFE8F3` | Cards, tables, inputs |
| Primary | `#0284C7` | Main actions, selected nav |
| Primary deep | `#075985` | Hover, active, strong headings |
| Accent aqua | `#06B6D4` | Charts, live states, focus |
| Text strong | `#0F172A` | Headings and key values |
| Text muted | `#64748B` | Secondary labels |
| Success | `#10B981` | Completed, ready, healthy |
| Warning | `#F59E0B` | Retrying, scheduled, degraded |
| Danger | `#EF4444` | Failed, dead, down |
| Neutral | `#64748B` | Cancelled, offline |

Dark mode should use deep navy backgrounds, not pure black:

- Background: `#061826`
- Surface: `#0B2536`
- Surface raised: `#0F3147`
- Border: `#16445E`
- Text strong: `#E6F8FF`
- Text muted: `#8DB2C4`

### Effects

Use effects sparingly and consistently:

- Soft glass header only: white/80 or navy/80 with backdrop blur.
- Focus rings in aqua.
- Hover states should lift by 1px at most.
- Active nav should use a filled pale aqua background and left accent line.
- Critical alerts should use solid color accents, not heavy gradients.
- Motion should be subtle: fade/slide under 180ms.

Avoid heavy hero gradients, large decorative blobs, and overly rounded cards. Orion should feel like an operations console.

### Typography

- Font: Inter or Geist Sans.
- Mono font: Geist Mono or JetBrains Mono.
- Page titles: 24-30px.
- Dashboard section headings: 16-18px.
- Table text: 13-14px.
- Metric numbers: 24-32px.
- Letter spacing: normal.

## Layout

Desktop:

- Sidebar width: 256px.
- Header height: 64px.
- Main content max width: none for dashboard pages.
- Use 24px page padding.
- Tables should fill available width.

Tablet:

- Sidebar collapses to icon rail.
- Header search remains visible.
- Metric cards become 2 columns.

Mobile:

- Sidebar becomes sheet navigation.
- Tables become stacked rows with priority fields.
- Complex DAG editing can be read-only with a prompt to use desktop.

## Home Page

The home page should introduce Orion without delaying access to the product.

First viewport:

- Brand: **Orion**
- Subtitle: Distributed ML job orchestration for Kubernetes.
- Primary action: Open Dashboard.
- Secondary action: Read Docs.
- Live system strip: API health, active workers, queued jobs, running pipelines.
- Background: real product screenshot or generated dashboard preview, lightly overlaid. Avoid abstract SVG-only visuals.

Sections:

- What Orion controls: Jobs, Pipelines, Queues, Workers, Observability.
- Architecture overview with compact diagram.
- Backend capabilities already available.
- Quick start commands and API examples.
- Links to roadmap, deployment, and runbook.

## Dashboard Overview

The overview is the operational cockpit.

Top row:

- API health
- Ready status
- Active workers
- Running jobs
- Queued jobs
- Failed/dead jobs

Main grid:

- Job throughput chart
- Status distribution chart
- Queue depth cards for high/default/low/dead
- Recent jobs table
- Active pipelines list
- Worker capacity panel
- Incident strip showing failed, dead, offline, or stale items

Expected interactions:

- Clicking a status filters Jobs.
- Clicking a queue opens Queue detail.
- Clicking a pipeline opens graph detail.
- Header refresh controls affect all live panels.

## Jobs

### Jobs List

Purpose: inspect and manage all jobs.

Controls:

- Search by name, id, worker id.
- Filter by status: queued, scheduled, running, completed, failed, retrying, dead, cancelled.
- Filter by queue: high, default, low.
- Filter by type: inline, k8s_job.
- Sort by created, updated, priority, attempt.
- Create job button.

Table columns:

- Status
- Name
- Type
- Queue
- Priority
- Attempt
- Worker
- Created
- Updated
- Actions

Row actions:

- View details
- Cancel when status allows it
- Copy job id

Status badges:

| Status | Color |
|---|---|
| queued | blue outline |
| scheduled | amber outline |
| running | aqua filled |
| completed | green filled |
| failed | red outline |
| retrying | amber filled |
| dead | red filled |
| cancelled | slate outline |

### Job Detail

Tabs:

- Overview
- Payload
- Executions
- Timeline
- Logs placeholder

Overview content:

- Status and state transition timeline.
- Timing fields: scheduled, started, completed, deadline, next retry.
- Queue, priority, max retries, attempt.
- Worker assignment.
- Error message panel if present.

Payload tab:

- JSON viewer for inline handler args or Kubernetes spec.
- Copy button.

Executions tab:

- Attempt table from `GET /jobs/{id}/executions`.
- Status, worker, start/end, exit code, error, logs ref.

Timeline tab:

- Visual lifecycle: queued -> scheduled -> running -> completed/failed/retrying/dead.
- Highlight current state and illegal/unused transitions as muted.

### Submit Job

Use a multi-section form:

- Basics: name, type, queue, priority.
- Execution: inline handler or Kubernetes spec.
- Reliability: max retries, scheduled at, deadline, idempotency key.
- Payload editor: structured form plus raw JSON preview.

Validation should mirror backend rules:

- Name required.
- Type must be `inline` or `k8s_job`.
- Inline requires `handler_name`.
- Kubernetes job requires image, command, namespace, resources.

## Pipelines

### Pipelines List

Controls:

- Search by name/id.
- Filter by status: pending, running, completed, failed, cancelled.
- Create pipeline button.

Table columns:

- Status
- Name
- Nodes
- Edges
- Created
- Updated
- Completed
- Actions

### Pipeline Detail

Primary layout:

- Left: React Flow DAG canvas.
- Right: selected node details.
- Bottom: pipeline jobs table.

DAG node design:

- Pending: muted outline.
- Running: aqua pulse ring.
- Completed: green filled check.
- Failed/dead: red filled alert.
- Not created yet: dashed outline.

Interactions:

- Select node to view job template, linked job, status, and payload.
- Click linked job to open Job Detail.
- Fit graph button.
- Mini-map for large DAGs.
- Toggle between graph and JSON DAG spec.

### Create Pipeline

Builder modes:

- Visual DAG builder using React Flow.
- JSON editor using Monaco/CodeMirror.
- Example templates: train/evaluate, preprocess/train/evaluate, fan-out batch.

Validation:

- At least one node.
- Unique node ids.
- Edges must reference existing nodes.
- No self loops.
- Each node must specify inline handler or Kubernetes spec.

## Queues

Purpose: operate scheduling behavior without restarting services.

List cards:

- High queue
- Default queue
- Low queue
- Dead queue visibility

Each queue detail should show:

- Depth from `GET /queues/{name}/stats`.
- Available rate tokens.
- Max concurrent.
- Weight.
- Rate per second.
- Burst.
- Enabled state.
- Updated at.

Editable controls:

- Max concurrent: numeric input.
- Weight: slider from 0 to 1.
- Rate per sec: numeric input.
- Burst: numeric input.
- Enabled: switch.

Saving calls `PUT /queues/{name}` and should show a clear success toast because changes apply live.

## Workers

Purpose: understand execution capacity.

Top metrics:

- Active workers
- Total concurrency
- Active jobs
- Available slots
- Offline/stale workers, when backend supports it

Worker table columns:

- Status
- Worker ID
- Hostname
- Queues
- Active jobs
- Concurrency
- Available slots
- Last heartbeat
- Registered at

Worker status:

- idle: green outline
- busy: aqua filled
- draining: amber
- offline: slate/red, depending severity

## Observability

This page should link Orion's built-in observability tools into one place.

Panels:

- Metrics endpoint status.
- Prometheus link.
- Grafana dashboard link.
- Jaeger tracing link.
- Recent API health checks.
- Service versions and environment.

Future embedded charts:

- Job created/completed/failed rate.
- Scheduler dispatch latency.
- Worker execution duration.
- Queue depth over time.
- Retry count and dead-letter count.

## Docs

The docs area should make existing Markdown easier to navigate.

Docs landing:

- Architecture overview.
- Job lifecycle.
- Pipeline DAGs.
- Queue scheduling and rate limiting.
- Deployment.
- Runbook.
- ADR index.
- Phase implementation notes.

Features:

- MDX renderer.
- Table of contents.
- Previous/next links.
- Search.
- Copyable code blocks.
- Mermaid support for architecture/state diagrams.

Recommended source mapping:

| UI Docs Section | Existing Docs |
|---|---|
| Architecture | `docs/architecture/overview.md`, `README.md` |
| Runbook | `docs/RUNBOOK.md` |
| Deployment | `docs/DEPLOYMENT_PLAN.md`, `deploy/helm`, `deploy/k8s` |
| ADRs | `docs/adr/*` |
| Implementation Phases | `docs/phases/*` |

## API Mapping

| UI Feature | Backend Endpoint |
|---|---|
| Health chip | `GET /healthz`, `GET /readyz` |
| Jobs list | `GET /jobs?status=&queue=` |
| Submit job | `POST /jobs` |
| Job detail | `GET /jobs/{id}` |
| Job executions | `GET /jobs/{id}/executions` |
| Cancel job | `POST /jobs/{id}/cancel` |
| Workers | `GET /workers` |
| Pipelines list | `GET /pipelines?status=` |
| Create pipeline | `POST /pipelines` |
| Pipeline detail | `GET /pipelines/{id}` |
| Pipeline jobs | `GET /pipelines/{id}/jobs` |
| Queues list | `GET /queues` |
| Queue detail | `GET /queues/{name}` |
| Queue update | `PUT /queues/{name}` |
| Queue stats | `GET /queues/{name}/stats` |
| Live job watch | gRPC watch stream, exposed to frontend through a backend gateway or WebSocket/SSE bridge |

## Backend Gaps To Consider

The UI can start with current endpoints, but the best dashboard will benefit from these additions:

- `GET /stats/overview`: aggregate counts for dashboard cards.
- `GET /jobs/{id}/events`: durable state timeline.
- `POST /jobs/{id}/retry`: manual retry from failed/dead states, if desired.
- `GET /logs/{job_id}` or signed `logs_ref` resolver.
- `GET /metrics/query`: optional Prometheus proxy for embedded charts.
- `GET /workers?include_offline=true`: worker history and stale workers.
- WebSocket/SSE bridge for gRPC job watch events.
- Auth/session endpoints before production exposure.

## Component Inventory

Base shadcn components:

- Button
- Badge
- Card
- Input
- Textarea
- Select
- Tabs
- Dialog
- Sheet
- Dropdown Menu
- Command
- Toast/Sonner
- Table
- Tooltip
- Popover
- Calendar
- Switch
- Slider
- Separator
- Skeleton
- Alert

Custom components:

- AppShell
- SidebarNav
- HealthChip
- StatusBadge
- MetricTile
- QueueDepthCard
- JobTable
- PipelineGraph
- WorkerCapacityBar
- JsonEditor
- EmptyState
- ErrorState
- RefreshControl
- EnvironmentSwitcher
- DocsLayout

## Empty, Loading, And Error States

Every operational page should define these states:

- Loading: skeleton rows/cards matching final layout.
- Empty: concise message and next action.
- Error: short explanation, retry button, raw error hidden behind details.
- Paused live mode: clear paused indicator.
- Stale data: timestamp and subtle amber border.

Examples:

- Jobs empty: "No jobs match these filters." Action: Clear filters.
- Pipelines empty: "No pipelines created yet." Action: Create pipeline.
- Workers empty: "No active workers have heartbeated in the last 45 seconds." Action: Open runbook.
- Queues error: "Queue stats unavailable." Action: Retry.

## Accessibility

- All controls keyboard accessible.
- Use visible focus rings.
- Status must not rely on color alone; include labels/icons.
- Tables need semantic headers.
- Dialogs and sheets must trap focus.
- Charts need textual summaries.
- Respect reduced motion.

## Implementation Plan

### Phase 1: Frontend Foundation

- Create Next.js app under `web/` or `frontend/`.
- Install Tailwind, shadcn/ui, lucide-react, TanStack Query.
- Build theme tokens, app shell, navigation, health chip.
- Add API client and typed DTOs based on existing Go JSON.

### Phase 2: Core Operations

- Jobs list/detail/submit.
- Workers page.
- Queues page with live config editing.
- Overview page using current endpoints and client-side aggregation.

### Phase 3: Pipelines

- Pipelines list/detail.
- React Flow DAG renderer.
- Create pipeline form with JSON editor.
- Node-to-job linking from graph to Job Detail.

### Phase 4: Docs

- MDX docs shell.
- Import or route existing docs.
- Mermaid support.
- Docs search.

### Phase 5: Real-Time And Observability

- Add SSE/WebSocket bridge for gRPC job watch.
- Embed operational charts.
- Add Grafana/Jaeger deep links.
- Add stale data detection and incident strip.

### Phase 6: Production Readiness

- Auth and role-aware actions.
- Audit logs for destructive operations.
- Environment configuration.
- E2E tests for critical workflows.
- Dockerfile and Helm wiring for frontend deployment.

## Design Acceptance Checklist

- Dashboard opens directly into useful operational data.
- The aqua/white/blue theme is visible but not overwhelming.
- Tables remain readable with many jobs.
- Queue tuning controls feel safe and explicit.
- Pipeline DAGs communicate status at a glance.
- Docs are navigable without leaving the app.
- Every page has loading, empty, error, and stale-data states.
- Mobile is usable for monitoring, while complex editing favors desktop.
- The implementation can start with current backend APIs.
