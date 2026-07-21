import manifest from "./docs-manifest.json";

export interface DocNavItem {
  slug: string;
  file: string;
  title: string;
  description?: string;
  section: string;
  parent?: string;
  children?: DocNavItem[];
}

export interface DocNavSection {
  title: string;
  items: DocNavItem[];
}

export const docs = manifest as DocNavItem[];

export const docsNavigation = docs.reduce<DocNavSection[]>((sections, item) => {
  // Skip sub-pages from becoming top-level items in sections
  if (item.parent) return sections;

  let section = sections.find((entry) => entry.title === item.section);
  if (!section) {
    section = { title: item.section, items: [] };
    sections.push(section);
  }

  // Find any sub-pages belonging to this parent item
  const children = docs.filter((d) => d.parent === item.slug);
  
  section.items.push({
    ...item,
    children: children.length > 0 ? children : undefined,
  });

  return sections;
}, []);

export function getAdjacentDocs(slug: string) {
  const index = docs.findIndex((doc) => doc.slug === slug);
  return {
    previous: index > 0 ? docs[index - 1] : undefined,
    next: index >= 0 && index < docs.length - 1 ? docs[index + 1] : undefined,
  };
}
