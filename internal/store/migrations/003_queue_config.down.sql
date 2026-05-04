-- Migration: 003_queue_config.down.sql
DROP INDEX IF EXISTS idx_queue_config_enabled;
DROP TABLE IF EXISTS queue_config;