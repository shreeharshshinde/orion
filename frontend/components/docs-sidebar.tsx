"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import headingsData from "@/config/docs-headings.json";
import { docsNavigation } from "@/config/docs-navigation";
import { cn } from "@/lib/utils";
import { DocsSearch } from "./docs-search";

export function DocsSidebar({ onNavigate }: { onNavigate?: () => void }) {
  const pathname = usePathname();

  return (
    <div className="flex h-full flex-col space-y-6">
      <div className="px-2 space-y-3">
        {/* Version Selector */}
        <div className="flex items-center justify-between rounded-lg border border-border/60 bg-muted/20 px-3 py-1.5 text-xs text-muted-foreground">
          <span className="font-semibold text-foreground">Version</span>
          <select className="bg-transparent font-mono font-semibold text-primary outline-none cursor-pointer hover:text-primary-hover focus:ring-0">
            <option value="v1.0.0" className="bg-[#0b1329] text-foreground">v1.0.0 (Latest)</option>
            <option value="v0.9.0" className="bg-[#0b1329] text-foreground">v0.9.0</option>
            <option value="v0.8.0" className="bg-[#0b1329] text-foreground">v0.8.0</option>
          </select>
        </div>
        <DocsSearch onNavigate={onNavigate} />
      </div>

      <nav aria-label="Documentation" className="flex-1 space-y-6 overflow-y-auto px-2 pb-8">
        {docsNavigation.map((section) => (
          <div key={section.title} className="space-y-2">
            <h3 className="px-3 text-[10px] font-bold uppercase tracking-wider text-muted-foreground/60">
              {section.title}
            </h3>
            <ul className="space-y-1">
              {section.items.map((item) => {
                const href = `/docs/${item.slug}`;
                const active = pathname === href || (pathname === "/docs" && item.slug === "overview");
                const headings = headingsData[item.slug as keyof typeof headingsData] || [];
                const hasHeadings = headings.length > 0;

                return (
                  <li key={item.slug} className="group/item">
                    <div className="flex items-center">
                      <Link
                        aria-current={active ? "page" : undefined}
                        className={cn(
                          "flex-1 py-1.5 px-3 text-sm transition-all border-l-2 -ml-[2px]",
                          active
                            ? "font-semibold text-primary border-primary bg-primary/5"
                            : "text-muted-foreground/80 hover:text-foreground hover:border-border/60 hover:bg-muted/10 border-transparent"
                        )}
                        href={href}
                        onClick={onNavigate}
                      >
                        <span className="truncate">{item.title}</span>
                      </Link>
                    </div>

                    {/* Subheadings: auto-expand only when active */}
                    {active && hasHeadings && (
                      <ul className="mt-1.5 mb-2 space-y-1 border-l border-border/40 ml-[12px] pl-3">
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
