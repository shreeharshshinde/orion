"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import {
  Activity, BookOpen, Boxes, BriefcaseBusiness,
  GitBranch, Home, Search, Server, Waypoints, ChevronRight, Zap
} from "lucide-react";
import type { ReactNode } from "react";

import { Button, StatusDot } from "@/components/ui";
import { cn } from "@/lib/utils";
import { useHealth, useJobs, useWorkers } from "@/lib/hooks";

const navItems = [
  { href: "/dashboard",           label: "Overview",  icon: Activity },
  { href: "/dashboard/jobs",      label: "Jobs",      icon: BriefcaseBusiness },
  { href: "/dashboard/pipelines", label: "Pipelines", icon: GitBranch },
  { href: "/dashboard/queues",    label: "Queues",    icon: Waypoints },
  { href: "/dashboard/workers",   label: "Workers",   icon: Server },
  { href: "/docs",                label: "Docs",      icon: BookOpen },
  { href: "/",                    label: "Home",      icon: Home },
];

export function AppShell({ children }: { children: ReactNode }) {
  const pathname = usePathname();

  const { data: jobs = [] }      = useJobs();
  const { data: workers = [] }   = useWorkers();
  const {
    isError: apiDown,
    isLoading: healthLoading,
    isSuccess: apiOk,
  } = useHealth();

  const activeJobs    = jobs.filter(j => j.status === "running" || j.status === "queued").length;
  const runningJobs   = jobs.filter(j => j.status === "running").length;
  const queuedJobs    = jobs.filter(j => j.status === "queued").length;
  const failedJobs    = jobs.filter(j => j.status === "failed" || j.status === "dead").length;
  const activeWorkers = workers.filter(w => w.status !== "offline").length;

  const healthTone: "success" | "warning" | "danger" =
    apiOk ? "success" : apiDown ? "danger" : "warning";
  const healthLabel = apiOk ? "API ready" : healthLoading ? "Checking API" : "API down";

  return (
    <div className="min-h-screen bg-grid">
      {/* Sidebar */}
      <aside className="fixed inset-y-0 left-0 z-30 hidden w-60 flex-col border-r border-border/60 bg-card/60 backdrop-blur-xl lg:flex">
        {/* Logo */}
        <div className="flex h-14 shrink-0 items-center gap-3 border-b border-border/60 px-4">
          <div className="flex h-8 w-8 items-center justify-center rounded-lg border border-primary/40 bg-primary/15 text-primary glow-primary">
            <Boxes className="h-4 w-4" />
          </div>
          <div className="min-w-0">
            <p className="font-display text-sm font-semibold tracking-wide text-gradient">Orion</p>
            <p className="text-[10px] text-muted-foreground">ML Orchestration</p>
          </div>
          <span className="ml-auto shrink-0 rounded border border-warning/30 bg-warning/10 px-1.5 py-0.5 text-[10px] font-medium text-warning">
            LOCAL
          </span>
        </div>

        {/* Nav */}
        <nav className="flex-1 space-y-0.5 overflow-y-auto px-2 py-3">
          <p className="mb-1 px-2 text-[10px] font-semibold uppercase tracking-widest text-muted-foreground/50">
            Platform
          </p>
          {navItems.map((item) => {
            const Icon   = item.icon;
            const active = item.href === "/dashboard"
              ? pathname === item.href
              : pathname.startsWith(item.href);

            const count = item.href === "/dashboard/jobs"    ? activeJobs
                        : item.href === "/dashboard/workers" ? activeWorkers
                        : undefined;

            return (
              <Link
                key={item.href}
                href={item.href}
                className={cn(
                  "group relative flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-all",
                  active
                    ? "border border-primary/25 bg-primary/10 text-primary shadow-sm"
                    : "text-muted-foreground hover:bg-muted/60 hover:text-foreground"
                )}
              >
                {active && <span className="absolute inset-y-2 left-0 w-0.5 rounded-r-full bg-primary" />}
                <Icon className={cn("h-4 w-4 shrink-0", active && "text-primary")} />
                <span className="flex-1">{item.label}</span>
                {count != null && count > 0 && (
                  <span className={cn(
                    "rounded-full px-1.5 py-0.5 text-[10px] font-semibold tabular-nums",
                    active ? "bg-primary/20 text-primary" : "bg-muted text-muted-foreground"
                  )}>
                    {count}
                  </span>
                )}
                {!active && <ChevronRight className="h-3 w-3 opacity-0 transition group-hover:opacity-40" />}
              </Link>
            );
          })}
        </nav>

        {/* System status footer */}
        <div className="shrink-0 border-t border-border/60 p-3">
          <div className="rounded-lg border border-border/60 bg-muted/30 p-3">
            <div className="flex items-center justify-between text-xs">
              <div className="flex items-center gap-2">
                <StatusDot tone={healthTone} pulse={healthLoading || apiOk} />
                <span className="font-medium">{healthLabel}</span>
              </div>
              <span className="text-muted-foreground">5s refresh</span>
            </div>
            <div className="mt-2 grid grid-cols-2 gap-1 text-[10px] text-muted-foreground">
              <span>{activeWorkers} workers</span>
              <span>{runningJobs} running</span>
              <span>{queuedJobs} queued</span>
              <span>{failedJobs} failed</span>
            </div>
          </div>
        </div>
      </aside>

      {/* Main area */}
      <div className="lg:pl-60">
        <header className="sticky top-0 z-20 flex h-14 items-center gap-3 border-b border-border/50 bg-background/70 px-4 backdrop-blur-xl lg:px-5">
          <Link href="/" className="flex items-center gap-2 font-semibold lg:hidden">
            <Boxes className="h-5 w-5 text-primary" />
            <span className="font-display text-sm">Orion</span>
          </Link>

          <div className="hidden h-8 flex-1 max-w-md items-center gap-2 rounded-lg border border-border/60 bg-muted/30 px-3 text-xs text-muted-foreground transition hover:border-primary/40 hover:bg-muted/50 md:flex cursor-pointer">
            <Search className="h-3.5 w-3.5 shrink-0" />
            <span>Search jobs, pipelines…</span>
            <kbd className="ml-auto rounded border border-border/80 bg-card px-1.5 py-0.5 font-mono text-[10px]">⌘K</kbd>
          </div>

          <div className="ml-auto flex items-center gap-2">
            <div className="hidden items-center gap-1.5 rounded-lg border border-border/60 bg-muted/30 px-3 py-1.5 text-xs sm:flex">
              <Zap className="h-3 w-3 text-primary" />
              <span className="font-medium text-primary">{runningJobs}</span>
              <span className="text-muted-foreground">running</span>
            </div>
            <Button size="sm" variant="outline">
              <Activity className="h-3.5 w-3.5" />
              Live
            </Button>
            <Button size="sm">Submit Job</Button>
          </div>
        </header>

        <main className="min-h-[calc(100vh-3.5rem)] px-4 py-6 lg:px-6">
          {children}
        </main>
      </div>
    </div>
  );
}
