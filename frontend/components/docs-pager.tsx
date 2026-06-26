import { ArrowLeft, ArrowRight } from "lucide-react";
import Link from "next/link";
import { getAdjacentDocs } from "@/config/docs-navigation";

export function DocsPager({ currentSlug }: { currentSlug: string }) {
  const { previous, next } = getAdjacentDocs(currentSlug);
  return (
    <nav aria-label="Documentation pagination" className="mt-14 grid gap-3 border-t border-border/70 pt-6 sm:grid-cols-2">
      {previous ? (
        <Link className="group rounded-xl border border-border/70 bg-card/40 p-4 hover:border-primary/40 hover:bg-primary/5" href={`/docs/${previous.slug}`}>
          <span className="flex items-center gap-1 text-[10px] uppercase tracking-widest text-muted-foreground"><ArrowLeft className="h-3 w-3" /> Previous</span>
          <span className="mt-1 block text-sm font-semibold group-hover:text-primary">{previous.title}</span>
        </Link>
      ) : <span />}
      {next && (
        <Link className="group rounded-xl border border-border/70 bg-card/40 p-4 text-right hover:border-primary/40 hover:bg-primary/5" href={`/docs/${next.slug}`}>
          <span className="flex items-center justify-end gap-1 text-[10px] uppercase tracking-widest text-muted-foreground">Next <ArrowRight className="h-3 w-3" /></span>
          <span className="mt-1 block text-sm font-semibold group-hover:text-primary">{next.title}</span>
        </Link>
      )}
    </nav>
  );
}
