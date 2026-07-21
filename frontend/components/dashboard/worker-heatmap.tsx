'use client';

import { motion } from 'framer-motion';
import { Server, ShieldAlert } from 'lucide-react';
import type { Worker } from '@/lib/api';

interface WorkerHeatmapProps {
  workers: Worker[];
}

export function WorkerHeatmap({ workers }: WorkerHeatmapProps) {
  return (
    <div className="relative flex flex-col rounded-xl border border-border/40 bg-card/45 p-5 backdrop-blur-md overflow-hidden h-[300px] w-full">
      <div className="flex items-center justify-between mb-4 shrink-0">
        <div>
          <h3 className="text-sm font-semibold text-text-bright">Cluster Node Occupancy</h3>
          <p className="text-xs text-muted-foreground">Active concurrency slot allocation per worker</p>
        </div>
        <div className="flex items-center gap-3 text-[10px] font-mono">
          <span className="flex items-center gap-1">
            <span className="h-2 w-2 rounded bg-cyan-400" /> busy slot
          </span>
          <span className="flex items-center gap-1">
            <span className="h-2 w-2 rounded border border-border/60 bg-transparent" /> idle slot
          </span>
        </div>
      </div>

      {/* Grid List of Worker Rows */}
      <div className="flex-1 overflow-y-auto space-y-3 pr-1">
        {workers.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-full text-center text-muted-foreground gap-2">
            <ShieldAlert className="h-8 w-8 opacity-40 text-rose-400" />
            <p className="text-sm font-medium">No active worker nodes connected</p>
          </div>
        ) : (
          workers.map((worker) => {
            const isOffline = worker.status === 'offline';
            const isDraining = worker.status === 'draining';
            const isBusy = worker.status === 'busy';

            return (
              <div
                key={worker.id}
                className="flex flex-col sm:flex-row sm:items-center justify-between gap-3 border border-border/20 bg-void/30 rounded-lg p-3 hover:border-border/40 transition-colors"
              >
                {/* Left side: hostname and queue badges */}
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <Server className={`h-4 w-4 shrink-0 ${isOffline ? 'text-muted-foreground' : isDraining ? 'text-amber-400' : 'text-cyan-400'}`} />
                    <span className="font-mono text-xs font-semibold text-text-bright truncate">
                      {worker.hostname}
                    </span>
                    <span
                      className={`text-[9px] font-semibold font-ui uppercase px-1.5 py-0.5 rounded border ${
                        isOffline
                          ? 'border-border/40 bg-muted/20 text-muted-foreground'
                          : isDraining
                          ? 'border-amber-500/30 bg-amber-500/10 text-amber-400'
                          : isBusy
                          ? 'border-cyan-500/30 bg-cyan-500/10 text-cyan-400'
                          : 'border-emerald-500/30 bg-emerald-500/10 text-emerald-400'
                      }`}
                    >
                      {worker.status}
                    </span>
                  </div>
                  <div className="flex flex-wrap gap-1 mt-1.5">
                    {worker.queue_names.map((q) => (
                      <span
                        key={q}
                        className="text-[9px] font-mono px-1 rounded bg-muted/30 border border-border/20 text-muted-foreground/80"
                      >
                        {q.replace('orion:queue:', '')}
                      </span>
                    ))}
                  </div>
                </div>

                {/* Right side: Slot Occupancy grid */}
                <div className="flex flex-col items-end shrink-0 gap-1.5">
                  <span className="text-[10px] font-mono text-muted-foreground">
                    concurrency: <strong className="text-text-bright">{worker.active_jobs}/{worker.concurrency}</strong>
                  </span>
                  <div className="flex gap-1">
                    {Array.from({ length: worker.concurrency }).map((_, idx) => {
                      const isActive = idx < worker.active_jobs;
                      return (
                        <motion.div
                          key={idx}
                          className={`h-3 w-3 rounded ${
                            isOffline
                              ? 'border border-border/40 bg-transparent opacity-40'
                              : isDraining && isActive
                              ? 'bg-amber-400 shadow-[0_0_8px_rgba(251,191,102,0.4)]'
                              : isActive
                              ? 'bg-cyan-400 shadow-[0_0_8px_rgba(34,211,238,0.4)] animate-pulse'
                              : 'border border-border/50 bg-transparent'
                          }`}
                          style={{
                            animationDuration: isActive ? `${1.5 + idx * 0.1}s` : undefined
                          }}
                        />
                      );
                    })}
                  </div>
                </div>
              </div>
            );
          })
        )}
      </div>
    </div>
  );
}
