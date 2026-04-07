-- Migration: 001_initial_schema.up.sql
-- Creates the core Orion schema.
-- Apply with: migrate -database $ORION_DATABASE_DSN -path ./migrations up
-- Or:         make migrate-up

-- Enable UUID generation (gen_random_uuid function)
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ============================================================
-- JOBS TABLE
-- The central table. One row per job submission.
-- Status transitions are CAS-enforced in Go (see store interface).
-- ============================================================
CREATE TABLE jobs (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    idempotency_key  VARCHAR(255) UNIQUE,         -- client-provided, prevents duplicate submissions
    name             VARCHAR(255) NOT NULL,
    type             VARCHAR(100) NOT NULL,        -- 'inline' or 'k8s_job'
    queue_name       VARCHAR(100) NOT NULL DEFAULT 'default',
    priority         SMALLINT NOT NULL DEFAULT 5 CHECK (priority BETWEEN 1 AND 10),
    status           VARCHAR(50)  NOT NULL DEFAULT 'queued',
    payload          JSONB NOT NULL DEFAULT '{}',  -- flexible, varies by job type
    max_retries      SMALLINT NOT NULL DEFAULT 3,
    attempt          SMALLINT NOT NULL DEFAULT 0,  -- incremented on each execution
    worker_id        VARCHAR(255),                 -- NULL until a worker claims it
    error_message    TEXT,

    -- Timing columns
    scheduled_at     TIMESTAMPTZ,                  -- NULL = run immediately
    deadline         TIMESTAMPTZ,                  -- NULL = no deadline
    next_retry_at    TIMESTAMPTZ,                  -- set by worker on failure
    started_at       TIMESTAMPTZ,                  -- set when worker begins execution
    completed_at     TIMESTAMPTZ,                  -- set when execution finishes
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),  -- auto-updated by trigger

    CONSTRAINT jobs_status_check CHECK (status IN (
        'queued', 'scheduled', 'running', 'completed',
        'failed', 'retrying', 'dead', 'cancelled'
    )),
    CONSTRAINT jobs_type_check CHECK (type IN ('inline', 'k8s_job'))
);

-- LOAD-BEARING INDEX: Scheduler queries this every 2 seconds.
-- Partial index (WHERE status='queued') means it only indexes queued rows.
-- With 1M total jobs but only 50 queued: scans 50 rows not 1,000,000.
CREATE INDEX idx_jobs_queued_priority
    ON jobs (priority DESC, created_at ASC)
    WHERE status = 'queued';

-- RETRY INDEX: Scheduler finds failed jobs whose backoff delay has expired.
CREATE INDEX idx_jobs_retry_eligible
    ON jobs (next_retry_at ASC)
    WHERE status = 'failed' AND next_retry_at IS NOT NULL;

-- ORPHAN INDEX: Scheduler finds running jobs whose worker stopped heartbeating.
CREATE INDEX idx_jobs_running_worker
    ON jobs (worker_id, updated_at)
    WHERE status = 'running';

-- API INDEX: for GET /jobs?queue=high&status=completed queries.
CREATE INDEX idx_jobs_queue_status
    ON jobs (queue_name, status, created_at DESC);

-- ============================================================
-- JOB_EXECUTIONS TABLE
-- Append-only audit log. NEVER UPDATE rows here — only INSERT.
-- Each attempt gets its own row, giving full execution history.
-- ============================================================
CREATE TABLE job_executions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id       UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    attempt      SMALLINT NOT NULL,
    worker_id    VARCHAR(255),
    status       VARCHAR(50) NOT NULL,
    started_at   TIMESTAMPTZ,
    finished_at  TIMESTAMPTZ,
    exit_code    INT,
    logs_ref     TEXT,              -- "s3://orion-logs/jobs/abc/attempt-2.log"
    error        TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Ensures one record per attempt number per job
    CONSTRAINT job_executions_unique_attempt UNIQUE (job_id, attempt)
);

CREATE INDEX idx_executions_job_id ON job_executions (job_id);

-- ============================================================
-- WORKERS TABLE
-- Registered worker instances. Workers UPSERT here on startup
-- and update last_heartbeat every 15 seconds.
-- ============================================================
CREATE TABLE workers (
    id             VARCHAR(255) PRIMARY KEY,   -- hostname or ORION_WORKER_ID
    hostname       VARCHAR(255),
    queue_names    TEXT[] NOT NULL DEFAULT '{}',  -- PostgreSQL array
    concurrency    INT NOT NULL DEFAULT 1,
    active_jobs    INT NOT NULL DEFAULT 0,
    status         VARCHAR(50) NOT NULL DEFAULT 'idle',
    last_heartbeat TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    registered_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT workers_status_check CHECK (status IN ('idle', 'busy', 'draining', 'offline'))
);

-- Scheduler queries this to find alive workers (WHERE last_heartbeat > NOW()-90s)
CREATE INDEX idx_workers_heartbeat ON workers (last_heartbeat)
    WHERE status != 'offline';

-- ============================================================
-- PIPELINES TABLE
-- DAG pipeline definitions. dag_spec stored as JSONB.
-- ============================================================
CREATE TABLE pipelines (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name         VARCHAR(255) NOT NULL,
    status       VARCHAR(50) NOT NULL DEFAULT 'pending',
    dag_spec     JSONB NOT NULL DEFAULT '{}',   -- adjacency list: nodes + edges
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,

    CONSTRAINT pipelines_status_check CHECK (status IN (
        'pending', 'running', 'completed', 'failed', 'cancelled'
    ))
);

-- ============================================================
-- PIPELINE_JOBS TABLE
-- Maps each pipeline DAG node to an actual jobs row.
-- ============================================================
CREATE TABLE pipeline_jobs (
    pipeline_id UUID NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    job_id      UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    node_id     VARCHAR(255) NOT NULL,    -- DAGNode.ID ("preprocess", "train", etc.)
    depends_on  UUID[] NOT NULL DEFAULT '{}',  -- job IDs that must complete first
    PRIMARY KEY (pipeline_id, job_id)
);

-- ============================================================
-- TRIGGER: auto-update updated_at on every UPDATE
-- So Go code never needs to manually set updated_at.
-- ============================================================
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_jobs_updated_at
    BEFORE UPDATE ON jobs
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TRIGGER trg_pipelines_updated_at
    BEFORE UPDATE ON pipelines
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();