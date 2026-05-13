import { GitBranch, Plus } from "lucide-react";

import { StatusBadge } from "@/components/status-badge";
import { Button, Card, PageHeader } from "@/components/ui";
import { pipelines } from "@/lib/api";
import { formatRelativeTime } from "@/lib/utils";

const nodeStatusColor: Record<string, string> = {
  running: "border-primary/40 bg-primary/10 text-primary",
  completed: "border-success/40 bg-success/10 text-success",
  failed: "border-danger/40 bg-danger/10 text-danger",
  pending: "border-slate-500/40 bg-slate-400/10 text-slate-300",
};

export default function PipelinesPage() {
  return (
    <>
      <PageHeader
        title="Pipelines"
        description="Track DAG-based workflows and the jobs created for each pipeline node."
        action={
          <Button>
            <Plus className="h-4 w-4" />
            Create Pipeline
          </Button>
        }
      />

      <div className="grid gap-4 lg:grid-cols-2">
        {pipelines.map((pipeline) => {
          const nodeColor = nodeStatusColor[pipeline.status] ?? nodeStatusColor.pending;
          return (
            <Card className="overflow-hidden" key={pipeline.id}>
              {/* Header */}
              <div className="flex items-start justify-between gap-4 p-5">
                <div>
                  <div className="flex items-center gap-2">
                    <GitBranch className="h-4 w-4 text-primary" />
                    <h2 className="font-semibold">{pipeline.name}</h2>
                  </div>
                  <p className="mt-1.5 text-sm text-muted-foreground">
                    {pipeline.dag_spec.nodes.length} nodes · {pipeline.dag_spec.edges.length} edges · updated {formatRelativeTime(pipeline.updated_at)}
                  </p>
                </div>
                <StatusBadge status={pipeline.status} />
              </div>

              {/* DAG preview */}
              <div className="border-t bg-muted/20 px-5 py-4">
                <p className="mb-3 text-xs font-medium uppercase tracking-normal text-muted-foreground">
                  DAG
                </p>
                <div className="flex flex-wrap items-center gap-2">
                  {pipeline.dag_spec.nodes.map((node, index) => (
                    <div className="flex items-center gap-2" key={node.id}>
                      <div
                        className={`rounded-md border px-3 py-1.5 text-sm font-medium shadow-sm ${nodeColor}`}
                      >
                        {node.id}
                      </div>
                      {index < pipeline.dag_spec.nodes.length - 1 && (
                        <svg className="h-4 w-4 shrink-0 text-muted-foreground" fill="none" viewBox="0 0 16 16">
                          <path d="M3 8h10M9 4l4 4-4 4" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth={1.5} />
                        </svg>
                      )}
                    </div>
                  ))}
                </div>
              </div>

              {/* Footer */}
              <div className="border-t px-5 py-3 text-xs text-muted-foreground">
                {pipeline.id}
                {pipeline.completed_at && (
                  <span className="ml-3 text-success">
                    Completed {formatRelativeTime(pipeline.completed_at)}
                  </span>
                )}
              </div>
            </Card>
          );
        })}
      </div>
    </>
  );
}
