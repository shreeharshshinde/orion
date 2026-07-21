"use client";

import { useRouter } from "next/navigation";
import Link from "next/link";
import { toast } from "sonner";
import {
  Activity, AlertTriangle, BriefcaseBusiness,
  GitBranch, Server, Waypoints, TrendingUp, Skull, Sparkles, RefreshCw
} from "lucide-react";

import {
  Card, CardHeader, CardTitle, CardBody, MetricCard, PageHeader,
  ProgressBar, SectionHeader, Table, Td, Th, Button,
  EnvironmentBadge, SkeletonTableRows, SkeletonChart, SkeletonMetricCard, SkeletonDAG
} from "@/components/ui";
import { StatusBadge, ConnectionIndicator } from "@/components/status-badge";
import { useTelemetry } from "@/lib/telemetry-context";
import { formatRelativeTime } from "@/lib/utils";
import { WorkerHeatmap } from "@/components/dashboard/worker-heatmap";
import { PipelineDagView } from "@/components/dashboard/pipeline-dag-view";
import { QueueDepthChart } from "@/components/dashboard/queue-depth-chart";
import { JobDurationChart } from "@/components/dashboard/job-duration-chart";

export default function DashboardPage() {
  const router = useRouter();
  const {
    demoMode,
    setDemoMode,
    apiConnected,
    environment,
    jobs,
    workers,
    pipelines,
    jobsLoading,
    pipelinesLoading,
    runningJobsCount,
    queuedJobsCount,
    failedJobsCount,
    activeWorkersCount,
    totalSlots,
    usedSlots,
  } = useTelemetry();

  const incidents = jobs.filter(j => j.status === "failed" || j.status === "dead" || j.status === "retrying");

  return (
    <>
      <PageHeader
        title="Orion Cockpit"
        description="Real-time telemetry and operational control panel."
        badge={
          <div className="flex items-center gap-2">
            <EnvironmentBadge env={environment} />
            <ConnectionIndicator connected={apiConnected} />
            {demoMode && (
              <span className="inline-flex items-center gap-1 px-2.5 py-0.5 rounded-full border border-cyan-400/30 bg-cyan-400/5 text-[10px] font-semibold font-mono uppercase tracking-wider text-cyan-400 animate-pulse">
                <Sparkles className="h-3 w-3 shrink-0" />
                demo active
              </span>
            )}
          </div>
        }
        action={
          <Button 
            variant="outline" 
            size="sm" 
            onClick={() => {
              const targetDemo = !demoMode;
              setDemoMode(targetDemo);
              if (!targetDemo && !apiConnected) {
                toast.warning("API unreachable. Showing offline status. Ensure orion-api is running on port :8080.");
              } else if (targetDemo) {
                toast.info("Switched to simulated Sandbox environment.");
              } else {
                toast.success("Successfully connected to live Orion API.");
              }
            }}
            className="flex items-center gap-1.5"
          >
            <RefreshCw className="h-3.5 w-3.5" />
            {demoMode ? "Switch to Live API" : "Simulate Sandbox"}
          </Button>
        }
      />

      {/* High-visibility Sandbox/Demo Warning Banner */}
      {demoMode && (
        <div className="mb-5 flex items-center justify-between gap-3 rounded-lg border border-cyan-500/20 bg-cyan-500/5 px-4 py-3 text-xs backdrop-blur-sm shadow-[0_0_15px_rgba(34,211,238,0.05)]">
          <div className="flex items-center gap-2">
            <Sparkles className="h-4 w-4 text-cyan-400 animate-pulse shrink-0" />
            <div>
              <span className="font-semibold text-cyan-400 font-mono uppercase tracking-wider">Sandbox Simulation Active</span>
              <p className="text-slate-400 mt-0.5">Showing synthetic telemetry. Live deployments and mutations are simulated.</p>
            </div>
          </div>
          <Button 
            size="sm" 
            variant="outline" 
            onClick={() => {
              setDemoMode(false);
              if (!apiConnected) {
                toast.warning("API unreachable. Showing offline status.");
              } else {
                toast.success("Connected to live Orion API.");
              }
            }}
            className="border-cyan-400/30 text-cyan-400 hover:bg-cyan-400/10 cursor-pointer text-[10px] font-semibold tracking-wider uppercase px-2.5 py-1 rounded-full shrink-0"
          >
            Exit Sandbox
          </Button>
        </div>
      )}

      {/* Incident Alert Strip */}
      {incidents.length > 0 && (
        <div className="mb-6 flex items-center justify-between gap-3 rounded-lg border border-rose-500/20 bg-rose-500/5 px-4 py-2.5 text-xs transition-all duration-300 backdrop-blur-sm">
          <div className="flex items-center gap-2">
            <AlertTriangle className="h-3.5 w-3.5 text-rose-400 animate-pulse shrink-0" />
            <span className="text-rose-400 font-mono uppercase tracking-wider font-semibold">incidents active:</span>
            <span className="text-slate-300 font-mono truncate max-w-lg">{incidents.map(j => `${j.name} (${j.status})`).join(", ")}</span>
          </div>
        </div>
      )}

      {/* Core Cockpit Topology & Metric Cards Row */}
      <section className="grid gap-5 lg:grid-cols-[1fr_1.1fr] mb-5">
        <WorkerHeatmap workers={workers} />
        
        <div className="grid gap-3 sm:grid-cols-2">
          <MetricCard 
            label="Worker Capacity"    
            value={`${usedSlots}/${totalSlots}`} 
            detail={`${activeWorkersCount} active nodes`} 
            icon={<Server className="h-5 w-5" />}            
            tone="aqua"    
          />
          <MetricCard 
            label="Active Executions"       
            value={runningJobsCount}  
            detail="scheduled and executing"                      
            icon={<BriefcaseBusiness className="h-5 w-5" />}  
            tone="success" 
          />
          <MetricCard 
            label="Queued Backlog"        
            value={queuedJobsCount}   
            detail="awaiting execution"                   
            icon={<Waypoints className="h-5 w-5" />}          
            tone="warning" 
          />
          <MetricCard 
            label="Terminal Failures" 
            value={failedJobsCount}   
            detail="unhandled terminations"                        
            icon={<Skull className="h-5 w-5" />}              
            tone={failedJobsCount > 0 ? "danger" : "success"} 
          />
        </div>
      </section>

      {/* Telemetry Charts Row */}
      <section className="grid gap-5 md:grid-cols-2 mb-5">
        <QueueDepthChart />
        <JobDurationChart />
      </section>

      {/* Tables & Graph Visualizer Row */}
      <section className="grid gap-5 xl:grid-cols-[1.2fr_0.8fr] mb-5">
        {/* Recent Jobs Table Card */}
        <Card className="flex flex-col">
          <CardHeader>
            <div className="flex items-center gap-2">
              <TrendingUp className="h-4 w-4 text-cyan-400" />
              <CardTitle>Recent Job Telemetry</CardTitle>
            </div>
            <span className="text-xs text-muted-foreground font-mono">{jobs.length} total jobs monitored</span>
          </CardHeader>
          <CardBody className="p-0 flex-1">
            {jobsLoading && !demoMode ? (
              <div className="p-4"><SkeletonTableRows rows={5} columns={5} /></div>
            ) : jobs.length === 0 ? (
              <div className="py-12 text-center text-sm text-muted-foreground">No jobs yet</div>
            ) : (
              <Table>
                <thead>
                  <tr>
                    <Th>Status</Th>
                    <Th>Identifier / Name</Th>
                    <Th>Queue</Th>
                    <Th>Retries</Th>
                    <Th>Last Updated</Th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border/20">
                  {jobs.slice(0, 6).map((job) => (
                    <tr 
                      key={job.id} 
                      onClick={() => router.push(`/dashboard/jobs/${job.id}`)}
                      className="hover:bg-muted/10 cursor-pointer transition-colors duration-100 group"
                    >
                      <Td className="py-2.5"><StatusBadge status={job.status} kind="job" /></Td>
                      <Td className="py-2.5">
                        <p className="font-semibold text-text-bright font-ui text-sm group-hover:text-primary transition-colors">{job.name}</p>
                        {job.error_message ? (
                          <p className="mt-0.5 text-xs text-rose-400 font-mono truncate max-w-xs">{job.error_message}</p>
                        ) : (
                          <p className="mt-0.5 text-[10px] text-muted-foreground font-mono truncate">{job.id}</p>
                        )}
                      </Td>
                      <Td className="py-2.5" onClick={(e) => e.stopPropagation()}>
                        <Link 
                          href="/dashboard/queues" 
                          className="rounded border border-border/40 bg-muted/20 px-2 py-0.5 font-mono text-[11px] text-cyan-400/90 hover:border-cyan-400/50 hover:bg-cyan-500/10 transition-colors cursor-pointer"
                        >
                          {job.queue_name.replace("orion:queue:", "")}
                        </Link>
                      </Td>
                      <Td className="tabular-nums text-muted-foreground font-mono text-xs py-2.5">{job.attempt} / {job.max_retries}</Td>
                      <Td className="text-muted-foreground text-[11px] font-mono py-2.5">{formatRelativeTime(job.updated_at)}</Td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </CardBody>
        </Card>

        {/* DAG / Orchestrator View Card */}
        <Card className="flex flex-col">
          <CardHeader>
            <div className="flex items-center gap-2">
              <GitBranch className="h-4 w-4 text-violet-400" />
              <CardTitle>Pipeline Orchestrator</CardTitle>
            </div>
            <span className="text-xs text-muted-foreground font-mono">{pipelines.length} active DAGs</span>
          </CardHeader>
          <CardBody className="p-4 space-y-4 flex-1">
            {pipelinesLoading && !demoMode ? (
              <SkeletonDAG />
            ) : pipelines.length === 0 ? (
              <div className="py-12 text-center text-sm text-muted-foreground">No pipelines active</div>
            ) : (
              pipelines.slice(0, 2).map((p) => (
                <PipelineDagView key={p.id} pipeline={p} />
              ))
            )}
          </CardBody>
        </Card>
      </section>

      {/* Job status distribution */}
      <section className="mb-5">
        <SectionHeader title="Job Status Distribution" />
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4 lg:grid-cols-8">
          {(["queued","scheduled","running","completed","retrying","failed","dead","cancelled"] as const).map(s => {
            const count = jobs.filter(j => j.status === s).length;
            return (
              <div 
                key={s} 
                onClick={() => router.push(`/dashboard/jobs?status=${s}`)}
                className="rounded-xl border border-border/45 bg-panel-solid/50 p-3.5 text-center hover:border-primary/40 hover:bg-panel-raised cursor-pointer transition-all duration-120 hover:scale-[1.02] active:scale-[0.98]"
              >
                <p className="text-2xl font-bold font-mono text-text-bright mb-1.5">{count}</p>
                <StatusBadge status={s} kind="job" size="sm" />
              </div>
            );
          })}
        </div>
      </section>
    </>
  );
}
