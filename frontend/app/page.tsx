"use client";

import {
  Activity,
  ArrowRight, BookOpen, Boxes,
  GitBranch,
  RefreshCw,
  Server,
  Shield,
  Waypoints, Zap
} from "lucide-react";
import Image from "next/image";
import Link from "next/link";

import { Badge, Button, Card } from "@/components/ui";
import { useJobs, usePipelines, useWorkers } from "@/lib/hooks";

// ─── Static feature data ──────────────────────────────────────────────────────

const features = [
  {
    icon: Waypoints,
    title: "Priority queues",
    desc: "Three Redis Streams queues — high, default, low — with weighted dispatch, per-queue rate limiting, and live config reload.",
  },
  {
    icon: Shield,
    title: "At-least-once delivery",
    desc: "Consumer groups + PEL tracking. Unacknowledged jobs are reclaimed by the orphan sweeper. No job is silently lost.",
  },
  {
    icon: Server,
    title: "Kubernetes execution",
    desc: "Launches K8s Jobs via client-go. Supports GPU requests, custom namespaces, service accounts, and backoff-0 retry ownership.",
  },
  {
    icon: GitBranch,
    title: "DAG pipelines",
    desc: "Topological node advancement with cascade-cancel on failure. Create multi-stage ML workflows as a single submission.",
  },
  {
    icon: Activity,
    title: "Real-time streaming",
    desc: "gRPC WatchJob / WatchPipeline driven by PostgreSQL LISTEN/NOTIFY. Zero polling on the client.",
  },
  {
    icon: RefreshCw,
    title: "Durable retries",
    desc: "Full-jitter exponential backoff. CAS state transitions prevent double-claiming across concurrent scheduler instances.",
  },
];

const SUBMIT_SNIPPET = `curl -sX POST http://localhost:8080/jobs \\
  -H 'Content-Type: application/json' \\
  -d '{
    "name": "train-resnet",
    "type": "k8s_job",
    "queue_name": "high",
    "priority": 8,
    "max_retries": 3,
    "payload": {
      "kubernetes_spec": {
        "image": "pytorch/pytorch:2.1.0-cuda11.8",
        "command": ["python", "train.py"],
        "resources": { "gpu": 1, "memory": "16Gi" }
      }
    }
  }'`;

// ─── Live stat pill ───────────────────────────────────────────────────────────

function StatPill({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="flex flex-col items-center rounded-xl border border-border/60 bg-card/60 px-5 py-3 backdrop-blur">
      <span className="font-display text-2xl font-semibold tabular-nums text-foreground">{value}</span>
      <span className="mt-0.5 text-xs text-muted-foreground">{label}</span>
    </div>
  );
}

// ─── Page ─────────────────────────────────────────────────────────────────────

export default function HomePage() {
  const { data: jobs = [] } = useJobs();
  const { data: workers = [] } = useWorkers();
  const { data: pipelines = [] } = usePipelines();

  const running = jobs.filter(j => j.status === "running").length;
  const queued = jobs.filter(j => j.status === "queued").length;
  const active = workers.filter(w => w.status !== "offline").length;
  const pRunning = pipelines.filter(p => p.status === "running").length;

  return (
    <main className="bg-grid min-h-screen">
      {/* ── Hero ─────────────────────────────────────────────────────────── */}
      <section className="relative mx-auto max-w-6xl px-6 pb-16 pt-24 text-center">
        {/* Ambient glow behind the heading */}
        <div className="pointer-events-none absolute left-1/2 top-8 h-72 w-72 -translate-x-1/2 rounded-full bg-primary/10 blur-3xl" />

        <div className="relative">

          <div className="flex flex-col items-center gap-4">
            <div className="flex flex-col items-center">
              <Image
                src="/orion_logo.png"
                alt="Orion"
                width={322}
                height={322}
                className="rounded-2xl"
                priority
              />

              <h1 className="font-display text-6xl font-semibold tracking-tight text-foreground sm:text-7xl">
                Orion
              </h1>

              <Badge tone="aqua" className="mt-6 inline-flex">
                <Zap className="h-3 w-3" />
                Distributed ML job orchestrator for Kubernetes
              </Badge>
            </div>
          </div>

          <p className="mx-auto mt-6 max-w-2xl text-lg leading-relaxed text-muted-foreground">
            Orion sits between your training pipelines and the cluster — handling priority queuing,
            retries, backpressure, DAG orchestration, and real-time status streaming so your
            application code doesn't have to.
          </p>

          <div className="mt-8 flex flex-wrap items-center justify-center gap-3">
            <Button asChild size="md">
              <Link href="/dashboard">
                Open Dashboard <ArrowRight className="h-4 w-4" />
              </Link>
            </Button>
            <Button asChild variant="outline" size="md">
              <Link href="/docs">
                <BookOpen className="h-4 w-4" />
                Read Docs
              </Link>
            </Button>
          </div>

          {/* Live stats */}
          <div className="mt-12 flex flex-wrap justify-center gap-3">
            <StatPill label="Workers active" value={active} />
            <StatPill label="Jobs running" value={running} />
            <StatPill label="Jobs queued" value={queued} />
            <StatPill label="Pipelines running" value={pRunning} />
          </div>
        </div>
      </section>

      {/* ── What Orion does ──────────────────────────────────────────────── */}
      <section className="mx-auto max-w-6xl px-6 pb-20">
        <div className="mb-10 text-center">
          <h2 className="font-display text-2xl font-semibold">Everything your ML platform needs</h2>
          <p className="mt-2 text-sm text-muted-foreground">From job submission to Kubernetes execution — one control plane.</p>
        </div>

        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {features.map(({ icon: Icon, title, desc }) => (
            <Card key={title} className="flex gap-4 p-5 hover:border-primary/30 transition-colors">
              <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border border-primary/30 bg-primary/10 text-primary">
                <Icon className="h-4 w-4" />
              </div>
              <div>
                <p className="font-display font-semibold">{title}</p>
                <p className="mt-1 text-sm leading-relaxed text-muted-foreground">{desc}</p>
              </div>
            </Card>
          ))}
        </div>
      </section>

      {/* ── Submit in seconds ────────────────────────────────────────────── */}
      <section className="mx-auto max-w-6xl px-6 pb-24">
        <div className="overflow-hidden rounded-2xl border border-border/60 bg-card/60 backdrop-blur">
          <div className="flex items-center gap-3 border-b border-border/60 bg-muted/30 px-5 py-3">
            <div className="flex gap-1.5">
              <span className="h-3 w-3 rounded-full bg-danger/60" />
              <span className="h-3 w-3 rounded-full bg-warning/60" />
              <span className="h-3 w-3 rounded-full bg-success/60" />
            </div>
            <span className="font-mono text-xs text-muted-foreground">Submit a GPU training job in one request</span>
            <div className="ml-auto flex items-center gap-2">
              <Boxes className="h-3.5 w-3.5 text-primary" />
              <span className="font-mono text-xs text-primary">orion · localhost:8080</span>
            </div>
          </div>
          <pre className="overflow-x-auto p-6 font-mono text-xs leading-6 text-foreground/90">
            <code>{SUBMIT_SNIPPET}</code>
          </pre>
        </div>
      </section>
    </main>
  );
}
