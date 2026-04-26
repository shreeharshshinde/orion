-- Migration: 002_pipeline_indexes.down.sql
-- Removes the Phase 5 pipeline performance indexes.
-- Use: make migrate-down

DROP INDEX IF EXISTS idx_pipelines_status_created;
DROP INDEX IF EXISTS idx_pipelines_status_list;
DROP INDEX IF EXISTS idx_pipeline_jobs_pipeline_node;