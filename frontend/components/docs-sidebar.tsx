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
        {/* Version Selector with pulsing status indicator */}
        <div className="flex items-center justify-between rounded-lg border border-border/50 bg-secondary/10 px-3 py-1.5 text-xs text-muted-foreground backdrop-blur-sm shadow-inner transition-all hover:border-primary/30">
          <div className="flex items-center gap-1.5">
            <span className="relative flex h-1.5 w-1.5">
              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75"></span>
              <span className="relative inline-flex rounded-full h-1.5 w-1.5 bg-emerald-500"></span>
            </span>
            <span className="font-semibold text-muted-foreground/90">Version</span>
          </div>
          <select className="bg-transparent font-mono font-bold text-primary outline-none cursor-pointer hover:text-primary-hover focus:ring-0 transition-colors">
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
            <h3 className="px-3 text-[10px] font-bold uppercase tracking-widest text-muted-foreground/40 mt-4 mb-2">
              {section.title}
            </h3>
            <ul className="space-y-1">
              {section.items.map((item) => {
                const href = `/docs/${item.slug}`;
                const isParentActive = pathname === href || (pathname === "/docs" && item.slug === "overview");
                const isChildActive = item.children?.some((child) => pathname === `/docs/${child.slug}`);
                const isCategoryActive = isParentActive || isChildActive;
                const headings = headingsData[item.slug as keyof typeof headingsData] || [];
                const hasHeadings = headings.length > 0;

                return (
                  <li key={item.slug} className="group/item">
                    <div className="flex items-center">
                      <Link
                        aria-current={isParentActive ? "page" : undefined}
                        className={cn(
                          "flex-1 py-1.5 px-3 text-sm transition-all border-l-2 -ml-[2px]",
                          isParentActive
                            ? "font-semibold text-primary border-primary bg-gradient-to-r from-primary/10 to-transparent"
                            : isChildActive
                              ? "font-medium text-foreground/95 border-primary/40 bg-muted/5"
                              : "text-muted-foreground/80 hover:text-foreground hover:border-border/60 hover:bg-muted/10 border-transparent"
                        )}
                        href={href}
                        onClick={onNavigate}
                      >
                        <span className="block leading-snug whitespace-normal break-words">{item.title}</span>
                      </Link>
                    </div>

                    {/* Sub-pages list: displayed only when category is active */}
                    {isCategoryActive && item.children && (
                      <ul className="mt-1 ml-4 border-l border-border/40 pl-3 space-y-1.5">
                        {item.children.map((child) => {
                          const childHref = `/docs/${child.slug}`;
                          const isCurrentChild = pathname === childHref;
                          return (
                            <li key={child.slug}>
                              <Link
                                href={childHref}
                                className={cn(
                                  "group flex items-center py-1.5 text-xs transition-all border-l-2 -ml-[15px] pl-3",
                                  isCurrentChild
                                    ? "font-semibold text-primary border-primary bg-gradient-to-r from-primary/10 to-transparent"
                                    : "text-muted-foreground/70 hover:text-foreground hover:border-border/60 hover:bg-muted/10 border-transparent"
                                )}
                                onClick={onNavigate}
                              >
                                <span className="block leading-snug whitespace-normal break-words">{child.title}</span>
                              </Link>
                            </li>
                          );
                        })}
                      </ul>
                    )}

                    {/* Subheadings: auto-expand only when active with a neat vertical tree line, and only when there are no sub-pages */}
                    {isParentActive && !item.children && hasHeadings && (
                      <ul className="mt-1 mb-2 ml-[12px] border-l border-border/40 pl-3 space-y-1">
                        {headings.map((heading) => (
                          <li key={heading.id}>
                            <Link
                              href={`/docs/${item.slug}#${heading.id}`}
                              className={cn(
                                "group relative flex items-center py-1 text-xs transition-all hover:text-foreground border-l border-transparent -ml-[13px] pl-3 hover:border-primary/50",
                                heading.level === 3 
                                  ? "pl-6 text-muted-foreground/50 hover:text-muted-foreground" 
                                  : "text-muted-foreground/70"
                              )}
                              onClick={onNavigate}
                            >
                              <span className="block leading-snug whitespace-normal break-words">{heading.text}</span>
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
