-- Migration: 002_pipeline_indexes.up.sql
-- Adds performance indexes for Phase 5 pipeline orchestration.
--
-- The pipelines and pipeline_jobs tables were created in migration 001 but
-- carry only a primary key. These indexes support the three query patterns
-- the scheduler and API use most heavily.
--
-- Apply: make migrate-up
-- Roll back: make migrate-down (runs 002_pipeline_indexes.down.sql)

-- ─────────────────────────────────────────────────────────────────────────────
-- Scheduler advancement query
-- ─────────────────────────────────────────────────────────────────────────────
-- ListPipelinesByStatus fetches all pending/running pipelines every 2 seconds.
-- Without this index, PostgreSQL scans the entire pipelines table on every tick.
-- With it, only the tiny active set (pending + running rows) is scanned.
--
-- PARTIAL INDEX: only covers the two statuses the scheduler ever queries.
-- As the pipeline history grows to millions of completed/failed rows,
-- this index stays small and fast — completed rows are invisible to it.
--
-- ORDER BY created_at ASC ensures FIFO fairness across concurrent pipelines
-- (oldest pending pipelines are advanced first).
CREATE INDEX IF NOT EXISTS idx_pipelines_status_created
    ON pipelines (status, created_at ASC)
    WHERE status IN ('pending', 'running');

-- ─────────────────────────────────────────────────────────────────────────────
-- API list query
-- ─────────────────────────────────────────────────────────────────────────────
-- GET /pipelines?status=completed uses this index for filtered, ordered listing.
-- Covers all statuses (not partial) because the API can list any status.
-- ORDER BY created_at DESC (newest first for the UI).
CREATE INDEX IF NOT EXISTS idx_pipelines_status_list
    ON pipelines (status, created_at DESC);

-- ─────────────────────────────────────────────────────────────────────────────
-- GetPipelineJobs JOIN
-- ─────────────────────────────────────────────────────────────────────────────
-- GetPipelineJobs runs:
--   SELECT ... FROM pipeline_jobs pj JOIN jobs j ON j.id = pj.job_id
--   WHERE pj.pipeline_id = $1
--   ORDER BY pj.node_id ASC
--
-- This index serves two parts of that query:
--   1. pj.pipeline_id = $1          → equality filter (leftmost column)
--   2. ORDER BY pj.node_id ASC      → already sorted, no sort step needed
--
-- Without it: full scan of pipeline_jobs, then sort.
-- With it: index scan delivers rows in node_id order, zero sort overhead.
CREATE INDEX IF NOT EXISTS idx_pipeline_jobs_pipeline_node
    ON pipeline_jobs (pipeline_id, node_id ASC);