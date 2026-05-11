import { Activity, AlertTriangle, BriefcaseBusiness, GitBranch, Server, Waypoints } from "lucide-react";

import { Card, MetricCard, PageHeader } from "@/components/ui";
import { StatusBadge } from "@/components/status-badge";
import { jobs, overview, pipelines, queues, workers } from "@/lib/api";
import { formatRelativeTime } from "@/lib/utils";

export default function DashboardPage() {
  const incidents = jobs.filter((j) => j.status === "failed" || j.status === "dead" || j.status === "retrying");

  return (
    <>
      <PageHeader
        title="Overview"
        description="Operational cockpit for Orion jobs, pipelines, queue pressure, worker capacity, and system health."
      />

      {/* Incident strip */}
      {incidents.length > 0 && (
        <div className="mb-6 flex items-start gap-3 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" />
          <div>
            <span className="font-medium text-amber-800">
              {incidents.length} job{incidents.length > 1 ? "s" : ""} need attention:
            </span>{" "}
            <span className="text-amber-700">
              {incidents.map((j) => j.name).join(", ")}
            </span>
          </div>
        </div>
      )}

      {/* Metric cards */}
      <section className="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
        <MetricCard
          label="API"
          value="Ready"
          detail="healthz and readyz responding"
          icon={<Activity className="h-5 w-5" />}
          tone="success"
        />
        <MetricCard
          label="Workers"
          value={overview.activeWorkers}
          detail={`${overview.totalConcurrency} total slots`}
          icon={<Server className="h-5 w-5" />}
        />
        <MetricCard
          label="Running jobs"
          value={overview.runningJobs}
          detail={`${overview.activeSlots} slots in use`}
          icon={<BriefcaseBusiness className="h-5 w-5" />}
        />
        <MetricCard
          label="Queued jobs"
          value={overview.queuedJobs}
          detail="waiting for dispatch"
          icon={<Waypoints className="h-5 w-5" />}
          tone="warning"
        />
        <MetricCard
          label="Failed / dead"
          value={overview.failedJobs}
          detail="needs attention"
          icon={<AlertTriangle className="h-5 w-5" />}
          tone={overview.failedJobs ? "danger" : "success"}
        />
      </section>

      {/* Recent jobs + Queue pressure */}
      <section className="mt-6 grid gap-4 xl:grid-cols-[1.3fr_0.7fr]">
        <Card className="p-5">
          <div className="mb-4 flex items-center justify-between">
            <h2 className="font-semibold">Recent jobs</h2>
            <span className="text-xs text-muted-foreground">mock data · API mapped</span>
          </div>
          <div className="overflow-hidden rounded-md border">
            <table className="w-full text-left text-sm">
              <thead className="bg-muted/60 text-muted-foreground">
                <tr>
                  <th className="px-4 py-2.5 font-medium">Status</th>
                  <th className="px-4 py-2.5 font-medium">Name</th>
                  <th className="px-4 py-2.5 font-medium">Queue</th>
                  <th className="px-4 py-2.5 font-medium">Updated</th>
                </tr>
              </thead>
              <tbody className="divide-y bg-card">
                {jobs.map((job) => (
                  <tr className="hover:bg-muted/30" key={job.id}>
                    <td className="px-4 py-3">
                      <StatusBadge status={job.status} />
                    </td>
                    <td className="px-4 py-3">
                      <p className="font-medium">{job.name}</p>
                      {job.error_message && (
                        <p className="mt-0.5 text-xs text-danger">{job.error_message}</p>
                      )}
                    </td>
                    <td className="px-4 py-3 text-muted-foreground">{job.queue_name}</td>
                    <td className="px-4 py-3 text-muted-foreground">{formatRelativeTime(job.updated_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>

        <Card className="p-5">
          <h2 className="mb-4 font-semibold">Queue pressure</h2>
          <div className="space-y-5">
            {queues.map((queue) => {
              const pct = Math.min((queue.depth ?? 0) * 3, 100);
              const color = pct > 70 ? "bg-danger" : pct > 40 ? "bg-warning" : "bg-primary";
              return (
                <div key={queue.queue_name}>
                  <div className="mb-1.5 flex items-center justify-between text-sm">
                    <span className="font-medium">{queue.queue_name.replace("orion:queue:", "")}</span>
                    <span className="text-muted-foreground">{queue.depth} depth</span>
                  </div>
                  <div className="h-2 overflow-hidden rounded-full bg-muted">
                    <div className={`h-full rounded-full transition-all ${color}`} style={{ width: `${pct}%` }} />
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {queue.rate_tokens_avail} tokens · {queue.max_concurrent} max concurrent
                  </p>
                </div>
              );
            })}
          </div>
        </Card>
      </section>

      {/* Pipelines + Workers */}
      <section className="mt-6 grid gap-4 lg:grid-cols-2">
        <Card className="p-5">
          <h2 className="mb-4 flex items-center gap-2 font-semibold">
            <GitBranch className="h-4 w-4 text-primary" />
            Active pipelines
          </h2>
          <div className="space-y-3">
            {pipelines.map((pipeline) => (
              <div
                className="flex items-center justify-between rounded-md border bg-muted/20 p-3 hover:bg-muted/40"
                key={pipeline.id}
              >
                <div>
                  <p className="font-medium">{pipeline.name}</p>
                  <p className="mt-0.5 text-xs text-muted-foreground">
                    {pipeline.dag_spec.nodes.length} nodes · {pipeline.dag_spec.edges.length} edges · {formatRelativeTime(pipeline.updated_at)}
                  </p>
                </div>
                <StatusBadge status={pipeline.status} />
              </div>
            ))}
          </div>
        </Card>

        <Card className="p-5">
          <h2 className="mb-4 flex items-center gap-2 font-semibold">
            <Server className="h-4 w-4 text-primary" />
            Worker capacity
          </h2>
          <div className="space-y-4">
            {workers.map((worker) => {
              const pct = (worker.active_jobs / worker.concurrency) * 100;
              const barColor = pct > 80 ? "bg-danger" : pct > 50 ? "bg-warning" : "bg-primary";
              return (
                <div key={worker.id}>
                  <div className="mb-1.5 flex items-center justify-between text-sm">
                    <div className="flex items-center gap-2">
                      <span className="font-medium">{worker.hostname}</span>
                      <StatusBadge status={worker.status} />
                    </div>
                    <span className="text-muted-foreground">{worker.active_jobs}/{worker.concurrency}</span>
                  </div>
                  <div className="h-2 overflow-hidden rounded-full bg-muted">
                    <div className={`h-full rounded-full transition-all ${barColor}`} style={{ width: `${pct}%` }} />
                  </div>
                </div>
              );
            })}
          </div>
        </Card>
      </section>
    </>
  );
}
