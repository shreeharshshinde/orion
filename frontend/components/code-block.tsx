"use client";

import { Check, Copy } from "lucide-react";
import { useState, type ComponentPropsWithoutRef, ReactNode, isValidElement } from "react";
import { Mermaid } from "./mermaid-renderer";

function childText(node: ReactNode): string {
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(childText).join("");
  if (isValidElement<{ children?: ReactNode }>(node)) return childText(node.props.children);
  return "";
}

export function CodeBlock({ children, ...props }: ComponentPropsWithoutRef<"pre">) {
  const [copied, setCopied] = useState(false);
  const child = isValidElement<{ className?: string; children?: ReactNode }>(children) ? children : null;
  const language = child?.props.className?.match(/language-([^\s]+)/)?.[1] ?? "text";
  const code = childText(children).replace(/\n$/, "");

  if (language === "mermaid") return <Mermaid chart={code} />;

  async function copy() {
    await navigator.clipboard.writeText(code);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1600);
  }

  const isShell = ["bash", "sh", "shell", "zsh"].includes(language);

  return (
    <div className="docs-code group">
      <div className="docs-code-toolbar flex items-center justify-between">
        <div className="flex items-center gap-1.5">
          <span className="h-2.5 w-2.5 rounded-full bg-[#ff5f56]/70" />
          <span className="h-2.5 w-2.5 rounded-full bg-[#ffbd2e]/70" />
          <span className="h-2.5 w-2.5 rounded-full bg-[#27c93f]/70" />
          <span className="ml-3 font-mono text-[10px] text-muted-foreground uppercase tracking-wider">
            {isShell ? "terminal" : language}
          </span>
        </div>
        <button 
          aria-label="Copy code" 
          onClick={copy}
          className="flex items-center gap-1.5 rounded-md px-2 py-0.5 hover:bg-white/5 transition-colors text-muted-foreground hover:text-foreground text-[10px] uppercase font-semibold"
        >
          {copied ? <Check className="h-3 w-3 text-success" /> : <Copy className="h-3 w-3" />}
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
      <pre {...props}>{children}</pre>
    </div>
  );
}
