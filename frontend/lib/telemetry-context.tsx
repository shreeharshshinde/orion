'use client';

import { createContext, useContext, useState, useEffect, ReactNode } from 'react';
import { useJobs, useWorkers, useQueues, usePipelines, useHealth } from '@/lib/hooks';
import type { Job, Worker, QueueConfig, Pipeline } from '@/lib/api';
import { DEMO_JOBS, DEMO_WORKERS, DEMO_QUEUES, DEMO_PIPELINES } from '@/lib/demo-data';

export type Environment = 'local' | 'sandbox' | 'production';

interface TelemetryContextType {
  demoMode: boolean;
  setDemoMode: (val: boolean) => void;
  apiConnected: boolean;
  healthLoading: boolean;
  environment: Environment;

  // Active Datasets
  jobs: Job[];
  workers: Worker[];
  queues: QueueConfig[];
  pipelines: Pipeline[];

  // Loading States
  jobsLoading: boolean;
  workersLoading: boolean;
  queuesLoading: boolean;
  pipelinesLoading: boolean;
  isInitialLoading: boolean;

  // Derived Metrics
  activeJobsCount: number;
  runningJobsCount: number;
  queuedJobsCount: number;
  failedJobsCount: number;
  activeWorkersCount: number;
  totalSlots: number;
  usedSlots: number;
}

const TelemetryContext = createContext<TelemetryContextType | undefined>(undefined);

export function TelemetryProvider({ children }: { children: ReactNode }) {
  // Live API status queries
  const { isSuccess: apiOk, isLoading: healthLoading, isError: healthError } = useHealth();
  const { data: apiJobs, isLoading: jobsLoading } = useJobs();
  const { data: apiWorkers, isLoading: workersLoading } = useWorkers();
  const { data: apiQueues, isLoading: queuesLoading } = useQueues();
  const { data: apiPipelines, isLoading: pipelinesLoading } = usePipelines();

  const [demoMode, setDemoMode] = useState(false);

  // Auto-enable demo mode if the API is offline
  useEffect(() => {
    if (healthError) {
      setDemoMode(true);
    }
  }, [healthError]);

  const apiConnected = apiOk && !healthError;

  // Determine active environment
  const environment: Environment = demoMode ? 'sandbox' : 'local';

  // Select source datasets based on demo mode
  const jobs = demoMode ? DEMO_JOBS : (apiJobs ?? []);
  const workers = demoMode ? DEMO_WORKERS : (apiWorkers ?? []);
  const queues = demoMode ? DEMO_QUEUES : (apiQueues ?? []);
  const pipelines = demoMode ? DEMO_PIPELINES : (apiPipelines ?? []);

  const isInitialLoading = !demoMode && (healthLoading || jobsLoading || workersLoading || queuesLoading || pipelinesLoading);

  // Compute derived metrics dynamically (NO static hardcoded duplication)
  const activeJobsCount = jobs.filter(j => j.status === 'running' || j.status === 'queued').length;
  const runningJobsCount = jobs.filter(j => j.status === 'running').length;
  const queuedJobsCount = jobs.filter(j => j.status === 'queued').length;
  const failedJobsCount = jobs.filter(j => j.status === 'failed' || j.status === 'dead').length;
  const activeWorkersCount = workers.filter(w => w.status !== 'offline').length;
  const totalSlots = workers.reduce((sum, w) => sum + w.concurrency, 0);
  const usedSlots = workers.reduce((sum, w) => sum + w.active_jobs, 0);

  return (
    <TelemetryContext.Provider
      value={{
        demoMode,
        setDemoMode,
        apiConnected,
        healthLoading,
        environment,
        jobs,
        workers,
        queues,
        pipelines,
        jobsLoading,
        workersLoading,
        queuesLoading,
        pipelinesLoading,
        isInitialLoading,
        activeJobsCount,
        runningJobsCount,
        queuedJobsCount,
        failedJobsCount,
        activeWorkersCount,
        totalSlots,
        usedSlots,
      }}
    >
      {children}
    </TelemetryContext.Provider>
  );
}

export function useTelemetry() {
  const context = useContext(TelemetryContext);
  if (!context) {
    throw new Error('useTelemetry must be used within a TelemetryProvider');
  }
  return context;
}
