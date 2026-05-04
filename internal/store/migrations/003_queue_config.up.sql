-- Migration: 003_queue_config.up.sql
-- Creates the queue_config table for per-queue rate limiting and fair scheduling.
-- Rows are reloaded by the scheduler on every tick — no restart needed to change limits.
--
-- Apply:  make migrate-up
-- Revert: make migrate-down  (runs 003_queue_config.down.sql)

CREATE TABLE IF NOT EXISTS queue_config (
    queue_name      TEXT        PRIMARY KEY,
    max_concurrent  INT         NOT NULL DEFAULT 10,    -- max simultaneous worker slots
    weight          FLOAT       NOT NULL DEFAULT 0.5,   -- dispatch weight (0.0 – 1.0)
    rate_per_sec    FLOAT       NOT NULL DEFAULT 50.0,  -- token bucket refill rate
    burst           INT         NOT NULL DEFAULT 10,    -- max token accumulation
    enabled         BOOLEAN     NOT NULL DEFAULT TRUE,  -- FALSE pauses all dispatch for this queue
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed the three standard Orion queues with sensible defaults.
-- Operators change these live via PUT /queues/{name} — no migration, no restart.
INSERT INTO queue_config (queue_name, max_concurrent, weight, rate_per_sec, burst)
VALUES
    ('orion:queue:high',    8, 0.8, 100.0, 20),
    ('orion:queue:default', 6, 0.6,  50.0, 10),
    ('orion:queue:low',     2, 0.2,  10.0,  5)
ON CONFLICT (queue_name) DO NOTHING;

-- Partial index for the scheduler's queue-config reload query.
-- Only active queues (enabled=TRUE) are scanned — disabled queues don't appear.
CREATE INDEX IF NOT EXISTS idx_queue_config_enabled
    ON queue_config (enabled)
    WHERE enabled = TRUE;