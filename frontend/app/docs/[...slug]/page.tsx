import type { Metadata } from "next";
import { notFound } from "next/navigation";

import { DocsPager } from "@/components/docs-pager";
import { TableOfContents } from "@/components/docs-toc";
import { MarkdownRenderer } from "@/components/markdown-renderer";
import { getAllDocSlugs, getDocBySlug } from "@/lib/docs";

interface DocsPageProps {
  params: Promise<{ slug: string[] }>;
}

export function generateStaticParams() {
  return getAllDocSlugs().map((slug) => ({ slug }));
}

export async function generateMetadata({ params }: DocsPageProps): Promise<Metadata> {
  const { slug } = await params;
  const doc = getDocBySlug(slug.join("/"));
  return doc ? { title: `${doc.title} · Orion Docs`, description: doc.description } : {};
}

export default async function DocsPage({ params }: DocsPageProps) {
  const { slug } = await params;
  const slugString = slug.join("/");
  const doc = getDocBySlug(slugString);
  if (!doc) notFound();

  return (
    <div className="flex items-start gap-10">
      <article className="min-w-0 flex-1">
        <header className="mb-9 border-b border-border/70 pb-8">
          <div className="mb-3 flex items-center gap-2 text-[10px] font-semibold uppercase tracking-[0.18em] text-primary">
            <span className="h-1.5 w-1.5 rounded-full bg-primary shadow-[0_0_10px_hsl(var(--primary))]" />
            {doc.section}
          </div>
          <h1 className="max-w-3xl text-3xl font-bold tracking-tight sm:text-4xl">{doc.title}</h1>
          {doc.description && <p className="mt-4 max-w-3xl text-base leading-7 text-muted-foreground">{doc.description}</p>}
        </header>
        <div className="docs-prose"><MarkdownRenderer content={doc.content} /></div>

        {/* Trust Signals: Edit on GitHub & Last Updated */}
        <div className="mt-12 flex flex-wrap items-center justify-between gap-4 border-t border-border/60 pt-6 text-xs text-muted-foreground">
          <a
            href={`https://github.com/shreeharshshinde/orion/blob/main/docs/${doc.file}`}
            target="_blank"
            rel="noopener noreferrer"
            className="flex items-center gap-1.5 hover:text-primary transition-colors font-medium"
          >
            <svg className="h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <path d="M12 20h9" />
              <path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z" />
            </svg>
            Edit this page on GitHub
          </a>
          {doc.lastModified && (
            <div className="flex items-center gap-1">
              <span>Last updated:</span>
              <span className="font-semibold text-foreground">{doc.lastModified}</span>
            </div>
          )}
        </div>

        <div className="mt-8">
          <DocsPager currentSlug={slugString} />
        </div>
      </article>
      <aside className="sticky top-20 hidden max-h-[calc(100vh-6rem)] w-56 shrink-0 overflow-y-auto 2xl:block">
        <TableOfContents headings={doc.headings} />
      </aside>
    </div>
  );
}
