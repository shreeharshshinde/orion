"use client";

import { Search } from "lucide-react";

interface DocsSearchProps {
  onNavigate?: () => void;
}

export function DocsSearch({ onNavigate }: DocsSearchProps) {
  const trigger = () => {
    window.dispatchEvent(new CustomEvent("open-global-search"));
    onNavigate?.();
  };

  return (
    <button
      onClick={trigger}
      className="flex h-10 w-full items-center gap-2 rounded-lg border border-border/70 bg-background/60 px-3 text-left transition-all hover:border-primary/60 hover:ring-2 hover:ring-primary/10"
      aria-label="Open global search"
    >
      <Search className="h-4 w-4 text-muted-foreground" />
      <span className="flex-1 text-xs text-muted-foreground/60">Search documentation…</span>
      <kbd className="rounded border border-border bg-muted/60 px-1.5 py-0.5 text-[10px] text-muted-foreground">/</kbd>
    </button>
  );
}
