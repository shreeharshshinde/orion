import { Save } from "lucide-react";

import { Button, Card, PageHeader } from "@/components/ui";
import { queues } from "@/lib/api";
import { formatRelativeTime } from "@/lib/utils";

export default function QueuesPage() {
  return (
    <>
      <PageHeader
        title="Queues"
        description="Tune scheduler behavior live: queue depth, concurrency, weights, rate limits, burst, and enabled state."
      />

      <div className="grid gap-4 xl:grid-cols-3">
        {queues.map((queue) => {
          const shortName = queue.queue_name.replace("orion:queue:", "");
          const depthPct = Math.min((queue.depth ?? 0) * 3, 100);
          const depthColor = depthPct > 70 ? "bg-danger" : depthPct > 40 ? "bg-warning" : "bg-primary";

          return (
            <Card className="overflow-hidden" key={queue.queue_name}>
              {/* Header */}
              <div className="flex items-start justify-between p-5 pb-4">
                <div>
                  <h2 className="font-semibold capitalize">{shortName}</h2>
                  <p className="mt-0.5 font-mono text-xs text-muted-foreground">{queue.queue_name}</p>
                  <p className="mt-1 text-xs text-muted-foreground">
                    Updated {formatRelativeTime(queue.updated_at)}
                  </p>
                </div>
                <span
                  className={`rounded-full border px-2.5 py-1 text-xs font-medium ${
                    queue.enabled
                      ? "border-success/40 bg-success/10 text-success"
                      : "border-slate-500/40 bg-slate-400/10 text-slate-300"
                  }`}
                >
                  {queue.enabled ? "enabled" : "disabled"}
                </span>
              </div>

              {/* Depth bar */}
              <div className="border-t bg-muted/20 px-5 py-4">
                <div className="mb-1.5 flex items-center justify-between text-sm">
                  <span className="font-medium">Queue depth</span>
                  <span className="font-semibold">{queue.depth}</span>
                </div>
                <div className="h-2.5 overflow-hidden rounded-full bg-muted">
                  <div
                    className={`h-full rounded-full transition-all ${depthColor}`}
                    style={{ width: `${depthPct}%` }}
                  />
                </div>
                <p className="mt-1.5 text-xs text-muted-foreground">
                  {queue.rate_tokens_avail} rate tokens available
                </p>
              </div>

              {/* Stats grid */}
              <div className="grid grid-cols-2 gap-3 border-t p-5">
                <QueueStat label="Max concurrent" value={queue.max_concurrent} />
                <QueueStat label="Weight" value={queue.weight} />
                <QueueStat label="Rate / sec" value={queue.rate_per_sec} />
                <QueueStat label="Burst" value={queue.burst} />
              </div>

              <div className="border-t px-5 pb-5">
                <Button className="w-full" variant="outline">
                  <Save className="h-4 w-4" />
                  Save live config
                </Button>
              </div>
            </Card>
          );
        })}
      </div>
    </>
  );
}

function QueueStat({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="rounded-md border bg-background p-3">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className="mt-1 text-lg font-semibold">{value}</p>
    </div>
  );
}
