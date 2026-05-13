import Link from "next/link";
import { ArrowUpRight, BookOpen, FileText } from "lucide-react";

import { Card, PageHeader } from "@/components/ui";

const docs = [
  ["Architecture", "System components, data flow, concurrency model, and failure modes.", "../docs/architecture/overview.md"],
  ["Runbook", "Operational commands and debugging procedures.", "../docs/RUNBOOK.md"],
  ["Deployment", "Docker, Kubernetes, Helm, Prometheus, Grafana, and rollout planning.", "../docs/DEPLOYMENT_PLAN.md"],
  ["Frontend Design", "The dashboard product design and implementation phases.", "../docs/frontend/DASHBOARD_DESIGN.md"],
  ["ADRs", "Queue design, leader election, CAS state transitions, and Kubernetes decisions.", "../docs/adr"]
];

export default function DocsPage() {
  return (
    <>
      <PageHeader
        title="Docs"
        description="A product docs hub for Orion architecture, runbooks, deployment notes, ADRs, and frontend planning."
      />

      <div className="grid gap-4 lg:grid-cols-2">
        {docs.map(([title, description, path]) => (
          <Card className="p-5 transition hover:-translate-y-0.5 hover:shadow-soft" key={title}>
            <div className="flex items-start gap-4">
              <div className="flex h-10 w-10 items-center justify-center rounded-md border border-primary/30 bg-primary/10 text-primary shadow-neon">
                {title === "Frontend Design" ? <BookOpen className="h-5 w-5" /> : <FileText className="h-5 w-5" />}
              </div>
              <div className="min-w-0">
                <h2 className="font-semibold">{title}</h2>
                <p className="mt-1 text-sm text-muted-foreground">{description}</p>
                <Link className="mt-4 inline-flex items-center gap-2 text-sm font-medium text-primary" href="#">
                  Wire MDX route for {path}
                  <ArrowUpRight className="h-4 w-4" />
                </Link>
              </div>
            </div>
          </Card>
        ))}
      </div>
    </>
  );
}
