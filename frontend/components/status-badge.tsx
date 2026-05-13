import { Badge } from "@/components/ui";
import type { JobStatus, PipelineStatus, WorkerStatus } from "@/lib/api";

type Status = JobStatus | PipelineStatus | WorkerStatus;

export function StatusBadge({ status }: { status: Status }) {
  const tone =
    status === "completed" || status === "idle"
      ? "success"
      : status === "running" || status === "busy"
        ? "aqua"
        : status === "scheduled" || status === "retrying" || status === "pending" || status === "draining"
          ? "warning"
          : status === "failed" || status === "dead"
            ? "danger"
            : "neutral";

  return <Badge tone={tone}>{status}</Badge>;
}
