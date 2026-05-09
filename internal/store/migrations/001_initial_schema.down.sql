-- Migration: 001_initial_schema.down.sql
-- Drops all tables created by 001_initial_schema.up.sql
-- Order matters: drop dependents before parents (FK constraints)

DROP TRIGGER IF EXISTS trg_pipelines_updated_at ON pipelines;
DROP TRIGGER IF EXISTS trg_jobs_updated_at ON jobs;
DROP FUNCTION IF EXISTS update_updated_at();

DROP TABLE IF EXISTS pipeline_jobs;
DROP TABLE IF EXISTS pipelines;
DROP TABLE IF EXISTS workers;
DROP TABLE IF EXISTS job_executions;
DROP TABLE IF EXISTS jobs;
