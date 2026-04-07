# Step 14 — Local Infrastructure
### File: `docker-compose.yml`

---

## What does this file do?

Docker Compose starts all the external services Orion depends on with **one command**:

```bash
docker compose up -d
```

This gives you: PostgreSQL, Redis, Jaeger, Prometheus, and Grafana — all configured and talking to each other — in about 30 seconds.

---

## What is Docker and Docker Compose?

**Docker** packages a program and all its dependencies into a portable "container". The container runs the same way on any machine.

**Docker Compose** defines and runs multiple Docker containers together. Instead of running 5 separate `docker run` commands with long flags, you write them once in `docker-compose.yml` and run `docker compose up`.

---

## Service 1: PostgreSQL (port 5432)

```yaml
postgres:
  image: postgres:16-alpine
  environment:
    POSTGRES_USER: orion
    POSTGRES_PASSWORD: orion
    POSTGRES_DB: orion
  ports:
    - "5432:5432"
  volumes:
    - postgres_data:/var/lib/postgresql/data
```

**Port mapping** `"5432:5432"` means: map port 5432 on your laptop to port 5432 in the container. You connect from your Go code with `localhost:5432`.

**Volume** `postgres_data:/var/lib/postgresql/data` means: store PostgreSQL's data files in a named Docker volume. Without this, every `docker compose restart` wipes your database.

**Connect manually:**
```bash
docker compose exec postgres psql -U orion -d orion

# Useful commands inside psql:
\dt              # list tables
\di              # list indexes
SELECT * FROM jobs LIMIT 5;
```

---

## Service 2: Redis (port 6379)

```yaml
redis:
  image: redis:7-alpine
  command: redis-server --appendonly yes --maxmemory 256mb --maxmemory-policy allkeys-lru
  ports:
    - "6379:6379"
  volumes:
    - redis_data:/data
```

### Why `--appendonly yes` is critical

Redis has two persistence modes:

| Mode | What it does | What happens on restart |
|---|---|---|
| RDB (default) | Periodic snapshots | Data since last snapshot is LOST |
| AOF (`--appendonly yes`) | Write every operation to disk | Replays log → full recovery |

Without `--appendonly yes`: if Redis restarts (crash, update, `docker compose restart`), all queued jobs vanish from Redis. They're stuck in `scheduled` state in PostgreSQL but gone from the queue — they won't be delivered to workers until the scheduler's orphan sweep requeues them (up to 90s later).

With `--appendonly yes`: Redis replays the AOF log on startup and fully restores all streams and consumer groups.

**Connect manually:**
```bash
docker compose exec redis redis-cli

# Useful commands:
XLEN orion:queue:default       # how many messages in default queue
XINFO GROUPS orion:queue:default  # consumer group info
KEYS orion:*                   # all Orion keys
```

---

## Service 3: Jaeger (ports 16686, 4317)

```yaml
jaeger:
  image: jaegertracing/all-in-one:1.55
  environment:
    COLLECTOR_OTLP_ENABLED: "true"
  ports:
    - "16686:16686"  # Jaeger UI
    - "4317:4317"    # OTLP gRPC receiver
```

Jaeger receives distributed traces from your Go services (via OTLP gRPC on port 4317) and lets you explore them at `http://localhost:16686`.

**`all-in-one`** means the collector, query service, and UI are all in one container. Perfect for development. In production you'd run them separately.

**In your Go config (`ORION_OTLP_ENDPOINT`):** `http://localhost:4317`

**Open the UI:**
```
http://localhost:16686
```
- Search by service name: `orion-api`, `orion-scheduler`, `orion-worker`
- Search by trace ID (copy from a log line)
- See every span for one job's complete journey

---

## Service 4: Prometheus (port 9090)

```yaml
prometheus:
  image: prom/prometheus:v2.49.0
  volumes:
    - ./deploy/prometheus/prometheus.yml:/etc/prometheus/prometheus.yml
  ports:
    - "9090:9090"
```

Prometheus scrapes your services' `/metrics` endpoint every 15 seconds.

**You need to create `deploy/prometheus/prometheus.yml`:**

```yaml
# deploy/prometheus/prometheus.yml
global:
  scrape_interval: 15s

scrape_configs:
  - job_name: 'orion-api'
    static_configs:
      - targets: ['host.docker.internal:9091']

  - job_name: 'orion-scheduler'
    static_configs:
      - targets: ['host.docker.internal:9092']

  - job_name: 'orion-worker'
    static_configs:
      - targets: ['host.docker.internal:9093']
```

`host.docker.internal` resolves to your laptop's IP from inside Docker — needed because Prometheus runs in a container but your Go services run directly on your laptop.

**Open Prometheus:**
```
http://localhost:9090
```

Try this query:
```
rate(orion_jobs_completed_total[5m])
```
This shows: how many jobs complete per second, averaged over the last 5 minutes.

---

## Service 5: Grafana (port 3000)

```yaml
grafana:
  image: grafana/grafana:10.2.0
  environment:
    GF_SECURITY_ADMIN_PASSWORD: admin
  ports:
    - "3000:3000"
  volumes:
    - grafana_data:/var/lib/grafana
```

Grafana is the dashboard layer on top of Prometheus. It queries Prometheus and draws charts.

**Open:**
```
http://localhost:3000
Login: admin / admin
```

**Quick setup:**
1. Go to Connections → Data Sources → Add data source
2. Choose Prometheus
3. URL: `http://prometheus:9090` (Grafana and Prometheus are on the same Docker network, so use the service name)
4. Save & Test

Then create dashboards or import community dashboards (search Grafana.com for Prometheus dashboards).

---

## Docker networks — how services find each other

All services are on an implicit Docker Compose network. Inside that network, services find each other by name:
- `postgres` → resolves to the PostgreSQL container
- `redis` → resolves to the Redis container
- `prometheus` → resolves to the Prometheus container (used by Grafana)

Your Go services running on your laptop use `localhost:5432`, `localhost:6379`, etc. because Docker maps container ports to localhost.

---

## Essential Docker Compose commands

```bash
# Start everything in background (-d = detached)
docker compose up -d

# Start just one service
docker compose up -d postgres

# View logs (follow mode)
docker compose logs -f postgres
docker compose logs -f redis

# Check what's running
docker compose ps

# Stop everything (data is preserved)
docker compose down

# Stop everything AND delete all data (fresh start)
docker compose down -v

# Run a command inside a container
docker compose exec postgres psql -U orion -d orion
docker compose exec redis redis-cli

# Restart one service
docker compose restart jaeger
```

---

## File location

```
orion/
├── docker-compose.yml          ← you are here
└── deploy/
    └── prometheus/
        └── prometheus.yml      ← you need to create this
    └── grafana/
        ├── dashboards/         ← optional: pre-built dashboards
        └── datasources/        ← optional: auto-configure Prometheus
```