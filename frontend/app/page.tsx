import Link from "next/link";
import { ArrowRight, BookOpen, Boxes, Gauge, GitBranch, Server, Waypoints } from "lucide-react";

import { Badge, Button, Card, MetricCard } from "@/components/ui";
import { overview } from "@/lib/api";

const capabilities = [
  { title: "Jobs", icon: Gauge, text: "Submit, inspect, retry-aware tracking, and execution history." },
  { title: "Pipelines", icon: GitBranch, text: "DAG-driven ML workflows with node-to-job visibility." },
  { title: "Queues", icon: Waypoints, text: "Live scheduling controls for weights, rates, bursts, and depth." },
  { title: "Workers", icon: Server, text: "Execution capacity, heartbeats, active jobs, and availability." }
];

export default function HomePage() {
  return (
    <main className="min-h-screen">
      <section className="mx-auto grid min-h-[88vh] max-w-7xl gap-10 px-6 py-10 lg:grid-cols-[0.95fr_1.05fr] lg:items-center">
        <div>
          <Badge tone="aqua">Distributed ML job orchestration</Badge>
          <h1 className="mt-5 font-display text-5xl font-semibold tracking-normal text-foreground sm:text-6xl">
            Orion
          </h1>
          <p className="mt-5 max-w-xl text-lg leading-8 text-muted-foreground">
            A polished control plane for scheduling, executing, and observing ML workloads on
            Kubernetes with queues, workers, pipelines, metrics, and traces in one place.
          </p>
          <div className="mt-8 flex flex-wrap gap-3">
            <Button asChild>
              <Link href="/dashboard">
                Open Dashboard <ArrowRight className="h-4 w-4" />
              </Link>
            </Button>
            <Button asChild variant="outline">
              <Link href="/docs">
                <BookOpen className="h-4 w-4" />
                Read Docs
              </Link>
            </Button>
          </div>
          <div className="mt-8 grid max-w-2xl grid-cols-2 gap-3 sm:grid-cols-4">
            <MetricCard label="Workers" value={overview.activeWorkers} detail="active" icon={<Server className="h-5 w-5" />} />
            <MetricCard label="Running" value={overview.runningJobs} detail="jobs" icon={<Gauge className="h-5 w-5" />} />
            <MetricCard label="Queued" value={overview.queuedJobs} detail="waiting" icon={<Waypoints className="h-5 w-5" />} />
            <MetricCard label="Pipelines" value={overview.runningPipelines} detail="running" icon={<GitBranch className="h-5 w-5" />} />
          </div>
        </div>

        <Card className="overflow-hidden p-0 shadow-neon">
          <div className="border-b bg-primary/10 px-5 py-4">
            <div className="flex items-center gap-3">
              <div className="flex h-10 w-10 items-center justify-center rounded-md border border-primary/40 bg-primary/20 text-primary shadow-neon">
                <Boxes className="h-5 w-5" />
              </div>
              <div>
                <p className="font-display font-semibold">Live operations preview</p>
                <p className="text-sm text-muted-foreground">Neon console theme · backend-ready</p>
              </div>
            </div>
          </div>
          <div className="grid gap-4 p-5">
            {capabilities.map((item) => {
              const Icon = item.icon;
              return (
                <div className="flex gap-4 rounded-lg border bg-muted/40 p-4" key={item.title}>
                  <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-md border border-primary/30 bg-primary/10 text-primary">
                    <Icon className="h-5 w-5" />
                  </div>
                  <div>
                    <h2 className="font-display font-semibold">{item.title}</h2>
                    <p className="mt-1 text-sm text-muted-foreground">{item.text}</p>
                  </div>
                </div>
              );
            })}
          </div>
        </Card>
      </section>
    </main>
  );
}
