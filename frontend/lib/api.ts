export type JobStatus =
  | "queued"
  | "scheduled"
  | "running"
  | "completed"
  | "failed"
  | "retrying"
  | "dead"
  | "cancelled";

export type PipelineStatus = "pending" | "running" | "completed" | "failed" | "cancelled";
export type WorkerStatus = "idle" | "busy" | "draining" | "offline";

export type Job = {
  id: string;
  name: string;
  type: "inline" | "k8s_job";
  queue_name: string;
  priority: number;
  status: JobStatus;
  attempt: number;
  max_retries: number;
  worker_id?: string;
  error_message?: string;
  created_at: string;
  updated_at: string;
};

export type Pipeline = {
  id: string;
  name: string;
  status: PipelineStatus;
  dag_spec: {
    nodes: Array<{ id: string; job_id?: string }>;
    edges: Array<{ source: string; target: string }>;
  };
  created_at: string;
  updated_at: string;
  completed_at?: string;
};

export type QueueConfig = {
  queue_name: string;
  max_concurrent: number;
  weight: number;
  rate_per_sec: number;
  burst: number;
  enabled: boolean;
  updated_at: string;
  depth?: number;
  rate_tokens_avail?: number;
};

export type Worker = {
  id: string;
  hostname: string;
  queue_names: string[];
  concurrency: number;
  active_jobs: number;
  status: WorkerStatus;
  last_heartbeat: string;
  registered_at: string;
};

const now = new Date();
const minutesAgo = (minutes: number) => new Date(now.getTime() - minutes * 60000).toISOString();

export const jobs: Job[] = [
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
    created_at: minutesAgo(42),
    updated_at: minutesAgo(2)
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
    created_at: minutesAgo(90),
    updated_at: minutesAgo(54)
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
    created_at: minutesAgo(66),
    updated_at: minutesAgo(9)
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
    created_at: minutesAgo(18),
    updated_at: minutesAgo(18)
  }
];

export const pipelines: Pipeline[] = [
  {
    id: "pipe_01",
    name: "train-and-evaluate",
    status: "running",
    dag_spec: {
      nodes: [{ id: "preprocess" }, { id: "train" }, { id: "evaluate" }],
      edges: [
        { source: "preprocess", target: "train" },
        { source: "train", target: "evaluate" }
      ]
    },
    created_at: minutesAgo(120),
    updated_at: minutesAgo(5)
  },
  {
    id: "pipe_02",
    name: "daily-feature-materialization",
    status: "completed",
    dag_spec: {
      nodes: [{ id: "extract" }, { id: "transform" }, { id: "publish" }],
      edges: [
        { source: "extract", target: "transform" },
        { source: "transform", target: "publish" }
      ]
    },
    created_at: minutesAgo(260),
    updated_at: minutesAgo(170),
    completed_at: minutesAgo(170)
  }
];

export const queues: QueueConfig[] = [
  {
    queue_name: "orion:queue:high",
    max_concurrent: 8,
    weight: 0.5,
    rate_per_sec: 40,
    burst: 16,
    enabled: true,
    depth: 7,
    rate_tokens_avail: 12.3,
    updated_at: minutesAgo(15)
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
    updated_at: minutesAgo(16)
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
    updated_at: minutesAgo(17)
  }
];

export const workers: Worker[] = [
  {
    id: "worker-a7f2",
    hostname: "orion-worker-0",
    queue_names: ["orion:queue:high", "orion:queue:default"],
    concurrency: 8,
    active_jobs: 5,
    status: "busy",
    last_heartbeat: minutesAgo(1),
    registered_at: minutesAgo(320)
  },
  {
    id: "worker-b19c",
    hostname: "orion-worker-1",
    queue_names: ["orion:queue:default", "orion:queue:low"],
    concurrency: 6,
    active_jobs: 1,
    status: "idle",
    last_heartbeat: minutesAgo(1),
    registered_at: minutesAgo(315)
  },
  {
    id: "worker-c41d",
    hostname: "orion-worker-2",
    queue_names: ["orion:queue:low"],
    concurrency: 4,
    active_jobs: 0,
    status: "idle",
    last_heartbeat: minutesAgo(2),
    registered_at: minutesAgo(240)
  }
];

export const overview = {
  apiReady: true,
  activeWorkers: workers.length,
  runningJobs: jobs.filter((job) => job.status === "running").length,
  queuedJobs: jobs.filter((job) => job.status === "queued").length,
  failedJobs: jobs.filter((job) => job.status === "failed" || job.status === "dead").length,
  runningPipelines: pipelines.filter((pipeline) => pipeline.status === "running").length,
  totalConcurrency: workers.reduce((sum, worker) => sum + worker.concurrency, 0),
  activeSlots: workers.reduce((sum, worker) => sum + worker.active_jobs, 0)
};
