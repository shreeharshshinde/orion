'use client';

import { motion } from 'framer-motion';
import Link from 'next/link';
import { StatusBadge } from '../status-badge';
import type { Pipeline } from '@/lib/api';

interface PipelineDagViewProps {
  pipeline: Pipeline;
}

export function PipelineDagView({ pipeline }: PipelineDagViewProps) {
  const { nodes, edges } = pipeline.dag_spec;

  // Let's lay out nodes in layers based on dependencies (simple BFS ranking)
  const ranks: Record<string, number> = {};
  const inDegree: Record<string, number> = {};

  nodes.forEach((node) => {
    ranks[node.id] = 0;
    inDegree[node.id] = 0;
  });

  edges.forEach((edge) => {
    inDegree[edge.target] = (inDegree[edge.target] || 0) + 1;
  });

  const queue: string[] = [];
  nodes.forEach((node) => {
    if (inDegree[node.id] === 0) {
      queue.push(node.id);
      ranks[node.id] = 0;
    }
  });

  while (queue.length > 0) {
    const current = queue.shift()!;
    const currentRank = ranks[current];

    edges
      .filter((e) => e.source === current)
      .forEach((e) => {
        ranks[e.target] = Math.max(ranks[e.target], currentRank + 1);
        inDegree[e.target]--;
        if (inDegree[e.target] === 0) {
          queue.push(e.target);
        }
      });
  }

  // Calculate coordinates
  const rankGroups: Record<number, string[]> = {};
  nodes.forEach((node) => {
    const r = ranks[node.id] || 0;
    if (!rankGroups[r]) rankGroups[r] = [];
    rankGroups[r].push(node.id);
  });

  const maxRank = Math.max(...Object.keys(rankGroups).map(Number), 0);
  const width = 600;
  const height = 180;
  const xStep = width / (maxRank + 2);

  const nodeCoords: Record<string, { x: number; y: number }> = {};
  Object.entries(rankGroups).forEach(([rStr, nodeIds]) => {
    const r = Number(rStr);
    const count = nodeIds.length;
    const x = xStep * (r + 1);

    nodeIds.forEach((id, idx) => {
      const yStep = height / (count + 1);
      const y = yStep * (idx + 1);
      nodeCoords[id] = { x, y };
    });
  });

  return (
    <div className="relative rounded-xl border border-border/40 bg-card/45 p-6 backdrop-blur-md overflow-hidden w-full">
      <div className="flex items-center justify-between mb-4">
        <div>
          <h3 className="text-sm font-semibold text-text-bright">{pipeline.name}</h3>
          <p className="text-xs text-muted-foreground">Pipeline Execution DAG</p>
        </div>
        <StatusBadge status={pipeline.status} kind="pipeline" size="sm" />
      </div>

      <div className="w-full">
        <div className="relative bg-[#090d16]/30 rounded-lg border border-border/10 p-2 w-full aspect-[600/180]">
          {/* SVG for edges */}
          <svg viewBox="0 0 600 180" className="absolute inset-0 w-full h-full pointer-events-none">
            <defs>
              <marker
                id="arrow"
                viewBox="0 0 10 10"
                refX="20"
                refY="5"
                markerWidth="6"
                markerHeight="6"
                orient="auto-start-reverse"
              >
                <path d="M 0 2 L 10 5 L 0 8 z" fill="var(--panel-edge)" />
              </marker>
              <marker
                id="arrow-active"
                viewBox="0 0 10 10"
                refX="20"
                refY="5"
                markerWidth="6"
                markerHeight="6"
                orient="auto-start-reverse"
              >
                <path d="M 0 2 L 10 5 L 0 8 z" fill="var(--nebula)" />
              </marker>
            </defs>
            {edges.map((edge, idx) => {
              const start = nodeCoords[edge.source];
              const end = nodeCoords[edge.target];
              if (!start || !end) return null;

              const isPipelineRunning = pipeline.status === 'running';

              return (
                <g key={idx}>
                  {/* Outer glow line when running */}
                  {isPipelineRunning && (
                    <motion.path
                      d={`M ${start.x} ${start.y} L ${end.x} ${end.y}`}
                      stroke="var(--nebula)"
                      strokeWidth="3"
                      opacity="0.15"
                      initial={{ pathLength: 0 }}
                      animate={{ pathLength: 1 }}
                      transition={{ duration: 1.5, repeat: Infinity, ease: 'easeInOut' }}
                    />
                  )}
                  <path
                    d={`M ${start.x} ${start.y} L ${end.x} ${end.y}`}
                    stroke={isPipelineRunning ? 'var(--nebula)' : 'var(--panel-edge)'}
                    strokeWidth="1.5"
                    fill="none"
                    markerEnd={isPipelineRunning ? 'url(#arrow-active)' : 'url(#arrow)'}
                  />
                </g>
              );
            })}
          </svg>

          {/* Node HTML tags */}
          {nodes.map((node) => {
            const coord = nodeCoords[node.id];
            if (!coord) return null;

            const isCurrentNodeRunning = pipeline.status === 'running';

            return (
              <Link
                key={node.id}
                href={`/dashboard/pipelines/${pipeline.id}`}
                style={{
                  position: 'absolute',
                  left: `${(coord.x / 600) * 100}%`,
                  top: `${(coord.y / 180) * 100}%`,
                  transform: 'translate(-50%, -50%)',
                }}
                className="group flex flex-col items-center cursor-pointer select-none"
              >
                <div
                  style={{
                    backgroundColor: isCurrentNodeRunning ? '#161c2c' : '#131826',
                  }}
                  className={`flex items-center justify-center rounded-lg border px-3 py-1.5 min-w-[90px] text-center font-mono text-[11px] shadow-md transition-all group-hover:scale-105 group-hover:border-primary/80 group-hover:shadow-[0_0_12px_rgba(34,211,238,0.2)] ${
                    isCurrentNodeRunning
                      ? 'border-nebula text-text-bright shadow-[0_0_12px_rgba(139,127,232,0.25)]'
                      : 'border-border/60 text-muted-foreground'
                  }`}
                >
                  {node.id}
                </div>
              </Link>
            );
          })}
        </div>
      </div>
    </div>
  );
}
