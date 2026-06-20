"use client";

import { ChevronLeft, Loader2, XCircle } from "lucide-react";
import Link from "next/link";
import { use, useCallback, useMemo, useState } from "react";
import ReactFlow, {
  Background,
  Controls,
  type Edge,
  type Node,
  type NodeMouseHandler,
} from "reactflow";
import { toast } from "sonner";

import { StatusBadge } from "@/components/status-badge";
import { Button, Card, CardBody, CardHeader, CardTitle, StatRow } from "@/components/ui";
import { useCancelPipeline, usePipeline, usePipelineJobs } from "@/lib/hooks";
import { formatRelativeTime } from "@/lib/utils";
import type { Job, JobStatus } from "@/lib/api";

// ─── Node color by status ────────────────────────────────────────────────────

const TERMINAL = new Set(["completed", "failed", "dead", "cancelled"]);

function statusStyle(status?: JobStatus): { background: string; border: string; color: string } {
  switch (status) {
    case "running":   return { background: "hsl(195 100% 10%)", border: "hsl(195 100% 45%)", color: "hsl(195 100% 80%)" };
    case "completed": return { background: "hsl(145 50% 10%)", border: "hsl(145 60% 40%)", color: "hsl(145 60% 70%)" };
    case "failed":
    case "dead":      return { background: "hsl(0 60% 12%)",   border: "hsl(0 70% 50%)",   color: "hsl(0 70% 75%)" };
    case "cancelled": return { background: "hsl(220 15% 14%)", border: "hsl(220 15% 35%)", color: "hsl(220 15% 60%)" };
    default:          return { background: "hsl(220 20% 14%)", border: "hsl(220 20% 30%)", color: "hsl(220 20% 65%)" };
  }
}

// ─── Build RF nodes / edges ──────────────────────────────────────────────────

function buildGraph(
  pipeline: { dag_spec: { nodes: Array<{ id: string; job_id?: string; depends_on?: string[] }>; edges: Array<{ source: string; target: string }> } },
  jobMap: Record<string, Job>,
): { nodes: Node[]; edges: Edge[] } {
  // Compute topological x-positions via layer assignment
  const dagNodes = pipeline.dag_spec.nodes;
  const layer: Record<string, number> = {};
  dagNodes.forEach(n => { layer[n.id] = 0; });

  // Simple layer pass: each node's layer = max(predecessor layers) + 1
  let changed = true;
  while (changed) {
    changed = false;
    for (const n of dagNodes) {
      for (const dep of (n.depends_on ?? [])) {
        const newLayer = (layer[dep] ?? 0) + 1;
        if (newLayer > (layer[n.id] ?? 0)) { layer[n.id] = newLayer; changed = true; }
      }
    }
  }

  // Group by layer
  const byLayer: Record<number, string[]> = {};
  for (const [id, l] of Object.entries(layer)) {
    (byLayer[l] ??= []).push(id);
  }

  // Assign positions
  const posMap: Record<string, { x: number; y: number }> = {};
  for (const [l, ids] of Object.entries(byLayer)) {
    ids.forEach((id, i) => {
      posMap[id] = { x: Number(l) * 200, y: i * 90 };
    });
  }

  const nodes: Node[] = dagNodes.map(n => {
    const job = n.job_id ? jobMap[n.job_id] : undefined;
    const style = statusStyle(job?.status);
    return {
      id: n.id,
      position: posMap[n.id] ?? { x: 0, y: 0 },
      data: { label: n.id, job },
      style: {
        background: style.background,
        border: `1.5px solid ${style.border}`,
        color: style.color,
        borderRadius: 8,
        padding: "8px 14px",
        fontSize: 12,
        fontWeight: 500,
        minWidth: 110,
        textAlign: "center" as const,
        boxShadow: job?.status === "running" ? `0 0 10px ${style.border}55` : undefined,
      },
    };
  });

  // Build edges from depends_on or explicit edges array
  const edgeSources: Array<{ source: string; target: string }> =
    dagNodes.some(n => n.depends_on?.length)
      ? dagNodes.flatMap(n => (n.depends_on ?? []).map(dep => ({ source: dep, target: n.id })))
      : pipeline.dag_spec.edges;

  const edges: Edge[] = edgeSources.map((e, i) => ({
    id: `e-${i}`,
    source: e.source,
    target: e.target,
    style: { stroke: "hsl(220 20% 40%)", strokeWidth: 1.5 },
    animated: false,
  }));

  return { nodes, edges };
}

// ─── Page ────────────────────────────────────────────────────────────────────

export default function PipelineDetailPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const { data: pipeline, isLoading, isError, refetch } = usePipeline(id);
  const { data: pipelineJobs = [] } = usePipelineJobs(id);
  const cancel = useCancelPipeline();

  const jobMap = useMemo(
    () => Object.fromEntries((pipelineJobs as Job[]).map(j => [j.id, j])),
    [pipelineJobs],
  );

  const { nodes, edges } = useMemo(
    () => pipeline ? buildGraph(pipeline, jobMap) : { nodes: [], edges: [] },
    [pipeline, jobMap],
  );

  const onNodeClick: NodeMouseHandler = useCallback((_e, node) => {
    setSelectedId(prev => (prev === node.id ? null : node.id));
  }, []);

  function handleCancel() {
    cancel.mutate(id, {
      onSuccess: () => toast.success("Pipeline cancelled"),
      onError: (e) => toast.error(e.message),
    });
  }

  if (isLoading) return (
    <div className="flex items-center justify-center py-32 text-muted-foreground">
      <Loader2 className="h-6 w-6 animate-spin" />
    </div>
  );

  if (isError || !pipeline) return (
    <div className="py-16 text-center">
      <p className="text-sm text-danger">Failed to load pipeline</p>
      <button className="mt-2 text-xs text-muted-foreground underline" onClick={() => refetch()}>Retry</button>
    </div>
  );

  const isTerminal = TERMINAL.has(pipeline.status);

  // Selected node panel data
  const selectedNode = selectedId ? pipeline.dag_spec.nodes.find(n => n.id === selectedId) : null;
  const selectedJob = selectedNode?.job_id ? jobMap[selectedNode.job_id] : undefined;

  return (
    <div className="flex flex-col gap-5">
      {/* Header */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-3">
          <Link href="/dashboard/pipelines" className="text-muted-foreground hover:text-foreground transition-colors">
            <ChevronLeft className="h-5 w-5" />
          </Link>
          <div>
            <h1 className="font-display text-xl font-semibold">{pipeline.name}</h1>
            <p className="font-mono text-xs text-muted-foreground">{pipeline.id}</p>
          </div>
          <StatusBadge status={pipeline.status} />
        </div>
        <Button
          variant="danger"
          size="sm"
          onClick={handleCancel}
          disabled={isTerminal || cancel.isPending}
        >
          {cancel.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <XCircle className="h-3.5 w-3.5" />}
          Cancel Pipeline
        </Button>
      </div>

      {/* DAG + side panel */}
      <div className="flex gap-4">
        {/* Canvas */}
        <Card className="flex-1 overflow-hidden" style={{ height: 480 }}>
          <ReactFlow
            nodes={nodes}
            edges={edges}
            onNodeClick={onNodeClick}
            fitView
            fitViewOptions={{ padding: 0.3 }}
            nodesDraggable={false}
            nodesConnectable={false}
            elementsSelectable
            proOptions={{ hideAttribution: true }}
          >
            <Background color="hsl(220 20% 20%)" gap={20} />
            <Controls showInteractive={false} />
          </ReactFlow>
        </Card>

        {/* Selected node panel */}
        {selectedNode && (
          <Card className="w-72 shrink-0">
            <CardHeader>
              <CardTitle className="text-sm">Node · {selectedNode.id}</CardTitle>
            </CardHeader>
            <CardBody className="space-y-3 text-sm">
              {selectedJob ? (
                <div className="divide-y divide-border/50">
                  <StatRow label="Status" value={<StatusBadge status={selectedJob.status} />} />
                  <StatRow label="Job ID" value={
                    <Link href={`/dashboard/jobs/${selectedJob.id}`} className="font-mono text-xs text-primary underline-offset-2 hover:underline truncate block max-w-[140px]">
                      {selectedJob.id}
                    </Link>
                  } />
                  <StatRow label="Attempt" value={`${selectedJob.attempt} / ${selectedJob.max_retries}`} />
                  <StatRow label="Worker" value={selectedJob.worker_id ?? "—"} mono />
                  <StatRow label="Started" value={formatRelativeTime(selectedJob.started_at)} />
                  <StatRow label="Completed" value={formatRelativeTime(selectedJob.completed_at)} />
                  {selectedJob.error_message && (
                    <p className="pt-2 text-xs text-danger">{selectedJob.error_message}</p>
                  )}
                </div>
              ) : (
                <div className="divide-y divide-border/50">
                  {selectedNode.job_template && (
                    <>
                      <StatRow label="Template name" value={selectedNode.job_template.name as string ?? "—"} />
                      <StatRow label="Type" value={selectedNode.job_template.type as string ?? "—"} />
                    </>
                  )}
                  <p className="pt-2 text-xs text-muted-foreground italic">No job spawned yet</p>
                </div>
              )}
            </CardBody>
          </Card>
        )}
      </div>

      {/* Meta */}
      <Card>
        <CardBody>
          <div className="grid grid-cols-2 gap-x-8 divide-x divide-border/50 text-sm sm:grid-cols-4">
            {[
              { label: "Nodes", value: pipeline.dag_spec.nodes.length },
              { label: "Edges", value: pipeline.dag_spec.edges.length },
              { label: "Created", value: formatRelativeTime(pipeline.created_at) },
              { label: "Completed", value: pipeline.completed_at ? formatRelativeTime(pipeline.completed_at) : "—" },
            ].map(({ label, value }) => (
              <div key={label} className="px-4 first:pl-0 last:pr-0">
                <p className="text-xs text-muted-foreground">{label}</p>
                <p className="mt-0.5 font-medium">{value}</p>
              </div>
            ))}
          </div>
        </CardBody>
      </Card>
    </div>
  );
}
