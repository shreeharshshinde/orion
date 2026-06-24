"use client";

import { useEffect, useId, useState } from "react";
import { Maximize2, ZoomIn, ZoomOut, X } from "lucide-react";


export function Mermaid({ chart }: { chart: string }) {
  const reactId = useId();
  const [svg, setSvg] = useState("");
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setSvg("");
    setFailed(false);
    import("mermaid")
      .then(async ({ default: mermaid }) => {
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: "strict",
          theme: "dark",
          themeVariables: {
            background: "#09111f",
            primaryColor: "#123044",
            primaryBorderColor: "#22d3ee",
            primaryTextColor: "#e6fbff",
            lineColor: "#7799aa",
            secondaryColor: "#20194a",
            tertiaryColor: "#0d2232",
          },
        });
        const id = `orion-mermaid-${reactId.replace(/[^a-z0-9]/gi, "")}`;
        const result = await mermaid.render(id, chart.trim());
        if (!cancelled) setSvg(result.svg);
      })
      .catch(() => { if (!cancelled) setFailed(true); });
    return () => { cancelled = true; };
  }, [chart, reactId]);

  const [maximized, setMaximized] = useState(false);
  const [zoom, setZoom] = useState(1);

  useEffect(() => {
    if (maximized) {
      document.body.style.overflow = "hidden";
    } else {
      document.body.style.overflow = "";
      setZoom(1);
    }
    return () => {
      document.body.style.overflow = "";
    };
  }, [maximized]);

  if (failed) return <div className="docs-diagram-error">This diagram could not be rendered. Its source is available in the document.</div>;
  if (!svg) return <div className="docs-diagram-loading">Rendering diagram…</div>;

  return (
    <>
      <div className="group relative my-6">
        <div className="docs-diagram" dangerouslySetInnerHTML={{ __html: svg }} />
        <button
          onClick={() => setMaximized(true)}
          className="absolute right-4 top-4 rounded-lg border border-border bg-card/85 p-2 text-muted-foreground opacity-0 shadow-sm transition-all duration-200 hover:bg-muted hover:text-foreground group-hover:opacity-100"
          aria-label="Maximize diagram"
          title="Zoom and inspect diagram"
        >
          <Maximize2 className="h-4 w-4" />
        </button>
      </div>

      {maximized && (
        <div className="fixed inset-0 z-50 flex flex-col bg-background/95 p-6 backdrop-blur-md animate-in fade-in duration-200">
          <div className="flex justify-between items-center mb-4 shrink-0">
            <div>
              <span className="text-xs font-semibold uppercase tracking-widest text-primary">Architecture Diagram Viewer</span>
              <p className="text-[10px] text-muted-foreground mt-0.5">Use the controls below or trackpad to inspect</p>
            </div>
            <button
              onClick={() => setMaximized(false)}
              className="rounded-lg border border-border bg-card p-2 text-muted-foreground hover:text-foreground hover:bg-muted"
            >
              <X className="h-4 w-4" />
            </button>
          </div>
          <div className="flex-1 flex items-center justify-center overflow-auto p-4 bg-muted/20 border border-border/60 rounded-xl relative select-none">
            <div className="overflow-auto max-w-full max-h-full p-4 flex justify-center items-center">
              <div
                className="max-w-none max-h-none flex justify-center items-center [&>svg]:max-w-none [&>svg]:h-auto"
                dangerouslySetInnerHTML={{ __html: svg }}
                style={{ transform: `scale(${zoom})`, transformOrigin: "center", transition: "transform 0.1s ease-out" }}
              />
            </div>
            <div className="absolute bottom-6 right-6 flex items-center gap-2 rounded-lg border border-border bg-card/95 p-1.5 shadow-lg backdrop-blur-sm shrink-0">
              <button
                onClick={() => setZoom((z) => Math.max(0.5, z - 0.25))}
                className="p-1.5 rounded hover:bg-muted text-muted-foreground hover:text-foreground transition"
                title="Zoom out"
              >
                <ZoomOut className="h-4 w-4" />
              </button>
              <span className="text-xs font-mono px-2 text-foreground font-semibold min-w-[3.5rem] text-center">{Math.round(zoom * 100)}%</span>
              <button
                onClick={() => setZoom((z) => Math.min(3, z + 0.25))}
                className="p-1.5 rounded hover:bg-muted text-muted-foreground hover:text-foreground transition"
                title="Zoom in"
              >
                <ZoomIn className="h-4 w-4" />
              </button>
              <button
                onClick={() => setZoom(1)}
                className="p-1.5 rounded hover:bg-muted text-[10px] uppercase font-bold tracking-wider px-2.5 transition text-primary hover:text-primary-hover"
              >
                Reset
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}
