import { Server, AlertCircle, Clock } from "lucide-react";

import { StatusBadge } from "@/components/status-badge";
import { Card, PageHeader } from "@/components/ui";
import { workers } from "@/lib/api";
import { formatRelativeTime } from "@/lib/utils";

export default function WorkersPage() {
  const totalConcurrency = workers.reduce((s, w) => s + w.concurrency, 0);
  const totalActive = workers.reduce((s, w) => s + w.active_jobs, 0);

  const getHeartbeatFreshness = (heartbeat: string) => {
    const now = new Date();
    const lastBeat = new Date(heartbeat);
    const secondsAgo = (now.getTime() - lastBeat.getTime()) / 1000;

    if (secondsAgo < 30) return { level: "fresh", label: "Fresh" };
    if (secondsAgo < 60) return { level: "recent", label: "Recent" };
    if (secondsAgo < 300) return { level: "stale", label: "Stale" };
    return { level: "offline", label: "Offline" };
        title="Workers"
        description="Monitor active worker heartbeats, queue coverage, concurrency, active jobs, and available execution slots."
      />

      {/* Summary strip */}
      <div className="mb-6 grid gap-4 sm:grid-cols-3">
        <SummaryTile label="Active workers" value={workers.filter(w => w.status !== "offline").length} />
        <SummaryTile label="Total concurrency" value={totalConcurrency} />
        <SummaryTile label="Available slots" value={totalConcurrency - totalActive} />
      </div>

      <div className="grid gap-4">
        {workers.map((worker) => {
          const available = worker.concurrency - worker.active_jobs;
          const pct = (worker.active_jobs / worker.concurrency) * 100;
          const barColor = pct > 80 ? "bg-danger" : pct > 50 ? "bg-warning" : "bg-primary";
          const freshness = getHeartbeatFreshness(worker.last_heartbeat);
          const isOffline = worker.status === "offline" || freshness.level === "offline";

          return (
            <Card
              className={`overflow-hidden ${isOffline ? "opacity-50 bg-muted/20" : ""}`}
              key={worker.id}
            >
              <div className="flex flex-col gap-4 p-5 lg:flex-row lg:items-center lg:justify-between">
                {/* Identity */}
                <div className="flex items-start gap-4">
                  <div className={`flex h-11 w-11 shrink-0 items-center justify-center rounded-md border shadow-neon ${isOffline
                      ? "border-muted/60 bg-muted/40 text-muted-foreground"
                      : "border-primary/30 bg-primary/10 text-primary"
                    }`}>
                    <Server className="h-5 w-5" />
                  </div>
                  <div className="flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <h2 className="font-semibold">{worker.hostname}</h2>
                      <StatusBadge status={worker.status} />
                      {isOffline && (
                        <span className="inline-flex items-center gap-1 rounded-full border border-muted/60 bg-muted/40 px-2 py-0.5 text-xs font-medium text-muted-foreground">
                          Offline
                        </span>
                      )}
                    </div>
                    <p className="mt-0.5 font-mono text-xs text-muted-foreground">{worker.id}</p>
                    <div className="mt-1.5 flex flex-wrap gap-1">
                      {worker.queue_names.map((q) => (
                        <span
                          className="rounded border bg-muted/60 px-1.5 py-0.5 font-mono text-xs text-muted-foreground"
                          key={q}
                        >
                          {q.replace("orion:queue:", "")}
                        </span>
                      ))}
                    </div>
                  </div>
                </div>

                {/* Stats */}
                <div className="grid grid-cols-3 gap-3 text-center sm:min-w-80">
                  <WorkerStat label="Active" value={worker.active_jobs} />
                  <WorkerStat label="Concurrency" value={worker.concurrency} />
                  <WorkerStat label="Available" value={available} highlight={available > 0} />
                </div>
              </div>

              {/* Capacity bar */}
              <div className="border-t bg-muted/10 px-5 py-4">
                <div className="mb-1.5 flex items-center justify-between text-xs text-muted-foreground">
                  <span>Capacity utilization</span>
                  <span className="font-medium">{Math.round(pct)}%</span>
                </div>
                <div className="h-2 overflow-hidden rounded-full bg-muted">
                  <div
                    className={`h-full rounded-full transition-all ${barColor}`}
                    style={{ width: `${pct}%` }}
                  />
                </div>

                {/* Heartbeat freshness */}
                <div className="mt-3 flex items-center justify-between">
                  <div className="flex items-center gap-2">
                    <Clock className="h-3.5 w-3.5 text-muted-foreground" />
                    <span className="text-xs text-muted-foreground">
                      Last heartbeat
                    </span>
                  </div>
                  <div className="flex items-center gap-1.5">
                    <span className="text-xs text-muted-foreground">
                      {formatRelativeTime(worker.last_heartbeat)}
                      <span className="ml-1 font-medium">({freshness.label})</span>
                    </span>
                  </div>
                </div>
                <p className="mt-2 text-xs text-muted-foreground">
                  Registered {formatRelativeTime(worker.registered_at)}
                </p>
              </div>
            </Card>
          );
        })}
      </div>
    </>
  );
}

function WorkerStat({
  label,
  value,
  highlight = false,
}: {
  label: string;
  value: number;
  highlight?: boolean;
}) {
  return (
    <div className="flex flex-col items-center gap-1">
      <p className={`text-sm ${highlight ? "font-semibold text-primary" : "text-muted-foreground"}`}>
        {value}
      </p>
      <p className="text-xs text-muted-foreground">{label}</p>
    </div>
  );
}

function SummaryTile({ label, value }: { label: string; value: number }) {
  return (
    <Card className="p-4">
      <p className="text-xs text-muted-foreground uppercase tracking-wider">{label}</p>
      <p className="mt-2 text-3xl font-semibold tabular-nums">{value}</p>
    </Card>
  );
}
<div className="rounded-md border bg-background p-3">
  <p className="text-xs text-muted-foreground">{label}</p>
  <p className={`mt-1 text-xl font-semibold ${highlight ? "text-success" : ""}`}>{value}</p>
</div>
  );
}

function SummaryTile({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-lg border bg-card p-4">
      <p className="text-sm text-muted-foreground">{label}</p>
      <p className="mt-1 text-2xl font-semibold">{value}</p>
    </div>
  );
}
