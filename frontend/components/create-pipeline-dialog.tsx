"use client";

import { X, Plus, Trash2, Loader2 } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui";
import { useCreatePipeline } from "@/lib/hooks";
import { cn } from "@/lib/utils";

// ─── Types ───────────────────────────────────────────────────────────────────

type NodeDef = { id: string; type: "inline" | "k8s_job"; depends_on: string[] };

// ─── Helpers ─────────────────────────────────────────────────────────────────

function hasCycle(nodes: NodeDef[]): boolean {
  const adj: Record<string, string[]> = {};
  for (const n of nodes) adj[n.id] = n.depends_on;

  const visited = new Set<string>();
  const stack   = new Set<string>();

  function dfs(id: string): boolean {
    if (stack.has(id)) return true;
    if (visited.has(id)) return false;
    visited.add(id); stack.add(id);
    for (const dep of adj[id] ?? []) if (dfs(dep)) return true;
    stack.delete(id);
    return false;
  }
  return nodes.some(n => dfs(n.id));
}

function toApiBody(name: string, nodes: NodeDef[]) {
  return {
    name,
    dag_spec: {
      nodes: nodes.map(n => ({
        id: n.id,
        job_template: { name: n.id, type: n.type },
        depends_on: n.depends_on,
      })),
    },
  };
}

const inputCls = "w-full rounded-md border bg-background px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-primary";

// ─── Visual builder ───────────────────────────────────────────────────────────

function VisualBuilder({ nodes, setNodes }: {
  nodes: NodeDef[];
  setNodes: React.Dispatch<React.SetStateAction<NodeDef[]>>;
}) {
  const ids = nodes.map(n => n.id);

  function addNode() {
    setNodes(prev => [...prev, { id: `node-${prev.length + 1}`, type: "inline", depends_on: [] }]);
  }

  function update(i: number, patch: Partial<NodeDef>) {
    setNodes(prev => prev.map((n, idx) => idx === i ? { ...n, ...patch } : n));
  }

  function remove(i: number) {
    const removedId = nodes[i].id;
    setNodes(prev => prev
      .filter((_, idx) => idx !== i)
      .map(n => ({ ...n, depends_on: n.depends_on.filter(d => d !== removedId) }))
    );
  }

  return (
    <div className="space-y-3">
      {nodes.map((node, i) => (
        <div key={i} className="rounded-lg border bg-muted/20 p-3 space-y-2">
          <div className="flex gap-2">
            <input
              className={cn(inputCls, "flex-1")}
              placeholder="node-id"
              value={node.id}
              onChange={e => update(i, { id: e.target.value })}
            />
            <select
              className={cn(inputCls, "w-32")}
              value={node.type}
              onChange={e => update(i, { type: e.target.value as NodeDef["type"] })}
            >
              <option value="inline">inline</option>
              <option value="k8s_job">k8s_job</option>
            </select>
            <button onClick={() => remove(i)} className="p-2 text-muted-foreground hover:text-danger rounded hover:bg-danger/10 transition-colors">
              <Trash2 className="h-4 w-4" />
            </button>
          </div>
          <div>
            <p className="mb-1 text-xs text-muted-foreground">Depends on</p>
            <div className="flex flex-wrap gap-2">
              {ids.filter((_, j) => j !== i).map(depId => (
                <label key={depId} className="flex items-center gap-1.5 text-xs cursor-pointer">
                  <input
                    type="checkbox"
                    className="accent-primary"
                    checked={node.depends_on.includes(depId)}
                    onChange={e => update(i, {
                      depends_on: e.target.checked
                        ? [...node.depends_on, depId]
                        : node.depends_on.filter(d => d !== depId),
                    })}
                  />
                  {depId}
                </label>
              ))}
              {ids.filter((_, j) => j !== i).length === 0 && (
                <span className="text-xs text-muted-foreground italic">No other nodes yet</span>
              )}
            </div>
          </div>
        </div>
      ))}
      <button
        type="button"
        onClick={addNode}
        className="flex items-center gap-2 rounded-md border border-dashed border-border/60 px-3 py-2 text-sm text-muted-foreground hover:border-primary/60 hover:text-primary transition-colors w-full justify-center"
      >
        <Plus className="h-4 w-4" /> Add node
      </button>
    </div>
  );
}

// ─── Dialog ───────────────────────────────────────────────────────────────────

export function CreatePipelineDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const create = useCreatePipeline();

  const [mode, setMode]   = useState<"visual" | "json">("visual");
  const [name, setName]   = useState("");
  const [nodes, setNodes] = useState<NodeDef[]>([
    { id: "node-1", type: "inline", depends_on: [] },
  ]);
  const [json, setJson]   = useState("");
  const [jsonErr, setJsonErr] = useState("");

  function close() {
    setName(""); setNodes([{ id: "node-1", type: "inline", depends_on: [] }]);
    setJson(""); setJsonErr(""); setMode("visual");
    onClose();
  }

  function validate(): { ok: boolean; body?: unknown; error?: string } {
    if (!name.trim()) return { ok: false, error: "Pipeline name is required" };

    if (mode === "json") {
      try {
        const parsed = JSON.parse(json);
        return { ok: true, body: { name, ...parsed } };
      } catch {
        return { ok: false, error: "Invalid JSON" };
      }
    }

    const ids = nodes.map(n => n.id.trim()).filter(Boolean);
    if (ids.length === 0) return { ok: false, error: "Add at least one node" };
    if (new Set(ids).size !== ids.length) return { ok: false, error: "Duplicate node IDs" };
    if (hasCycle(nodes)) return { ok: false, error: "Cycle detected in dependencies" };

    return { ok: true, body: toApiBody(name, nodes) };
  }

  function handleSubmit() {
    const { ok, body, error } = validate();
    if (!ok) { setJsonErr(error!); return; }
    setJsonErr("");

    create.mutate(body, {
      onSuccess: (p) => {
        toast.success(`Pipeline "${name}" created — ${(p as { id: string }).id}`);
        close();
      },
      onError: (e) => toast.error(e.message),
    });
  }

  if (!open) return null;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={close} />
      <div className="relative z-10 w-full max-w-lg max-h-[90vh] overflow-y-auto rounded-xl border border-border/60 bg-card shadow-2xl">
        {/* Header */}
        <div className="flex items-center justify-between border-b px-5 py-4">
          <h2 className="font-display font-semibold">Create Pipeline</h2>
          <button onClick={close} className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground">
            <X className="h-4 w-4" />
          </button>
        </div>

        <div className="space-y-4 px-5 py-5">
          {/* Name */}
          <div className="space-y-1">
            <label className="block text-xs font-medium text-muted-foreground">Pipeline name *</label>
            <input className={inputCls} placeholder="resnet-pipeline" value={name} onChange={e => setName(e.target.value)} />
          </div>

          {/* Mode toggle */}
          <div className="flex rounded-lg border p-1 gap-1">
            {(["visual", "json"] as const).map(m => (
              <button
                key={m}
                type="button"
                onClick={() => setMode(m)}
                className={cn(
                  "flex-1 rounded-md px-3 py-1.5 text-sm font-medium transition-colors capitalize",
                  mode === m ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:text-foreground"
                )}
              >
                {m === "visual" ? "Visual builder" : "JSON editor"}
              </button>
            ))}
          </div>

          {mode === "visual" ? (
            <VisualBuilder nodes={nodes} setNodes={setNodes} />
          ) : (
            <div className="space-y-1">
              <label className="block text-xs font-medium text-muted-foreground">DAG spec (JSON)</label>
              <textarea
                className={cn(inputCls, "min-h-[200px] resize-y font-mono text-xs")}
                placeholder={`{\n  "dag_spec": {\n    "nodes": [\n      { "id": "preprocess", "job_template": { "type": "k8s_job" } },\n      { "id": "train", "depends_on": ["preprocess"], "job_template": { "type": "k8s_job" } }\n    ]\n  }\n}`}
                value={json}
                onChange={e => setJson(e.target.value)}
              />
            </div>
          )}

          {jsonErr && <p className="text-xs text-danger">{jsonErr}</p>}

          {/* Footer */}
          <div className="flex justify-end gap-2 border-t pt-4">
            <Button type="button" variant="ghost" onClick={close}>Cancel</Button>
            <Button onClick={handleSubmit} disabled={create.isPending}>
              {create.isPending && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
              Create
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}
