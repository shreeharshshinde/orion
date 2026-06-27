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
        <DocsPager currentSlug={slugString} />
      </article>
      <aside className="sticky top-20 hidden max-h-[calc(100vh-6rem)] w-56 shrink-0 overflow-y-auto 2xl:block">
        <TableOfContents headings={doc.headings} />
      </aside>
    </div>
  );
}
