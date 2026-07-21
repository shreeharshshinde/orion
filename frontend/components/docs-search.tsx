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
      className="group flex h-10 w-full items-center gap-2.5 rounded-lg border border-border/40 bg-muted/5 hover:bg-muted/15 px-3.5 text-left transition-all hover:border-primary/45 focus:outline-none focus:ring-2 focus:ring-primary/20 backdrop-blur-md relative"
      aria-label="Open global search"
    >
      <Search className="h-4 w-4 text-muted-foreground/75 group-hover:text-primary transition-colors" />
      <span className="flex-1 text-xs text-muted-foreground/50 group-hover:text-muted-foreground/80 transition-colors">
        Search documentation...
      </span>
      <kbd className="hidden sm:inline-flex h-5 items-center rounded border border-border/60 bg-muted/40 px-1.5 font-mono text-[9px] font-medium text-muted-foreground/60 shadow-sm">
        /
      </kbd>
    </button>
  );
}
