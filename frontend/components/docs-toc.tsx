"use client";

import { useEffect, useState } from "react";
import type { DocHeading } from "@/lib/docs";
import { cn } from "@/lib/utils";

export function TableOfContents({ headings }: { headings: DocHeading[] }) {
  const [activeId, setActiveId] = useState(headings[0]?.id ?? "");

  useEffect(() => {
    const update = () => {
      const current = headings
        .map(({ id }) => document.getElementById(id))
        .filter((element): element is HTMLElement => Boolean(element))
        .filter((element) => element.getBoundingClientRect().top <= 140)
        .at(-1);
      setActiveId(current?.id ?? headings[0]?.id ?? "");
    };
    update();
    window.addEventListener("scroll", update, { passive: true });
    return () => window.removeEventListener("scroll", update);
  }, [headings]);

  if (!headings.length) return null;
  return (
    <nav aria-label="On this page">
      <p className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground/70">On this page</p>
      <ul className="mt-3 border-l border-border/70">
        {headings.map((heading) => (
          <li key={heading.id}>
            <a
              className={cn(
                "relative block py-1.5 text-xs leading-4 hover:text-foreground",
                heading.level === 3 ? "pl-6" : "pl-4",
                activeId === heading.id ? "font-medium text-primary" : "text-muted-foreground"
              )}
              href={`#${heading.id}`}
            >
              {activeId === heading.id && <span className="absolute -left-px inset-y-1.5 w-0.5 rounded-full bg-primary" />}
              {heading.text}
            </a>
          </li>
        ))}
      </ul>
    </nav>
  );
}
