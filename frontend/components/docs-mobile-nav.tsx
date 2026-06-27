"use client";

import { Menu, X } from "lucide-react";
import { useState } from "react";
import { DocsSidebar } from "./docs-sidebar";

export function DocsMobileNav() {
  const [open, setOpen] = useState(false);
  return (
    <div className="mb-5 xl:hidden">
      <button className="flex w-full items-center justify-between rounded-xl border border-border/70 bg-card/70 px-4 py-3 text-sm font-medium" onClick={() => setOpen(true)}>
        <span className="flex items-center gap-2"><Menu className="h-4 w-4 text-primary" /> Browse documentation</span>
        <span className="text-xs text-muted-foreground">Menu</span>
      </button>
      {open && (
        <div className="fixed inset-0 z-50 xl:hidden">
          <button aria-label="Close navigation" className="absolute inset-0 bg-background/80 backdrop-blur-sm" onClick={() => setOpen(false)} />
          <aside className="absolute inset-y-0 left-0 w-[min(88vw,22rem)] overflow-y-auto border-r border-border bg-card p-5 shadow-2xl">
            <button aria-label="Close navigation" className="absolute right-4 top-4 rounded-md p-2 text-muted-foreground hover:bg-muted hover:text-foreground" onClick={() => setOpen(false)}><X className="h-4 w-4" /></button>
            <DocsSidebar onNavigate={() => setOpen(false)} />
          </aside>
        </div>
      )}
    </div>
  );
}
