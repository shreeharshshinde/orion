import { cn } from "@/lib/utils";
import { Slot } from "@radix-ui/react-slot";
import type { ButtonHTMLAttributes, HTMLAttributes, ReactNode, TdHTMLAttributes, ThHTMLAttributes } from "react";

// ─── Button ──────────────────────────────────────────────────────────────────

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  asChild?: boolean;
  variant?: "default" | "secondary" | "ghost" | "outline" | "danger";
  size?: "xs" | "sm" | "md" | "icon";
};

export function Button({ asChild, className, variant = "default", size = "md", ...props }: ButtonProps) {
  const Comp = asChild ? Slot : "button";
  return (
    <Comp
      className={cn(
        "inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium transition focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-40",
        variant === "default" && "bg-primary text-primary-foreground shadow hover:bg-primary/80 glow-primary",
        variant === "secondary" && "border border-violet/30 bg-violet/15 text-foreground hover:bg-violet/25",
        variant === "ghost" && "hover:bg-muted text-muted-foreground hover:text-foreground",
        variant === "outline" && "border bg-card/60 hover:border-primary/60 hover:bg-primary/10 hover:text-primary",
        variant === "danger" && "border border-danger/40 bg-danger/10 text-danger hover:bg-danger/20",
        size === "xs" && "h-6 px-2 text-xs",
        size === "sm" && "h-8 px-3",
        size === "md" && "h-9 px-4",
        size === "icon" && "h-8 w-8",
        className
      )}
      {...props}
    />
  );
}

// ─── Card ────────────────────────────────────────────────────────────────────

export function Card({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("rounded-xl border bg-card/70 text-card-foreground shadow-soft backdrop-blur-md", className)}
      {...props}
    />
  );
}

export function CardHeader({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("flex items-center justify-between border-b px-5 py-4", className)} {...props} />;
}

export function CardTitle({ className, ...props }: HTMLAttributes<HTMLHeadingElement>) {
  return <h2 className={cn("font-display font-semibold", className)} {...props} />;
}

export function CardBody({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("p-5", className)} {...props} />;
}

// ─── Badge ───────────────────────────────────────────────────────────────────

export function Badge({
  className,
  tone = "default",
  ...props
}: HTMLAttributes<HTMLSpanElement> & {
  tone?: "default" | "success" | "warning" | "danger" | "neutral" | "aqua" | "violet";
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs font-medium",
        tone === "default" && "border-primary/40 bg-primary/10 text-primary",
        tone === "success" && "border-success/40 bg-success/10 text-success",
        tone === "warning" && "border-warning/40 bg-warning/10 text-warning",
        tone === "danger" && "border-danger/40 bg-danger/10 text-danger",
        tone === "neutral" && "border-border/60 bg-muted/60 text-muted-foreground",
        tone === "aqua" && "border-primary/40 bg-primary/10 text-primary",
        tone === "violet" && "border-violet/40 bg-violet/10 text-violet-300",
        className
      )}
      {...props}
    />
  );
}

// ─── MetricCard ───────────────────────────────────────────────────────────────

export function MetricCard({
  label,
  value,
  detail,
  icon,
  tone = "aqua",
  delta,
  deltaLabel,
}: {
  label: string;
  value: string | number;
  detail: string;
  icon?: ReactNode;
  tone?: "aqua" | "success" | "warning" | "danger";
  delta?: number;          // positive = up, negative = down
  deltaLabel?: string;     // e.g. "vs last hour"
}) {
  const toneClass = {
    aqua: "border-primary/30 bg-primary/10 text-primary",
    success: "border-success/30 bg-success/10 text-success",
    warning: "border-warning/30 bg-warning/10 text-warning",
    danger: "border-danger/30 bg-danger/10 text-danger",
  }[tone];

  return (
    <Card className="relative overflow-hidden p-5">
      {/* faint background glow */}
      <div className={cn("pointer-events-none absolute -right-6 -top-6 h-24 w-24 rounded-full blur-2xl opacity-20",
        tone === "aqua" && "bg-primary",
        tone === "success" && "bg-success",
        tone === "warning" && "bg-warning",
        tone === "danger" && "bg-danger",
      )} />
      <div className="relative flex items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">{label}</p>
          <p className="mt-2 font-display text-3xl font-semibold tabular-nums">{value}</p>
        </div>
        {icon && (
          <div className={cn("rounded-lg border p-2.5 shrink-0", toneClass)}>
            {icon}
          </div>
        )}
      </div>
      <div className="relative mt-3 flex items-center justify-between">
        <p className="text-xs text-muted-foreground">{detail}</p>
        {delta !== undefined && (
          <span className={cn("text-xs font-medium", delta >= 0 ? "text-success" : "text-danger")}>
            {delta >= 0 ? "↑" : "↓"} {Math.abs(delta)}
            {deltaLabel && <span className="ml-1 text-muted-foreground font-normal">{deltaLabel}</span>}
          </span>
        )}
      </div>
    </Card>
  );
}

// ─── PageHeader ──────────────────────────────────────────────────────────────

export function PageHeader({ title, description, action, badge }: {
  title: string;
  description?: string;
  action?: ReactNode;
  badge?: ReactNode;
}) {
  return (
    <div className="mb-6 flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
      <div>
        <div className="flex items-center gap-3">
          <h1 className="font-display text-2xl font-semibold">{title}</h1>
          {badge}
        </div>
        {description && <p className="mt-1.5 max-w-2xl text-sm text-muted-foreground">{description}</p>}
      </div>
      {action && <div className="shrink-0">{action}</div>}
    </div>
  );
}

// ─── StatusDot ───────────────────────────────────────────────────────────────

export function StatusDot({ tone = "success", pulse = false }: {
  tone?: "success" | "warning" | "danger" | "neutral";
  pulse?: boolean;
}) {
  return (
    <span className={cn(
      "inline-block h-2 w-2 rounded-full shrink-0",
      tone === "success" && "bg-success",
      tone === "warning" && "bg-warning",
      tone === "danger" && "bg-danger",
      tone === "neutral" && "bg-muted-foreground",
      pulse && "animate-pulse",
    )} />
  );
}

// ─── Table primitives ─────────────────────────────────────────────────────────

export function Table({ className, ...props }: HTMLAttributes<HTMLTableElement>) {
  return (
    <div className="overflow-x-auto">
      <table className={cn("w-full text-left text-sm", className)} {...props} />
    </div>
  );
}

export function Th({ className, ...props }: ThHTMLAttributes<HTMLTableCellElement>) {
  return (
    <th className={cn("border-b bg-muted/40 px-4 py-2.5 text-xs font-medium uppercase tracking-wider text-muted-foreground first:pl-5 last:pr-5", className)} {...props} />
  );
}

export function Td({ className, ...props }: TdHTMLAttributes<HTMLTableCellElement>) {
  return <td className={cn("px-4 py-3 first:pl-5 last:pr-5", className)} {...props} />;
}

// ─── StatRow ──────────────────────────────────────────────────────────────────

export function StatRow({ label, value, mono = false }: { label: string; value: ReactNode; mono?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-4 py-2 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className={cn("font-medium", mono && "font-mono text-xs")}>{value}</span>
    </div>
  );
}

// ─── ProgressBar ─────────────────────────────────────────────────────────────

export function ProgressBar({ value, max = 100, tone = "primary" }: {
  value: number;
  max?: number;
  tone?: "primary" | "success" | "warning" | "danger";
}) {
  const pct = Math.min(Math.round((value / max) * 100), 100);
  return (
    <div className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
      <div
        className={cn(
          "h-full rounded-full transition-all duration-500",
          tone === "primary" && "bg-primary",
          tone === "success" && "bg-success",
          tone === "warning" && "bg-warning",
          tone === "danger" && "bg-danger",
        )}
        style={{ width: `${pct}%` }}
      />
    </div>
  );
}

// ─── SectionHeader ───────────────────────────────────────────────────────────

export function SectionHeader({ title, action, icon }: {
  title: string;
  action?: ReactNode;
  icon?: ReactNode;
}) {
  return (
    <div className="mb-3 flex items-center justify-between">
      <div className="flex items-center gap-2">
        {icon && <span className="text-primary">{icon}</span>}
        <h2 className="font-display text-sm font-semibold uppercase tracking-wider text-muted-foreground">{title}</h2>
      </div>
      {action}
    </div>
  );
}

// ─── UtilizationRing ──────────────────────────────────────────────────────────

export function UtilizationRing({
  value,
  max = 100,
  size = 80,
}: {
  value: number;
  max?: number;
  size?: number;
}) {
  const pct = Math.min((value / max) * 100, 100);
  const circumference = 2 * Math.PI * 30; // radius = 30
  const offset = circumference - (pct / 100) * circumference;

  const strokeColor = "#3b82f6"; // primary/aqua color
  const bgColor = "hsl(var(--primary) / 0.1)";

  return (
    <div className="flex flex-col items-center gap-2">
      <svg width={size} height={size} viewBox="0 0 80 80" className="shrink-0">
        <circle cx="40" cy="40" r="30" fill="none" stroke={bgColor} strokeWidth="3" />
        <circle
          cx="40"
          cy="40"
          r="30"
          fill="none"
          stroke={strokeColor}
          strokeWidth="3"
          strokeDasharray={circumference}
          strokeDashoffset={offset}
          strokeLinecap="round"
          transform="rotate(-90 40 40)"
          className="transition-all duration-500"
        />
        <text x="40" y="45" textAnchor="middle" className="text-sm font-semibold" fill="currentColor">
          {Math.round(pct)}%
        </text>
      </svg>
    </div>
  );
}

// ─── EmptyState ───────────────────────────────────────────────────────────────

export function EmptyState({
  icon,
  title,
  description,
  action,
}: {
  icon?: ReactNode;
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <div className="flex flex-col items-center justify-center gap-4 py-16 text-center">
      {icon && <div className="text-muted-foreground opacity-40">{icon}</div>}
      <div>
        <p className="font-medium text-muted-foreground">{title}</p>
        {description && <p className="mt-1 max-w-sm text-xs text-muted-foreground/60">{description}</p>}
      </div>
      {action && <div className="mt-2">{action}</div>}
    </div>
  );
}

// ─── ErrorState ───────────────────────────────────────────────────────────────

export function ErrorState({
  message,
  onRetry,
}: {
  message: string;
  onRetry?: () => void;
}) {
  return (
    <div className="flex flex-col items-center justify-center gap-4 py-16 text-center border border-danger/25 rounded-xl bg-danger/5 px-6">
      <div className="text-danger">
        <svg className="h-8 w-8" fill="none" stroke="currentColor" strokeWidth="2" viewBox="0 0 24 24">
          <path strokeLinecap="round" strokeLinejoin="round" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z" />
        </svg>
      </div>
      <div>
        <p className="font-semibold text-danger">Connection issue</p>
        <p className="mt-1 max-w-md text-xs text-muted-foreground/70">{message}</p>
      </div>
      {onRetry && (
        <Button onClick={onRetry} variant="danger" size="sm">
          Retry connection
        </Button>
      )}
    </div>
  );
}

// ─── EnvironmentBadge ─────────────────────────────────────────────────────────

import { ENV_META, type Environment } from "@/lib/status-colors";

export function EnvironmentBadge({ env }: { env: Environment }) {
  const meta = ENV_META[env];

  return (
    <span
      className="inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-[10px] font-semibold font-mono uppercase tracking-wider bg-card/65 backdrop-blur-sm"
      style={{
        borderColor: `${meta.color}35`,
        color: meta.color,
      }}
    >
      <span className="h-1.5 w-1.5 rounded-full shrink-0" style={{ backgroundColor: meta.color }} />
      {meta.label}
    </span>
  );
}

// ─── Skeletons ────────────────────────────────────────────────────────────────

import { motion } from "framer-motion";

const shimmer = {
  animate: { opacity: [0.35, 0.65, 0.35] },
  transition: { duration: 1.5, repeat: Infinity, ease: "easeInOut" as const },
};

function Bar({ width, height = 12 }: { width: string | number; height?: number }) {
  return (
    <motion.div
      {...shimmer}
      className="bg-muted-foreground/15 rounded animate-pulse"
      style={{
        width,
        height,
      }}
    />
  );
}

export function SkeletonTableRows({ rows = 5, columns = 4 }: { rows?: number; columns?: number }) {
  return (
    <div className="flex flex-col gap-1 w-full">
      {Array.from({ length: rows }).map((_, r) => (
        <div
          key={r}
          className="grid gap-4 py-3 px-4 border-b border-border/40"
          style={{
            gridTemplateColumns: `repeat(${columns}, 1fr)`,
          }}
        >
          {Array.from({ length: columns }).map((_, c) => (
            <Bar key={c} width={c === 0 ? "70%" : "45%"} />
          ))}
        </div>
      ))}
    </div>
  );
}

export function SkeletonChart({ height = 200 }: { height?: number }) {
  const bars = [40, 65, 45, 80, 60, 90, 70, 85, 55, 95, 75, 100];
  return (
    <div className="flex items-end gap-2.5 px-2 w-full" style={{ height }}>
      {bars.map((h, i) => (
        <motion.div
          key={i}
          animate={{ opacity: [0.25, 0.5, 0.25] }}
          transition={{ duration: 1.5, repeat: Infinity, ease: "easeInOut", delay: i * 0.05 }}
          className="flex-1 bg-muted-foreground/15 rounded-t"
          style={{
            height: `${h}%`,
          }}
        />
      ))}
    </div>
  );
}

export function SkeletonMetricCard() {
  return (
    <div className="bg-card/70 border border-border/50 rounded-xl p-5 flex flex-col gap-2.5">
      <Bar width="60%" height={10} />
      <Bar width="40%" height={26} />
    </div>
  );
}

export function SkeletonDAG() {
  const nodes = [{ x: 8 }, { x: 40 }, { x: 40 }, { x: 72 }];
  return (
    <div className="relative w-full px-2" style={{ height: 140 }}>
      {nodes.map((n, i) => (
        <motion.div
          key={i}
          {...shimmer}
          transition={{ ...shimmer.transition, delay: i * 0.1 }}
          className="absolute bg-muted-foreground/15 rounded-lg"
          style={{
            left: `${n.x}%`,
            top: i === 1 ? "20%" : i === 2 ? "60%" : "40%",
            width: 96,
            height: 40,
          }}
        />
      ))}
    </div>
  );
}
