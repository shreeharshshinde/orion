import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Job, Pipeline, QueueConfig, Worker } from "./api";
import { api } from "./client";

// ─── Jobs ────────────────────────────────────────────────────────────────────

export type JobFilter = { status?: string; queue?: string; type?: string };

export function useJobs(filters?: JobFilter) {
  const params = new URLSearchParams(
    Object.entries(filters ?? {}).filter(([, v]) => !!v) as [string, string][]
  ).toString();
  const path = "/jobs" + (params ? `?${params}` : "");
  return useQuery<Job[]>({
    queryKey: ["jobs", filters],
    queryFn: () => api.get<Job[]>(path),
    refetchInterval: 5000,
  });
}

export function useJob(id: string) {
  return useQuery<Job>({
    queryKey: ["job", id],
    queryFn: () => api.get<Job>(`/jobs/${id}`),
    refetchInterval: 3000,
    enabled: !!id,
  });
}

export function useJobExecutions(id: string) {
  return useQuery({
    queryKey: ["executions", id],
    queryFn: () => api.get(`/jobs/${id}/executions`),
    enabled: !!id,
  });
}

export function useCancelJob() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.post(`/jobs/${id}/cancel`),
    onSuccess: (_data, id) => {
      qc.invalidateQueries({ queryKey: ["job", id] });
      qc.invalidateQueries({ queryKey: ["jobs"] });
    },
  });
}

export function useReplayJob() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.post(`/jobs/${id}/replay`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["jobs"] }),
  });
}

export function useSubmitJob() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: unknown) => api.post<Job>("/jobs", body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["jobs"] }),
  });
}

// ─── Pipelines ───────────────────────────────────────────────────────────────

export function usePipelines() {
  return useQuery<Pipeline[]>({
    queryKey: ["pipelines"],
    queryFn: () => api.get<Pipeline[]>("/pipelines"),
    refetchInterval: 5000,
  });
}

export function usePipeline(id: string) {
  return useQuery<Pipeline>({
    queryKey: ["pipeline", id],
    queryFn: () => api.get<Pipeline>(`/pipelines/${id}`),
    refetchInterval: 3000,
    enabled: !!id,
  });
}

export function usePipelineJobs(id: string) {
  return useQuery<Job[]>({
    queryKey: ["pipeline-jobs", id],
    queryFn: () => api.get<Job[]>(`/pipelines/${id}/jobs`),
    refetchInterval: 3000,
    enabled: !!id,
  });
}

export function useCreatePipeline() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: unknown) => api.post<Pipeline>("/pipelines", body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["pipelines"] }),
  });
}

export function useCancelPipeline() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.post(`/pipelines/${id}/cancel`),
    onSuccess: (_data, id) => {
      qc.invalidateQueries({ queryKey: ["pipeline", id] });
      qc.invalidateQueries({ queryKey: ["pipelines"] });
    },
  });
}

// ─── Queues ──────────────────────────────────────────────────────────────────

export function useQueues() {
  return useQuery<QueueConfig[]>({
    queryKey: ["queues"],
    queryFn: () => api.get<QueueConfig[]>("/queues"),
    refetchInterval: 5000,
  });
}

export function useQueueStats(name: string) {
  return useQuery({
    queryKey: ["queue-stats", name],
    queryFn: () => api.get(`/queues/${name}/stats`),
    refetchInterval: 5000,
    enabled: !!name,
  });
}

export function useUpdateQueue() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ name, body }: { name: string; body: unknown }) =>
      api.put(`/queues/${name}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["queues"] }),
  });
}

// ─── Workers ─────────────────────────────────────────────────────────────────

export function useWorkers() {
  return useQuery<Worker[]>({
    queryKey: ["workers"],
    queryFn: () => api.get<Worker[]>("/workers"),
    refetchInterval: 5000,
  });
}

// ─── Health ──────────────────────────────────────────────────────────────────

export function useHealth() {
  return useQuery({
    queryKey: ["health"],
    queryFn: () => api.get("/readyz"),
    refetchInterval: 5000,
    retry: false,
  });
}
