import fs from "node:fs/promises";
import path from "node:path";
import { NextResponse } from "next/server";

const DOCS_DIRECTORY = path.resolve(process.cwd(), "../docs");
const CONTENT_TYPES: Record<string, string> = {
  ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".svg": "image/svg+xml", ".webp": "image/webp",
};

export async function GET(_: Request, { params }: { params: Promise<{ path: string[] }> }) {
  const segments = (await params).path;
  const target = path.resolve(DOCS_DIRECTORY, ...segments);
  if (!target.startsWith(`${DOCS_DIRECTORY}${path.sep}`)) return new NextResponse("Not found", { status: 404 });
  try {
    const body = await fs.readFile(target);
    const contentType = CONTENT_TYPES[path.extname(target).toLowerCase()] ?? "application/octet-stream";
    return new NextResponse(body, { headers: { "Content-Type": contentType, "Cache-Control": "public, max-age=86400" } });
  } catch {
    return new NextResponse("Not found", { status: 404 });
  }
}
