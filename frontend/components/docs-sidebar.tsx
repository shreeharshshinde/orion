"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";

import headingsData from "@/config/docs-headings.json";
import { docsNavigation } from "@/config/docs-navigation";
import { cn } from "@/lib/utils";
import { DocsSearch } from "./docs-search";

function cleanHeadingTitle(text: string): string {
  return text.replace(/^\d+[\.\)]\s*/, "").trim();
}

export function DocsSidebar({ onNavigate }: { onNavigate?: () => void }) {
  const pathname = usePathname();
  const [activeHeadingId, setActiveHeadingId] = useState<string>("");

  // Determine current slug
  const currentSlug = pathname === "/docs" 
    ? "overview" 
    : pathname.replace(/^\/docs\//, "");

  // Scrollspy to highlight active top-level (H2) section in the sidebar
  useEffect(() => {
    const currentHeadings = (headingsData[currentSlug as keyof typeof headingsData] || [])
      .filter((h) => h.level === 2);

    if (!currentHeadings.length) return;

    const updateActiveHeading = () => {
      const current = currentHeadings
        .map(({ id }) => document.getElementById(id))
        .filter((element): element is HTMLElement => Boolean(element))
        .filter((element) => element.getBoundingClientRect().top <= 140)
        .at(-1);

      if (current) {
        setActiveHeadingId(current.id);
      } else if (currentHeadings[0]) {
        setActiveHeadingId(currentHeadings[0].id);
      }
    };

    updateActiveHeading();
    window.addEventListener("scroll", updateActiveHeading, { passive: true });
    return () => window.removeEventListener("scroll", updateActiveHeading);
  }, [currentSlug]);

  const handleHeadingClick = (e: React.MouseEvent<HTMLAnchorElement>, id: string) => {
    const elem = document.getElementById(id);
    if (elem) {
      e.preventDefault();
      const offset = 80;
      const top = elem.getBoundingClientRect().top + window.scrollY - offset;
      window.scrollTo({ top, behavior: "smooth" });
      window.history.pushState(null, "", `#${id}`);
      setActiveHeadingId(id);
      onNavigate?.();
    }
  };

  return (
    <div className="flex h-full flex-col space-y-5">
      <div className="px-1">
        <DocsSearch onNavigate={onNavigate} />
      </div>

      <nav aria-label="Documentation" className="flex-1 space-y-5 overflow-y-auto px-1 pb-8">
        {docsNavigation.map((section) => (
          <div key={section.title} className="space-y-1">
            <h3 className="px-2.5 text-[11px] font-mono font-medium uppercase tracking-wider text-muted-foreground/70 mt-4 mb-1.5">
              {section.title}
            </h3>

            <ul className="space-y-0.5">
              {section.items.map((item) => {
                const href = `/docs/${item.slug}`;
                const isParentActive = pathname === href || (pathname === "/docs" && item.slug === "overview");
                const isChildActive = item.children?.some((child) => pathname === `/docs/${child.slug}`);
                const isCategoryActive = isParentActive || isChildActive;
                
                // Only include major top-level topics (H2), excluding nested subpoints of subpoints (H3)
                const headings = (headingsData[item.slug as keyof typeof headingsData] || [])
                  .filter((h) => h.level === 2);
                const hasHeadings = headings.length > 0;

                return (
                  <li key={item.slug} className="group/item">
                    <div className="flex items-center">
                      <Link
                        aria-current={isParentActive ? "page" : undefined}
                        className={cn(
                          "flex-1 flex items-center justify-between rounded-md py-1.5 px-2.5 text-sm transition-all",
                          isParentActive
                            ? "font-semibold text-primary bg-primary/10"
                            : isChildActive
                              ? "font-medium text-foreground/95 bg-muted/15"
                              : "text-muted-foreground/80 hover:text-foreground hover:bg-muted/15"
                        )}
                        href={href}
                        onClick={onNavigate}
                      >
                        <span className="leading-snug whitespace-normal break-words">{item.title}</span>
                      </Link>
                    </div>

                    {/* Sub-pages list: displayed only when category is active */}
                    {isCategoryActive && item.children && (
                      <ul className="mt-1 ml-3 border-l border-border/60 pl-2.5 space-y-0.5">
                        {item.children.map((child) => {
                          const childHref = `/docs/${child.slug}`;
                          const isCurrentChild = pathname === childHref;
                          return (
                            <li key={child.slug}>
                              <Link
                                href={childHref}
                                className={cn(
                                  "group flex items-center justify-between rounded-md py-1 px-2 text-xs transition-all",
                                  isCurrentChild
                                    ? "font-semibold text-primary bg-primary/10"
                                    : "text-muted-foreground/75 hover:text-foreground hover:bg-muted/15"
                                )}
                                onClick={onNavigate}
                              >
                                <span className="leading-snug whitespace-normal break-words">{cleanHeadingTitle(child.title)}</span>
                              </Link>
                            </li>
                          );
                        })}
                      </ul>
                    )}

                    {/* Major topics (H2 only): concise navigation without subpoint clutter */}
                    {isParentActive && !item.children && hasHeadings && (
                      <ul className="mt-1 mb-2 ml-3 border-l border-border/60 pl-2.5 space-y-0.5">
                        {headings.map((heading) => {
                          const isActive = activeHeadingId === heading.id;
                          return (
                            <li key={heading.id}>
                              <a
                                href={`/docs/${item.slug}#${heading.id}`}
                                onClick={(e) => handleHeadingClick(e, heading.id)}
                                className={cn(
                                  "group relative block py-1 text-xs transition-colors -ml-[11px] pl-2.5 border-l-2",
                                  isActive
                                    ? "font-medium text-primary border-primary bg-primary/5 rounded-r"
                                    : "text-muted-foreground/75 hover:text-foreground hover:border-border/80 border-transparent"
                                )}
                              >
                                <span className="block leading-snug whitespace-normal break-words">
                                  {cleanHeadingTitle(heading.text)}
                                </span>
                              </a>
                            </li>
                          );
                        })}
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
