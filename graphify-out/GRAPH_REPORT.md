# Orion Knowledge Graph Report

Generated: graphify analysis of 62 files (~175K words)

## Metadata
- **Total Nodes**: 389
- **Total Edges**: 1053  
- **Communities Detected**: 24
- **Extraction Status**: AST (370 nodes) + Semantic (27 cached, 35 extracted)

---

## God Nodes (Highest Connectivity)

Key components with most connections in the knowledge graph:

1. **New()** — test helper function with high test coverage
2. **newTestStore()** — database test utility
3. **testExecutor()** — executor test helper
4. **makeK8sJob()** — Kubernetes job spec builder
5. **testLogger()** — logging test utility

These are testing utilities that interconnect multiple components during test execution.

## Core Architecture Nodes

1. **API Server** — job submission endpoint
2. **Scheduler** — job scheduling and queue management
3. **Worker Pool** — job execution management
4. **PostgreSQL Store** — persistent job and pipeline state
5. **Redis Queue** — distributed job queue

## Surprising Connections

Unexpected cross-component relationships and shortcuts:

- Test utilities deeply integrated with core domain models
- Leader election mechanism (PG Advisory Locks) used across phases
- Executor interface abstraction enables multiple execution backends
- DAG advancement algorithm coordinates multi-phase pipelines
- Retry and backoff policies span from API to worker execution

---

## Community Structure

**24 detected communities** organize components by functional domain:

### Core Pipeline Processing (Community 0)
API Server, Scheduler, Worker Pool, Redis Queue, PostgreSQL Store

### Execution Backends (Community 1)
InlineExecutor, KubernetesExecutor, Executor Interface, Handler Registry

### Reliability Patterns (Community 2)
Leader Election, State Transitions (CAS), Idempotency Keys, Orphan Reclamation, Heartbeat Mechanism

### Infrastructure Stack (Community 3)
PostgreSQL Store, Redis Streams, k8s.io/client-go, Observability (Prometheus/Jaeger)

### Development Phases (Community 4)
Phase 1-5 progression: Foundation → Store → Inline → K8s → DAG Orchestration

### Domain Models (Community 5)
Job Entity, Worker Entity, Pipeline Entity, Type Definitions

### Testing Utilities (Communities 6-24)
Various test fixtures, mocks, and helper functions for all components

---

## Key Architectural Patterns

### 1. **Layered Architecture**
```
API ↓ (submit jobs) ↓ Scheduler ↓ (dispatch) ↓ Worker Pool ↓ (execute) ↓ Executor
```

### 2. **Pluggable Executors**
```
Executor Interface
├── InlineExecutor (local testing)
└── KubernetesExecutor (distributed production)
```

### 3. **Queue-Driven Coordination**
```
API → PostgreSQL Store ← Scheduler → Redis Queue → Worker Pool
       ↑ (status updates) ↑                              ↓
       └──────────────────────────────────────────────────┘
```

### 4. **Reliability Mechanisms**
- **Leader Election**: PostgreSQL advisory locks prevent duplicate scheduling
- **State Machine**: Compare-and-swap (CAS) ensures atomic transitions
- **Orphan Reclamation**: Scheduler sweeps failed workers every 30s
- **Heartbeat**: Workers send periodic signals to prevent premature reclamation
- **Idempotency**: All API operations are safely retryable

### 5. **Pipeline DAG Orchestration** (Phase 5)
- Job status machine tracks execution progress
- DAG advancement algorithm evaluates dependencies
- Pipeline domain type orchestrates multi-step workflows

---

## Phase Progression

1. **Phase 1 (Foundation)**: Core interfaces and skeleton
2. **Phase 2 (PostgreSQL Store)**: Persistent state management
3. **Phase 3 (Inline Executor)**: Local job execution with handler registry
4. **Phase 4 (Kubernetes Executor)**: Distributed execution on K8s clusters
5. **Phase 5 (DAG Orchestration)**: Multi-job pipeline coordination

---

## Artifacts Generated

- **graph.json** — GraphRAG-ready knowledge graph (queryable)
- **graph.html** — Interactive visualization (open in browser)
- **.graphify_extract.json** — Complete extraction with all nodes/edges
- **.graphify_analysis.json** — Community detection and god nodes
- **.graphify_detect.json** — File type and corpus statistics

