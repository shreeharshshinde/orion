"use client";

import { useEffect, useState, useMemo, useRef } from "react";
import {
  Search,
  BookOpen,
  Activity,
  BriefcaseBusiness,
  GitBranch,
  Waypoints,
  Server,
  Plus,
  CornerDownLeft,
  FileText
} from "lucide-react";
import { useRouter } from "next/navigation";
import { cn } from "@/lib/utils";

interface SearchItem {
  slug: string;
  title: string;
  description: string;
  body: string;
}

interface SearchDialogProps {
  open: boolean;
  onClose: () => void;
  onSubmitJob?: () => void;
}

const QUICK_LINKS = [
  { href: "/dashboard", label: "Dashboard Overview", category: "Navigation", icon: Activity },
  { href: "/dashboard/jobs", label: "Jobs Monitor", category: "Navigation", icon: BriefcaseBusiness },
  { href: "/dashboard/pipelines", label: "Pipelines DAGs", category: "Navigation", icon: GitBranch },
  { href: "/dashboard/queues", label: "Priority Queues", category: "Navigation", icon: Waypoints },
  { href: "/dashboard/workers", label: "Workers Status", category: "Navigation", icon: Server },
  { href: "/docs", label: "Documentation Portal", category: "Navigation", icon: BookOpen },
  { action: "submit-job", label: "Submit New ML Job", category: "Actions", icon: Plus },
];

export function SearchDialog({ open, onClose, onSubmitJob }: SearchDialogProps) {
  const router = useRouter();
  const [query, setQuery] = useState("");
  const [index, setIndex] = useState<SearchItem[]>([]);
  const [activeIdx, setActiveIdx] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const containerRef = useRef<HTMLDivElement>(null);

  // Fetch search index on open
  useEffect(() => {
    if (open) {
      fetch("/docs-search-index.json")
        .then((res) => (res.ok ? res.json() : []))
        .then(setIndex)
        .catch(() => setIndex([]));
      setQuery("");
      setActiveIdx(0);
      setTimeout(() => inputRef.current?.focus(), 50);
    }
  }, [open]);

  // Prevent scroll when dialog is open
  useEffect(() => {
    if (open) {
      document.body.style.overflow = "hidden";
    } else {
      document.body.style.overflow = "";
    }
    return () => {
      document.body.style.overflow = "";
    };
  }, [open]);

  const filteredLinks = useMemo(() => {
    if (!query) return QUICK_LINKS;
    const q = query.toLowerCase();
    return QUICK_LINKS.filter(
      (link) =>
        link.label.toLowerCase().includes(q) ||
        link.category.toLowerCase().includes(q)
    );
  }, [query]);

  const filteredDocs = useMemo(() => {
    const terms = query.toLowerCase().trim().split(/\s+/).filter(Boolean);
    if (!terms.length) return [];
    return index
      .map((item) => {
        const title = item.title.toLowerCase();
        const haystack = `${title} ${item.description} ${item.body}`.toLowerCase();
        const score = terms.reduce((total, term) => total + (title.includes(term) ? 5 : haystack.includes(term) ? 1 : -20), 0);
        return { item, score };
      })
      .filter(({ score }) => score >= terms.length)
      .sort((a, b) => b.score - a.score)
      .slice(0, 5)
      .map(({ item }) => item);
  }, [index, query]);

  const combinedItems = useMemo(() => {
    const docsMapped = filteredDocs.map((doc) => ({
      label: doc.title,
      description: doc.description || doc.body.slice(0, 90) + "...",
      category: "Documentation",
      slug: doc.slug,
      icon: FileText,
      href: `/docs/${doc.slug}`,
    }));

    return [...filteredLinks, ...docsMapped];
  }, [filteredLinks, filteredDocs]);

  // Keep active item in view inside scroll container
  useEffect(() => {
    const activeEl = containerRef.current?.querySelector(`[data-active="true"]`);
    if (activeEl) {
      activeEl.scrollIntoView({ block: "nearest" });
    }
  }, [activeIdx]);

  // Keyboard navigation inside modal
  useEffect(() => {
    if (!open) return;

    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      } else if (e.key === "ArrowDown") {
        e.preventDefault();
        setActiveIdx((prev) => (combinedItems.length ? (prev + 1) % combinedItems.length : 0));
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        setActiveIdx((prev) => (combinedItems.length ? (prev - 1 + combinedItems.length) % combinedItems.length : 0));
      } else if (e.key === "Enter") {
        e.preventDefault();
        const activeItem = combinedItems[activeIdx];
        if (activeItem) {
          triggerItem(activeItem);
        }
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [open, combinedItems, activeIdx]);

  const triggerItem = (item: typeof combinedItems[0]) => {
    if ("action" in item && item.action === "submit-job") {
      onSubmitJob?.();
    } else if (item.href) {
      router.push(item.href);
      onClose();
    }
  };

  const highlightText = (text: string, q: string) => {
    if (!q) return <span>{text}</span>;
    const parts = text.split(new RegExp(`(${q.replace(/[-\/\\^$*+?.()|[\]{}]/g, "\\$&")})`, "gi"));
    return (
      <span>
        {parts.map((part, i) =>
          part.toLowerCase() === q.toLowerCase() ? (
            <mark key={i} className="bg-primary/20 text-primary font-semibold rounded px-0.5">
              {part}
            </mark>
          ) : (
            part
          )
        )}
      </span>
    );
  };

  if (!open) return null;

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-background/80 p-4 pt-[12vh] backdrop-blur-sm transition-all duration-200">
      {/* Click outside to close */}
      <div className="absolute inset-0 -z-10" onClick={onClose} />

      {/* Command Palette Card */}
      <div className="flex max-h-[60vh] w-full max-w-xl flex-col rounded-xl border border-border/80 bg-card/95 shadow-neon backdrop-blur-md overflow-hidden">
        {/* Search header */}
        <div className="flex h-12 items-center gap-3 border-b border-border/60 px-4">
          <Search className="h-4 w-4 text-muted-foreground shrink-0" />
          <input
            ref={inputRef}
            type="text"
            className="w-full bg-transparent text-sm text-foreground placeholder:text-muted-foreground/60 outline-none border-none py-3"
            placeholder="Search docs, pages or commands..."
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setActiveIdx(0);
            }}
          />
          <kbd className="rounded border border-border bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">ESC</kbd>
        </div>

        {/* Scrollable results list */}
        <div ref={containerRef} className="flex-1 overflow-y-auto p-2">
          {combinedItems.length > 0 ? (
            <div className="space-y-4">
              {/* Group items by category */}
              {Array.from(new Set(combinedItems.map((item) => item.category))).map((category) => {
                const categoryItems = combinedItems.filter((item) => item.category === category);
                return (
                  <div key={category} className="space-y-1">
                    <p className="px-3 py-1.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground/60">
                      {category}
                    </p>
                    {categoryItems.map((item) => {
                      const globalIdx = combinedItems.indexOf(item);
                      const Icon = item.icon;
                      const active = activeIdx === globalIdx;

                      return (
                        <button
                          key={item.label + ("slug" in item ? item.slug : "")}
                          data-active={active}
                          onClick={() => triggerItem(item)}
                          className={cn(
                            "flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left text-xs transition-all relative border border-transparent",
                            active
                              ? "bg-primary/10 text-primary border-primary/20 font-medium"
                              : "text-muted-foreground hover:bg-muted/40 hover:text-foreground"
                          )}
                        >
                          <Icon className={cn("h-4 w-4 shrink-0", active ? "text-primary" : "text-muted-foreground/80")} />
                          <span className="min-w-0 flex-1">
                            <span className="block truncate text-foreground font-semibold">
                              {highlightText(item.label, query)}
                            </span>
                            {"description" in item && item.description && (
                              <span className="mt-0.5 block line-clamp-1 text-[11px] leading-4 text-muted-foreground/80">
                                {highlightText(item.description, query)}
                              </span>
                            )}
                          </span>
                          {active && (
                            <span className="flex items-center gap-0.5 rounded border border-primary/30 bg-primary/10 px-1 py-0.5 text-[9px] text-primary animate-pulse">
                              <span>Select</span>
                              <CornerDownLeft className="h-2.5 w-2.5" />
                            </span>
                          )}
                        </button>
                      );
                    })}
                  </div>
                );
              })}
            </div>
          ) : (
            <div className="flex flex-col items-center justify-center py-12 text-center">
              <Search className="h-8 w-8 text-muted-foreground/30 mb-2" />
              <p className="text-sm font-medium text-muted-foreground">No matches found</p>
              <p className="text-xs text-muted-foreground/60 mt-1">Try searching for other terms or shortcuts.</p>
            </div>
          )}
        </div>

        {/* Footer shortcuts */}
        <div className="flex h-10 shrink-0 items-center justify-between border-t border-border/60 px-4 bg-muted/20 text-[10px] text-muted-foreground">
          <div className="flex items-center gap-3">
            <span>↑↓ to navigate</span>
            <span>↵ to select</span>
          </div>
          <div>
            <span>ESC to close</span>
          </div>
        </div>
      </div>
    </div>
  );
}
