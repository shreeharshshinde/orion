"use client";

import {
  AlertCircle,
  AlertTriangle,
  Check,
  Copy,
  Info,
  Lightbulb,
  ShieldAlert
} from "lucide-react";
import React, { isValidElement, useState } from "react";
import type { ComponentPropsWithoutRef, ReactNode } from "react";
import ReactMarkdown from "react-markdown";
import rehypeAutolinkHeadings from "rehype-autolink-headings";
import rehypeHighlight from "rehype-highlight";
import rehypeSlug from "rehype-slug";
import remarkGfm from "remark-gfm";
import { cn } from "@/lib/utils";
import { Mermaid } from "./mermaid-renderer";

function parseAlert(children: ReactNode): {
  type: "note" | "tip" | "important" | "warning" | "caution" | null;
  cleanedChildren: ReactNode;
} {
  const childrenArray = React.Children.toArray(children);
  if (childrenArray.length === 0) return { type: null, cleanedChildren: children };

  const firstChild = childrenArray[0];
  if (!isValidElement<{ children?: ReactNode }>(firstChild)) return { type: null, cleanedChildren: children };

  const firstChildProps = firstChild.props;
  if (!firstChildProps.children) return { type: null, cleanedChildren: children };

  const pChildrenArray = React.Children.toArray(firstChildProps.children);
  if (pChildrenArray.length === 0) return { type: null, cleanedChildren: children };

  const firstTextNode = pChildrenArray[0];
  if (typeof firstTextNode !== "string") return { type: null, cleanedChildren: children };

  const match = firstTextNode.match(/^\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\](?:\r?\n)?(.*)/i);
  if (!match) return { type: null, cleanedChildren: children };

  const alertType = match[1].toLowerCase() as "note" | "tip" | "important" | "warning" | "caution";
  const remainingText = match[2];

  const updatedPChildren = [...pChildrenArray];
  if (remainingText.trim() === "") {
    updatedPChildren.shift();
  } else {
    updatedPChildren[0] = remainingText;
  }

  const updatedFirstChild = React.cloneElement(firstChild, {}, ...updatedPChildren);
  const cleanedChildren = [updatedFirstChild, ...childrenArray.slice(1)];

  return { type: alertType, cleanedChildren };
}

function childText(node: ReactNode): string {
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(childText).join("");
  if (isValidElement<{ children?: ReactNode }>(node)) return childText(node.props.children);
  return "";
}

function CodeBlock({ children, ...props }: ComponentPropsWithoutRef<"pre">) {
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

export function MarkdownRenderer({ content }: { content: string }) {
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      rehypePlugins={[rehypeSlug, [rehypeAutolinkHeadings, { behavior: "wrap" }], rehypeHighlight]}
      components={{
        pre: CodeBlock,
        a: ({ href, children, ...props }) => {
          const external = href?.startsWith("http");
          return <a href={href} {...props} rel={external ? "noreferrer" : undefined} target={external ? "_blank" : undefined}>{children}</a>;
        },
        input: ({ type, ...props }) => <input type={type} {...props} disabled={type === "checkbox" || props.disabled} />,
        blockquote: ({ children, ...props }) => {
          const { type, cleanedChildren } = parseAlert(children);
          if (!type) {
            return <blockquote {...props}>{children}</blockquote>;
          }

          const alertStyles = {
            note: {
              border: "border-l-4 border-primary",
              bg: "bg-primary/5",
              text: "text-primary",
              icon: <Info className="h-4 w-4 text-primary shrink-0" />,
              title: "Note",
            },
            tip: {
              border: "border-l-4 border-success",
              bg: "bg-success/5",
              text: "text-success",
              icon: <Lightbulb className="h-4 w-4 text-success shrink-0" />,
              title: "Tip",
            },
            important: {
              border: "border-l-4 border-violet/60",
              bg: "bg-violet/5",
              text: "text-violet-300",
              icon: <AlertCircle className="h-4 w-4 text-violet-300 shrink-0" />,
              title: "Important",
            },
            warning: {
              border: "border-l-4 border-warning",
              bg: "bg-warning/5",
              text: "text-warning",
              icon: <AlertTriangle className="h-4 w-4 text-warning shrink-0" />,
              title: "Warning",
            },
            caution: {
              border: "border-l-4 border-danger",
              bg: "bg-danger/5",
              text: "text-danger",
              icon: <ShieldAlert className="h-4 w-4 text-danger shrink-0" />,
              title: "Caution",
            },
          }[type];

          return (
            <div className={cn("my-6 rounded-r-lg border border-y-border/40 border-r-border/40 p-4 shadow-sm backdrop-blur-sm", alertStyles.border, alertStyles.bg)}>
              <div className={cn("mb-2 flex items-center gap-2 text-xs font-semibold uppercase tracking-wider", alertStyles.text)}>
                {alertStyles.icon}
                <span>{alertStyles.title}</span>
              </div>
              <div className="text-muted-foreground prose-sm [&>p]:my-1 leading-relaxed">
                {cleanedChildren}
              </div>
            </div>
          );
        },
      }}
    >
      {content}
    </ReactMarkdown>
  );
}
