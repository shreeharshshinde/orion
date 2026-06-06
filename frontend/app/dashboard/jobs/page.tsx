import { AlertCircle, ChevronRight, Plus } from "lucide-react";

import { StatusBadge } from "@/components/status-badge";
import { Button, Card, PageHeader } from "@/components/ui";
import { jobs } from "@/lib/api";
import { formatRelativeTime } from "@/lib/utils";
import { useState } from "react";

export default function JobsPage() {
  const [expandedId, setExpandedId] = useState<string | null>(null);

  return (
    <>
      <PageHeader
        title="Jobs"
        description="Inspect submitted work, execution status, queue placement, attempts, assigned workers, and retry behavior."
        action={
          <Button>
            <Plus className="h-4 w-4" />
            Submit Job
          </Button>
        }
      />

      <Card>
        {/* Filters */}
        <div className="border-b p-4">
          <div className="grid gap-3 sm:grid-cols-2 md:grid-cols-4">
            <div className="flex items-center gap-2 rounded-md border bg-background px-3 py-2 text-sm text-muted-foreground">
              <span>Search jobs…</span>
            </div>
            <div className="rounded-md border bg-background px-3 py-2 text-sm text-muted-foreground">
              Status: all
            </div>
            <div className="rounded-md border bg-background px-3 py-2 text-sm text-muted-foreground">
              Queue: all
            </div>
            <div className="rounded-md border bg-background px-3 py-2 text-sm text-muted-foreground">
              Type: all
            </div>
          </div>
        </div>

        {/* Table */}
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead className="border-b bg-muted/50 text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium text-xs uppercase tracking-wider">Status</th>
                <th className="px-3 py-2 font-medium text-xs uppercase tracking-wider">Name <span className="ml-1 text-xs font-normal">↑↓</span></th>
                <th className="px-3 py-2 font-medium text-xs uppercase tracking-wider">Type</th>
                <th className="px-3 py-2 font-medium text-xs uppercase tracking-wider">Queue</th>
                <th className="px-3 py-2 font-medium text-xs uppercase tracking-wider">Priority <span className="ml-1 text-xs font-normal">↑↓</span></th>
                <th className="px-3 py-2 font-medium text-xs uppercase tracking-wider">Attempt</th>
                <th className="px-3 py-2 font-medium text-xs uppercase tracking-wider">Worker</th>
                <th className="px-3 py-2 font-medium text-xs uppercase tracking-wider">Updated <span className="ml-1 text-xs font-normal">↑↓</span></th>
                <th className="px-3 py-2 w-8"></th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border/50">
              {jobs.map((job) => (
                <tr
                  className="group cursor-pointer bg-card hover:bg-muted/20 transition-colors"
                  key={job.id}
                >
                  <td className="px-3 py-2">
                    <StatusBadge status={job.status} />
                  </td>
                  <td className="px-3 py-2">
                    <p className="font-medium text-sm group-hover:text-primary truncate">{job.name}</p>
                    <p className="mt-0.5 font-mono text-xs text-muted-foreground truncate">{job.id}</p>
                    {job.error_message && (
                      <div className="mt-1 flex items-center gap-1 text-xs text-danger truncate">
                        <AlertCircle className="h-3 w-3 shrink-0" />
                        <span className="truncate">{job.error_message}</span>
                      </div>
                    )}
                  </td>
                  <td className="px-3 py-2">
                    <span className="rounded border border-border/60 bg-muted/40 px-1.5 py-0.5 font-mono text-xs whitespace-nowrap">
                      {job.type}
                    </span>
                  </td>
                  <td className="px-3 py-2 text-muted-foreground text-xs">
                    {job.queue_name.replace("orion:queue:", "")}
                  </td>
                  <td className="px-3 py-2">
                    <span className="inline-flex rounded-full border border-primary/40 bg-primary/10 px-2 py-0.5 text-xs font-semibold text-primary">
                      P{job.priority}
                    </span>
                  </td>
                  <td className="px-3 py-2 text-muted-foreground text-xs tabular-nums">
                    {job.attempt}/{job.max_retries}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-muted-foreground truncate">
                    {job.worker_id ?? <span className="italic opacity-60">—</span>}
                  </td>
                  <td className="px-3 py-2 text-muted-foreground text-xs whitespace-nowrap">
                    {formatRelativeTime(job.updated_at)}
                  </td>
                  <td className="px-3 py-2">
                    <div className="opacity-0 group-hover:opacity-100 transition-opacity flex gap-1">
                      <button
                        className="p-1 hover:bg-primary/20 rounded transition-colors"
                        title="View details"
                        onClick={() => setExpandedId(expandedId === job.id ? null : job.id)}
                      >
                        <ChevronRight className="h-4 w-4 text-muted-foreground" />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>

        <div className="border-t px-4 py-3 text-xs text-muted-foreground">
          {jobs.length} jobs · mock data · API mapped to <code className="font-mono">GET /jobs</code>
        </div>
      </Card>
    </>
  );
}
