# Phase 12 — Documentation Site Integration

**Status:** Planned
**Prerequisite:** Phase 11 (Frontend Completion)
**Location:** `frontend/` (Next.js 15 app router, MDX integration, Tailwind CSS)

---

## Current State

The Orion project has extensive documentation written in Markdown, located under the repository's root `docs/` directory:
- System Architecture (`docs/architecture/overview.md`, `docs/project_architecture_details.md`)
- Operational Runbooks (`docs/RUNBOOK.md`)
- Deployment Guides (`docs/DEPLOYMENT_PLAN.md` and phase-specific notes)
- Architecture Decision Records (10 ADRs under `docs/adr/`)
- Phase Guides and Implementation Records (Phases 1-11 under `docs/phases/`)

Currently, the frontend has a placeholder docs landing page (`frontend/app/docs/page.tsx`) displaying a grid of cards with hardcoded descriptions. All links are inactive or wireframe markers (`href="#"`). There is no live parsing, sidebar navigation, dynamic table of contents, search, or formatting suitable for an interactive developer-focused docs experience.

---

## Technical Stack to Add

To implement a premium, interactive documentation experience within our Next.js dashboard, we will install and configure:

```bash
npm install next-mdx-remote gray-matter    # MDX processing & frontmatter parsing
npm install remark-gfm                     # Github Flavored Markdown support (tables, task lists)
npm install rehype-slug                    # Auto-inject ID attributes to headings for scrollspy/anchors
npm install rehype-autolink-headings       # Inject anchor links next to headings
npm install rehype-highlight               # Syntax highlighting for code blocks
npm install @tailwindcss/typography        # Sleek, customizable styles for markdown elements
```

---

## Architecture of the Docs Portal

The documentation system will run directly within Next.js using dynamic catch-all routing. Docs are stored as static `.md` or `.mdx` files in the repository. At request time (in production, optimized via Next.js static generation/ISR), the page reads these markdown files, parses their frontmatter/content, and renders them through a custom MDX layout.

```
┌────────────────────────────────────────────────────────────────────────┐
│                          frontend/app/docs                             │
│                                                                        │
│  ┌──────────────────────┐  ┌─────────────────┐  ┌───────────────────┐  │
│  │   LEFT SIDEBAR       │  │  MAIN CONTENT   │  │   RIGHT SIDEBAR   │  │
│  │                      │  │                 │  │                   │  │
│  │  🔍 Search docs      │  │  # OpenClaw     │  │  On this page     │  │
│  │                      │  │  Logo / Image   │  │  - What is Orion? │  │
│  │  Getting Started     │  │                 │  │  - How it works   │  │
│  │  - Overview          │  │  Rendered text  │  │  - Quick start    │  │
│  │  - Showcase          │  │  and styled MDX │  │  - Configuration  │  │
│  │                      │  │                 │  │                   │  │
│  │  Guides              │  │  ┌───────────┐  │  │                   │  │
│  │  - Runbook           │  │  │ Code /    │  │  │                   │  │
│  │  - Deployment        │  │  │ Mermaid   │  │  │                   │  │
│  │                      │  │  └───────────┘  │  │                   │  │
│  └──────────────────────┘  └─────────────────┘  └───────────────────┘  │
└────────────────────────────────────────────────────────────────────────┘
```

---

## Implementation Plan

### Step 1 — MDX Setup and File System Reader
**Files to create:** `frontend/lib/docs.ts`

Create a helper library using Node.js filesystem APIs (`fs`, `path`) to crawl, read, and parse documents from the root `/docs` directory. This needs to handle mapping URLs (e.g., `/docs/adr/001-queue-design`) to actual file paths (e.g., `docs/adr/ADR-001-queue-design.md`).

```typescript
import fs from 'fs';
import path from 'path';
import matter from 'gray-matter';

const DOCS_DIRECTORY = path.join(process.cwd(), '../docs');

export interface DocMetadata {
  title: string;
  description?: string;
  category?: string;
  order?: number;
}

export interface DocContent {
  slug: string[];
  metadata: DocMetadata;
  content: string;
}

// Map user-friendly slugs to actual workspace files
export const slugToFileMap: Record<string, string> = {
  'overview': 'architecture/overview.md',
  'architecture-details': 'project_architecture_details.md',
  'runbook': 'RUNBOOK.md',
  'deployment': 'DEPLOYMENT_PLAN.md',
  // ADRs
  'adr/queue-design': 'adr/ADR-001-queue-design.md',
  'adr/leader-election': 'adr/ADR-002-leader-election.md',
  'adr/cas-state-transitions': 'adr/ADR-003-cas-state-transitions.md',
  'adr/buffered-job-backpressure': 'adr/ADR-004-buffered-jobch-backpressure.md',
  'adr/kubernetes-testability': 'adr/ADR-005-kubernetes-interface-testability.md',
  'adr/kubernetes-restart-policy': 'adr/ADR-006-k8s-backofflimit-restartpolicy.md',
  'adr/jsonb-payloads': 'adr/ADR-007-jsonb-payload-dagspec.md',
  'adr/qos-ml-pods': 'adr/ADR-008-guaranteed-qos-ml-pods.md',
  'adr/token-bucket-rate-limits': 'adr/ADR-009-token-bucket-rate-limiting.md',
  'adr/weighted-fair-scheduling': 'adr/ADR-010-weighted-fair-scheduling.md',
  // Phases
  'phases/1-foundation': 'phases/phase-01/phase-01-guide.md',
  'phases/2-postgres': 'phases/phase-02/phase_02_guide.md',
  'phases/3-inline-executor': 'phases/phase-03/phase_03_guide.md',
  'phases/4-k8s-executor': 'phases/phase-04/phase_04_guide.md',
  'phases/5-pipeline-dag': 'phases/phase-05/PHASE_05_GUIDE.md',
  'phases/6-observability': 'phases/phase-06/PHASE_06_MASTER.md',
  'phases/7-grpc-streaming': 'phases/phase-07/PHASE_07_GUIDE.md',
  'phases/8-rate-limiting': 'phases/phase-08/PHASE_08_GUIDE.md',
  'phases/9-helm-deployment': 'phases/phase-09/PHASE_09_GUIDE.md',
  'phases/10-observability-hardening': 'phases/phase-10/OBSERVABILITY_HARDENING.md',
  'phases/11-frontend-completion': 'phases/phase-11/FRONTEND_COMPLETION.md',
};

export function getDocBySlug(slugString: string): DocContent | null {
  const relativePath = slugToFileMap[slugString];
  if (!relativePath) return null;

  const fullPath = path.join(DOCS_DIRECTORY, relativePath);
  if (!fs.existsSync(fullPath)) return null;

  const fileContents = fs.readFileSync(fullPath, 'utf8');
  const { data, content } = matter(fileContents);

  return {
    slug: slugString.split('/'),
    metadata: {
      title: data.title || getDefaultTitle(slugString),
      description: data.description || '',
      category: data.category || getCategoryFromSlug(slugString),
      order: data.order || 99,
    },
    content,
  };
}

function getDefaultTitle(slug: string): string {
  const parts = slug.split('/');
  const name = parts[parts.length - 1];
  return name.replace(/-/g, ' ').replace(/\b\w/g, c => c.toUpperCase());
}

function getCategoryFromSlug(slug: string): string {
  if (slug.startsWith('adr/')) return 'Architecture Decision Records';
  if (slug.startsWith('phases/')) return 'Implementation Phases';
  return 'Documentation';
}
```

---

### Step 2 — Navigation Configuration and Structure
**Files to create:** `frontend/config/docs-navigation.ts`

Define a structured navigation array to build the left sidebar hierarchy. This structure maps logical categories to display names and paths.

```typescript
export interface NavItem {
  title: string;
  href: string;
  disabled?: boolean;
}

export interface NavSection {
  title: string;
  items: NavItem[];
}

export const docsNavigation: NavSection[] = [
  {
    title: "Get Started",
    items: [
      { title: "Overview", href: "/docs/overview" },
      { title: "Architecture Details", href: "/docs/architecture-details" },
      { title: "Runbook", href: "/docs/runbook" },
      { title: "Deployment Plan", href: "/docs/deployment" },
    ],
  },
  {
    title: "Architecture Decision Records (ADRs)",
    items: [
      { title: "ADR-001: Queue Design", href: "/docs/adr/queue-design" },
      { title: "ADR-002: Leader Election", href: "/docs/adr/leader-election" },
      { title: "ADR-003: CAS State Transitions", href: "/docs/adr/cas-state-transitions" },
      { title: "ADR-004: Job Concurrency Backpressure", href: "/docs/adr/buffered-job-backpressure" },
      { title: "ADR-005: Kubernetes Testability", href: "/docs/adr/kubernetes-testability" },
      { title: "ADR-006: Kubernetes Restart Policy", href: "/docs/adr/kubernetes-restart-policy" },
      { title: "ADR-007: JSONB Payload & DAG Specs", href: "/docs/adr/jsonb-payloads" },
      { title: "ADR-008: ML Pod QoS Guarantee", href: "/docs/adr/qos-ml-pods" },
      { title: "ADR-009: Token Bucket Rate Limiting", href: "/docs/adr/token-bucket-rate-limits" },
      { title: "ADR-010: Weighted Fair Scheduling", href: "/docs/adr/weighted-fair-scheduling" },
    ],
  },
  {
    title: "Implementation Phases",
    items: [
      { title: "Phase 1: Foundation Skeleton", href: "/docs/phases/1-foundation" },
      { title: "Phase 2: PostgreSQL Store", href: "/docs/phases/2-postgres" },
      { title: "Phase 3: Inline Executor", href: "/docs/phases/3-inline-executor" },
      { title: "Phase 4: Kubernetes Executor", href: "/docs/phases/4-k8s-executor" },
      { title: "Phase 5: Pipeline Orchestration", href: "/docs/phases/5-pipeline-dag" },
      { title: "Phase 6: Observability Integration", href: "/docs/phases/6-observability" },
      { title: "Phase 7: gRPC Streaming API", href: "/docs/phases/7-grpc-streaming" },
      { title: "Phase 8: Rate Limiting & Priorities", href: "/docs/phases/8-rate-limiting" },
      { title: "Phase 9: Helm & Production", href: "/docs/phases/9-helm-deployment" },
      { title: "Phase 10: Observability Hardening", href: "/docs/phases/10-observability-hardening" },
      { title: "Phase 11: Frontend Completion", href: "/docs/phases/11-frontend-completion" },
      { title: "Phase 12: Documentation Site", href: "/docs/phases/12-docs-integration" },
    ],
  },
];
```

---

### Step 3 — Dynamic Catch-All Docs Layout and Page
**Files to create/modify:** 
- `frontend/app/docs/layout.tsx`
- `frontend/app/docs/[[...slug]]/page.tsx`
- `frontend/components/docs-sidebar.tsx`

Create a three-column layout:
1. **Left Sidebar:** Search input + Navigation tree (`docsNavigation`) with collapsible headers and active-route glow highlight.
2. **Center Panel:** Scrollable reading area container with `@tailwindcss/typography` formatting (`prose prose-invert`).
3. **Right Sidebar:** Responsive Table of Contents (TOC) that tracks reader position on headings (Scrollspy).

#### 1. Docs Layout (`frontend/app/docs/layout.tsx`)
```tsx
import { ReactNode } from "react";
import { DocsSidebar } from "@/components/docs-sidebar";

export default function DocsLayout({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-screen bg-[#061826] text-[#E6F8FF]">
      {/* Sidebar Navigation */}
      <aside className="fixed top-14 bottom-0 left-0 z-20 hidden w-64 border-r border-[#16445E] bg-[#0B2536]/80 backdrop-blur-xl lg:block overflow-y-auto px-4 py-6">
        <DocsSidebar />
      </aside>

      {/* Main Content & Table of Contents */}
      <div className="lg:pl-64 flex-1">
        <div className="mx-auto w-full max-w-7xl px-4 py-8 md:px-8">
          {children}
        </div>
      </div>
    </div>
  );
}
```

#### 2. Docs Page (`frontend/app/docs/[[...slug]]/page.tsx`)
```tsx
import { use } from "react";
import { notFound } from "next/navigation";
import { getDocBySlug } from "@/lib/docs";
import { MDXRemote } from "next-mdx-remote/rsc";
import { TableOfContents } from "@/components/docs-toc";
import { DocsPager } from "@/components/docs-pager";
import { mdxComponents } from "@/components/mdx-components";

// Next.js MDX formatting tools
import remarkGfm from "remark-gfm";
import rehypeSlug from "rehype-slug";
import rehypeHighlight from "rehype-highlight";

interface PageProps {
  params: Promise<{ slug?: string[] }>;
}

export default function DocsPage({ params }: PageProps) {
  const { slug } = use(params);
  const slugString = slug ? slug.join("/") : "overview";

  const doc = getDocBySlug(slugString);
  if (!doc) {
    notFound();
  }

  const mdxOptions = {
    mdxOptions: {
      remarkPlugins: [remarkGfm],
      rehypePlugins: [rehypeSlug, rehypeHighlight],
    },
  };

  return (
    <div className="flex gap-12">
      {/* Center Reading Column */}
      <article className="flex-1 min-w-0">
        <header className="mb-8 border-b border-[#16445E] pb-6">
          <p className="text-xs font-semibold uppercase tracking-widest text-[#06B6D4]">
            {doc.metadata.category}
          </p>
          <h1 className="mt-2 font-display text-3xl font-bold tracking-tight text-white sm:text-4xl">
            {doc.metadata.title}
          </h1>
          {doc.metadata.description && (
            <p className="mt-4 text-base text-[#8DB2C4]">
              {doc.metadata.description}
            </p>
          )}
        </header>

        {/* Prose formatting */}
        <div className="prose prose-invert prose-blue max-w-none prose-pre:bg-[#0B2536] prose-pre:border prose-pre:border-[#16445E] prose-headings:font-display prose-a:text-[#06B6D4] hover:prose-a:text-[#0284C7] prose-th:text-white prose-td:text-[#8DB2C4] prose-code:text-[#06B6D4] prose-code:bg-[#0F3147]/50 prose-code:px-1.5 prose-code:py-0.5 prose-code:rounded prose-code:before:content-none prose-code:after:content-none">
          <MDXRemote source={doc.content} components={mdxComponents} options={mdxOptions} />
        </div>

        {/* Previous / Next buttons */}
        <DocsPager currentSlug={slugString} />
      </article>

      {/* Right Column: Table of Contents */}
      <aside className="hidden w-60 shrink-0 xl:block">
        <div className="sticky top-20">
          <TableOfContents content={doc.content} />
        </div>
      </aside>
    </div>
  );
}
```

---

### Step 4 — Dynamic Scrollspy Table of Contents
**Files to create:** `frontend/components/docs-toc.tsx`

Extract headings dynamically from the Markdown content using regular expressions. Render them as vertical anchor links. Use the browser `IntersectionObserver` API to track scroll position and add active state styling (`text-[#06B6D4] border-l-2 border-[#06B6D4]`) to the heading currently in view.

```tsx
"use client";

import { useEffect, useState } from "react";
import { cn } from "@/lib/utils";

interface Heading {
  id: string;
  text: string;
  level: number;
}

export function TableOfContents({ content }: { content: string }) {
  const [headings, setHeadings] = useState<Heading[]>([]);
  const [activeId, setActiveId] = useState<string>("");

  useEffect(() => {
    // Regex matches md headings (e.g. ## Key Decisions)
    const headingRegex = /^(##|###)\s+(.+)$/gm;
    const matches: Heading[] = [];
    let match;

    while ((match = headingRegex.exec(content)) !== null) {
      const level = match[1].length; // 2 for '##', 3 for '###'
      const text = match[2].trim();
      // Generate ID matching rehype-slug standard
      const id = text
        .toLowerCase()
        .replace(/[^a-z0-9\s-]/g, "")
        .replace(/\s+/g, "-");

      matches.push({ id, text, level });
    }

    setHeadings(matches);
  }, [content]);

  useEffect(() => {
    const observer = new IntersectionObserver(
      (entries) => {
        const visibleEntry = entries.find((entry) => entry.isIntersecting);
        if (visibleEntry) {
          setActiveId(visibleEntry.target.id);
        }
      },
      { rootMargin: "0px 0px -70% 0px", threshold: 0.1 }
    );

    // Track all parsed headings in the DOM
    headings.forEach((heading) => {
      const el = document.getElementById(heading.id);
      if (el) observer.observe(el);
    });

    return () => observer.disconnect();
  }, [headings]);

  if (headings.length === 0) return null;

  return (
    <nav className="space-y-2">
      <p className="text-xs font-semibold uppercase tracking-wider text-[#8DB2C4]">
        On this page
      </p>
      <ul className="mt-4 space-y-2.5 border-l border-[#16445E] pl-0">
        {headings.map((heading) => (
          <li
            key={heading.id}
            className={cn(
              "pl-4 text-xs transition-all duration-150",
              heading.level === 3 ? "pl-7" : "",
              activeId === heading.id
                ? "border-l-2 border-[#06B6D4] -ml-[1px] font-medium text-white"
                : "text-[#8DB2C4] hover:text-[#E6F8FF]"
            )}
          >
            <a href={`#${heading.id}`}>{heading.text}</a>
          </li>
        ))}
      </ul>
    </nav>
  );
}
```

---

### Step 5 — Interactive MDX Components (Code & Mermaid Support)
**Files to create:** `frontend/components/mdx-components.tsx`

We need to override default markdown element renderers to include custom premium features:
1. **Code Blocks:** Wrap in custom panels featuring syntax coloring, file headers, and a "Copy Code" button.
2. **Mermaid Diagrams:** Render dynamic system architectures and DAG state machines directly.

```tsx
"use client";

import { useState } from "react";
import { Check, Copy } from "lucide-react";
import { Mermaid } from "@/components/mermaid-renderer";

export const mdxComponents = {
  // Override code block rendering
  pre: ({ children, ...props }: any) => {
    const [copied, setCopied] = useState(false);
    
    // Safely extract raw text code from react children
    const code = children?.props?.children || "";
    const language = children?.props?.className?.replace("language-", "") || "text";

    // Handle Mermaid blocks dynamically
    if (language === "mermaid") {
      return <Mermaid chart={code} />;
    }

    const copyToClipboard = () => {
      navigator.clipboard.writeText(code);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    };

    return (
      <div className="relative group my-6 overflow-hidden rounded-lg border border-[#16445E] bg-[#0B2536]">
        {/* Header toolbar */}
        <div className="flex items-center justify-between border-b border-[#16445E] px-4 py-2 bg-[#0F3147]/40 text-xs font-mono text-[#8DB2C4]">
          <span>{language}</span>
          <button
            onClick={copyToClipboard}
            className="flex items-center gap-1.5 rounded border border-[#16445E] bg-[#0B2536] px-2 py-1 transition-all hover:bg-[#16445E] hover:text-white"
          >
            {copied ? (
              <>
                <Check className="h-3.5 w-3.5 text-emerald-400" />
                <span>Copied</span>
              </>
            ) : (
              <>
                <Copy className="h-3.5 w-3.5" />
                <span>Copy</span>
              </>
            )}
          </button>
        </div>
        {/* Scrollable code area */}
        <pre {...props} className="m-0 p-4 overflow-x-auto text-sm leading-relaxed">
          {children}
        </pre>
      </div>
    );
  },
};
```

#### Client-side Mermaid Renderer (`frontend/components/mermaid-renderer.tsx`)
```tsx
"use client";

import { useEffect, useRef, useState } from "react";

export function Mermaid({ chart }: { chart: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const [svg, setSvg] = useState<string>("");
  const [error, setError] = useState<boolean>(false);

  useEffect(() => {
    // Dynamically load Mermaid library on client side only
    import("mermaid").then((m) => {
      const mermaid = m.default;
      mermaid.initialize({
        startOnLoad: false,
        theme: "dark",
        themeVariables: {
          background: "#0B2536",
          primaryColor: "#0284C7",
          lineColor: "#16445E",
          textColor: "#E6F8FF",
        },
      });

      const id = `mermaid-${Math.floor(Math.random() * 10000)}`;
      try {
        mermaid.render(id, chart).then((result) => {
          setSvg(result.svg);
        });
      } catch (err) {
        console.error("Mermaid parsing error:", err);
        setError(true);
      }
    });
  }, [chart]);

  if (error) {
    return (
      <div className="my-6 rounded-lg border border-red-900 bg-red-950/20 p-4 text-xs font-mono text-red-400">
        [Failed to render architecture diagram]
      </div>
    );
  }

  if (!svg) {
    return (
      <div className="my-6 flex h-48 items-center justify-center rounded-lg border border-[#16445E] bg-[#0B2536] animate-pulse text-[#8DB2C4]">
        Loading diagrams...
      </div>
    );
  }

  return (
    <div
      ref={ref}
      className="my-6 flex justify-center rounded-lg border border-[#16445E] bg-[#0B2536] p-6 overflow-x-auto"
      dangerouslySetInnerHTML={{ __html: svg }}
    />
  );
}
```

---

### Step 6 — Search Engine & Fuzzy Searching
**Files to create:** 
- `frontend/components/docs-search.tsx`
- `scripts/build-docs-index.mjs`

To make the docs searchable without heavy dependencies or cloud APIs, we will write a Node.js script that compiles all headings, descriptions, titles, and text contents into a JSON search index at build time. The search component then loads this index and runs a lightweight client-side fuzzy match.

#### 1. Index Generator Script (`scripts/build-docs-index.mjs`)
Add this script to run during the Next.js pre-build phase:

```javascript
import fs from 'fs';
import path from 'path';
import matter from 'gray-matter';

const DOCS_DIR = path.resolve('..', 'docs');
const OUTPUT_FILE = path.resolve('public', 'docs-search-index.json');

const slugToFileMap = {
  'overview': 'architecture/overview.md',
  'architecture-details': 'project_architecture_details.md',
  'runbook': 'RUNBOOK.md',
  'deployment': 'DEPLOYMENT_PLAN.md',
  // ... maps matching slugToFileMap in frontend
};

const searchIndex = [];

for (const [slug, file] of Object.entries(slugToFileMap)) {
  const filePath = path.join(DOCS_DIR, file);
  if (!fs.existsSync(filePath)) continue;

  const content = fs.readFileSync(filePath, 'utf8');
  const { data, content: body } = matter(content);

  // Clean raw body from markdown links, images, code blocks to optimize search weights
  const cleanBody = body
    .replace(/```[\s\S]*?```/g, '') // remove code blocks
    .replace(/[#*`_\[\]]/g, '');   // remove formatting symbols

  searchIndex.push({
    slug,
    title: data.title || slug,
    description: data.description || '',
    body: cleanBody.substring(0, 1500), // Index first 1500 characters to keep file small
  });
}

fs.writeFileSync(OUTPUT_FILE, JSON.stringify(searchIndex, null, 2));
console.log('Docs search index built successfully.');
```

#### 2. Docs Search Component (`frontend/components/docs-search.tsx`)
Create an input in the sidebar that allows filtering search results dynamically, listing them inside a floating popover or command list.

```tsx
"use client";

import { useEffect, useState } from "react";
import { Search } from "lucide-react";
import Link from "next/link";

interface IndexItem {
  slug: string;
  title: string;
  description: string;
  body: string;
}

export function DocsSearch() {
  const [query, setQuery] = useState("");
  const [index, setIndex] = useState<IndexItem[]>([]);
  const [results, setResults] = useState<IndexItem[]>([]);

  useEffect(() => {
    // Load precompiled search index on demand
    fetch("/docs-search-index.json")
      .then((res) => res.json())
      .then((data) => setIndex(data))
      .catch((err) => console.error("Failed to load search index:", err));
  }, []);

  const handleSearch = (val: string) => {
    setQuery(val);
    if (!val.trim()) {
      setResults([]);
      return;
    }

    const filtered = index.filter(
      (item) =>
        item.title.toLowerCase().includes(val.toLowerCase()) ||
        item.description.toLowerCase().includes(val.toLowerCase()) ||
        item.body.toLowerCase().includes(val.toLowerCase())
    );

    setResults(filtered.slice(0, 5)); // Limit to top 5 results
  };

  return (
    <div className="relative mb-6">
      <div className="flex h-9 w-full items-center gap-2 rounded-lg border border-[#16445E] bg-[#0F3147]/30 px-3 text-xs text-[#8DB2C4] transition hover:border-[#06B6D4] focus-within:border-[#06B6D4]">
        <Search className="h-3.5 w-3.5 shrink-0" />
        <input
          type="text"
          placeholder="Search docs..."
          value={query}
          onChange={(e) => handleSearch(e.target.value)}
          className="flex-1 bg-transparent border-0 outline-none text-white placeholder-[#8DB2C4]/60"
        />
      </div>

      {/* Floating Results Box */}
      {results.length > 0 && (
        <div className="absolute top-10 left-0 right-0 z-30 rounded-lg border border-[#16445E] bg-[#0B2536] p-2 shadow-2xl">
          <p className="px-2 py-1 text-[10px] font-semibold uppercase tracking-wider text-[#8DB2C4]">
            Search Results
          </p>
          <ul className="mt-2 space-y-1">
            {results.map((item) => (
              <li key={item.slug}>
                <Link
                  href={`/docs/${item.slug}`}
                  onClick={() => setResults([])}
                  className="block rounded px-2 py-1.5 hover:bg-[#0F3147]/50"
                >
                  <p className="text-xs font-semibold text-white">{item.title}</p>
                  {item.description && (
                    <p className="text-[10px] text-[#8DB2C4] line-clamp-1">
                      {item.description}
                    </p>
                  )}
                </Link>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
```

---

## Design and Visual Verification

1. **Category Headers:** Clean and color-coded corresponding to the current section (e.g., `#06B6D4` for ADRs).
2. **Dynamic Navigation Trees:** Navigation state updates automatically.
3. **Copyable Syntax Highlighter:** Block header reveals file/command types, and copying returns instant user toast cues.
4. **Scrollspy Alignment:** Highlighted state synchronizes smoothly as the reader scrolls the prose page.

---

## Completion Criteria

```bash
# Verify Tailwind CSS Typography plugin works
npm run lint

# Pre-build index script succeeds
node scripts/build-docs-index.mjs

# Build production Next.js application
npm run build

# Start server locally
npm run start
```
1. Accessing `/docs` redirects correctly to `/docs/overview` or loads Overview natively.
2. Mermaid diagram blocks parse and render on-screen as high-quality SVGs.
3. Click navigation within dynamic sidebars updates layout contents.
4. Copy commands from code logs capture correct block strings.
