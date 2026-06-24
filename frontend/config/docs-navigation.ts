import manifest from "./docs-manifest.json";

export interface DocNavItem {
  slug: string;
  file: string;
  title: string;
  description?: string;
  section: string;
}

export interface DocNavSection {
  title: string;
  items: DocNavItem[];
}

export const docs = manifest as DocNavItem[];

export const docsNavigation = docs.reduce<DocNavSection[]>((sections, item) => {
  let section = sections.find((entry) => entry.title === item.section);
  if (!section) {
    section = { title: item.section, items: [] };
    sections.push(section);
  }
  section.items.push(item);
  return sections;
}, []);

export function getAdjacentDocs(slug: string) {
  const index = docs.findIndex((doc) => doc.slug === slug);
  return {
    previous: index > 0 ? docs[index - 1] : undefined,
    next: index >= 0 && index < docs.length - 1 ? docs[index + 1] : undefined,
  };
}
