"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import {
  Activity,
  BookOpen,
  Boxes,
  BriefcaseBusiness,
  Gauge,
  GitBranch,
  Home,
  Search,
  Server,
  Waypoints
} from "lucide-react";
import type { ReactNode } from "react";

import { Button, StatusDot } from "@/components/ui";
import { cn } from "@/lib/utils";

const navItems = [
  { href: "/dashboard", label: "Overview", icon: Gauge },
  { href: "/dashboard/jobs", label: "Jobs", icon: BriefcaseBusiness },
  { href: "/dashboard/pipelines", label: "Pipelines", icon: GitBranch },
  { href: "/dashboard/queues", label: "Queues", icon: Waypoints },
  { href: "/dashboard/workers", label: "Workers", icon: Server },
  { href: "/docs", label: "Docs", icon: BookOpen },
  { href: "/", label: "Home", icon: Home }
];

export function AppShell({ children }: { children: ReactNode }) {
  const pathname = usePathname();

  return (
    <div className="min-h-screen">
      {/* Sidebar */}
      <aside className="fixed inset-y-0 left-0 z-30 hidden w-64 border-r bg-card/75 backdrop-blur-xl lg:flex lg:flex-col">
        {/* Logo */}
        <div className="flex h-16 shrink-0 items-center gap-3 border-b px-5">
          <div className="flex h-9 w-9 items-center justify-center rounded-md border border-primary/40 bg-primary/20 text-primary shadow-neon">
            <Boxes className="h-5 w-5" />
          </div>
          <div>
            <p className="font-display font-semibold tracking-normal">Orion</p>
            <p className="text-xs text-muted-foreground">ML orchestration</p>
          </div>
        </div>

        {/* Nav */}
        <nav className="flex-1 space-y-0.5 overflow-y-auto p-3">
          {navItems.map((item) => {
            const Icon = item.icon;
            const active =
              item.href === "/dashboard"
                ? pathname === item.href
                : pathname === item.href || pathname.startsWith(`${item.href}/`);

            return (
              <Link
                className={cn(
                  "group relative flex items-center gap-3 rounded-md px-3 py-2.5 text-sm font-medium transition-colors duration-150",
                  active
                    ? "border border-primary/30 bg-primary/10 text-primary shadow-neon"
                    : "text-muted-foreground hover:bg-muted/80 hover:text-foreground"
                )}
                href={item.href}
                key={item.href}
              >
                {/* Active accent line */}
                {active && (
                  <span className="absolute inset-y-1 left-0 w-0.5 rounded-full bg-primary shadow-neon" />
                )}
                <Icon className={cn("h-4 w-4 shrink-0", active && "text-primary")} />
                {item.label}
              </Link>
            );
          })}
        </nav>

        {/* Health indicator */}
        <div className="shrink-0 p-3">
          <div className="rounded-lg border bg-muted/50 p-3 shadow-neon">
            <div className="flex items-center gap-2 text-sm font-medium">
              <StatusDot />
              <span>API ready</span>
            </div>
            <p className="mt-1 text-xs text-muted-foreground">Local · refresh 5s</p>
          </div>
        </div>
      </aside>

      {/* Main */}
      <div className="lg:pl-64">
        {/* Header */}
        <header className="sticky top-0 z-20 flex h-16 items-center gap-3 border-b bg-background/80 px-4 backdrop-blur-xl lg:px-6">
          {/* Mobile logo */}
          <Link className="flex items-center gap-2 font-semibold lg:hidden" href="/">
            <Boxes className="h-5 w-5 text-primary" />
            <span className="font-display">Orion</span>
          </Link>

          {/* Search bar */}
          <div className="hidden h-9 flex-1 items-center gap-2 rounded-md border bg-card/70 px-3 text-sm text-muted-foreground transition-colors hover:border-primary/60 md:flex">
            <Search className="h-4 w-4 shrink-0" />
            <span>Search jobs, pipelines, queues…</span>
            <kbd className="ml-auto hidden rounded border bg-muted px-1.5 py-0.5 text-xs font-mono text-muted-foreground sm:inline">⌘K</kbd>
          </div>

          <div className="ml-auto flex items-center gap-2">
            <Button size="sm" variant="outline">
              <Activity className="h-4 w-4" />
              Live
            </Button>
            <Button size="sm">Submit Job</Button>
          </div>
        </header>

        <main className="px-4 py-6 lg:px-6">{children}</main>
      </div>
    </div>
  );
}
