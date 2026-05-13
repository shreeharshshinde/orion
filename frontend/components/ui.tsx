import { Slot } from "@radix-ui/react-slot";
import type { ButtonHTMLAttributes, HTMLAttributes, ReactNode } from "react";

import { cn } from "@/lib/utils";

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  asChild?: boolean;
  variant?: "default" | "secondary" | "ghost" | "outline";
  size?: "sm" | "md" | "icon";
};

export function Button({
  asChild,
  className,
  variant = "default",
  size = "md",
  ...props
}: ButtonProps) {
  const Comp = asChild ? Slot : "button";

  return (
    <Comp
      className={cn(
        "inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium transition focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50",
        variant === "default" && "bg-primary text-primary-foreground shadow-neon hover:bg-cyan-300",
        variant === "secondary" && "border border-violet/30 bg-violet/20 text-foreground hover:bg-violet/25",
        variant === "ghost" && "hover:bg-muted",
        variant === "outline" && "border bg-card/80 hover:border-primary/70 hover:bg-primary/10 hover:text-primary",
        size === "sm" && "h-8 px-3",
        size === "md" && "h-10 px-4",
        size === "icon" && "h-9 w-9",
        className
      )}
      {...props}
    />
  );
}

export function Card({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "rounded-lg border bg-card/80 text-card-foreground shadow-soft backdrop-blur-md",
        className
      )}
      {...props}
    />
  );
}

export function Badge({
  className,
  tone = "default",
  ...props
}: HTMLAttributes<HTMLSpanElement> & {
  tone?: "default" | "success" | "warning" | "danger" | "neutral" | "aqua";
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full border px-2.5 py-1 text-xs font-medium",
        tone === "default" && "border-primary/40 bg-primary/10 text-primary",
        tone === "success" && "border-success/40 bg-success/10 text-success",
        tone === "warning" && "border-warning/40 bg-warning/10 text-warning",
        tone === "danger" && "border-danger/40 bg-danger/10 text-danger",
        tone === "neutral" && "border-slate-500/40 bg-slate-400/10 text-slate-300",
        tone === "aqua" && "border-cyan-300/40 bg-cyan-300/10 text-cyan-200",
        className
      )}
      {...props}
    />
  );
}

export function MetricCard({
  label,
  value,
  detail,
  icon,
  tone = "aqua"
}: {
  label: string;
  value: string | number;
  detail: string;
  icon: ReactNode;
  tone?: "aqua" | "success" | "warning" | "danger";
}) {
  return (
    <Card className="p-5">
      <div className="flex items-start justify-between gap-4">
        <div>
          <p className="text-sm text-muted-foreground">{label}</p>
          <p className="mt-2 text-3xl font-semibold tracking-normal">{value}</p>
        </div>
        <div
          className={cn(
            "rounded-md border p-2 shadow-neon",
            tone === "aqua" && "border-primary/30 bg-primary/10 text-primary",
            tone === "success" && "border-success/30 bg-success/10 text-success",
            tone === "warning" && "border-warning/30 bg-warning/10 text-warning",
            tone === "danger" && "border-danger/30 bg-danger/10 text-danger"
          )}
        >
          {icon}
        </div>
      </div>
      <p className="mt-4 text-sm text-muted-foreground">{detail}</p>
    </Card>
  );
}

export function PageHeader({
  title,
  description,
  action
}: {
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <div className="mb-6 flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
      <div>
        <h1 className="font-display text-2xl font-semibold tracking-normal">{title}</h1>
        <p className="mt-2 max-w-3xl text-sm text-muted-foreground">{description}</p>
      </div>
      {action}
    </div>
  );
}

export function StatusDot({ tone = "success" }: { tone?: "success" | "warning" | "danger" }) {
  return (
    <span
      className={cn(
        "h-2.5 w-2.5 rounded-full shadow-neon",
        tone === "success" && "bg-success",
        tone === "warning" && "bg-warning",
        tone === "danger" && "bg-danger"
      )}
    />
  );
}
