import { AlertCircle, Plus } from "lucide-react";

import { StatusBadge } from "@/components/status-badge";
import { Button, Card, PageHeader } from "@/components/ui";
import { jobs } from "@/lib/api";
import { formatRelativeTime } from "@/lib/utils";

export default function JobsPage() {
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
                <th className="px-4 py-3 font-medium">Status</th>
                <th className="px-4 py-3 font-medium">Name</th>
                <th className="px-4 py-3 font-medium">Type</th>
                <th className="px-4 py-3 font-medium">Queue</th>
                <th className="px-4 py-3 font-medium">Priority</th>
                <th className="px-4 py-3 font-medium">Attempt</th>
                <th className="px-4 py-3 font-medium">Worker</th>
                <th className="px-4 py-3 font-medium">Updated</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {jobs.map((job) => (
                <tr
                  className="group cursor-pointer bg-card hover:bg-muted/30"
                  key={job.id}
                >
                  <td className="px-4 py-3">
                    <StatusBadge status={job.status} />
                  </td>
                  <td className="px-4 py-3">
                    <p className="font-medium group-hover:text-primary">{job.name}</p>
                    <p className="mt-0.5 font-mono text-xs text-muted-foreground">{job.id}</p>
                    {job.error_message && (
                      <div className="mt-1 flex items-center gap-1 text-xs text-danger">
                        <AlertCircle className="h-3 w-3 shrink-0" />
                        {job.error_message}
                      </div>
                    )}
                  </td>
                  <td className="px-4 py-3">
                    <span className="rounded border bg-muted/60 px-1.5 py-0.5 font-mono text-xs">
                      {job.type}
                    </span>
                  </td>
                  <td className="px-4 py-3 text-muted-foreground">
                    {job.queue_name.replace("orion:queue:", "")}
                  </td>
                  <td className="px-4 py-3 font-medium">{job.priority}</td>
                  <td className="px-4 py-3 text-muted-foreground">
                    {job.attempt}/{job.max_retries}
                  </td>
                  <td className="px-4 py-3 font-mono text-xs text-muted-foreground">
                    {job.worker_id ?? <span className="italic">Unassigned</span>}
                  </td>
                  <td className="px-4 py-3 text-muted-foreground">
                    {formatRelativeTime(job.updated_at)}
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
