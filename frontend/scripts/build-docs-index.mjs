import fs from "node:fs";
import path from "node:path";
import matter from "gray-matter";

const root = process.cwd();
const docsDirectory = path.resolve(root, "../docs");
const manifest = JSON.parse(fs.readFileSync(path.join(root, "config/docs-manifest.json"), "utf8"));

const headingsMap = {};

const index = manifest.flatMap((entry) => {
  const file = path.resolve(docsDirectory, entry.file);
  if (!file.startsWith(`${docsDirectory}${path.sep}`) || !fs.existsSync(file)) return [];
  const content = fs.readFileSync(file, "utf8");
  const parsed = matter(content);

  // Parse H2 & H3 headings
  const headings = [];
  const lines = parsed.content.split("\n");
  for (const line of lines) {
    const match = line.match(/^(##|###)\s+(.+)$/);
    if (match) {
      const level = match[1].length;
      const text = match[2].trim();
      const cleanText = text.replace(/\[([^\]]+)\]\([^)]*\)/g, "$1").replace(/[*_`]/g, "");
      const id = cleanText.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/(^-|-$)/g, "");
      headings.push({ id, text: cleanText, level });
    }
  }
  headingsMap[entry.slug] = headings;

  const firstHeading = parsed.content.match(/^#\s+(.+)$/m)?.[1];
  const body = parsed.content
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/!\[[^\]]*\]\([^)]*\)/g, " ")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/[#>*_`|~-]/g, " ")
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, 5000);
  return [{
    slug: entry.slug,
    title: typeof parsed.data.title === "string" ? parsed.data.title : (firstHeading || entry.title),
    description: typeof parsed.data.description === "string" ? parsed.data.description : (entry.description || ""),
    body,
  }];
});

fs.mkdirSync(path.join(root, "public"), { recursive: true });
fs.writeFileSync(path.join(root, "public/docs-search-index.json"), JSON.stringify(index));
fs.writeFileSync(path.join(root, "config/docs-headings.json"), JSON.stringify(headingsMap, null, 2));
console.log(`Indexed ${index.length} Orion documents and extracted headings.`);
