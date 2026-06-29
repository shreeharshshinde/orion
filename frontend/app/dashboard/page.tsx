"use client";

import {
  Activity, AlertTriangle, BriefcaseBusiness,
  GitBranch, Server, Waypoints, TrendingUp, Skull, Loader2
} from "lucide-react";

import { Card, CardHeader, CardTitle, MetricCard, PageHeader, ProgressBar, SectionHeader, Table, Td, Th } from "@/components/ui";
import { StatusBadge } from "@/components/status-badge";
import { useJobs, usePipelines, useQueues, useWorkers } from "@/lib/hooks";
import { formatRelativeTime } from "@/lib/utils";

function Sparkline({ values, color = "hsl(var(--primary))" }: { values: number[]; color?: string }) {
  const max = Math.max(...values, 1);
  const w = 80; const h = 28;
  const step = w / (values.length - 1);
  const pts = values.map((v, i) => `${i * step},${h - (v / max) * h}`).join(" ");
  return (
    <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} className="shrink-0 opacity-70">
      <polyline fill="none" stroke={color} strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" points={pts} />
    </svg>
  );
}

const throughputSpark = [2, 5, 3, 8, 6, 9, 7, 12, 10, 14, 11, 8];
const errorSpark      = [0, 1, 0, 0, 2, 1, 0, 0, 1, 0, 0, 0];

function LoadingSpinner() {
  return (
    <div className="flex items-center justify-center py-12 text-muted-foreground">
      <Loader2 className="h-5 w-5 animate-spin" />
    </div>
  );
}

export default function DashboardPage() {
  const { data: jobs = [], isLoading: jobsLoading } = useJobs();
  const { data: workers = [], isLoading: workersLoading } = useWorkers();
  const { data: queues = [], isLoading: queuesLoading } = useQueues();
  const { data: pipelines = [], isLoading: pipelinesLoading } = usePipelines();

  const incidents = jobs.filter(j => j.status === "failed" || j.status === "dead" || j.status === "retrying");
  const totalSlots = workers.reduce((s, w) => s + w.concurrency, 0);
  const usedSlots  = workers.reduce((s, w) => s + w.active_jobs, 0);
  const runningJobs  = jobs.filter(j => j.status === "running").length;
  const queuedJobs   = jobs.filter(j => j.status === "queued").length;
  const failedJobs   = jobs.filter(j => j.status === "failed" || j.status === "dead").length;
  const activeWorkers = workers.filter(w => w.status !== "offline").length;

  return (
    <>
      <PageHeader
        title="Overview"
        description="Operational cockpit — jobs, pipelines, queues, workers, and system health."
        badge={<span className="rounded-full border border-success/30 bg-success/10 px-2 py-0.5 text-xs font-medium text-success">Live</span>}
      />

      {/* Incident strip */}
      {incidents.length > 0 && (
        <div className="mb-5 flex items-start gap-3 rounded-xl border border-warning/30 bg-warning/8 px-4 py-3 text-sm">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warning" />
          <div>
            <span className="font-semibold text-warning">{incidents.length} job{incidents.length > 1 ? "s" : ""} need attention — </span>
            <span className="text-muted-foreground">{incidents.map(j => j.name).join(", ")}</span>
          </div>
        </div>
      )}

      {/* Metric cards */}
      <section className="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
        <MetricCard label="API Status"    value="Ready"        detail="healthz · readyz responding"            icon={<Activity className="h-5 w-5" />}           tone="success" />
        <MetricCard label="Workers"       value={activeWorkers} detail={`${usedSlots}/${totalSlots} slots used`} icon={<Server className="h-5 w-5" />}            tone="aqua"    />
        <MetricCard label="Running"       value={runningJobs}  detail="active executions"                      icon={<BriefcaseBusiness className="h-5 w-5" />}  tone="aqua"    delta={2} deltaLabel="vs 1h ago" />
        <MetricCard label="Queued"        value={queuedJobs}   detail="waiting for dispatch"                   icon={<Waypoints className="h-5 w-5" />}          tone="warning" />
        <MetricCard label="Failed / Dead" value={failedJobs}   detail="needs attention"                        icon={<Skull className="h-5 w-5" />}              tone={failedJobs > 0 ? "danger" : "success"} />
      </section>

      {/* Throughput + Queue pressure */}
      <section className="mt-5 grid gap-4 xl:grid-cols-[1.4fr_0.6fr]">
        <Card>
          <CardHeader>
            <CardTitle>Recent jobs</CardTitle>
            <div className="flex items-center gap-4">
              <div className="flex items-center gap-2 text-xs text-muted-foreground">
                <TrendingUp className="h-3.5 w-3.5" />
                <span>Throughput</span>
                <Sparkline values={throughputSpark} />
              </div>
              <div className="flex items-center gap-2 text-xs text-muted-foreground">
                <AlertTriangle className="h-3.5 w-3.5 text-danger" />
                <span>Errors</span>
                <Sparkline values={errorSpark} color="hsl(var(--danger))" />
              </div>
            </div>
          </CardHeader>
          {jobsLoading ? <LoadingSpinner /> : jobs.length === 0 ? (
            <div className="py-8 text-center text-sm text-muted-foreground">No jobs yet</div>
          ) : (
            <Table>
              <thead>
                <tr>
                  <Th>Status</Th>
                  <Th>Name</Th>
                  <Th>Queue</Th>
                  <Th>Attempt</Th>
                  <Th>Updated</Th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border/40">
                {jobs.slice(0, 8).map((job) => (
                  <tr key={job.id} className="hover:bg-muted/20 cursor-pointer">
                    <Td><StatusBadge status={job.status} /></Td>
                    <Td>
                      <p className="font-medium text-sm">{job.name}</p>
                      {job.error_message && (
                        <p className="mt-0.5 text-xs text-danger truncate max-w-xs">{job.error_message}</p>
                      )}
                    </Td>
                    <Td><span className="rounded border border-border/60 bg-muted/40 px-1.5 py-0.5 font-mono text-xs">{job.queue_name.replace("orion:queue:", "")}</span></Td>
                    <Td className="tabular-nums text-muted-foreground">{job.attempt}/{job.max_retries}</Td>
                    <Td className="text-muted-foreground text-xs">{formatRelativeTime(job.updated_at)}</Td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Queue depth</CardTitle>
            <span className="text-xs text-muted-foreground">{queues.length} active</span>
          </CardHeader>
          {queuesLoading ? <LoadingSpinner /> : (
            <div className="p-5 space-y-5">
              {queues.map((q) => {
                const name = q.queue_name.replace("orion:queue:", "");
                const pct  = Math.min((q.depth ?? 0) * 3, 100);
                const tone = pct > 70 ? "danger" : pct > 40 ? "warning" : "primary";
                return (
                  <div key={q.queue_name}>
                    <div className="mb-1.5 flex items-center justify-between text-sm">
                      <span className="font-medium capitalize">{name}</span>
                      <span className="tabular-nums font-semibold">{q.depth ?? 0}</span>
                    </div>
                    <ProgressBar value={pct} tone={tone} />
                    <div className="mt-1 flex justify-between text-[11px] text-muted-foreground">
                      <span>{q.rate_tokens_avail?.toFixed(1) ?? "—"} tokens</span>
                      <span>{q.max_concurrent} concurrent</span>
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </Card>
      </section>

      {/* Pipelines + Workers */}
      <section className="mt-4 grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <div className="flex items-center gap-2">
              <GitBranch className="h-4 w-4 text-primary" />
              <CardTitle>Active pipelines</CardTitle>
            </div>
            <span className="text-xs text-muted-foreground">{pipelines.length} total</span>
          </CardHeader>
          {pipelinesLoading ? <LoadingSpinner /> : pipelines.length === 0 ? (
            <div className="py-8 text-center text-sm text-muted-foreground">No pipelines</div>
          ) : (
            <div className="p-4 space-y-2">
              {pipelines.map((p) => (
                <div key={p.id} className="flex items-center justify-between rounded-lg border border-border/40 bg-muted/20 px-4 py-3 hover:bg-primary/5 cursor-pointer transition-colors">
                  <div>
                    <p className="font-medium text-sm">{p.name}</p>
                    <p className="mt-0.5 text-xs text-muted-foreground">
                      {p.dag_spec.nodes.length} nodes · {p.dag_spec.edges.length} edges · {formatRelativeTime(p.updated_at)}
                    </p>
                  </div>
                  <StatusBadge status={p.status} />
                </div>
              ))}
            </div>
          )}
        </Card>

        <Card>
          <CardHeader>
            <div className="flex items-center gap-2">
              <Server className="h-4 w-4 text-primary" />
              <CardTitle>Worker capacity</CardTitle>
            </div>
            <div className="text-xs text-muted-foreground">{usedSlots}/{totalSlots} slots</div>
          </CardHeader>
          {workersLoading ? <LoadingSpinner /> : workers.length === 0 ? (
            <div className="py-8 text-center text-sm text-muted-foreground">No workers online</div>
          ) : (
            <div className="p-4 space-y-4">
              {workers.map((w) => {
                const pct   = Math.round((w.active_jobs / w.concurrency) * 100);
                const tone  = pct > 80 ? "danger" : pct > 50 ? "warning" : "primary";
                return (
                  <div key={w.id}>
                    <div className="mb-1.5 flex items-center justify-between text-sm">
                      <div className="flex items-center gap-2">
                        <span className="font-medium">{w.hostname}</span>
                        <StatusBadge status={w.status} />
                      </div>
                      <span className="tabular-nums text-muted-foreground">{w.active_jobs}/{w.concurrency}</span>
                    </div>
                    <ProgressBar value={w.active_jobs} max={w.concurrency} tone={tone} />
                  </div>
                );
              })}
            </div>
          )}
        </Card>
      </section>

      {/* Job status distribution */}
      <section className="mt-4">
        <SectionHeader title="Job status distribution" />
        <div className="grid grid-cols-4 gap-3 sm:grid-cols-8">
          {(["queued","scheduled","running","completed","retrying","failed","dead","cancelled"] as const).map(s => {
            const count = jobs.filter(j => j.status === s).length;
            return (
              <div key={s} className="rounded-xl border border-border/40 bg-card/40 p-3 text-center hover:border-primary/30 cursor-pointer transition-colors">
                <p className="text-xl font-semibold tabular-nums">{count}</p>
                <StatusBadge status={s} />
              </div>
            );
          })}
        </div>
      </section>
    </>
  );
}
