"use client";

import {
  Activity, BookOpen,
  BriefcaseBusiness,
  ChevronRight,
  GitBranch, Home, Search, Server, Waypoints,
  Zap, Sun, Moon
} from "lucide-react";
import Image from "next/image";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useTheme } from "next-themes";
import { type ReactNode, useState, useEffect } from "react";

import { SearchDialog } from "@/components/search-dialog";
import { SubmitJobDialog } from "@/components/submit-job-dialog";
import { Button, StatusDot, EnvironmentBadge } from "@/components/ui";
import { useHealth, useJobs, useWorkers } from "@/lib/hooks";
import { cn } from "@/lib/utils";

const navItems = [
  { href: "/dashboard", label: "Overview", icon: Activity },
  { href: "/dashboard/jobs", label: "Jobs", icon: BriefcaseBusiness },
  { href: "/dashboard/pipelines", label: "Pipelines", icon: GitBranch },
  { href: "/dashboard/queues", label: "Queues", icon: Waypoints },
  { href: "/dashboard/workers", label: "Workers", icon: Server },
  { href: "/docs", label: "Docs", icon: BookOpen },
  { href: "/", label: "Home", icon: Home },
];

export function AppShell({ children }: { children: ReactNode }) {
  const pathname = usePathname();
  const [dialogOpen, setDialogOpen] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);
  const { theme, setTheme } = useTheme();
  const [mounted, setMounted] = useState(false);

  useEffect(() => {
    setMounted(true);
  }, []);

  useEffect(() => {
    const handleOpen = () => setSearchOpen(true);
    window.addEventListener("open-global-search", handleOpen);
    return () => window.removeEventListener("open-global-search", handleOpen);
  }, []);

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (document.activeElement?.tagName === "INPUT" || document.activeElement?.tagName === "TEXTAREA") {
        return;
      }
      if (((e.metaKey || e.ctrlKey) && e.key === "k") || e.key === "/") {
        e.preventDefault();
        setSearchOpen(true);
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, []);

  const { data: jobs = [] } = useJobs();
  const { data: workers = [] } = useWorkers();
  const {
    isError: apiDown,
    isLoading: healthLoading,
    isSuccess: apiOk,
  } = useHealth();

  const activeJobs = jobs.filter(j => j.status === "running" || j.status === "queued").length;
  const runningJobs = jobs.filter(j => j.status === "running").length;
  const queuedJobs = jobs.filter(j => j.status === "queued").length;
  const failedJobs = jobs.filter(j => j.status === "failed" || j.status === "dead").length;
  const activeWorkers = workers.filter(w => w.status !== "offline").length;

  const healthTone: "success" | "warning" | "danger" =
    apiOk ? "success" : apiDown ? "danger" : "warning";
  const healthLabel = apiOk ? "API ready" : healthLoading ? "Checking API" : "API down";

  return (
    <div className="min-h-screen bg-grid">
      {/* Sidebar */}
      <aside className="fixed inset-y-0 left-0 z-30 hidden w-60 flex-col border-r border-border/60 bg-card/60 backdrop-blur-xl lg:flex">
        {/* Logo */}
        <div className="flex h-14 shrink-0 items-center gap-2 border-b border-border/60 px-4">
          <Image src="/orion_logo.png" alt="Orion Logo" width={28} height={28} className="rounded shrink-0" />
          <span className="font-display text-sm font-semibold text-foreground tracking-wide">Orion</span>
          <div className="ml-auto shrink-0 scale-90 origin-right">
            <EnvironmentBadge env="local" />
          </div>
        </div>

        {/* Nav */}
        <nav className="flex-1 space-y-0.5 overflow-y-auto px-2 py-3">
          <p className="mb-1 px-2 text-[10px] font-semibold uppercase tracking-widest text-muted-foreground/50">
            Platform
          </p>
          {navItems.map((item) => {
            const Icon = item.icon;
            const active = item.href === "/dashboard"
              ? pathname === item.href
              : pathname.startsWith(item.href);

            const count = item.href === "/dashboard/jobs" ? activeJobs
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
        <div className="shrink-0 border-t border-border/50 p-4">
          <div className="rounded-xl border border-border/60 bg-muted/30 p-3.5 shadow-sm">
            <div className="flex items-center justify-between text-xs">
              <div className="flex items-center gap-2">
                <StatusDot tone={healthTone} pulse={healthLoading || apiOk} />
                <span className="font-medium text-foreground/90">{healthLabel}</span>
              </div>
              <span className="text-[10px] text-muted-foreground/80 font-mono">5s refresh</span>
            </div>
            <div className="mt-3 space-y-2 border-t border-border/40 pt-3 text-[11px] text-muted-foreground">
              <div className="flex items-center justify-between">
                <span>Active Workers</span>
                <span className="font-mono font-semibold text-foreground">{activeWorkers}</span>
              </div>
              <div className="flex items-center justify-between">
                <span>Running Jobs</span>
                <span className="font-mono font-semibold text-foreground">{runningJobs}</span>
              </div>
              <div className="flex items-center justify-between">
                <span>Queued Jobs</span>
                <span className="font-mono font-semibold text-foreground">{queuedJobs}</span>
              </div>
              <div className="flex items-center justify-between">
                <span>Failed Jobs</span>
                <span className="font-mono font-semibold text-foreground">{failedJobs}</span>
              </div>
            </div>
          </div>
        </div>
      </aside>

      {/* Main area */}
      <div className="lg:pl-60">
        <header className="sticky top-0 z-20 flex h-14 items-center gap-3 border-b border-border/50 bg-background/70 px-4 backdrop-blur-xl lg:px-5">
          <Link href="/" className="flex items-center gap-2 font-semibold lg:hidden">
            <Image src="/orion_logo.png" alt="Orion Logo" width={28} height={28} className="rounded" />
            <span className="font-display text-sm text-foreground">Orion</span>
          </Link>

          <button
            onClick={() => setSearchOpen(true)}
            className="hidden h-8 flex-1 max-w-md items-center gap-2 rounded-lg border border-border/60 bg-muted/30 px-3 text-xs text-muted-foreground transition hover:border-primary/40 hover:bg-muted/50 md:flex cursor-pointer text-left"
          >
            <Search className="h-3.5 w-3.5 shrink-0" />
            <span>Search docs, pages, commands…</span>
            <kbd className="ml-auto rounded border border-border/80 bg-card px-1.5 py-0.5 font-mono text-[10px]">⌘K</kbd>
          </button>

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
            <Button size="sm" onClick={() => setDialogOpen(true)}>Submit Job</Button>
            <Button
              variant="ghost"
              size="icon"
              className="h-8 w-8 rounded-lg text-muted-foreground hover:text-foreground"
              onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
              aria-label="Toggle theme"
            >
              {mounted ? (
                theme === "dark" ? (
                  <Sun className="h-4 w-4" />
                ) : (
                  <Moon className="h-4 w-4" />
                )
              ) : (
                <div className="h-4 w-4" />
              )}
            </Button>
          </div>
        </header>

        <main className="min-h-[calc(100vh-3.5rem)] px-4 py-6 lg:px-6">
          {children}
        </main>
      </div>

      <SubmitJobDialog open={dialogOpen} onClose={() => setDialogOpen(false)} />
      <SearchDialog
        open={searchOpen}
        onClose={() => setSearchOpen(false)}
        onSubmitJob={() => {
          setSearchOpen(false);
          setDialogOpen(true);
        }}
      />
    </div>
  );
}
