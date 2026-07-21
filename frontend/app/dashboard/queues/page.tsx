"use client";

import { Loader2, Save, Zap } from "lucide-react";
import { toast } from "sonner";

import { Button, Card, PageHeader, UtilizationRing } from "@/components/ui";
import { useUpdateQueue } from "@/lib/hooks";
import { useTelemetry } from "@/lib/telemetry-context";
import { formatRelativeTime } from "@/lib/utils";
import type { QueueConfig } from "@/lib/api";

export default function QueuesPage() {
  const { queues, queuesLoading, apiConnected, demoMode } = useTelemetry();
  const updateQueue = useUpdateQueue();

  function handleSave(queue: QueueConfig) {
    const name = queue.queue_name.replace("orion:queue:", "");
    if (demoMode) {
      toast.success(`Queue ${name} config update simulated in Sandbox mode`);
      return;
    }
    const { max_concurrent, weight, rate_per_sec, burst, enabled } = queue;
    updateQueue.mutate(
      { name, body: { max_concurrent, weight, rate_per_sec, burst, enabled } },
      {
        onSuccess: () => toast.success(`Queue ${name} config updated (live)`),
        onError: (err: Error) => toast.error(err.message),
      }
    );
  }

  if (queuesLoading) {
    return (
      <>
        <PageHeader title="Queues" description="Tune scheduler behavior live." />
        <div className="flex items-center justify-center py-16 text-muted-foreground">
          <Loader2 className="h-5 w-5 animate-spin text-primary" />
        </div>
      </>
    );
  }

  if (!apiConnected && !demoMode) {
    return (
      <>
        <PageHeader title="Queues" description="Tune scheduler behavior live." />
        <div className="py-12 text-center">
          <p className="text-sm text-danger">Failed to connect to live Orion API</p>
          <p className="text-xs text-muted-foreground mt-1">Check if the backend is running, or switch to simulated Sandbox above.</p>
        </div>
      </>
    );
  }

  return (
    <>
      <PageHeader
        title="Queues"
        description="Tune scheduler behavior live: queue depth, concurrency, weights, rate limits, burst, and enabled state."
      />

      <div className="grid gap-4 xl:grid-cols-3">
        {queues.length === 0 && (
          <div className="col-span-3 py-12 text-center text-sm text-muted-foreground">No queues found</div>
        )}
        {queues.map((queue) => {
          const shortName = queue.queue_name.replace("orion:queue:", "");
          return (
            <Card className="overflow-hidden" key={queue.queue_name}>
              <div className="flex items-start justify-between p-5 pb-4">
                <div>
                  <h2 className="font-semibold capitalize">{shortName}</h2>
                  <p className="mt-0.5 font-mono text-xs text-muted-foreground">{queue.queue_name}</p>
                  <p className="mt-1 text-xs text-muted-foreground">Updated {formatRelativeTime(queue.updated_at)}</p>
                </div>
                <span className={`rounded-full border px-2.5 py-1 text-xs font-medium ${queue.enabled ? "border-success/40 bg-success/10 text-success" : "border-slate-500/40 bg-slate-400/10 text-slate-300"}`}>
                  {queue.enabled ? "enabled" : "disabled"}
                </span>
              </div>

              <div className="border-t bg-muted/20 px-5 py-5 flex flex-col items-center">
                <div className="flex flex-col items-center gap-2 mb-4">
                  <UtilizationRing value={queue.depth ?? 0} max={33} size={80} />
                  <p className="text-xs text-muted-foreground text-center">Queue depth</p>
                </div>
                <p className="text-xs text-muted-foreground text-center">
                  <span className="font-semibold text-foreground">{queue.depth ?? 0}</span> / <span className="text-muted-foreground">33 max</span>
                </p>
              </div>

              <div className="border-t bg-background/40 px-5 py-4">
                <div className="flex items-center justify-between mb-2">
                  <div className="flex items-center gap-2">
                    <Zap className="h-4 w-4 text-primary" />
                    <span className="text-xs font-medium">Rate tokens</span>
                  </div>
                  <span className="text-xs font-semibold text-primary">
                    {(queue.rate_tokens_avail ?? 0).toFixed(1)} / {queue.burst}
                  </span>
                </div>
                <div className="h-1.5 overflow-hidden rounded-full bg-muted">
                  <div
                    className="h-full rounded-full transition-all duration-500 bg-primary"
                    style={{ width: `${Math.min(((queue.rate_tokens_avail ?? 0) / queue.burst) * 100, 100)}%` }}
                  />
                </div>
                <p className="mt-2 text-xs text-muted-foreground">Refills at {queue.rate_per_sec}/sec</p>
              </div>

              <div className="grid grid-cols-2 gap-3 border-t p-5">
                <QueueStat label="Max concurrent" value={queue.max_concurrent} />
                <QueueStat label="Weight" value={queue.weight} />
                <QueueStat label="Rate / sec" value={queue.rate_per_sec} />
                <QueueStat label="Burst" value={queue.burst} />
              </div>

              <div className="border-t px-5 pb-5">
                <Button
                  className="w-full"
                  variant="outline"
                  onClick={() => handleSave(queue)}
                  disabled={updateQueue.isPending}
                >
                  {updateQueue.isPending
                    ? <Loader2 className="h-4 w-4 animate-spin" />
                    : <Save className="h-4 w-4" />}
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
