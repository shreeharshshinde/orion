"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { X, Loader2 } from "lucide-react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

import { Button } from "@/components/ui";
import { useSubmitJob } from "@/lib/hooks";
import { cn } from "@/lib/utils";

// ─── Schema ──────────────────────────────────────────────────────────────────

const base = z.object({
  name:             z.string().min(1, "Required"),
  type:             z.enum(["inline", "k8s_job"]),
  queue_name:       z.enum(["high", "default", "low"]),
  priority:         z.coerce.number().int().min(0).max(10),
  max_retries:      z.coerce.number().int().min(0).max(10),
  idempotency_key:  z.string().optional(),
  // inline
  handler_name:     z.string().optional(),
  handler_args:     z.string().optional(), // JSON string
  // k8s
  image:            z.string().optional(),
  command:          z.string().optional(), // space-separated
  namespace:        z.string().optional(),
  cpu:              z.string().optional(),
  memory:           z.string().optional(),
  gpu:              z.coerce.number().int().min(0).optional(),
});

type FormValues = z.infer<typeof base>;

// ─── Field ───────────────────────────────────────────────────────────────────

function Field({ label, error, children }: { label: string; error?: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1">
      <label className="block text-xs font-medium text-muted-foreground">{label}</label>
      {children}
      {error && <p className="text-xs text-danger">{error}</p>}
    </div>
  );
}

const inputCls = "w-full rounded-md border bg-background px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-primary";

// ─── Dialog ──────────────────────────────────────────────────────────────────

export function SubmitJobDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const submit = useSubmitJob();

  const { register, handleSubmit, watch, reset, formState: { errors } } =
    useForm<FormValues>({
      resolver: zodResolver(base),
      defaultValues: {
        type: "k8s_job",
        queue_name: "default",
        priority: 5,
        max_retries: 3,
      },
    });

  const jobType = watch("type");

  function close() { reset(); onClose(); }

  async function onSubmit(values: FormValues) {
    const payload =
      values.type === "inline"
        ? {
            handler_name: values.handler_name,
            args: values.handler_args ? JSON.parse(values.handler_args) : undefined,
          }
        : {
            kubernetes_spec: {
              image:     values.image,
              command:   values.command?.split(/\s+/).filter(Boolean),
              namespace: values.namespace || "orion-jobs",
              resources: {
                cpu:    values.cpu    || undefined,
                memory: values.memory || undefined,
                gpu:    values.gpu    || undefined,
              },
            },
          };

    submit.mutate(
      {
        name:            values.name,
        type:            values.type,
        queue_name:      values.queue_name,
        priority:        values.priority,
        max_retries:     values.max_retries,
        idempotency_key: values.idempotency_key || undefined,
        payload,
      },
      {
        onSuccess: (job) => {
          toast.success(`Job "${values.name}" submitted — ${(job as { id: string }).id}`);
          close();
        },
        onError: (e) => toast.error(e.message),
      },
    );
  }

  if (!open) return null;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      {/* Backdrop */}
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={close} />

      {/* Panel */}
      <div className="relative z-10 w-full max-w-lg max-h-[90vh] overflow-y-auto rounded-xl border border-border/60 bg-card shadow-2xl">
        {/* Header */}
        <div className="flex items-center justify-between border-b px-5 py-4">
          <h2 className="font-display font-semibold">Submit Job</h2>
          <button onClick={close} className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground">
            <X className="h-4 w-4" />
          </button>
        </div>

        <form onSubmit={handleSubmit(onSubmit)} className="space-y-4 px-5 py-5">
          {/* Type toggle */}
          <div className="flex rounded-lg border p-1 gap-1">
            {(["k8s_job", "inline"] as const).map((t) => (
              <label
                key={t}
                className={cn(
                  "flex-1 cursor-pointer rounded-md px-3 py-1.5 text-center text-sm font-medium transition-colors",
                  jobType === t ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:text-foreground"
                )}
              >
                <input type="radio" value={t} className="sr-only" {...register("type")} />
                {t === "k8s_job" ? "Kubernetes Job" : "Inline Handler"}
              </label>
            ))}
          </div>

          {/* Core fields */}
          <Field label="Job name *" error={errors.name?.message}>
            <input className={inputCls} placeholder="train-resnet" {...register("name")} />
          </Field>

          <div className="grid grid-cols-3 gap-3">
            <Field label="Queue" error={errors.queue_name?.message}>
              <select className={inputCls} {...register("queue_name")}>
                <option value="high">high</option>
                <option value="default">default</option>
                <option value="low">low</option>
              </select>
            </Field>
            <Field label="Priority (0–10)" error={errors.priority?.message}>
              <input className={inputCls} type="number" min={0} max={10} {...register("priority")} />
            </Field>
            <Field label="Max retries" error={errors.max_retries?.message}>
              <input className={inputCls} type="number" min={0} max={10} {...register("max_retries")} />
            </Field>
          </div>

          <Field label="Idempotency key" error={errors.idempotency_key?.message}>
            <input className={inputCls} placeholder="optional — e.g. run-2026-001" {...register("idempotency_key")} />
          </Field>

          {/* Inline-specific */}
          {jobType === "inline" && (
            <>
              <Field label="Handler name *" error={errors.handler_name?.message}>
                <input className={inputCls} placeholder="my_handler_func" {...register("handler_name")} />
              </Field>
              <Field label="Args (JSON)" error={errors.handler_args?.message}>
                <textarea className={cn(inputCls, "min-h-[80px] resize-y font-mono text-xs")} placeholder='{"key": "value"}' {...register("handler_args")} />
              </Field>
            </>
          )}

          {/* K8s-specific */}
          {jobType === "k8s_job" && (
            <>
              <Field label="Image *" error={errors.image?.message}>
                <input className={inputCls} placeholder="pytorch/pytorch:2.1.0-cuda11.8-cudnn8-runtime" {...register("image")} />
              </Field>
              <Field label="Command" error={errors.command?.message}>
                <input className={inputCls} placeholder="python train.py --epochs 50" {...register("command")} />
              </Field>
              <Field label="Namespace" error={errors.namespace?.message}>
                <input className={inputCls} placeholder="orion-jobs" {...register("namespace")} />
              </Field>
              <div className="grid grid-cols-3 gap-3">
                <Field label="CPU" error={errors.cpu?.message}>
                  <input className={inputCls} placeholder="4000m" {...register("cpu")} />
                </Field>
                <Field label="Memory" error={errors.memory?.message}>
                  <input className={inputCls} placeholder="16Gi" {...register("memory")} />
                </Field>
                <Field label="GPU" error={errors.gpu?.message}>
                  <input className={inputCls} type="number" min={0} placeholder="0" {...register("gpu")} />
                </Field>
              </div>
            </>
          )}

          {/* Footer */}
          <div className="flex justify-end gap-2 border-t pt-4">
            <Button type="button" variant="ghost" onClick={close}>Cancel</Button>
            <Button type="submit" disabled={submit.isPending}>
              {submit.isPending && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
              Submit
            </Button>
          </div>
        </form>
      </div>
    </div>
  );
}
