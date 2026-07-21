import type { Job, Pipeline, QueueConfig, Worker } from "./api";

// Seeded historical datasets for charts
export interface DepthPoint {
  timestamp: string;
  high: number;
  default: number;
  low: number;
}

export interface DurationPoint {
  timestamp: string;
  p50: number; // median duration
  p90: number; // 90th percentile
  p99: number; // 99th percentile
}

const generateQueueDepthHistory = (): DepthPoint[] => {
  const points: DepthPoint[] = [];
  const now = Date.now();
  for (let i = 24; i >= 0; i--) {
    const t = new Date(now - i * 3600000); // Hourly intervals for the last 24h
    // Seeded random walk
    const hour = t.getHours();
    const multiplier = hour >= 9 && hour <= 18 ? 1.8 : 0.6; // Business hours surge
    points.push({
      timestamp: t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
      high: Math.max(0, Math.round((Math.sin(i * 0.5) * 5 + 8) * multiplier)),
      default: Math.max(0, Math.round((Math.cos(i * 0.3) * 12 + 20) * multiplier)),
      low: Math.max(0, Math.round((Math.sin(i * 0.2) * 18 + 25) * multiplier)),
    });
  }
  return points;
};

const generateDurationHistory = (): DurationPoint[] => {
  const points: DurationPoint[] = [];
  const now = Date.now();
  for (let i = 12; i >= 0; i--) {
    const t = new Date(now - i * 7200000); // 2-hour intervals
    points.push({
      timestamp: t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
      p50: Math.round(180 + Math.sin(i) * 30),
      p90: Math.round(420 + Math.cos(i * 0.8) * 80),
      p99: Math.round(920 + Math.sin(i * 1.5) * 150),
    });
  }
  return points;
};

export const DEMO_QUEUE_DEPTH_HISTORY = generateQueueDepthHistory();
export const DEMO_DURATION_HISTORY = generateDurationHistory();

export const DEMO_JOBS: Job[] = [
  {
    id: "job_01",
    name: "train-recommendation-ranker",
    type: "k8s_job",
    queue_name: "orion:queue:high",
    priority: 10,
    status: "running",
    attempt: 1,
    max_retries: 3,
    worker_id: "worker-a7f2",
    created_at: new Date(Date.now() - 42 * 60000).toISOString(),
    updated_at: new Date(Date.now() - 2 * 60000).toISOString(),
  },
  {
    id: "job_02",
    name: "preprocess-clickstream-batch",
    type: "inline",
    queue_name: "orion:queue:default",
    priority: 5,
    status: "completed",
    attempt: 1,
    max_retries: 3,
    worker_id: "worker-b19c",
    created_at: new Date(Date.now() - 90 * 60000).toISOString(),
    updated_at: new Date(Date.now() - 54 * 60000).toISOString(),
  },
  {
    id: "job_03",
    name: "evaluate-model-candidate",
    type: "inline",
    queue_name: "orion:queue:default",
    priority: 8,
    status: "retrying",
    attempt: 2,
    max_retries: 3,
    error_message: "handler timeout after deadline",
    created_at: new Date(Date.now() - 66 * 60000).toISOString(),
    updated_at: new Date(Date.now() - 9 * 60000).toISOString(),
  },
  {
    id: "job_04",
    name: "nightly-embedding-refresh",
    type: "k8s_job",
    queue_name: "orion:queue:low",
    priority: 3,
    status: "queued",
    attempt: 0,
    max_retries: 3,
    created_at: new Date(Date.now() - 18 * 60000).toISOString(),
    updated_at: new Date(Date.now() - 18 * 60000).toISOString(),
  }
];

export const DEMO_PIPELINES: Pipeline[] = [
  {
    id: "pipe_01",
    name: "train-and-evaluate",
    status: "running",
    dag_spec: {
      nodes: [
        { id: "preprocess", depends_on: [] },
        { id: "train", depends_on: ["preprocess"] },
        { id: "evaluate", depends_on: ["train"] }
      ],
      edges: [
        { source: "preprocess", target: "train" },
        { source: "train", target: "evaluate" }
      ]
    },
    created_at: new Date(Date.now() - 120 * 60000).toISOString(),
    updated_at: new Date(Date.now() - 5 * 60000).toISOString()
  },
  {
    id: "pipe_02",
    name: "daily-feature-materialization",
    status: "completed",
    dag_spec: {
      nodes: [
        { id: "extract", depends_on: [] },
        { id: "transform", depends_on: ["extract"] },
        { id: "publish", depends_on: ["transform"] }
      ],
      edges: [
        { source: "extract", target: "transform" },
        { source: "transform", target: "publish" }
      ]
    },
    created_at: new Date(Date.now() - 260 * 60000).toISOString(),
    updated_at: new Date(Date.now() - 170 * 60000).toISOString(),
    completed_at: new Date(Date.now() - 170 * 60000).toISOString()
  }
];

export const DEMO_QUEUES: QueueConfig[] = [
  {
    queue_name: "orion:queue:high",
    max_concurrent: 8,
    weight: 0.5,
    rate_per_sec: 40,
    burst: 16,
    enabled: true,
    depth: 7,
    rate_tokens_avail: 12.3,
    updated_at: new Date(Date.now() - 15 * 60000).toISOString()
  },
  {
    queue_name: "orion:queue:default",
    max_concurrent: 6,
    weight: 0.35,
    rate_per_sec: 25,
    burst: 10,
    enabled: true,
    depth: 18,
    rate_tokens_avail: 6.8,
    updated_at: new Date(Date.now() - 16 * 60000).toISOString()
  },
  {
    queue_name: "orion:queue:low",
    max_concurrent: 3,
    weight: 0.15,
    rate_per_sec: 10,
    burst: 4,
    enabled: true,
    depth: 31,
    rate_tokens_avail: 2.1,
    updated_at: new Date(Date.now() - 17 * 60000).toISOString()
  }
];

export const DEMO_WORKERS: Worker[] = [
  {
    id: "worker-a7f2",
    hostname: "orion-worker-0",
    queue_names: ["orion:queue:high", "orion:queue:default"],
    concurrency: 8,
    active_jobs: 5,
    status: "busy",
    last_heartbeat: new Date(Date.now() - 1 * 60000).toISOString(),
    registered_at: new Date(Date.now() - 320 * 60000).toISOString()
  },
  {
    id: "worker-b19c",
    hostname: "orion-worker-1",
    queue_names: ["orion:queue:default", "orion:queue:low"],
    concurrency: 6,
    active_jobs: 1,
    status: "idle",
    last_heartbeat: new Date(Date.now() - 1 * 60000).toISOString(),
    registered_at: new Date(Date.now() - 315 * 60000).toISOString()
  },
  {
    id: "worker-c41d",
    hostname: "orion-worker-2",
    queue_names: ["orion:queue:low"],
    concurrency: 4,
    active_jobs: 0,
    status: "idle",
    last_heartbeat: new Date(Date.now() - 2 * 60000).toISOString(),
    registered_at: new Date(Date.now() - 240 * 60000).toISOString()
  }
];
