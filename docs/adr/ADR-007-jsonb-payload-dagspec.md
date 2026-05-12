# ADR-007: JSONB for Job Payload and Pipeline DAG Spec

**Date:** 2024-02-15
**Status:** Accepted
**Authors:** Orion Platform Team

---

## Context

Orion jobs carry a `payload` field whose shape varies by job type:

- `inline` jobs carry `handler_name` and an arbitrary `args` map
- `k8s_job` jobs carry a `kubernetes_spec` with image, command, resources, env vars

Pipeline DAG specs carry a `dag_spec` field containing nodes and edges, where each node embeds a `job_template` (which is itself a `JobPayload`).

Three storage strategies were considered:

1. **Typed columns**: add a column for every possible payload field (`handler_name TEXT`, `image TEXT`, `cpu TEXT`, `gpu INT`, `command TEXT[]`, `args JSONB`, ...)
2. **Separate tables**: `inline_job_payloads`, `k8s_job_payloads`, joined to `jobs` on `job_id`
3. **JSONB column**: store the entire payload as a single `JSONB` column

## Decision

**Store `payload` and `dag_spec` as `JSONB` columns.** The schema uses a single opaque column; the Go type system enforces structure at the application layer.

```sql
-- jobs table
payload  JSONB NOT NULL DEFAULT '{}'

-- pipelines table
dag_spec JSONB NOT NULL DEFAULT '{}'
```

Go structs are marshalled to JSONB on write and unmarshalled on read:

```go
// Write
payloadJSON, err := json.Marshal(job.Payload)
db.pool.Exec(ctx, q, ..., payloadJSON)

// Read
var payloadJSON []byte
row.Scan(..., &payloadJSON)
json.Unmarshal(payloadJSON, &job.Payload)
```

Evaluated options:

| Option | Schema migrations on change | Query filtering | Type safety | Complexity |
|---|---|---|---|---|
| Typed columns | Required for every new field | Native SQL index | DB-enforced | High |
| Separate tables | Required for new job type | JOIN needed | DB-enforced | Very high |
| **JSONB** | **Never** | **GIN index if needed** | **Go-enforced** | **Low** |

The decisive factor: ML job payloads evolve constantly. Adding a new handler argument (`dropout_rate`, `learning_rate_schedule`, `mixed_precision`) would require a migration for typed columns. With JSONB, the Go struct simply adds a new field — zero migration, zero downtime.

## Consequences

**Positive:**
- Adding new payload fields (new handler args, new Kubernetes spec options) requires zero database migrations
- The `dag_spec` JSONB column absorbs the full pipeline DAG including new node fields without schema changes
- Arbitrary `args` maps (`map[string]any`) are stored natively without a separate EAV (Entity-Attribute-Value) table
- PostgreSQL's JSONB supports GIN indexes if filtering by payload field becomes necessary (`WHERE payload->>'handler_name' = 'train_model'`)

**Negative:**
- The database cannot enforce payload schema — a malformed payload is only caught at Go unmarshal time, not at INSERT time. Mitigation: the API handler validates `handler_name` presence for inline jobs and `kubernetes_spec` presence for k8s jobs before inserting.
- JSONB values are not directly visible in `psql` without `->` operators. Slightly harder to inspect manually.
- No foreign key constraints inside JSONB — `handler_name` referencing a registered handler cannot be enforced at the DB layer.

## Implementation Notes

Located in `internal/store/postgres/db.go` (job payload) and `internal/store/postgres/pipeline.go` (dag_spec). The `json.Marshal`/`json.Unmarshal` calls are the only coupling between Go struct layout and JSONB storage. If a field is renamed in Go, existing JSONB rows retain the old field name — handle with `json:"old_name"` tags or a data migration script.

PostgreSQL's JSONB (binary JSON) is used rather than the plain `JSON` type because JSONB supports indexing, is stored in a parsed format (faster reads), and supports operators like `@>` (contains) for filtering.