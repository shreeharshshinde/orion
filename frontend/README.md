# Orion Frontend

Next.js dashboard for the Orion distributed ML job orchestrator.

## Stack

- Next.js App Router
- TypeScript
- Tailwind CSS
- shadcn-inspired local UI primitives
- lucide-react icons

## Run Locally

```bash
cd frontend
npm install
npm run dev
```

The app defaults to mock data while the API client is wired to the existing backend routes:

- `GET /jobs`
- `GET /pipelines`
- `GET /queues`
- `GET /workers`
- `GET /healthz`
- `GET /readyz`

## Current Routes

- `/` - Orion home page
- `/dashboard` - operations overview
- `/dashboard/jobs` - jobs table
- `/dashboard/pipelines` - pipeline cards and DAG preview
- `/dashboard/queues` - queue tuning overview
- `/dashboard/workers` - worker capacity
- `/docs` - docs landing page

