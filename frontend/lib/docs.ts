import "server-only";

import fs from "node:fs";
import path from "node:path";
import matter from "gray-matter";

import { docs, type DocNavItem } from "@/config/docs-navigation";

const DOCS_DIRECTORY = path.resolve(process.cwd(), "../docs");

export interface DocHeading {
  id: string;
  text: string;
  level: number;
}

export interface DocContent extends DocNavItem {
  content: string;
  headings: DocHeading[];
}

function plainText(value: string) {
  return value
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/[*_`~]/g, "")
    .replace(/<[^>]+>/g, "")
    .trim();
}

function slugify(value: string, seen: Map<string, number>) {
  const base = plainText(value)
    .toLowerCase()
    .replace(/&[a-z]+;/g, "")
    .replace(/[^\p{L}\p{N}\s-]/gu, "")
    .trim()
    .replace(/\s+/g, "-");
  const count = seen.get(base) ?? 0;
  seen.set(base, count + 1);
  return count ? `${base}-${count}` : base;
}

export function extractHeadings(content: string): DocHeading[] {
  const headings: DocHeading[] = [];
  const seen = new Map<string, number>();
  let inFence = false;

  for (const line of content.split("\n")) {
    if (/^\s*```/.test(line)) {
      inFence = !inFence;
      continue;
    }
    if (inFence) continue;
    const match = /^(##|###)\s+(.+?)\s*#*\s*$/.exec(line);
    if (!match) continue;
    const text = plainText(match[2]);
    headings.push({ id: slugify(text, seen), text, level: match[1].length });
  }
  return headings;
}

function rewriteDocLinks(content: string, sourceFile: string) {
  const fileToSlug = new Map(docs.map((doc) => [doc.file.toLowerCase(), doc.slug]));
  const sourceDirectory = path.posix.dirname(sourceFile);

  return content.replace(/(!?)\[([^\]]*)\]\(([^)\s]+)(?:\s+["'][^"']*["'])?\)/g, (full, image, label, target) => {
    if (/^(?:https?:|mailto:|#|\/)/i.test(target)) return full;
    const [rawPath, hash] = target.split("#", 2);
    const resolved = path.posix.normalize(path.posix.join(sourceDirectory, decodeURIComponent(rawPath)));
    if (image) return `![${label}](/docs-assets/${resolved})`;
    if (!/\.mdx?$/i.test(resolved)) return full;
    const slug = fileToSlug.get(resolved.toLowerCase());
    return slug ? `[${label}](/docs/${slug}${hash ? `#${hash}` : ""})` : full;
  });
}

export function getDocBySlug(slug: string): DocContent | null {
  const entry = docs.find((doc) => doc.slug === slug);
  if (!entry) return null;
  const fullPath = path.resolve(DOCS_DIRECTORY, entry.file);
  if (!fullPath.startsWith(`${DOCS_DIRECTORY}${path.sep}`) || !fs.existsSync(fullPath)) return null;

  const parsed = matter(fs.readFileSync(fullPath, "utf8"));
  const title = typeof parsed.data.title === "string" ? parsed.data.title : entry.title;
  const description = typeof parsed.data.description === "string" ? parsed.data.description : entry.description;
  const content = rewriteDocLinks(parsed.content.replace(/^#\s+.+\n+/, ""), entry.file);
  return { ...entry, title, description, content, headings: extractHeadings(content) };
}

export function getAllDocSlugs() {
  return docs.map((doc) => doc.slug.split("/"));
}
