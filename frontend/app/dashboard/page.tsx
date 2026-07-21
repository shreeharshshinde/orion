"use client";

import { useState, useEffect } from "react";
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
import { useJobs, usePipelines, useQueues, useWorkers, useHealth } from "@/lib/hooks";
import { formatRelativeTime } from "@/lib/utils";
import { OrbitTopology } from "@/components/dashboard/orbit-topology";
import { PipelineDagView } from "@/components/dashboard/pipeline-dag-view";
import { QueueDepthChart } from "@/components/dashboard/queue-depth-chart";
import { JobDurationChart } from "@/components/dashboard/job-duration-chart";
import {
  DEMO_JOBS, DEMO_PIPELINES, DEMO_QUEUES, DEMO_WORKERS
} from "@/lib/demo-data";

export default function DashboardPage() {
  const { data: healthData, isError: healthError } = useHealth();
  const { data: apiJobs, isLoading: jobsLoading } = useJobs();
  const { data: apiWorkers, isLoading: workersLoading } = useWorkers();
  const { data: apiQueues, isLoading: queuesLoading } = useQueues();
  const { data: apiPipelines, isLoading: pipelinesLoading } = usePipelines();

  const apiConnected = !!healthData && !healthError;
  const [demoMode, setDemoMode] = useState(false);

  // Auto-enable demo mode if the API is offline
  useEffect(() => {
    if (!apiConnected && healthError) {
      setDemoMode(true);
    }
  }, [apiConnected, healthError]);

  // Select source datasets based on demo mode
  const jobs = demoMode ? DEMO_JOBS : (apiJobs ?? []);
  const workers = demoMode ? DEMO_WORKERS : (apiWorkers ?? []);
  const queues = demoMode ? DEMO_QUEUES : (apiQueues ?? []);
  const pipelines = demoMode ? DEMO_PIPELINES : (apiPipelines ?? []);

  const incidents = jobs.filter(j => j.status === "failed" || j.status === "dead" || j.status === "retrying");
  const totalSlots = workers.reduce((s, w) => s + w.concurrency, 0);
  const usedSlots  = workers.reduce((s, w) => s + w.active_jobs, 0);
  const runningJobs  = jobs.filter(j => j.status === "running").length;
  const queuedJobs   = jobs.filter(j => j.status === "queued").length;
  const failedJobs   = jobs.filter(j => j.status === "failed" || j.status === "dead").length;
  const activeWorkers = workers.filter(w => w.status !== "offline").length;

  return (
    <>
      <PageHeader
        title="Orion Cockpit"
        description="Real-time telemetry and operational control panel."
        badge={
          <div className="flex items-center gap-2">
            <EnvironmentBadge env={demoMode ? "staging" : "production"} />
            <ConnectionIndicator connected={apiConnected} />
            {demoMode && (
              <span className="flex items-center gap-1 text-[11px] font-mono text-cyan-400 border border-cyan-400/30 bg-cyan-400/10 px-2.5 py-0.5 rounded-full animate-pulse">
                <Sparkles className="h-3 w-3" />
                demo mode active
              </span>
            )}
          </div>
        }
        action={
          <Button 
            variant="outline" 
            size="sm" 
            onClick={() => setDemoMode(!demoMode)}
            className="flex items-center gap-1.5"
          >
            <RefreshCw className="h-3.5 w-3.5" />
            {demoMode ? "Switch to Live API" : "Simulate Sandbox"}
          </Button>
        }
      />

      {/* Incident Alert Strip */}
      {incidents.length > 0 && (
        <div className="mb-5 flex items-start gap-3 rounded-xl border border-rose-500/30 bg-rose-500/10 px-4 py-3 text-sm transition-all duration-300">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-rose-400 animate-bounce" />
          <div>
            <span className="font-semibold text-rose-400 font-ui">{incidents.length} system incident{incidents.length > 1 ? "s" : ""} requiring action: </span>
            <span className="text-slate-300 font-mono text-xs">{incidents.map(j => j.name).join(", ")}</span>
          </div>
        </div>
      )}

      {/* Core Cockpit Topology & Metric Cards Row */}
      <section className="grid gap-5 lg:grid-cols-[1fr_1.1fr] mb-5">
        <OrbitTopology 
          apiConnected={apiConnected || demoMode} 
          schedulerActive={apiConnected || demoMode} 
          workerCount={activeWorkers} 
        />
        
        <div className="grid gap-3 sm:grid-cols-2">
          <MetricCard 
            label="Worker Capacity"    
            value={`${usedSlots}/${totalSlots}`} 
            detail={`${activeWorkers} active nodes`} 
            icon={<Server className="h-5 w-5" />}            
            tone="aqua"    
          />
          <MetricCard 
            label="Active Executions"       
            value={runningJobs}  
            detail="scheduled and executing"                      
            icon={<BriefcaseBusiness className="h-5 w-5" />}  
            tone="success" 
          />
          <MetricCard 
            label="Queued Backlog"        
            value={queuedJobs}   
            detail="awaiting execution"                   
            icon={<Waypoints className="h-5 w-5" />}          
            tone="warning" 
          />
          <MetricCard 
            label="Terminal Failures" 
            value={failedJobs}   
            detail="unhandled terminations"                        
            icon={<Skull className="h-5 w-5" />}              
            tone={failedJobs > 0 ? "danger" : "success"} 
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
                    <tr key={job.id} className="hover:bg-muted/10 cursor-pointer transition-colors duration-100">
                      <Td className="py-2.5"><StatusBadge status={job.status} kind="job" /></Td>
                      <Td className="py-2.5">
                        <p className="font-semibold text-text-bright font-ui text-sm">{job.name}</p>
                        {job.error_message ? (
                          <p className="mt-0.5 text-xs text-rose-400 font-mono truncate max-w-xs">{job.error_message}</p>
                        ) : (
                          <p className="mt-0.5 text-[10px] text-muted-foreground font-mono truncate">{job.id}</p>
                        )}
                      </Td>
                      <Td className="py-2.5">
                        <span className="rounded border border-border/40 bg-muted/20 px-2 py-0.5 font-mono text-[11px] text-cyan-400/90">
                          {job.queue_name.replace("orion:queue:", "")}
                        </span>
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
              <div key={s} className="rounded-xl border border-border/45 bg-panel-solid/50 p-3.5 text-center hover:border-primary/40 hover:bg-panel-raised cursor-pointer transition-all duration-120">
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
