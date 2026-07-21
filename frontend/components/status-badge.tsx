'use client';

import { motion } from 'framer-motion';
import { 
  JOB_STATUS_META, 
  PIPELINE_STATUS_META, 
  WORKER_STATUS_META, 
  type JobStatus, 
  type PipelineStatus, 
  type WorkerStatus 
} from '@/lib/status-colors';

interface StatusBadgeProps {
  status: JobStatus | PipelineStatus | WorkerStatus;
  kind?: 'job' | 'pipeline' | 'worker';
  size?: 'sm' | 'md';
}

/**
 * The single status badge used everywhere in the dashboard — job tables,
 * pipeline lists, DAG nodes, detail panels. Color and pulse behavior come
 * from lib/status-colors.ts, never hardcoded here.
 */
export function StatusBadge({ status, kind, size = 'sm' }: StatusBadgeProps) {
  // Auto-detect kind if not specified for maximum compatibility
  let resolvedKind = kind;
  if (!resolvedKind) {
    if (['idle', 'busy', 'draining', 'offline'].includes(status)) {
      resolvedKind = 'worker';
    } else if (['pending'].includes(status)) {
      resolvedKind = 'pipeline';
    } else {
      resolvedKind = 'job';
    }
  }

  const meta =
    resolvedKind === 'worker'
      ? WORKER_STATUS_META[status as WorkerStatus]
      : resolvedKind === 'pipeline'
      ? PIPELINE_STATUS_META[status as PipelineStatus]
      : JOB_STATUS_META[status as JobStatus];

  if (!meta) return null;

  const dotSize = size === 'sm' ? 6 : 8;
  const fontSize = size === 'sm' ? 'var(--text-xs)' : 'var(--text-sm)';

  return (
    <span
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 6,
        padding: size === 'sm' ? '2px 8px' : '4px 10px',
        borderRadius: 999,
        background: meta.dim,
        fontFamily: 'var(--font-data)',
        fontSize,
        color: meta.color,
        lineHeight: 1.4,
      }}
    >
      <span style={{ position: 'relative', width: dotSize, height: dotSize, flexShrink: 0 }}>
        <span
          style={{
            position: 'absolute',
            inset: 0,
            borderRadius: '50%',
            background: meta.color,
          }}
        />
        {meta.pulses && (
          <motion.span
            style={{
              position: 'absolute',
              inset: 0,
              borderRadius: '50%',
              background: meta.color,
            }}
            animate={{ scale: [1, 2.2, 1], opacity: [0.6, 0, 0.6] }}
            transition={{ duration: 2, repeat: Infinity, ease: 'easeInOut' }}
          />
        )}
      </span>
      {meta.label}
    </span>
  );
}

/**
 * ConnectionIndicator — the ONLY component allowed to render API reachability.
 * This is the fix for the bug where the Overview card said "Ready" while the
 * floating corner widget simultaneously said "API down". There is now exactly
 * one place this state is read from and exactly one place it is rendered from
 * per page — use React context or a shared hook (useApiHealth) to feed it,
 * never a second independent poller.
 */
export function ConnectionIndicator({ connected }: { connected: boolean }) {
  const color = connected ? 'var(--drift)' : 'var(--collapse)';
  const dim = connected ? 'var(--drift-dim)' : 'var(--collapse-dim)';
  return (
    <span
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 6,
        padding: '4px 10px',
        borderRadius: 999,
        background: dim,
        fontFamily: 'var(--font-data)',
        fontSize: 'var(--text-xs)',
        color,
      }}
    >
      <span style={{ width: 6, height: 6, borderRadius: '50%', background: color }} />
      {connected ? 'api connected' : 'api unreachable'}
    </span>
  );
}
