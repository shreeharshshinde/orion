"use client";

import { AlertCircle, ChevronLeft, ClipboardCopy, Loader2, RefreshCw, XCircle } from "lucide-react";
import Link from "next/link";
import { use, useState } from "react";
import { toast } from "sonner";

import { StatusBadge } from "@/components/status-badge";
import { Badge, Button, Card, CardBody, CardHeader, CardTitle, StatRow, Table, Td, Th } from "@/components/ui";
import { useCancelJob, useJob, useJobExecutions, useReplayJob } from "@/lib/hooks";
import { useTelemetry } from "@/lib/telemetry-context";
import { formatRelativeTime } from "@/lib/utils";

const TERMINAL = new Set(["completed", "failed", "dead", "cancelled"]);

type Execution = {
  id: string;
  attempt: number;
  worker_id: string;
  status: string;
  started_at?: string;
  finished_at?: string;
  exit_code?: number;
  error?: string;
};

export default function JobDetailPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  const [tab, setTab] = useState<"overview" | "payload" | "executions">("overview");

  const { jobs, apiConnected, demoMode } = useTelemetry();
  const { data: apiJob, isLoading: apiJobLoading, isError: apiJobError, refetch } = useJob(id);
  const { data: apiExecutions = [] } = useJobExecutions(id);

  const cancel = useCancelJob();
  const replay = useReplayJob();

  const job = demoMode ? jobs.find(j => j.id === id) : apiJob;
  const isLoading = demoMode ? false : apiJobLoading;
  const isError = demoMode ? !job : (apiJobError || !job);

  const executions = demoMode 
    ? (job ? [{
        id: `exec-${job.id}`,
        attempt: job.attempt,
        worker_id: job.worker_id ?? "worker-alpha",
        status: job.status,
        started_at: job.started_at ?? job.created_at,
        finished_at: job.completed_at,
        exit_code: job.status === "completed" ? 0 : job.status === "failed" ? 1 : undefined,
        error: job.error_message,
      }] : [])
    : apiExecutions;

  function handleCancel() {
    if (demoMode) {
      toast.success("Job cancel simulated in Sandbox mode");
      return;
    }
    cancel.mutate(id, {
      onSuccess: () => toast.success("Job cancelled"),
      onError: (e) => toast.error(e.message),
    });
  }

  function handleReplay() {
    if (demoMode) {
      toast.success("Job replay simulated in Sandbox mode");
      return;
    }
    replay.mutate(id, {
      onSuccess: () => toast.success("Job re-queued"),
      onError: (e) => toast.error(e.message),
    });
  }

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-32 text-muted-foreground">
        <Loader2 className="h-6 w-6 animate-spin" />
      </div>
    );
  }

  if (isError || !job) {
    return (
      <div className="py-16 text-center">
        <p className="text-sm text-danger">Failed to load job</p>
        <button className="mt-2 text-xs text-muted-foreground underline" onClick={() => refetch()}>Retry</button>
      </div>
    );
  }

  const isTerminal = TERMINAL.has(job.status);
  const canReplay = job.status === "dead" || job.status === "failed";

  return (
    <div className="space-y-5">
      {/* Back + actions */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-3">
          <Link href="/dashboard/jobs" className="text-muted-foreground hover:text-foreground transition-colors">
            <ChevronLeft className="h-5 w-5" />
          </Link>
          <div>
            <h1 className="font-display text-xl font-semibold">{job.name}</h1>
            <p className="font-mono text-xs text-muted-foreground">{job.id}</p>
          </div>
          <StatusBadge status={job.status} />
        </div>
        <div className="flex items-center gap-2">
          {canReplay && (
            <Button
              variant="secondary"
              size="sm"
              onClick={handleReplay}
              disabled={replay.isPending}
            >
              {replay.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RefreshCw className="h-3.5 w-3.5" />}
              Replay
            </Button>
          )}
          <Button
            variant="danger"
            size="sm"
            onClick={handleCancel}
            disabled={isTerminal || cancel.isPending}
          >
            {cancel.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <XCircle className="h-3.5 w-3.5" />}
            Cancel
          </Button>
        </div>
      </div>

      {/* Tabs */}
      <div className="flex gap-1 border-b">
        {(["overview", "payload", "executions"] as const).map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={`px-4 py-2 text-sm font-medium capitalize transition-colors ${
              tab === t
                ? "border-b-2 border-primary text-foreground"
                : "text-muted-foreground hover:text-foreground"
            }`}
          >
            {t}
          </button>
        ))}
      </div>

      {/* Overview tab */}
      {tab === "overview" && (
        <div className="grid gap-4 md:grid-cols-2">
          <Card>
            <CardHeader><CardTitle>Details</CardTitle></CardHeader>
            <CardBody className="divide-y divide-border/50 py-0">
              <StatRow label="Type" value={<Badge tone="neutral">{job.type}</Badge>} />
              <StatRow label="Queue" value={job.queue_name.replace("orion:queue:", "")} />
              <StatRow label="Priority" value={<Badge tone="default">P{job.priority}</Badge>} />
              <StatRow label="Attempt" value={`${job.attempt} / ${job.max_retries}`} />
              {job.worker_id && <StatRow label="Worker" value={job.worker_id} mono />}
              {job.idempotency_key && <StatRow label="Idempotency key" value={job.idempotency_key} mono />}
            </CardBody>
          </Card>

          <Card>
            <CardHeader><CardTitle>Timing</CardTitle></CardHeader>
            <CardBody className="divide-y divide-border/50 py-0">
              <StatRow label="Created" value={formatRelativeTime(job.created_at)} />
              <StatRow label="Scheduled" value={formatRelativeTime(job.scheduled_at)} />
              <StatRow label="Started" value={formatRelativeTime(job.started_at)} />
              <StatRow label="Completed" value={formatRelativeTime(job.completed_at)} />
              {job.next_retry_at && <StatRow label="Next retry" value={formatRelativeTime(job.next_retry_at)} />}
              {job.deadline_at && <StatRow label="Deadline" value={formatRelativeTime(job.deadline_at)} />}
            </CardBody>
          </Card>

          {job.error_message && (
            <Card className="md:col-span-2 border-danger/30">
              <CardBody>
                <div className="flex items-start gap-2 text-danger">
                  <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
                  <p className="text-sm">{job.error_message}</p>
                </div>
              </CardBody>
            </Card>
          )}
        </div>
      )}

      {/* Payload tab */}
      {tab === "payload" && (
        <Card>
          <CardHeader>
            <CardTitle>Payload</CardTitle>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                navigator.clipboard.writeText(JSON.stringify(job.payload ?? {}, null, 2));
                toast.success("Copied to clipboard");
              }}
            >
              <ClipboardCopy className="h-3.5 w-3.5" />
              Copy
            </Button>
          </CardHeader>
          <CardBody>
            <pre className="overflow-x-auto rounded-md bg-muted/50 p-4 text-xs leading-relaxed text-foreground">
              {JSON.stringify(job.payload ?? {}, null, 2)}
            </pre>
          </CardBody>
        </Card>
      )}

      {/* Executions tab */}
      {tab === "executions" && (
        <Card>
          <CardHeader><CardTitle>Execution history</CardTitle></CardHeader>
          {(executions as Execution[]).length === 0 ? (
            <CardBody>
              <p className="text-center text-sm text-muted-foreground">No executions recorded yet</p>
            </CardBody>
          ) : (
            <Table>
              <thead>
                <tr>
                  {["Attempt", "Worker", "Status", "Started", "Finished", "Exit code", "Error"].map((h) => (
                    <Th key={h}>{h}</Th>
                  ))}
                </tr>
              </thead>
              <tbody className="divide-y divide-border/50">
                {(executions as Execution[]).map((ex) => (
                  <tr key={ex.id} className="bg-card hover:bg-muted/20 transition-colors">
                    <Td className="tabular-nums">{ex.attempt}</Td>
                    <Td className="font-mono text-xs text-muted-foreground">{ex.worker_id ?? "—"}</Td>
                    <Td><StatusBadge status={ex.status as never} /></Td>
                    <Td className="text-xs text-muted-foreground whitespace-nowrap">{formatRelativeTime(ex.started_at)}</Td>
                    <Td className="text-xs text-muted-foreground whitespace-nowrap">{formatRelativeTime(ex.finished_at)}</Td>
                    <Td className="tabular-nums text-xs">{ex.exit_code ?? "—"}</Td>
                    <Td className="max-w-xs truncate text-xs text-danger">{ex.error ?? "—"}</Td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </Card>
      )}
    </div>
  );
}
