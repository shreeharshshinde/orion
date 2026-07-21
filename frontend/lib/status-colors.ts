/**
 * Single source of truth for job/pipeline/worker status → color/label mapping.
 * Every component that renders a status (badges, dots, chart series, DAG nodes)
 * imports from here. Never hardcode a status color inline — if the palette
 * changes, it changes in exactly one place.
 */

export type JobStatus =
  | 'queued'
  | 'scheduled'
  | 'running'
  | 'completed'
  | 'retrying'
  | 'failed'
  | 'dead'
  | 'cancelled';

export type PipelineStatus = 'pending' | 'running' | 'completed' | 'failed' | 'cancelled';

export type WorkerStatus = 'idle' | 'busy' | 'draining' | 'offline';

interface StatusMeta {
  color: string; // CSS var reference, e.g. 'var(--status-running)'
  dim: string; // low-opacity background for pills
  label: string; // exact lowercase string matching the API enum — never paraphrased
  pulses: boolean; // only 'running' or 'busy' should visually pulse
}

export const JOB_STATUS_META: Record<JobStatus, StatusMeta> = {
  queued: { color: 'var(--text-faint)', dim: 'transparent', label: 'queued', pulses: false },
  scheduled: { color: 'var(--flare)', dim: 'var(--flare-dim)', label: 'scheduled', pulses: false },
  running: { color: 'var(--star)', dim: 'var(--star-dim)', label: 'running', pulses: true },
  completed: { color: 'var(--drift)', dim: 'var(--drift-dim)', label: 'completed', pulses: false },
  retrying: { color: 'var(--flare)', dim: 'var(--flare-dim)', label: 'retrying', pulses: true },
  failed: { color: 'var(--collapse)', dim: 'var(--collapse-dim)', label: 'failed', pulses: false },
  dead: { color: 'var(--collapse)', dim: 'var(--collapse-dim)', label: 'dead', pulses: false },
  cancelled: { color: 'var(--text-faint)', dim: 'transparent', label: 'cancelled', pulses: false },
};

export const PIPELINE_STATUS_META: Record<PipelineStatus, StatusMeta> = {
  pending: { color: 'var(--text-faint)', dim: 'transparent', label: 'pending', pulses: false },
  running: { color: 'var(--nebula)', dim: 'var(--nebula-dim)', label: 'running', pulses: true },
  completed: { color: 'var(--drift)', dim: 'var(--drift-dim)', label: 'completed', pulses: false },
  failed: { color: 'var(--collapse)', dim: 'var(--collapse-dim)', label: 'failed', pulses: false },
  cancelled: { color: 'var(--text-faint)', dim: 'transparent', label: 'cancelled', pulses: false },
};

export const WORKER_STATUS_META: Record<WorkerStatus, StatusMeta> = {
  idle: { color: 'var(--drift)', dim: 'var(--drift-dim)', label: 'idle', pulses: false },
  busy: { color: 'var(--star)', dim: 'var(--star-dim)', label: 'busy', pulses: true },
  draining: { color: 'var(--flare)', dim: 'var(--flare-dim)', label: 'draining', pulses: false },
  offline: { color: 'var(--text-faint)', dim: 'transparent', label: 'offline', pulses: false },
};

export type Environment = 'local' | 'staging' | 'production';

export const ENV_META: Record<Environment, { color: string; label: string }> = {
  local: { color: 'var(--env-local)', label: 'local' },
  staging: { color: 'var(--env-staging)', label: 'staging' },
  production: { color: 'var(--env-prod)', label: 'production' },
};
