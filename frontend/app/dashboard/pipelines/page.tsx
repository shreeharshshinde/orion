"use client";

import { GitBranch, Loader2, Plus } from "lucide-react";
import Link from "next/link";
import { useState } from "react";

import { StatusBadge } from "@/components/status-badge";
import { CreatePipelineDialog } from "@/components/create-pipeline-dialog";
import { Button, Card, PageHeader } from "@/components/ui";
import { usePipelines } from "@/lib/hooks";
import { formatRelativeTime } from "@/lib/utils";

function DAGPreview({ nodes, edges }: {
  nodes: Array<{ id: string; job_id?: string }>;
  edges: Array<{ source: string; target: string }>;
}) {
  const nodeWidth = 100;
  const nodeHeight = 40;
  const horizontalSpacing = 140;
  const svgWidth = Math.max(300, nodes.length * horizontalSpacing);
  const svgHeight = 80;

  const nodePositions: Record<string, [number, number]> = {};
  nodes.forEach((node, index) => {
    nodePositions[node.id] = [index * horizontalSpacing + 20, 20];
  });

  return (
    <div className="overflow-x-auto border rounded-lg bg-muted/20 p-4">
      <svg width={svgWidth} height={svgHeight} className="min-w-full">
        <defs>
          <marker id="arrowhead" markerWidth="10" markerHeight="10" refX="9" refY="3" orient="auto">
            <polygon points="0 0, 10 3, 0 6" fill="hsl(var(--muted-foreground))" opacity="0.6" />
          </marker>
        </defs>
        {edges.map((edge, i) => {
          const [x1, y1] = nodePositions[edge.source] ?? [0, 0];
          const [x2, y2] = nodePositions[edge.target] ?? [0, 0];
          return (
            <line key={`e-${i}`}
              x1={x1 + nodeWidth / 2} y1={y1 + nodeHeight / 2}
              x2={x2 - nodeWidth / 2} y2={y2 + nodeHeight / 2}
              stroke="hsl(var(--muted-foreground))" strokeWidth="1.5"
              markerEnd="url(#arrowhead)" opacity="0.6"
            />
          );
        })}
        {nodes.map((node) => {
          const [x, y] = nodePositions[node.id];
          return (
            <g key={node.id}>
              <rect x={x} y={y} width={nodeWidth} height={nodeHeight} rx={4}
                className="fill-primary/10 stroke-primary/40" strokeWidth="1.5" />
              <text x={x + nodeWidth / 2} y={y + nodeHeight / 2}
                textAnchor="middle" dominantBaseline="middle"
                className="text-xs font-medium pointer-events-none" fill="currentColor">
                {node.id}
              </text>
            </g>
          );
        })}
      </svg>
    </div>
  );
}

export default function PipelinesPage() {
  const { data: pipelines = [], isLoading, isError, refetch } = usePipelines();
  const [dialogOpen, setDialogOpen] = useState(false);

  return (
    <>
      <PageHeader
        title="Pipelines"
        description="Track DAG-based workflows and the jobs created for each pipeline node."
        action={
          <Button onClick={() => setDialogOpen(true)}>
            <Plus className="h-4 w-4" />
            Create Pipeline
          </Button>
        }
      />

      {isLoading ? (
        <div className="flex items-center justify-center py-16 text-muted-foreground">
          <Loader2 className="h-5 w-5 animate-spin" />
        </div>
      ) : isError ? (
        <div className="py-12 text-center">
          <p className="text-sm text-danger">Failed to load pipelines</p>
          <button className="mt-2 text-xs text-muted-foreground underline" onClick={() => refetch()}>Retry</button>
        </div>
      ) : pipelines.length === 0 ? (
        <div className="py-12 text-center text-sm text-muted-foreground">No pipelines yet</div>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {pipelines.map((pipeline) => (
            <Card className="overflow-hidden flex flex-col group" key={pipeline.id}>
              <Link href={`/dashboard/pipelines/${pipeline.id}`} className="flex items-start justify-between gap-4 p-5 hover:bg-muted/20 transition-colors">
                <div>
                  <div className="flex items-center gap-2">
                    <GitBranch className="h-4 w-4 text-primary" />
                    <h2 className="font-semibold group-hover:text-primary transition-colors">{pipeline.name}</h2>
                  </div>
                  <p className="mt-1.5 text-sm text-muted-foreground">
                    {pipeline.dag_spec.nodes.length} nodes · {pipeline.dag_spec.edges.length} edges · updated {formatRelativeTime(pipeline.updated_at)}
                  </p>
                </div>
                <StatusBadge status={pipeline.status} />
              </Link>

              <div className="border-t bg-muted/20 px-0 py-3 flex-1">
                <p className="px-5 mb-3 text-xs font-medium uppercase tracking-normal text-muted-foreground">DAG Workflow</p>
                <DAGPreview nodes={pipeline.dag_spec.nodes} edges={pipeline.dag_spec.edges} />
              </div>

              <div className="border-t px-5 py-3 text-xs text-muted-foreground">
                <div className="flex items-center justify-between">
                  <span className="font-mono">{pipeline.id}</span>
                  {pipeline.completed_at && (
                    <span className="text-success">Completed {formatRelativeTime(pipeline.completed_at)}</span>
                  )}
                </div>
              </div>
            </Card>
          ))}
        </div>
      )}

      <CreatePipelineDialog open={dialogOpen} onClose={() => setDialogOpen(false)} />
    </>
  );
}
