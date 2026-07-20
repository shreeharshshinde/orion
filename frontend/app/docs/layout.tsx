"use client";

import { ArrowRight, Menu, Moon, Search, Sun, X } from "lucide-react";
import Image from "next/image";
import Link from "next/link";
import { useTheme } from "next-themes";
import { useState, useEffect, type ReactNode } from "react";

import { DocsSidebar } from "@/components/docs-sidebar";
import { SearchDialog } from "@/components/search-dialog";
import { Button } from "@/components/ui";

export default function DocsLayout({ children }: { children: ReactNode }) {
  const [searchOpen, setSearchOpen] = useState(false);
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);
  const { theme, setTheme } = useTheme();
  const [mounted, setMounted] = useState(false);

  useEffect(() => {
    setMounted(true);
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

  return (
    <div className="min-h-screen bg-background text-foreground">
      {/* Standalone Docs Header */}
      <header className="sticky top-0 z-40 w-full border-b border-border/50 bg-background/80 backdrop-blur-xl">
        <div className="mx-auto flex h-14 max-w-[96rem] items-center justify-between px-4 sm:px-6 lg:px-8">
          <div className="flex items-center gap-6">
            <Link href="/" className="flex items-center gap-2">
              <Image src="/orion_logo.png" alt="Orion Logo" width={28} height={28} className="rounded" />
              <span className="font-display font-semibold tracking-wide text-foreground">Orion</span>
              <span className="rounded bg-primary/10 px-1.5 py-0.5 text-[10px] font-medium text-primary">
                DOCS
              </span>
            </Link>
            <nav className="hidden md:flex items-center gap-4 border-l border-border/60 pl-6 text-sm font-medium text-muted-foreground">
              <Link href="/" className="transition-colors hover:text-foreground">
                Home
              </Link>
            </nav>
          </div>

          {/* Docs Search Trigger */}
          <button
            onClick={() => setSearchOpen(true)}
            className="hidden h-8 w-full max-w-sm items-center gap-2 rounded-lg border border-border/60 bg-muted/30 px-3 text-xs text-muted-foreground transition hover:border-primary/40 hover:bg-muted/50 md:flex cursor-pointer text-left"
          >
            <Search className="h-3.5 w-3.5 shrink-0" />
            <span>Search docs...</span>
            <kbd className="ml-auto rounded border border-border/80 bg-card px-1.5 py-0.5 font-mono text-[10px]">⌘K</kbd>
          </button>

          {/* Right Controls */}
          <div className="flex items-center gap-2">
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
            {/* Go to Console button removed temporarily for docs-only deployment */}

            {/* Mobile Navigation Trigger */}
            <button
              onClick={() => setMobileMenuOpen(true)}
              className="rounded-lg p-1.5 text-muted-foreground hover:bg-muted hover:text-foreground md:hidden"
              aria-label="Open documentation navigation"
            >
              <Menu className="h-5 w-5" />
            </button>
          </div>
        </div>
      </header>

      {/* Docs Layout Containers */}
      <div className="mx-auto max-w-[96rem] px-4 sm:px-6 lg:px-8">
        <div className="xl:grid xl:grid-cols-[16rem_minmax(0,1fr)] xl:gap-8">
          {/* Docs Left Navigation Sidebar */}
          <aside className="fixed bottom-0 top-14 hidden w-64 overflow-y-auto border-r border-border/50 py-8 pr-6 xl:sticky xl:block">
            <DocsSidebar />
          </aside>

          {/* Docs Core Content */}
          <main className="py-8 min-w-0">
            {children}
          </main>
        </div>
      </div>

      {/* Mobile Menu Drawer */}
      {mobileMenuOpen && (
        <div className="fixed inset-0 z-50 md:hidden">
          <div className="absolute inset-0 bg-background/80 backdrop-blur-sm" onClick={() => setMobileMenuOpen(false)} />
          <aside className="absolute inset-y-0 left-0 w-[min(85vw,20rem)] overflow-y-auto border-r border-border bg-card p-6 shadow-2xl">
            <div className="mb-6 flex items-center justify-between">
              <span className="font-display font-semibold tracking-wide">Documentation</span>
              <button
                onClick={() => setMobileMenuOpen(false)}
                className="rounded-md p-1.5 text-muted-foreground hover:bg-muted hover:text-foreground"
                aria-label="Close menu"
              >
                <X className="h-4 w-4" />
              </button>
            </div>
            <div className="mb-4">
              <Link
                href="/"
                className="flex items-center gap-2 rounded-lg px-3 py-2 text-sm font-medium text-muted-foreground hover:bg-muted hover:text-foreground transition-colors"
                onClick={() => setMobileMenuOpen(false)}
              >
                Home Page
              </Link>
            </div>
            <button
              onClick={() => {
                setMobileMenuOpen(false);
                setSearchOpen(true);
              }}
              className="mb-6 flex h-9 w-full items-center gap-2 rounded-lg border border-border/60 bg-muted/30 px-3 text-xs text-muted-foreground text-left"
            >
              <Search className="h-3.5 w-3.5" />
              <span>Search docs...</span>
            </button>
            <DocsSidebar onNavigate={() => setMobileMenuOpen(false)} />
          </aside>
        </div>
      )}

      <SearchDialog open={searchOpen} onClose={() => setSearchOpen(false)} />
    </div>
  );
}
