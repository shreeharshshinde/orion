import {
  AlertCircle,
  AlertTriangle,
  Info,
  Lightbulb,
  ShieldAlert
} from "lucide-react";
import React, { isValidElement } from "react";
import type { ReactNode } from "react";
import ReactMarkdown from "react-markdown";
import rehypeAutolinkHeadings from "rehype-autolink-headings";
import rehypeHighlight from "rehype-highlight";
import rehypeSlug from "rehype-slug";
import remarkGfm from "remark-gfm";
import { cn } from "@/lib/utils";
import { CodeBlock } from "@/components/code-block";

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
