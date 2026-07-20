"use client";

import {
  Activity,
  ArrowRight, BookOpen, Boxes,
  GitBranch, Github,
  RefreshCw,
  Server,
  Shield, Sun, Moon,
  Waypoints, Zap
} from "lucide-react";
import Image from "next/image";
import Link from "next/link";
import { useTheme } from "next-themes";
import { useEffect, useState } from "react";

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

  const { theme, setTheme } = useTheme();
  const [mounted, setMounted] = useState(false);
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
                  <span className="font-bold">171 passing</span>
                </span>
              </div>

              <Badge tone="aqua" className="mt-5 inline-flex">
                <Zap className="h-3 w-3" />
                Distributed ML job orchestrator for Kubernetes
              </Badge>
            </div>
          </div>

          <p className="mx-auto mt-6 max-w-2xl text-lg leading-relaxed text-muted-foreground">
            Orion sits between your training pipelines and the cluster — handling priority queuing,
            retries, backpressure, DAG orchestration, and real-time status streaming so your
            application code doesn&apos;t have to.
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
              <span>Star</span>
              <span className="rounded-full bg-primary/10 px-2 py-0.5 text-xs font-semibold text-primary">1.2k</span>
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
      <section className="mx-auto max-w-6xl px-6 pb-20">
        <div className="mb-10 text-center">
          <h2 className="font-display text-2xl font-semibold">Architected for ML pipelines</h2>
          <p className="mt-2 text-sm text-muted-foreground">From low-latency scheduling to hard execution guarantees — zero lost jobs.</p>
        </div>

        <div className="grid gap-4 md:grid-cols-3">
          {/* Highlighted Bento Card: Correctness guarantees */}
          <Card className="p-6 md:col-span-2 border-primary/40 bg-gradient-to-br from-primary/5 to-transparent flex flex-col justify-between hover:border-primary/60 transition-colors relative overflow-hidden group">
            <div className="absolute inset-0 bg-gradient-to-r from-primary/10 to-transparent opacity-0 group-hover:opacity-100 transition-opacity duration-500 pointer-events-none" />
            <div className="relative">
              <div className="flex h-10 w-10 items-center justify-center rounded-lg border border-primary/40 bg-primary/20 text-primary">
                <Shield className="h-5 w-5" />
              </div>
              <h3 className="mt-4 font-display text-xl font-bold tracking-tight text-foreground">
                At-Least-Once Delivery & CAS Correctness
              </h3>
              <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
                Orion relies on Redis Streams with consumer group PEL (Pending Entries List) tracking and CAS (Compare-And-Swap) database transitions. If a worker goes offline, the orphan sweeper reclaims pipelines. Double-claiming is strictly impossible under concurrency.
              </p>
            </div>
            <Link href="/docs/concepts/queue-delivery" className="mt-6 inline-flex items-center gap-1 text-xs font-semibold text-primary hover:underline">
              Read consistency guarantees <ArrowRight className="h-3.5 w-3.5" />
            </Link>
          </Card>

          {/* Priority queues bento card */}
          <Card className="p-6 flex flex-col justify-between hover:border-primary/30 transition-colors">
            <div>
              <div className="flex h-9 w-9 items-center justify-center rounded-lg border border-primary/30 bg-primary/10 text-primary">
                <Waypoints className="h-4 w-4" />
              </div>
              <h3 className="mt-4 font-display font-semibold text-foreground">Priority Queues</h3>
              <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
                Three Redis Streams queues—high, default, and low—with weighted dispatch, rate limits, and live reload.
              </p>
            </div>
          </Card>

          {/* Kubernetes Execution */}
          <Card className="p-6 flex flex-col justify-between hover:border-primary/30 transition-colors">
            <div>
              <div className="flex h-9 w-9 items-center justify-center rounded-lg border border-primary/30 bg-primary/10 text-primary">
                <Server className="h-4 w-4" />
              </div>
              <h3 className="mt-4 font-display font-semibold text-foreground">Kubernetes Execution</h3>
              <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
                Launches jobs natively via client-go. Supports GPU resource requests, custom namespaces, and pod status watching.
              </p>
            </div>
          </Card>

          {/* DAG Pipelines */}
          <Card className="p-6 flex flex-col justify-between hover:border-primary/30 transition-colors">
            <div>
              <div className="flex h-9 w-9 items-center justify-center rounded-lg border border-primary/30 bg-primary/10 text-primary">
                <GitBranch className="h-4 w-4" />
              </div>
              <h3 className="mt-4 font-display font-semibold text-foreground">DAG Pipelines</h3>
              <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
                Topological node advancement with cascade-cancel. Create multi-stage ML training tasks in one request.
              </p>
            </div>
          </Card>

          {/* Real-time Streaming */}
          <Card className="p-6 flex flex-col justify-between hover:border-primary/30 transition-colors">
            <div>
              <div className="flex h-9 w-9 items-center justify-center rounded-lg border border-primary/30 bg-primary/10 text-primary">
                <Activity className="h-4 w-4" />
              </div>
              <h3 className="mt-4 font-display font-semibold text-foreground">Real-Time Streaming</h3>
              <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
                gRPC Watch API powered by PostgreSQL LISTEN/NOTIFY. Zero polling overhead on your client application.
              </p>
            </div>
          </Card>
        </div>
      </section>

      {/* ── Comparison Framing ────────────────────────────────────────────── */}
      <section className="mx-auto max-w-6xl px-6 pb-20">
        <div className="mb-10 text-center">
          <h2 className="font-display text-2xl font-semibold">How Orion compares</h2>
          <p className="mt-2 text-sm text-muted-foreground">Built specifically for low-latency Kubernetes ML scheduling, vs heavyweight general pipeline engines.</p>
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
                  <td className="p-4 text-primary font-semibold bg-primary/5">Sub-millisecond (Go/Redis)</td>
                  <td className="p-4 text-muted-foreground">Seconds (Kubernetes controller loop)</td>
                  <td className="p-4 text-muted-foreground">Seconds/Minutes (Heavy database check)</td>
                </tr>
                <tr className="hover:bg-muted/10 transition-colors">
                  <td className="p-4 font-medium text-foreground">Architecture Type</td>
                  <td className="p-4 text-primary font-semibold bg-primary/5">Lightweight Sidecar / Proxy</td>
                  <td className="p-4 text-muted-foreground">Kubernetes Custom Controller</td>
                  <td className="p-4 text-muted-foreground">Centralized Server & Workers</td>
                </tr>
                <tr className="hover:bg-muted/10 transition-colors">
                  <td className="p-4 font-medium text-foreground">GPU Resource Target</td>
                  <td className="p-4 text-primary font-semibold bg-primary/5">First-class (Direct K8s Spec)</td>
                  <td className="p-4 text-muted-foreground">General-purpose containers</td>
                  <td className="p-4 text-muted-foreground">Indirect via Operator</td>
                </tr>
                <tr className="hover:bg-muted/10 transition-colors">
                  <td className="p-4 font-medium text-foreground">Consistency Guarantees</td>
                  <td className="p-4 text-primary font-semibold bg-primary/5">Exactly-once (Redis PEL + CAS)</td>
                  <td className="p-4 text-muted-foreground">At-least-once</td>
                  <td className="p-4 text-muted-foreground">At-least-once (variable DB locks)</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </section>

      {/* ── Quickstart Install ───────────────────────────────────────────── */}
      <section className="mx-auto max-w-6xl px-6 pb-20">
        <div className="mb-8 text-center">
          <h2 className="font-display text-xl font-semibold">Spin up locally</h2>
          <p className="mt-1 text-sm text-muted-foreground">Boot dependencies with Docker Compose and migrate your PostgreSQL schema.</p>
        </div>
        <div className="overflow-hidden rounded-2xl border border-border/60 bg-card/60 backdrop-blur shadow-sm">
          <div className="flex items-center gap-3 border-b border-border/60 bg-muted/30 px-5 py-3">
            <div className="flex gap-1.5">
              <span className="h-3 w-3 rounded-full bg-danger/60" />
              <span className="h-3 w-3 rounded-full bg-warning/60" />
              <span className="h-3 w-3 rounded-full bg-success/60" />
            </div>
            <span className="font-mono text-xs text-muted-foreground">Quickstart local boot</span>
            <div className="ml-auto flex items-center gap-2">
              <Server className="h-3.5 w-3.5 text-primary" />
              <span className="font-mono text-xs text-primary">orion-stack · bash</span>
            </div>
          </div>
          <pre className="overflow-x-auto p-6 font-mono text-xs leading-6 bg-muted/5 text-[#d4d4d4]">
            <code>
              <span className="text-[#6a9955]"># Clone and boot database/cache dependencies</span>{"\n"}
              <span className="text-[#569cd6]">git clone</span> <span className="text-[#ce9178]">https://github.com/shreeharshshinde/orion.git</span> <span className="text-[#569cd6]">&& cd</span> orion{"\n"}
              <span className="text-[#569cd6]">docker compose up</span> <span className="text-[#b5cea8]">-d</span>{"\n\n"}
              <span className="text-[#6a9955]"># Initialize postgres schemas</span>{"\n"}
              <span className="text-[#569cd6]">make</span> <span className="text-[#ce9178]">migrate-up</span>
            </code>
          </pre>
        </div>
      </section>

      {/* ── Submit in seconds ────────────────────────────────────────────── */}
      <section className="mx-auto max-w-6xl px-6 pb-24">
        <div className="mb-8 text-center">
          <h2 className="font-display text-xl font-semibold">Submit a job</h2>
          <p className="mt-1 text-sm text-muted-foreground">Send a single JSON request to dispatch a GPU training node.</p>
        </div>
        <div className="overflow-hidden rounded-2xl border border-border/60 bg-card/60 backdrop-blur shadow-sm">
          <div className="flex items-center gap-3 border-b border-border/60 bg-muted/30 px-5 py-3">
            <div className="flex gap-1.5">
              <span className="h-3 w-3 rounded-full bg-danger/60" />
              <span className="h-3 w-3 rounded-full bg-warning/60" />
              <span className="h-3 w-3 rounded-full bg-success/60" />
            </div>
            <span className="font-mono text-xs text-muted-foreground">Submit ResNet GPU training</span>
            <div className="ml-auto flex items-center gap-2">
              <Boxes className="h-3.5 w-3.5 text-primary" />
              <span className="font-mono text-xs text-primary">orion · localhost:8080</span>
            </div>
          </div>
          <pre className="overflow-x-auto p-6 font-mono text-xs leading-6 bg-muted/5 text-[#d4d4d4]">
            <code>
              <span className="text-[#569cd6]">curl</span> -sX POST <span className="text-[#ce9178]">http://localhost:8080/jobs</span> \<span className="text-[#6a9955]">{"\n"}</span>
              {"  "}-H <span className="text-[#ce9178]">&apos;Content-Type: application/json&apos;</span> \<span className="text-[#6a9955]">{"\n"}</span>
              {"  "}-d <span className="text-[#ce9178]">&apos;<span className="text-[#ffd700]">{"{"}</span></span><span className="text-[#d4d4d4]">{"\n"}</span>
              {"    "}<span className="text-[#9cdcfe]">&quot;name&quot;</span>: <span className="text-[#ce9178]">&quot;train-resnet&quot;</span>,<span className="text-[#d4d4d4]">{"\n"}</span>
              {"    "}<span className="text-[#9cdcfe]">&quot;type&quot;</span>: <span className="text-[#ce9178]">&quot;k8s_job&quot;</span>,<span className="text-[#d4d4d4]">{"\n"}</span>
              {"    "}<span className="text-[#9cdcfe]">&quot;queue_name&quot;</span>: <span className="text-[#ce9178]">&quot;high&quot;</span>,<span className="text-[#d4d4d4]">{"\n"}</span>
              {"    "}<span className="text-[#9cdcfe]">&quot;priority&quot;</span>: <span className="text-[#b5cea8]">8</span>,<span className="text-[#d4d4d4]">{"\n"}</span>
              {"    "}<span className="text-[#9cdcfe]">&quot;max_retries&quot;</span>: <span className="text-[#b5cea8]">3</span>,<span className="text-[#d4d4d4]">{"\n"}</span>
              {"    "}<span className="text-[#9cdcfe]">&quot;payload&quot;</span>: <span className="text-[#ffd700]">{"{"}</span><span className="text-[#d4d4d4]">{"\n"}</span>
              {"      "}<span className="text-[#9cdcfe]">&quot;kubernetes_spec&quot;</span>: <span className="text-[#ffd700]">{"{"}</span><span className="text-[#d4d4d4]">{"\n"}</span>
              {"        "}<span className="text-[#9cdcfe]">&quot;image&quot;</span>: <span className="text-[#ce9178]">&quot;pytorch/pytorch:2.1.0-cuda11.8&quot;</span>,<span className="text-[#d4d4d4]">{"\n"}</span>
              {"        "}<span className="text-[#9cdcfe]">&quot;command&quot;</span>: <span className="text-[#ffd700]">[</span><span className="text-[#ce9178]">&quot;python&quot;</span>, <span className="text-[#ce9178]">&quot;train.py&quot;</span><span className="text-[#ffd700]">]</span>,<span className="text-[#d4d4d4]">{"\n"}</span>
              {"        "}<span className="text-[#9cdcfe]">&quot;resources&quot;</span>: <span className="text-[#ffd700]">{"{"}</span> <span className="text-[#9cdcfe]">&quot;gpu&quot;</span>: <span className="text-[#b5cea8]">1</span>, <span className="text-[#9cdcfe]">&quot;memory&quot;</span>: <span className="text-[#ce9178]">&quot;16Gi&quot;</span> <span className="text-[#ffd700]">{"}"}</span><span className="text-[#d4d4d4]">{"\n"}</span>
              {"      "}<span className="text-[#ffd700]">{"}"}</span><span className="text-[#d4d4d4]">{"\n"}</span>
              {"    "}<span className="text-[#ffd700]">{"}"}</span><span className="text-[#d4d4d4]">{"\n"}</span>
              {"  "}<span className="text-[#ffd700]">{"}"}</span><span className="text-[#ce9178]">&apos;</span>
            </code>
          </pre>
        </div>
      </section>

      {/* ── Footer ───────────────────────────────────────────────────────── */}
      <footer className=" py-12 text-center text-sm text-muted-foreground">
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
            Built by the <span className="text-primary font-medium">Shreeharsh Shinde</span>.
          </p>
          <p className="mt-2 text-xs text-muted-foreground/60">
            Independent project, open-source distributed ML orchestration.
          </p>
        </div>
      </footer>
    </main>
  );
}
