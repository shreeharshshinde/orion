import { cn } from "@/lib/utils";
import type { JobStatus, PipelineStatus, WorkerStatus } from "@/lib/api";

type Status = JobStatus | PipelineStatus | WorkerStatus;

const config: Record<string, { dot: string; text: string; bg: string; border: string; pulse?: boolean }> = {
  // Job statuses
  queued:     { dot: "bg-primary",          text: "text-primary",          bg: "bg-primary/10",  border: "border-primary/30" },
  scheduled:  { dot: "bg-warning",          text: "text-warning",          bg: "bg-warning/10",  border: "border-warning/30" },
  running:    { dot: "bg-primary",          text: "text-primary",          bg: "bg-primary/10",  border: "border-primary/40", pulse: true },
  completed:  { dot: "bg-success",          text: "text-success",          bg: "bg-success/10",  border: "border-success/30" },
  failed:     { dot: "bg-danger",           text: "text-danger",           bg: "bg-danger/10",   border: "border-danger/30" },
  retrying:   { dot: "bg-warning",          text: "text-warning",          bg: "bg-warning/10",  border: "border-warning/40", pulse: true },
  dead:       { dot: "bg-danger",           text: "text-danger",           bg: "bg-danger/15",   border: "border-danger/50" },
  cancelled:  { dot: "bg-muted-foreground", text: "text-muted-foreground", bg: "bg-muted/50",    border: "border-border/60" },
  // Pipeline statuses
  pending:    { dot: "bg-muted-foreground", text: "text-muted-foreground", bg: "bg-muted/40",    border: "border-border/40" },
  // Worker statuses
  idle:       { dot: "bg-success",          text: "text-success",          bg: "bg-success/10",  border: "border-success/30" },
  busy:       { dot: "bg-primary",          text: "text-primary",          bg: "bg-primary/10",  border: "border-primary/40", pulse: true },
  draining:   { dot: "bg-warning",          text: "text-warning",          bg: "bg-warning/10",  border: "border-warning/30" },
  offline:    { dot: "bg-danger",           text: "text-muted-foreground", bg: "bg-muted/30",    border: "border-border/40" },
};

export function StatusBadge({ status }: { status: Status }) {
  const c = config[status] ?? config.cancelled;
  return (
    <span className={cn(
      "inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs font-medium",
      c.bg, c.border, c.text
    )}>
      <span className={cn("h-1.5 w-1.5 rounded-full shrink-0", c.dot, c.pulse && "animate-pulse")} />
      {status}
    </span>
  );
}
