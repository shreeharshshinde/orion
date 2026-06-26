"use client";

import { ChevronRight, FileText } from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";

import headingsData from "@/config/docs-headings.json";
import { docsNavigation } from "@/config/docs-navigation";
import { cn } from "@/lib/utils";
import { DocsSearch } from "./docs-search";

export function DocsSidebar({ onNavigate }: { onNavigate?: () => void }) {
  const pathname = usePathname();
  const [expandedSlugs, setExpandedSlugs] = useState<Record<string, boolean>>({});

  // Ensure active document's subheadings are expanded by default when path changes
  useEffect(() => {
    const activeSlug = pathname.replace(/^\/docs\/?/, "") || "overview";
    setExpandedSlugs((prev) => ({ ...prev, [activeSlug]: true }));
  }, [pathname]);

  const toggleExpand = (slug: string, e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setExpandedSlugs((prev) => ({
      ...prev,
      [slug]: !prev[slug],
    }));
  };

  return (
    <div className="flex h-full flex-col space-y-6">
      <div className="px-2">
        <DocsSearch onNavigate={onNavigate} />
      </div>

      <nav aria-label="Documentation" className="flex-1 space-y-6 overflow-y-auto px-2 pb-8">
        {docsNavigation.map((section) => (
          <div key={section.title} className="space-y-2">
            <h3 className="px-3 text-xs font-semibold uppercase tracking-wider text-muted-foreground/80">
              {section.title}
            </h3>
            <ul className="space-y-2">
              {section.items.map((item) => {
                const href = `/docs/${item.slug}`;
                const active = pathname === href || (pathname === "/docs" && item.slug === "overview");
                const headings = headingsData[item.slug as keyof typeof headingsData] || [];
                const hasHeadings = headings.length > 0;
                const isExpanded = !!expandedSlugs[item.slug];

                return (
                  <li key={item.slug} className="group/item">
                    <div className="flex items-center gap-1">
                      {/* Triangle caret button to toggle subparts without page transition */}
                      {hasHeadings ? (
                        <button
                          onClick={(e) => toggleExpand(item.slug, e)}
                          className={cn(
                            "flex h-5 w-5 items-center justify-center rounded-md hover:bg-muted transition-all shrink-0 cursor-pointer",
                            isExpanded ? "text-foreground" : "text-muted-foreground/50 hover:text-foreground"
                          )}
                          aria-label={isExpanded ? "Collapse section" : "Expand section"}
                        >
                          <ChevronRight className={cn("h-3.5 w-3.5 transform transition-transform duration-200", isExpanded && "rotate-90")} />
                        </button>
                      ) : (
                        <div className="w-5 h-5 shrink-0" />
                      )}

                      <Link
                        aria-current={active ? "page" : undefined}
                        className={cn(
                          "flex-1 flex items-center gap-2 rounded-md py-1.5 px-2 text-sm transition-all",
                          active
                            ? "bg-primary/5 font-semibold text-primary"
                            : "text-muted-foreground/80 hover:text-foreground hover:bg-muted/30"
                        )}
                        href={href}
                        onClick={onNavigate}
                      >
                        <FileText className={cn("h-3.5 w-3.5 shrink-0", active ? "text-primary" : "text-muted-foreground/50")} />
                        <span className="truncate">{item.title}</span>
                      </Link>
                    </div>

                    {/* Subparts: display if expanded */}
                    {isExpanded && hasHeadings && (
                      <ul className="mt-1 space-y-1 border-l border-border/40 ml-[29px] pl-3">
                        {headings.map((heading) => (
                          <li key={heading.id}>
                            <Link
                              href={`/docs/${item.slug}#${heading.id}`}
                              className={cn(
                                "group flex items-center py-1 text-xs transition-colors hover:text-foreground",
                                heading.level === 3 
                                  ? "pl-3 text-muted-foreground/60 hover:text-muted-foreground" 
                                  : "text-muted-foreground/80"
                              )}
                              onClick={onNavigate}
                            >
                              <span className="truncate">{heading.text}</span>
                            </Link>
                          </li>
                        ))}
                      </ul>
                    )}
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
      </nav>
    </div>
  );
}
