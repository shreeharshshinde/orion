"use client";

import { AlertCircle, ChevronRight, Loader2, Plus } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";

import { StatusBadge } from "@/components/status-badge";
import { SubmitJobDialog } from "@/components/submit-job-dialog";
import { Button, Card, PageHeader } from "@/components/ui";
import { useTelemetry } from "@/lib/telemetry-context";
import { type JobFilter } from "@/lib/hooks";
import { formatRelativeTime } from "@/lib/utils";

export default function JobsPage() {
  const router = useRouter();
  const [filters, setFilters] = useState<JobFilter>({});
  const [dialogOpen, setDialogOpen] = useState(false);
  const { jobs, jobsLoading, apiConnected, demoMode } = useTelemetry();

  // Apply filters locally so both Sandbox and Live API datasets react instantly
  const filteredJobs = jobs.filter((job) => {
    if (filters.status && job.status !== filters.status) return false;
    if (filters.queue && !job.queue_name.includes(filters.queue)) return false;
    if (filters.type && job.type !== filters.type) return false;
    return true;
  });

  return (
    <>
      <PageHeader
        title="Jobs"
        description="Inspect submitted work, execution status, queue placement, attempts, assigned workers, and retry behavior."
        action={
          <Button onClick={() => setDialogOpen(true)}>
            <Plus className="h-4 w-4" />
            Submit Job
          </Button>
        }
      />

      <Card>
        {/* Filters */}
        <div className="border-b p-4">
          <div className="grid gap-3 sm:grid-cols-2 md:grid-cols-4">
            <input
              className="rounded-md border bg-background px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-primary"
              placeholder="Search jobs…"
              disabled
            />
            <select
              className="rounded-md border bg-background px-3 py-2 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-primary"
              value={filters.status ?? ""}
              onChange={e => setFilters(f => ({ ...f, status: e.target.value || undefined }))}
            >
              <option value="">Status: all</option>
              {["queued","scheduled","running","completed","retrying","failed","dead","cancelled"].map(s => (
                <option key={s} value={s}>{s}</option>
              ))}
            </select>
            <select
              className="rounded-md border bg-background px-3 py-2 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-primary"
              value={filters.queue ?? ""}
              onChange={e => setFilters(f => ({ ...f, queue: e.target.value || undefined }))}
            >
              <option value="">Queue: all</option>
              {["high","default","low"].map(q => <option key={q} value={q}>{q}</option>)}
            </select>
            <select
              className="rounded-md border bg-background px-3 py-2 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-primary"
              value={filters.type ?? ""}
              onChange={e => setFilters(f => ({ ...f, type: e.target.value || undefined }))}
            >
              <option value="">Type: all</option>
              <option value="inline">inline</option>
              <option value="k8s_job">k8s_job</option>
            </select>
          </div>
        </div>

        {/* Body */}
        {jobsLoading ? (
          <div className="flex items-center justify-center py-16 text-muted-foreground">
            <Loader2 className="h-5 w-5 animate-spin text-primary" />
          </div>
        ) : (!apiConnected && !demoMode) ? (
          <div className="py-12 text-center">
            <p className="text-sm text-danger">Failed to connect to live Orion API</p>
            <p className="text-xs text-muted-foreground mt-1">Check if the backend is running, or switch to simulated Sandbox above.</p>
          </div>
        ) : filteredJobs.length === 0 ? (
          <div className="py-12 text-center text-sm text-muted-foreground">No jobs found matching filters</div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead className="border-b bg-muted/50 text-muted-foreground">
                <tr>
                  {["Status","Name","Type","Queue","Priority","Attempt","Worker","Updated",""].map(h => (
                    <th key={h} className="px-3 py-2 font-medium text-xs uppercase tracking-wider">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody className="divide-y divide-border/50">
                {filteredJobs.map((job) => (
                  <tr 
                    className="group cursor-pointer bg-card hover:bg-muted/20 transition-colors" 
                    key={job.id}
                    onClick={() => router.push(`/dashboard/jobs/${job.id}`)}
                  >
                    <td className="px-3 py-2"><StatusBadge status={job.status} /></td>
                    <td className="px-3 py-2">
                      <div>
                        <p className="font-semibold text-text-bright text-sm group-hover:text-primary transition-colors truncate max-w-xs">{job.name}</p>
                        <p className="mt-0.5 font-mono text-[10px] text-muted-foreground truncate max-w-xs">{job.id}</p>
                        {job.error_message && (
                          <div className="mt-1 flex items-center gap-1 text-xs text-danger">
                            <AlertCircle className="h-3 w-3 shrink-0" />
                            <span className="truncate max-w-xs">{job.error_message}</span>
                          </div>
                        )}
                      </div>
                    </td>
                    <td className="px-3 py-2">
                      <span className="rounded border border-border/60 bg-muted/40 px-1.5 py-0.5 font-mono text-xs">{job.type}</span>
                    </td>
                    <td className="px-3 py-2 text-muted-foreground text-xs">{job.queue_name.replace("orion:queue:", "")}</td>
                    <td className="px-3 py-2">
                      <span className="inline-flex rounded-full border border-primary/40 bg-primary/10 px-2 py-0.5 text-xs font-semibold text-primary">P{job.priority}</span>
                    </td>
                    <td className="px-3 py-2 text-muted-foreground text-xs tabular-nums">{job.attempt}/{job.max_retries}</td>
                    <td className="px-3 py-2 font-mono text-xs text-muted-foreground truncate max-w-[120px]">
                      {job.worker_id ?? <span className="italic opacity-60">—</span>}
                    </td>
                    <td className="px-3 py-2 text-muted-foreground text-xs whitespace-nowrap">{formatRelativeTime(job.updated_at)}</td>
                    <td className="px-3 py-2">
                      <div className="opacity-0 group-hover:opacity-100 transition-opacity">
                        <div className="p-1 hover:bg-primary/20 rounded inline-flex">
                          <ChevronRight className="h-4 w-4 text-muted-foreground" />
                        </div>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        <div className="border-t px-4 py-3 text-xs text-muted-foreground font-mono">
          {filteredJobs.length} jobs · {demoMode ? "sandbox simulation" : "live api"} · <code className="text-primary">{demoMode ? "mock-stream" : "GET /jobs"}</code>
        </div>
      </Card>

      <SubmitJobDialog open={dialogOpen} onClose={() => setDialogOpen(false)} />
    </>
  );
}
