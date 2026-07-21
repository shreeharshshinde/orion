# Phase 9: Helm & Production

> **Estimated Reading Time:** 15 minutes  
> **Status:** Completed  
> **Deliverables:** Multi-stage scratch Dockerfiles, complete Helm Chart, and production scaling configurations.

---

## 📋 Overview & Objectives

In Phase 9, we containerize and package the Orion architecture to transition it from a developer's local machine (`make run-*`) into a production-hardened Kubernetes deployment.

Our main objectives for this phase are:
1. **Containerization**: Create secure, extremely lightweight multi-stage Docker images (`~12-15MB`) using Go's static compilation.
2. **Orchestration & Packaging**: Write a modular Helm Chart to manage all configurations, deployments, and access controls under a single release.
3. **Autoscaling (HPA)**: Configure event-driven horizontal autoscaling of worker pods based on queue depth rather than lagging CPU metrics.
4. **High Availability (HA)**: Establish a hot-standby redundancy model for the scheduler via PostgreSQL advisory locks.
5. **Security Hardening**: Enforce non-root execution, read-only root filesystems, dropped capabilities, and robust secret separation.

---

## 🚀 Prerequisites

To deploy and verify this phase, you need:
- **Go 1.22+** installed locally
- **Docker** or a compatible container builder (e.g., Podman)
- **Helm v3** command-line tool installed
- **A Kubernetes cluster** running locally (such as **kind** or **minikube**) or in cloud staging/production
- **Prometheus Operator** (optional, for ServiceMonitor scraping validation)

---

## 📦 What Phase 9 Delivers

A single command, `helm install orion ./deploy/helm`, deploys the entire production stack:

```
Kubernetes Cluster (ml-platform namespace):
  │
  ├── Deployments:
  │     ├── orion-api         (3 replicas, stateless HTTP/gRPC API)
  │     ├── orion-scheduler   (3 replicas, active/standby via PG advisory lock)
  │     └── orion-worker      (initial=5, autoscales 2-50 based on queue depth)
  │
  ├── Services:
  │     ├── orion-api            (ClusterIP, ports 8080/9090/9091)
  │     ├── orion-scheduler-metrics
  │     └── orion-worker-metrics
  │
  ├── Auto-Scaling & Scheduling:
  │     ├── HPA: orion-worker     (Scales based on orion_queue_depth)
  │     ├── PDB: orion-api        (minAvailable: 2)
  │     └── PDB: orion-scheduler  (minAvailable: 1)
  │
  ├── Configuration & Security:
  │     ├── ServiceAccount: orion-worker (RBAC configured for creating job pods)
  │     ├── ConfigMap: orion-config      (non-sensitive env vars)
  │     └── Secret: orion-secrets        (DB/Redis credentials - or external)
  │
  └── Isolated Namespace:
        └── orion-jobs                   (For dynamic ML GPU pod execution)
```

---

## 🗺️ Guide Navigation

To navigate this comprehensive phase guide, please use the following sub-pages:

### 1. [Dockerfiles Build & Design](/docs/phases/9-helm-deployment/dockerfiles)
Deep dive into our multi-stage, static Go builds that target an empty `scratch` base image. Learns how we reduced the image size to under 15MB, secured the execution context with a custom UID, and implemented layer caching to speed up builds.

### 2. [Helm Chart Templates & Configuration](/docs/phases/9-helm-deployment/helm-chart)
Detailed walkthrough of all Helm templates, including the dynamic `values.yaml` setup, custom helper templates, zone-aware topology constraints, Prometheus ServiceMonitor scraping, and production RBAC rules.

### 3. [Implementation & Operational Runbook](/docs/phases/9-helm-deployment/implementation)
A step-by-step operational guide for building, upgrading, and testing the deployment in a local `kind` cluster. Includes instructions for validating scheduler failovers, observing HPA behavior under stress, and deploying zero-downtime rolling updates.

---

## 📐 The Three-Binary Deployment Model

Orion isolates its primary components into separate deployments to accommodate different scaling and reliability requirements:

| Component | Scaling Type | Default Replicas | Reason |
| :--- | :--- | :--- | :--- |
| **`orion-api`** | CPU Utilization | 3 replicas | Stateless HTTP/gRPC frontend. Needs high availability across zones. |
| **`orion-scheduler`** | Hot Standby | 3 replicas (1 active) | Database-backed leader election. Stands by to acquire PG advisory lock. |
| **`orion-worker`** | Queue Depth | 2-50 replicas | Scales dynamically based on queue pressure to optimize compute costs. |