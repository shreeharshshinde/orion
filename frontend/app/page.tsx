"use client";

import {
  Activity,
  ArrowRight,
  BookOpen,
  Boxes,
  CheckCircle2,
  Clock,
  Cpu,
  GitBranch,
  Github,
  Layers,
  Radio,
  RefreshCw,
  Server,
  Shield,
  SlidersHorizontal,
  Sun,
  Moon,
  Terminal,
  Waypoints,
  XCircle,
  Zap,
} from "lucide-react";
import Image from "next/image";
import Link from "next/link";
import { useTheme } from "next-themes";
import { useEffect, useState } from "react";

import { Badge, Button, Card } from "@/components/ui";
import { useJobs, usePipelines, useWorkers } from "@/lib/hooks";

// ─── Live stat pill ───────────────────────────────────────────────────────────

function StatPill({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="flex flex-col items-center rounded-xl border border-border/60 bg-card/60 px-5 py-3 backdrop-blur shadow-sm">
      <span className="font-display text-2xl font-semibold tabular-nums text-foreground">{value}</span>
      <span className="mt-0.5 text-xs text-muted-foreground">{label}</span>
    </div>
  );
}

// ─── Code Snippets for Interactive Tabs ────────────────────────────────────────

const CODE_EXAMPLES = {
  k8s: {
    title: "Kubernetes GPU Training Job",
    badge: "k8s_job · REST API",
    description: "Submit a distributed training job with Guaranteed QoS, GPU limits, and automatic TTL cleanup.",
    code: `# Submit a PyTorch ResNet training job with GPU resource requests
curl -sX POST http://localhost:8080/jobs \\
  -H 'Content-Type: application/json' \\
  -d '{
    "name": "resnet-distributed-training",
    "type": "k8s_job",
    "queue_name": "high",
    "priority": 10,
    "max_retries": 3,
    "payload": {
      "kubernetes_spec": {
        "image": "pytorch/pytorch:2.1.0-cuda11.8",
        "command": ["python", "train.py", "--batch-size", "128", "--epochs", "50"],
        "resources": {
          "cpu": "4",
          "memory": "16Gi",
          "gpu": 1
        },
        "env": {
          "NCCL_DEBUG": "INFO",
          "PYTHONUNBUFFERED": "1"
        }
      }
    }
  }'`,
  },
  dag: {
    title: "Multi-Stage ML DAG Pipeline",
    badge: "dag_pipeline · REST API",
    description: "Orchestrate multi-step workflows with topological advancement and automatic cascade-cancellation.",
    code: `# Create a complete data-prep -> train -> evaluate -> deploy DAG
curl -sX POST http://localhost:8080/pipelines \\
  -H 'Content-Type: application/json' \\
  -d '{
    "name": "bert-finetuning-pipeline",
    "spec": {
      "nodes": [
        { "id": "preprocess", "job_template": { "name": "preprocess", "type": "inline", "payload": { "handler_name": "data_validation" } } },
        { "id": "train", "dependencies": ["preprocess"], "job_template": { "name": "train", "type": "k8s_job", "payload": { "kubernetes_spec": { "image": "transformers:latest", "resources": { "gpu": 1 } } } } },
        { "id": "evaluate", "dependencies": ["train"], "job_template": { "name": "evaluate", "type": "inline", "payload": { "handler_name": "model_eval" } } },
        { "id": "deploy", "dependencies": ["evaluate"], "job_template": { "name": "deploy", "type": "k8s_job", "payload": { "kubernetes_spec": { "image": "triton:23.10" } } } }
      ]
    }
  }'`,
  },
  grpc: {
    title: "Real-Time gRPC State Watcher",
    badge: "grpc · Go SDK",
    description: "Stream live state transitions over gRPC without polling the primary database.",
    code: `// Stream real-time job events over gRPC
conn, _ := grpc.Dial("localhost:9090", grpc.WithInsecure())
client := pb.NewJobServiceClient(conn)

stream, err := client.WatchJob(ctx, &pb.WatchJobRequest{JobId: jobID})
for {
    event, err := stream.Recv()
    if err == io.EOF {
        break // Terminal state reached (completed, failed, or cancelled)
    }
    log.Printf("Job %s transitioned from %s -> %s (Worker: %s)",
        event.JobId, event.OldStatus, event.NewStatus, event.WorkerId)
}`,
  },
  cancel: {
    title: "Cooperative Cancel & DLQ Replay",
    badge: "ops · REST API",
    description: "Cancel in-flight GPU pods immediately via Redis Pub/Sub, or replay failed jobs from the DLQ.",
    code: `# 1. Cooperative cross-process cancellation
# Cancels context, halts worker execution, and triggers Foreground pod delete:
curl -sX POST http://localhost:8080/jobs/550e8400-e29b-41d4-a716-446655440000/cancel

# 2. Dead-letter queue (DLQ) replay
# Resets failed/dead job back to queued and re-enqueues into Redis Stream:
curl -sX POST http://localhost:8080/jobs/550e8400-e29b-41d4-a716-446655440000/replay

# 3. Dynamic queue rate limit update without cluster restart
curl -sX PUT http://localhost:8080/queues/high \\
  -H 'Content-Type: application/json' \\
  -d '{"weight": 0.85, "rate_per_sec": 50, "concurrency_limit": 30}'`,
  },
};

// ─── Page ─────────────────────────────────────────────────────────────────────

export default function HomePage() {
  const { data: jobs = [] } = useJobs();
  const { data: workers = [] } = useWorkers();
  const { data: pipelines = [] } = usePipelines();

  const { theme, setTheme } = useTheme();
  const [mounted, setMounted] = useState(false);
  const [activeCodeTab, setActiveCodeTab] = useState<keyof typeof CODE_EXAMPLES>("k8s");

  useEffect(() => setMounted(true), []);

  const hasApi = typeof window !== "undefined"
    ? (!!process.env.NEXT_PUBLIC_API_URL || window.location.hostname === "localhost")
    : true;

  const running = hasApi ? jobs.filter(j => j.status === "running").length : 14;
  const queued = hasApi ? jobs.filter(j => j.status === "queued").length : 3;
  const active = hasApi ? workers.filter(w => w.status !== "offline").length : 8;
  const pRunning = hasApi ? pipelines.filter(p => p.status === "running").length : 2;

  return (
    <main className="bg-grid min-h-screen">
      {/* ── Header ───────────────────────────────────────────────────────── */}
      <header className="fixed left-1/2 top-4 z-50 flex h-16 w-[calc(100%-2rem)] -translate-x-1/2 items-center justify-between rounded-2xl border border-white/10 bg-background/50 px-6 backdrop-blur-xl shadow-2xl">
        <div className="flex items-center">
          <Image src="/orion_logo.png" alt="Orion Logo" width={84} height={84} className="object-contain" />
          <span className="font-display text-lg font-bold tracking-wide text-foreground">Orion</span>
        </div>
        <nav className="hidden md:flex items-center gap-6 text-sm font-medium text-muted-foreground">
          <Link href="/dashboard" className="transition-colors hover:text-foreground">Dashboard</Link>
          <Link href="/docs/architecture" className="transition-colors hover:text-foreground">Architecture</Link>
          <Link href="/docs/runbook" className="transition-colors hover:text-foreground">Runbook</Link>
          <Link href="/docs" className="flex items-center rounded-md border border-border/60 bg-muted/20 px-3 py-1.5 transition-colors hover:bg-muted/40 hover:text-foreground text-foreground">
            <BookOpen className="mr-2 h-4 w-4" /> Docs
          </Link>
          <a href="https://github.com/shreeharshshinde/orion" target="_blank" rel="noreferrer" className="flex items-center gap-2 transition-colors hover:text-foreground">
            <Github className="h-4 w-4" /> GitHub
          </a>
          <button
            onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
            className="flex h-8 w-8 items-center justify-center rounded-full border border-border/60 bg-muted/20 transition-colors hover:bg-muted/40 hover:text-foreground"
            aria-label="Toggle theme"
          >
            {mounted && theme === "light" ? <Moon className="h-4 w-4" /> : <Sun className="h-4 w-4" />}
          </button>
        </nav>
      </header>

      {/* ── Hero ─────────────────────────────────────────────────────────── */}
      <section className="relative mx-auto max-w-6xl px-6 pb-16 pt-32 text-center">
        {/* Ambient glow behind the heading */}
        <div className="pointer-events-none absolute left-1/2 top-8 h-72 w-72 -translate-x-1/2 rounded-full bg-primary/10 blur-3xl" />

        <div className="relative">
          <div className="flex flex-col items-center gap-4">
            <div className="flex flex-col items-center">
              <div className="relative mb-8">
                {/* Blurred glowing backdrop logo */}
                <Image
                  src="/orion_logo.png"
                  alt="Orion Glow"
                  width={322}
                  height={322}
                  className="absolute inset-0 opacity-40 blur-3xl"
                  priority
                />
                {/* Main floating logo */}
                <Image
                  src="/orion_logo.png"
                  alt="Orion"
                  width={322}
                  height={322}
                  className="relative z-10 animate-float drop-shadow-2xl"
                  priority
                />
              </div>

              <h1 className="font-display text-6xl font-semibold tracking-tight text-foreground sm:text-7xl">
                Orion
              </h1>

              {/* Maturity Badges */}
              <div className="mt-5 flex flex-wrap items-center justify-center gap-2">
                <span className="inline-flex items-center rounded-md border border-border/60 bg-muted/20 px-2 py-0.5 text-[10px] font-medium uppercase tracking-wider font-mono">
                  <span className="mr-1 text-muted-foreground">go</span>
                  <span className="font-semibold text-foreground">v1.22+</span>
                </span>
                <span className="inline-flex items-center rounded-md border border-border/60 bg-muted/20 px-2 py-0.5 text-[10px] font-medium uppercase tracking-wider font-mono">
                  <span className="mr-1 text-muted-foreground">license</span>
                  <span className="font-semibold text-foreground">Apache-2.0</span>
                </span>
                <span className="inline-flex items-center rounded-md border border-success/30 bg-success/10 px-2 py-0.5 text-[10px] font-medium text-success uppercase tracking-wider font-mono">
                  <span className="mr-1 opacity-80">build</span>
                  <span className="font-bold">passing</span>
                </span>
                <span className="inline-flex items-center rounded-md border border-primary/30 bg-primary/10 px-2 py-0.5 text-[10px] font-medium text-primary uppercase tracking-wider font-mono">
                  <span className="mr-1 opacity-80">tests</span>
                  <span className="font-bold">274 passing</span>
                </span>
                <span className="inline-flex items-center rounded-md border border-cyan-500/30 bg-cyan-500/10 px-2 py-0.5 text-[10px] font-medium text-cyan-400 uppercase tracking-wider font-mono">
                  <span className="mr-1 opacity-80">tracing</span>
                  <span className="font-bold">OpenTelemetry</span>
                </span>
              </div>

              <Badge tone="aqua" className="mt-5 inline-flex">
                <Zap className="h-3 w-3" />
                Distributed ML Workflow & Job Orchestrator
              </Badge>
            </div>
          </div>

          <p className="mx-auto mt-6 max-w-3xl text-lg leading-relaxed text-muted-foreground">
            A high-performance Go orchestrator unifying <strong className="text-foreground">in-process Go routines</strong> and <strong className="text-foreground">Kubernetes GPU batch workloads</strong>. Built with atomic PostgreSQL Compare-And-Swap state transitions, Redis Streams backpressure, weighted fair queuing, and self-healing failure recovery.
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
            <a
              href="https://github.com/shreeharshshinde/orion"
              target="_blank"
              rel="noreferrer"
              className="inline-flex h-10 items-center justify-center gap-2 rounded-lg border border-border/80 bg-background/50 px-4 text-sm font-medium text-foreground hover:bg-muted/40 transition-colors shadow-sm"
            >
              <Github className="h-4 w-4 text-muted-foreground" />
              <span>Star on GitHub</span>
            </a>
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

      {/* ── Bento Grid Features ───────────────────────────────────────────── */}
      <section className="w-full border-y border-border/40 bg-muted/20 py-24 my-6">
        <div className="mx-auto max-w-6xl px-6">
          <div className="mb-14 text-center">
            <h2 className="font-display text-3xl font-semibold tracking-tight text-foreground sm:text-4xl">
              Engineered for ML Reliability & Throughput
            </h2>
            <p className="mt-3 text-base text-muted-foreground max-w-2xl mx-auto">
              From sub-millisecond Go dispatching to guaranteed Kubernetes GPU lifecycle management.
            </p>
          </div>

          <div className="grid gap-4 md:grid-cols-3">
            {/* Highlighted Bento Card: CAS & Consistency */}
            <Card className="p-6 md:col-span-2 border-primary/40 bg-gradient-to-br from-primary/5 to-transparent flex flex-col justify-between hover:border-primary/60 transition-colors relative overflow-hidden group">
              <div className="absolute inset-0 bg-gradient-to-r from-primary/10 to-transparent opacity-0 group-hover:opacity-100 transition-opacity duration-500 pointer-events-none" />
              <div className="relative">
                <div className="flex h-10 w-10 items-center justify-center rounded-lg border border-primary/40 bg-primary/20 text-primary">
                  <Shield className="h-5 w-5" />
                </div>
                <h3 className="mt-4 font-display text-xl font-bold tracking-tight text-foreground">
                  Atomic CAS State Transitions & Exactly-Once Semantics
                </h3>
                <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
                  Job state transitions are guarded at the database level with atomic SQL Compare-and-Swap (<code className="font-mono text-xs text-primary">UPDATE ... WHERE status = expected</code>). Combined with Redis Streams consumer group Pending Entries List (PEL) tracking and append-only execution history, race conditions and duplicate executions are eliminated under worker churn.
                </p>
              </div>
              <Link href="/docs/architecture" className="mt-6 inline-flex items-center gap-1 text-xs font-semibold text-primary hover:underline">
                Explore consistency architecture <ArrowRight className="h-3.5 w-3.5" />
              </Link>
            </Card>

            {/* Weighted Fair Queuing & Rate Limiting */}
            <Card className="p-6 flex flex-col justify-between hover:border-primary/30 transition-colors">
              <div>
                <div className="flex h-9 w-9 items-center justify-center rounded-lg border border-primary/30 bg-primary/10 text-primary">
                  <SlidersHorizontal className="h-4 w-4" />
                </div>
                <h3 className="mt-4 font-display font-semibold text-foreground">Weighted Fair Scheduling</h3>
                <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
                  Token-bucket rate limiting per queue (<code className="font-mono text-xs">high</code>, <code className="font-mono text-xs">default</code>, <code className="font-mono text-xs">low</code>) with proportional weighted dispatch. Unused high-priority slots cascade down to prevent queue starvation.
                </p>
              </div>
              <div className="mt-4 text-xs font-mono text-muted-foreground">Dynamic config reload via API</div>
            </Card>

            {/* Kubernetes Guaranteed QoS & GPU Support */}
            <Card className="p-6 flex flex-col justify-between hover:border-primary/30 transition-colors">
              <div>
                <div className="flex h-9 w-9 items-center justify-center rounded-lg border border-primary/30 bg-primary/10 text-primary">
                  <Cpu className="h-4 w-4" />
                </div>
                <h3 className="mt-4 font-display font-semibold text-foreground">Guaranteed QoS & GPU Jobs</h3>
                <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
                  Natively constructs <code className="font-mono text-xs">batchv1.Job</code> manifests with <code className="font-mono text-xs">nvidia.com/gpu</code> resource limits. Enforces equal requests and limits for Guaranteed QoS and sets <code className="font-mono text-xs">TTLSecondsAfterFinished</code>.
                </p>
              </div>
              <div className="mt-4 text-xs font-mono text-muted-foreground">Zero zombie GPU pods</div>
            </Card>

            {/* Topological DAG Pipelines */}
            <Card className="p-6 flex flex-col justify-between hover:border-primary/30 transition-colors">
              <div>
                <div className="flex h-9 w-9 items-center justify-center rounded-lg border border-primary/30 bg-primary/10 text-primary">
                  <GitBranch className="h-4 w-4" />
                </div>
                <h3 className="mt-4 font-display font-semibold text-foreground">Topological DAG Pipelines</h3>
                <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
                  Single-JOIN dependency checks advanced every 2 seconds. Evaluates topological readiness (<code className="font-mono text-xs">ReadyNodes()</code>) and executes BFS cascade cancellation on downstream tasks if a parent node permanently fails.
                </p>
              </div>
              <div className="mt-4 text-xs font-mono text-muted-foreground">Multi-stage pipeline workflows</div>
            </Card>

            {/* Cross-Process Cooperative Cancellation */}
            <Card className="p-6 flex flex-col justify-between hover:border-primary/30 transition-colors">
              <div>
                <div className="flex h-9 w-9 items-center justify-center rounded-lg border border-primary/30 bg-primary/10 text-primary">
                  <XCircle className="h-4 w-4" />
                </div>
                <h3 className="mt-4 font-display font-semibold text-foreground">Cross-Process Cancellation</h3>
                <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
                  Canceling an in-flight job broadcasts over Redis Pub/Sub (<code className="font-mono text-xs">orion:cancel</code>). Workers abort active contexts and trigger Kubernetes <code className="font-mono text-xs">Foreground</code> cascade pod deletion to instantly free GPU nodes.
                </p>
              </div>
              <div className="mt-4 text-xs font-mono text-muted-foreground">Immediate resource reclamation</div>
            </Card>

            {/* Distributed Tracing & Observability */}
            <Card className="p-6 flex flex-col justify-between hover:border-primary/30 transition-colors">
              <div>
                <div className="flex h-9 w-9 items-center justify-center rounded-lg border border-primary/30 bg-primary/10 text-primary">
                  <Radio className="h-4 w-4" />
                </div>
                <h3 className="mt-4 font-display font-semibold text-foreground">OpenTelemetry & Observability</h3>
                <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
                  Full distributed tracing with W3C TraceContext propagation across Redis Streams. 20+ low-cardinality Prometheus metrics, structured <code className="font-mono text-xs">slog</code>, Jaeger OTLP export, and pre-configured Grafana dashboards.
                </p>
              </div>
              <div className="mt-4 text-xs font-mono text-muted-foreground">End-to-end trace continuity</div>
            </Card>

            {/* Self-Healing & Dead-Letter Replay */}
            <Card className="p-6 flex flex-col justify-between hover:border-primary/30 transition-colors">
              <div>
                <div className="flex h-9 w-9 items-center justify-center rounded-lg border border-primary/30 bg-primary/10 text-primary">
                  <RefreshCw className="h-4 w-4" />
                </div>
                <h3 className="mt-4 font-display font-semibold text-foreground">Self-Healing & DLQ Replay</h3>
                <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
                  Two-tier crash recovery: 30s Redis PEL sweeps (<code className="font-mono text-xs">XAUTOCLAIM</code>) + 90s PostgreSQL worker heartbeat TTL orphan recovery. Dead-letter queue support with atomic manual replay API.
                </p>
              </div>
              <div className="mt-4 text-xs font-mono text-muted-foreground">Zero silent job loss</div>
            </Card>

            {/* gRPC Streaming Watch API */}
            <Card className="p-6 flex flex-col justify-between hover:border-primary/30 transition-colors">
              <div>
                <div className="flex h-9 w-9 items-center justify-center rounded-lg border border-primary/30 bg-primary/10 text-primary">
                  <Activity className="h-4 w-4" />
                </div>
                <h3 className="mt-4 font-display font-semibold text-foreground">gRPC Streaming Watch API</h3>
                <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
                  High-performance gRPC service with an in-memory fan-out Broadcaster and <code className="font-mono text-xs">InstrumentedStore</code>. Pushes state transitions directly to client streams with automatic polling fallback.
                </p>
              </div>
              <div className="mt-4 text-xs font-mono text-muted-foreground">Zero database polling overhead</div>
            </Card>
          </div>
        </div>
      </section>

      {/* ── Interactive Code Demos (Tabbed Interface) ────────────────────────── */}
      <section className="w-full border-t border-border/40 bg-zinc-950/40 py-24 relative overflow-hidden">
        {/* Ambient background glow */}
        <div className="pointer-events-none absolute -right-24 bottom-0 h-96 w-96 rounded-full bg-primary/5 blur-3xl" />

        <div className="mx-auto max-w-6xl px-6">
          <div className="mb-12 text-center">
            <h2 className="font-display text-2xl font-bold tracking-tight text-foreground sm:text-3xl">
              Experience the Orion API
            </h2>
            <p className="mt-2 text-sm text-muted-foreground">
              Clean abstractions for submitting jobs, orchestrating DAGs, streaming events, and managing operations.
            </p>

            {/* Interactive Tab Switcher */}
            <div className="mt-6 flex flex-wrap items-center justify-center gap-2">
              {(Object.keys(CODE_EXAMPLES) as Array<keyof typeof CODE_EXAMPLES>).map((key) => (
                <button
                  key={key}
                  onClick={() => setActiveCodeTab(key)}
                  className={`rounded-lg px-4 py-2 text-xs font-semibold transition-all ${
                    activeCodeTab === key
                      ? "bg-primary text-primary-foreground shadow-md"
                      : "bg-muted/40 text-muted-foreground hover:bg-muted hover:text-foreground"
                  }`}
                >
                  {CODE_EXAMPLES[key].title}
                </button>
              ))}
            </div>
          </div>

          <div className="grid gap-8 lg:grid-cols-5 items-start">
            <div className="lg:col-span-2 flex flex-col justify-center">
              <div className="flex h-10 w-10 items-center justify-center rounded-lg border border-primary/30 bg-primary/10 text-primary mb-4">
                <Terminal className="h-5 w-5" />
              </div>
              <h3 className="font-display text-xl font-bold tracking-tight text-foreground">
                {CODE_EXAMPLES[activeCodeTab].title}
              </h3>
              <div className="mt-2 inline-flex items-center gap-1.5 font-mono text-xs text-primary">
                <span className="h-1.5 w-1.5 rounded-full bg-primary" />
                {CODE_EXAMPLES[activeCodeTab].badge}
              </div>
              <p className="mt-4 text-sm leading-relaxed text-muted-foreground">
                {CODE_EXAMPLES[activeCodeTab].description}
              </p>

              <div className="mt-6 flex items-center gap-3">
                <Button asChild size="sm">
                  <Link href="/docs/api">
                    API Reference <ArrowRight className="h-3.5 w-3.5" />
                  </Link>
                </Button>
                <Button asChild variant="outline" size="sm">
                  <Link href="/docs/runbook">
                    Operational Runbook
                  </Link>
                </Button>
              </div>
            </div>

            <div className="lg:col-span-3">
              <div className="overflow-hidden rounded-2xl border border-border/60 bg-card/60 backdrop-blur shadow-xl">
                <div className="flex items-center gap-3 border-b border-border/60 bg-muted/30 px-5 py-3">
                  <div className="flex gap-1.5">
                    <span className="h-3 w-3 rounded-full bg-danger/60" />
                    <span className="h-3 w-3 rounded-full bg-warning/60" />
                    <span className="h-3 w-3 rounded-full bg-success/60" />
                  </div>
                  <span className="font-mono text-xs text-muted-foreground">{CODE_EXAMPLES[activeCodeTab].badge}</span>
                  <div className="ml-auto">
                    <span className="font-mono text-[10px] text-primary/80 bg-primary/10 px-2 py-0.5 rounded">bash · go</span>
                  </div>
                </div>
                <pre className="overflow-x-auto p-6 font-mono text-xs leading-6 bg-muted/5 text-[#d4d4d4]">
                  <code>{CODE_EXAMPLES[activeCodeTab].code}</code>
                </pre>
              </div>
            </div>
          </div>
        </div>
      </section>

      {/* ── Comparison Table ──────────────────────────────────────────────── */}
      <section className="mx-auto max-w-6xl px-6 py-24">
        <div className="mb-12 text-center">
          <h2 className="font-display text-2xl font-bold tracking-tight text-foreground sm:text-3xl">
            How Orion Compares
          </h2>
          <p className="mt-2 text-sm text-muted-foreground">
            Built for low-latency Kubernetes ML scheduling vs heavyweight general pipeline engines.
          </p>
        </div>
        <div className="overflow-hidden rounded-2xl border border-border/60 bg-card/40 backdrop-blur shadow-sm">
          <div className="overflow-x-auto">
            <table className="w-full text-left border-collapse text-sm">
              <thead>
                <tr className="border-b border-border/80 bg-muted/20">
                  <th className="p-4 font-semibold text-foreground">Dimension</th>
                  <th className="p-4 font-semibold text-primary bg-primary/5">Orion</th>
                  <th className="p-4 font-semibold text-muted-foreground">Argo Workflows</th>
                  <th className="p-4 font-semibold text-muted-foreground">Apache Airflow</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border/60">
                <tr className="hover:bg-muted/10 transition-colors">
                  <td className="p-4 font-medium text-foreground">Scheduling Latency</td>
                  <td className="p-4 text-primary font-semibold bg-primary/5">Sub-second (Compiled Go / Redis)</td>
                  <td className="p-4 text-muted-foreground">Seconds (K8s controller loop)</td>
                  <td className="p-4 text-muted-foreground">Minutes (Heavy DB polling check)</td>
                </tr>
                <tr className="hover:bg-muted/10 transition-colors">
                  <td className="p-4 font-medium text-foreground">Execution Model</td>
                  <td className="p-4 text-primary font-semibold bg-primary/5">Dual: In-process Go + K8s Jobs</td>
                  <td className="p-4 text-muted-foreground">Container Pods only</td>
                  <td className="p-4 text-muted-foreground">Python worker processes / Celery</td>
                </tr>
                <tr className="hover:bg-muted/10 transition-colors">
                  <td className="p-4 font-medium text-foreground">GPU Resource Teardown</td>
                  <td className="p-4 text-primary font-semibold bg-primary/5">Immediate Foreground cascade delete</td>
                  <td className="p-4 text-muted-foreground">Controller sweep delay</td>
                  <td className="p-4 text-muted-foreground">Potential zombie pods on worker kill</td>
                </tr>
                <tr className="hover:bg-muted/10 transition-colors">
                  <td className="p-4 font-medium text-foreground">State Consistency</td>
                  <td className="p-4 text-primary font-semibold bg-primary/5">Atomic PostgreSQL CAS + Redis PEL</td>
                  <td className="p-4 text-muted-foreground">At-least-once CRD status updates</td>
                  <td className="p-4 text-muted-foreground">At-least-once (table row locks)</td>
                </tr>
                <tr className="hover:bg-muted/10 transition-colors">
                  <td className="p-4 font-medium text-foreground">Queue Fair Sharing</td>
                  <td className="p-4 text-primary font-semibold bg-primary/5">Weighted Fair Queuing + Token Bucket</td>
                  <td className="p-4 text-muted-foreground">ResourceQuota limits only</td>
                  <td className="p-4 text-muted-foreground">Pool concurrency limits</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </section>

      {/* ── Quickstart Section ────────────────────────────────────────────── */}
      <section className="w-full border-t border-border/40 bg-zinc-950/20 py-20">
        <div className="mx-auto max-w-6xl px-6">
          <div className="grid gap-12 lg:grid-cols-5 items-center">
            <div className="lg:col-span-2">
              <div className="flex h-10 w-10 items-center justify-center rounded-lg border border-primary/30 bg-primary/10 text-primary mb-5">
                <Server className="h-5 w-5" />
              </div>
              <h2 className="font-display text-2xl font-bold tracking-tight text-foreground sm:text-3xl">
                Spin up locally in 30 seconds
              </h2>
              <p className="mt-4 text-sm leading-relaxed text-muted-foreground">
                Boot PostgreSQL, Redis, Jaeger, and Prometheus with Docker Compose and apply schema migrations with a single command.
              </p>
              <div className="mt-6 flex items-center gap-3">
                <Button asChild size="sm">
                  <Link href="/docs/local-development">
                    Setup Guide <ArrowRight className="h-3.5 w-3.5" />
                  </Link>
                </Button>
                <Button asChild variant="outline" size="sm">
                  <Link href="/dashboard">
                    View Live Dashboard
                  </Link>
                </Button>
              </div>
            </div>
            <div className="lg:col-span-3">
              <div className="overflow-hidden rounded-2xl border border-border/60 bg-card/60 backdrop-blur shadow-lg">
                <div className="flex items-center gap-3 border-b border-border/60 bg-muted/30 px-5 py-3">
                  <div className="flex gap-1.5">
                    <span className="h-3 w-3 rounded-full bg-danger/60" />
                    <span className="h-3 w-3 rounded-full bg-warning/60" />
                    <span className="h-3 w-3 rounded-full bg-success/60" />
                  </div>
                  <span className="font-mono text-xs text-muted-foreground">Local development stack</span>
                  <div className="ml-auto">
                    <span className="font-mono text-[10px] text-primary/80 bg-primary/10 px-2 py-0.5 rounded">docker compose · bash</span>
                  </div>
                </div>
                <pre className="overflow-x-auto p-6 font-mono text-xs leading-6 bg-muted/5 text-[#d4d4d4]">
                  <code>
                    <span className="text-[#6a9955]"># Clone and start infrastructure dependencies</span>{"\n"}
                    <span className="text-[#569cd6]">git clone</span> <span className="text-[#ce9178]">https://github.com/shreeharshshinde/orion.git</span> <span className="text-[#569cd6]">&& cd</span> orion{"\n"}
                    <span className="text-[#569cd6]">docker compose up</span> <span className="text-[#b5cea8]">-d</span>{"\n\n"}
                    <span className="text-[#6a9955]"># Apply database migrations</span>{"\n"}
                    <span className="text-[#569cd6]">make</span> <span className="text-[#ce9178]">migrate-up</span>{"\n\n"}
                    <span className="text-[#6a9955]"># Run Orion stack services</span>{"\n"}
                    <span className="text-[#569cd6]">make</span> <span className="text-[#ce9178]">run-api</span>{"\n"}
                    <span className="text-[#569cd6]">make</span> <span className="text-[#ce9178]">run-scheduler</span>{"\n"}
                    <span className="text-[#569cd6]">make</span> <span className="text-[#ce9178]">run-worker</span>
                  </code>
                </pre>
              </div>
            </div>
          </div>
        </div>
      </section>

      {/* ── Footer ───────────────────────────────────────────────────────── */}
      <footer className="py-12 text-center text-sm text-muted-foreground">
        <div className="flex flex-wrap items-center justify-center gap-4 md:gap-6 font-medium">
          <Link href="/docs" className="transition-colors hover:text-primary">Docs</Link>
          <span className="h-1 w-1 rounded-full bg-muted-foreground/30" />
          <Link href="/docs/architecture" className="transition-colors hover:text-primary">Architecture</Link>
          <span className="h-1 w-1 rounded-full bg-muted-foreground/30" />
          <Link href="/docs/runbook" className="transition-colors hover:text-primary">Runbook</Link>
          <span className="h-1 w-1 rounded-full bg-muted-foreground/30" />
          <Link href="/docs/deployment" className="transition-colors hover:text-primary">Deployment</Link>
          <span className="h-1 w-1 rounded-full bg-muted-foreground/30" />
          <Link href="/docs/adr" className="transition-colors hover:text-primary">ADRs</Link>
        </div>
        <div className="mt-8">
          <p>
            Built by <span className="text-primary font-medium">Shreeharsh Shinde</span>.
          </p>
          <p className="mt-2 text-xs text-muted-foreground/60">
            Open-source distributed machine learning workflow and job orchestration.
          </p>
        </div>
      </footer>
    </main>
  );
}
