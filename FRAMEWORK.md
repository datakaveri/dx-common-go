# dx-common-go — Framework Architecture

The internal application framework for every CDPG Go microservice. Philosophy:
**Spring-Boot intent (the framework owns infrastructure; services own business
logic), Go-idiomatic execution** — composition, generics, functional options,
explicit APIs; no inheritance, no reflection magic, no ambient annotation-style
behavior. A service imports the packages it needs and writes only domain code.

---

## 1. Package map (by concern)

### Configuration & bootstrap
| Package | Purpose |
|---|---|
| `config` | env-driven, type-safe config loader (`LoadService[T]`) |
| `httpserver` | graceful HTTP server with sane timeouts |
| `openapi` | embedded spec loader + request-validation middleware + Swagger UI |

### Persistence — Postgres (`database/postgres/*`)
| Package | Purpose |
|---|---|
| `client` | pool lifecycle (`NewPool`), `Config`, tracers |
| `dao` | generic `BaseDAO[T]` + fluent `Finder` (the low-level engine) |
| `repository` | embeddable `Base[R]` facade — CRUD + DSL, tx-propagation-aware |
| `query` | SQL builder + condition/spec DSL (14 operators, joins, paging) |
| `transaction` | `InTransaction`, `InRetryableTransaction`, advisory locks, ctx propagation |
| `migrate` | golang-migrate wrapper (embed.FS, per-service history table) |
| `sqlcx` | tx-aware `DBTX` provider for sqlc-generated queries |

**Three-legged persistence standard (normative):** `repository.Base` for CRUD +
dynamic filter/sort/page; **sqlc** for static complex JOIN/aggregate reads; raw
`$N` SQL only for dynamic-WHERE + JSONB/PostGIS/window functions. No ORM.

### Persistence — Elasticsearch (`database/elasticsearch/*`)
| Package | Purpose |
|---|---|
| `client` | transport, config, health, the `Do`/`DoNDJSON` request seam |
| `query` | pure request DSL (bool/term/range/geo/agg/highlight/KNN) |
| `repository` | `Repo[T]`, search execution, document CRUD, scroll/PIT |
| `mapping` | `MappingBuilder`/`AutoMap` + alias/reindex/migrate/ILM lifecycle |
| `indexing` | bulk engine + reindex + generic `Source→bulk` Syncer + Worker |

### Caching (`cache`, `database/redis`)
`cache` owns the `Cache` contract + `GetOrLoad[T]` (singleflight stampede
protection); `database/redis` is the production implementation (`NewCache`) plus
the low-level client.

### Messaging (`messaging/*`)
| Package | Purpose |
|---|---|
| `rabbitmq` | reconnecting client, confirmed `ReliablePublisher`, `ConsumerRunner` + DLQ |
| `outbox` | Postgres transactional outbox (`PGStore` + `Dispatcher`) |

### Auth & identity (`auth/*`, `transport/headers`, `crypto/envelope`, `mtls`, `trust`)
| Package | Purpose |
|---|---|
| `auth` | `DxUser` + context helpers |
| `auth/jwt` | Keycloak JWKS validation |
| `auth/resolver` | X-Subject-* identity headers → `DxUser` middleware |
| `auth/fga` | OpenFGA REST client (`Check`, tuple writes) |
| `auth/appid` | M2M app-credential gRPC + Keycloak token source |
| `auth/authorization` | role/scope helpers |
| `transport/headers` | signed `X-Subject-*` identity headers |
| `crypto/envelope`, `mtls`, `trust` | federated-deployment (SADx) crypto/trust |

### Cross-cutting
| Package | Purpose |
|---|---|
| `middleware` | RequestID, Logger, CORS, Compression, RateLimiter, tracing, `StandardStack` |
| `resilience` | retry + circuit breaker + HTTP/gRPC wrappers (§4) |
| `observability` | OpenTelemetry SDK lifecycle (`Init`) |
| `metrics` | Prometheus handler |
| `health` | liveness/readiness aggregator + pgx/redis/rabbitmq/ES checkers |
| `errors` | `DxError` taxonomy + URN mapping + HTTP status |
| `validation` | struct validation helpers |
| `response` / `request` / `pagination` | envelope writer, request builder, page info |
| `auditing` | user-activity audit record + publisher + opt-in middleware |
| `scheduler` | in-process job runner (interval, jitter, singleton advisory-lock) |
| `notify/email` | email dispatch via RMQ |
| `storage/s3` | S3/MinIO client + STS |
| `model` | shared DTOs |

### Tooling
| Package | Purpose |
|---|---|
| `cmd/dx` | CLI: `dx new migration`, `dx sqlc init` |
| `dxtest/containers` | testcontainers (Postgres/Redis/ES/Rabbit) with DSN fallback |

---

## 2. Module dependency rules

- **Leaf utilities** (`errors`, `config`, `resilience`, `metrics`) import only stdlib
  (+ their driver). Everything may import them.
- **No cross-store imports**: `database/postgres/*` and `database/elasticsearch/*` never
  import each other.
- **Within a store**, the layering is one-directional: `query`/`client` are leaves →
  `indexing`/`mapping`/`dao` → `repository`. No layer reaches "up".
- **Instrument at the driver seam**, never via a framework abstraction (pgx `Tracer`,
  ES `Transport`, redis `InstrumentTracing`) — one place, covers every query path.
- Leaf packages may import the OpenTelemetry **API** only (no SDK in libraries).

---

## 3. Naming conventions (bind new APIs)

- `New…` constructs; functional `With…` options configure (never positional config structs
  for optional knobs).
- `Get`/`FindByID` = by-id lookup, NotFound on miss. `Find*` = condition queries.
  `Search*` = ES query DSL. Cache miss = `(zero, false, nil)`, never an error.
- Every non-2xx upstream maps to the shared `errors` taxonomy so handlers get the right
  HTTP status for free.
- A service package named `middleware/`, `logger/`, `httpclient/` is a smell unless it's a
  thin adapter — cross-cutting concerns come from here.

---

## 4. Key decision tables

**Persistence** — see §1 three-legged standard.

**Scheduling:** in-process `scheduler` for sub-minute/stateful loops (outbox drain, cache
refresh, token rotation); **K8s CronJob** for infrequent wall-clock batch. No cron-expression
engine in the framework.

**Resilience (`resilience`):** retry only idempotent operations by default; attach a circuit
breaker to any outbound dependency to fail fast when it is down. One `Policy` type drives
`Retry`, `NewHTTPClient`, and the gRPC interceptor — don't hand-roll backoff loops.

**Distributed tracing:** `observability.Init` owns the SDK; every signal is wired at its
driver seam and is a no-op until `Init` runs — enable them unconditionally.

| Signal | Seam | How to enable |
|---|---|---|
| HTTP in | `otelhttp` middleware | `middleware.Standard(log, t, middleware.WithTracing())` |
| Postgres (DSL/sqlc/raw) | pgx `MultiTracer` + `otelpgx` | `client.NewPool(cfg, client.WithTracers(...))` |
| Elasticsearch | `Transport` wrap (`otelhttp`) | `client.Config.EnableTracing = true` |
| Redis | `redisotel.InstrumentTracing` | `redis.Config.EnableTracing = true` |
| RabbitMQ | W3C traceparent on message headers | automatic — `ReliablePublisher` injects, `ConsumerRunner` extracts |

AMQP has no upstream OTel instrumentation, so the framework carries a minimal producer/consumer
span + traceparent propagation (`messaging/rabbitmq/otel.go`); no config knob, no cost when
tracing is off.

---

## 5. Adopting the framework in a new service

```go
cfg, _ := config.LoadService[Config]("SVC")
pool, _ := postgres.NewPool(cfg.Postgres)                 // client
dxmigrate.Run(...)                                        // migrate
repo := &ItemRepo{repository.New[itemRow](pool, ...)}     // repository.Base
es, _ := esclient.New(cfg.Elastic)                        // elasticsearch
pub, _ := rabbitmq.NewReliablePublisher(cfg.RabbitMQ)     // messaging
hh := health.NewHandler(); hh.Register("db", health.NewPgxPoolChecker(pool))
r := chi.NewRouter(); r.Use(middleware.StandardStack(...)) // middleware
// auth via auth/resolver; audit via auditing.Middleware; serve via httpserver.New
```

Adoption rule: before writing any infrastructure in a service, check here; **>~30 lines of
generic infra in a service is a PR to `dx-common-go` instead.**

The full, compiling reference wiring (config → observability → migrations → pool+tracers →
repository → outbox+scheduler → `middleware.Standard(WithTracing())` → health → serve) lives in
[`examples/minimal-service`](examples/minimal-service) — a separate module CI builds so the
template can't rot. Copy-and-rename it to start a service.

---

## 6. Testing

- Unit tests use injectable seams (clocks, transports, jitter/sleep) — deterministic, no real
  sleeps or servers (see `resilience`, `scheduler`).
- Integration tests use `dxtest/containers` — a throwaway Postgres/Redis/ES/Rabbit, or bind
  `DX_TEST_PG_DSN`/`ES_TEST_ADDR`; they **skip** (never fail) when Docker is absent, so plain
  `go test ./...` stays green everywhere.
- Every public package ships doc comments + tests; new stores/modules add a package README
  (see `database/elasticsearch/README.md`, `resilience/README.md`).

---

## 7. Deliberately not built
ORM · JOIN/CTE/window query DSL (use sqlc) · hiding sqlc behind adapters · repository
interceptor/hook chains · cron-expression engine · distributed leader election (advisory locks
suffice) · hot config reload (K8s rollout is the reload) · secrets management (external-secrets →
env → `config`).
