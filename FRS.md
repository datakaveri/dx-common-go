# Functional Requirements Specification
# dx-common-go — Shared Platform Library

**Version:** 1.0
**Status:** Active
**Type:** Shared Library (not a deployable service)

---

## 1. Purpose and Scope

`dx-common-go` is a shared Go library that provides standardised, reusable infrastructure components for all Go-based services on the CDPG data exchange platform. It is imported as a dependency — it has no runtime process of its own.

**In scope:** Authentication, authorization, HTTP server, middleware, database clients, messaging, storage, error handling, response formatting, configuration loading, and OpenAPI tooling.

**Out of scope:** Business logic, domain models, data persistence schemas specific to any one service.

---

## 2. Functional Requirements

---

### FR-01: JWT Authentication

**Package:** `auth/jwt`

| ID | Requirement |
|----|-------------|
| FR-01.1 | The library MUST validate JSON Web Tokens issued by Keycloak using the RS256 algorithm. |
| FR-01.2 | Token validation MUST fetch the public key from a configurable JWKS endpoint. |
| FR-01.3 | JWKS keys MUST be cached with a configurable refresh interval (default: 5 minutes) to avoid per-request network calls. |
| FR-01.4 | The validator MUST verify the `iss` (issuer), `aud` (audience), and `exp` (expiry) claims. |
| FR-01.5 | A configurable leeway (in seconds) MUST be supported to tolerate minor clock skew between services. |
| FR-01.6 | The library MUST provide a Chi HTTP middleware that extracts the Bearer token from the `Authorization` header, validates it, and injects parsed claims into the request context. |
| FR-01.7 | If the token is missing, malformed, expired, or has invalid claims, the middleware MUST return `401 Unauthorized` with a standardised error body. |
| FR-01.8 | The validator MUST expose a method to retrieve the authenticated user from context for use in handler functions. |

---

### FR-02: Role-Based Access Control

**Package:** `auth/authorization`

| ID | Requirement |
|----|-------------|
| FR-02.1 | The library MUST support role enforcement at the route level via Chi middleware. |
| FR-02.2 | The following platform roles MUST be defined as constants: `RoleProvider`, `RoleConsumer`, `RoleCosAdmin`, `RoleOrgAdmin`. |
| FR-02.3 | A middleware constructor (`RequireRoles`) MUST accept one or more roles and return `403 Forbidden` if the authenticated user holds none of the specified roles. |
| FR-02.4 | Role values MUST be sourced from the `realm_access.roles` array in the Keycloak JWT. |
| FR-02.5 | The library MUST support scope-based access checks in addition to role checks. |

---

### FR-03: User Identity Model

**Package:** `auth`

| ID | Requirement |
|----|-------------|
| FR-03.1 | The library MUST define a `User` struct with at minimum: `Sub` (Keycloak subject), `Email`, `Name`, `Roles []string`, `OrgUnitPath`. |
| FR-03.2 | A `UserFromContext` function MUST be provided to retrieve the authenticated user from a `context.Context`. |
| FR-03.3 | `UserFromContext` MUST return an error if no authenticated user exists in the context. |

---

### FR-04: HTTP Server

**Package:** `httpserver`

| ID | Requirement |
|----|-------------|
| FR-04.1 | The library MUST provide a production-ready HTTP server wrapping the Chi router. |
| FR-04.2 | The server MUST support configurable timeouts: `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, and `ShutdownTimeout`. |
| FR-04.3 | The server MUST implement graceful shutdown: on receiving `SIGTERM` or `SIGINT`, it MUST stop accepting new connections and wait up to `ShutdownTimeout` for active requests to complete. |
| FR-04.4 | TLS configuration MUST be supported via optional cert/key file paths. |

---

### FR-05: HTTP Middleware

**Package:** `middleware`

| ID | Requirement |
|----|-------------|
| FR-05.1 | **Request ID:** MUST generate and inject a unique UUID into each request as both an internal context value and an `X-Request-ID` response header. |
| FR-05.2 | **Structured Logging:** MUST log each request with: method, path, status code, latency (ms), and request ID. |
| FR-05.3 | **Panic Recovery:** MUST catch panics in handlers, log the stack trace, and return `500 Internal Server Error` without crashing the server process. |
| FR-05.4 | **CORS:** MUST support configurable allowed origins, with `Access-Control-Allow-Origin`, `Access-Control-Allow-Methods`, and `Access-Control-Allow-Headers`. |
| FR-05.5 | **Request Timeout:** MUST cancel the request context and return `408 Request Timeout` if a handler exceeds the configured duration. |
| FR-05.6 | **Audit:** MUST provide a middleware that emits audit events (user, action, resource, timestamp) for compliance trail. |

---

### FR-06: PostgreSQL Database Client

**Package:** `database/postgres`

| ID | Requirement |
|----|-------------|
| FR-06.1 | The library MUST provide a connection pool backed by `jackc/pgx/v5` with configurable `MaxConns` and `MinConns`. |
| FR-06.2 | The pool MUST validate connectivity on creation (ping check) and return an error if the database is unreachable. |
| FR-06.3 | The library MUST provide a `WithTx` function that wraps a callback in a database transaction, automatically committing on `nil` return and rolling back on error. |
| FR-06.4 | The library MUST provide a query builder (`database/postgres/query`) for constructing safe, parameterised queries with dynamic WHERE clauses. |
| FR-06.5 | The library MUST expose base DAO interfaces (`database/postgres/dao`) defining standard CRUD operation contracts. |

---

### FR-07: Redis Client

**Package:** `database/redis`

| ID | Requirement |
|----|-------------|
| FR-07.1 | The library MUST provide a Redis client wrapper using `go-redis/v9`. |
| FR-07.2 | The client MUST validate connectivity on creation via `PING`. |
| FR-07.3 | Configurable fields MUST include: address, password, database index, and pool size. |

---

### FR-08: RabbitMQ Messaging

**Package:** `messaging/rabbitmq`

| ID | Requirement |
|----|-------------|
| FR-08.1 | The library MUST provide a RabbitMQ client using `rabbitmq/amqp091-go`. |
| FR-08.2 | The client MUST support declaring exchanges (direct, topic, fanout) and queues. |
| FR-08.3 | A `Publish` method MUST support publishing messages with configurable routing key, exchange, and persistence flag. |
| FR-08.4 | A `Consume` method MUST support registering a handler function that processes incoming messages. |
| FR-08.5 | Published messages MUST support both JSON-serialised structs and raw byte payloads. |
| FR-08.6 | Messages published with `Persistent: true` MUST use `DeliveryMode = Persistent` to survive broker restarts. |

---

### FR-09: S3/MinIO Storage Client

**Package:** `storage/s3`

| ID | Requirement |
|----|-------------|
| FR-09.1 | The library MUST provide an S3 client using `aws/aws-sdk-go-v2` compatible with both AWS S3 and MinIO. |
| FR-09.2 | The client MUST support configurable: endpoint URL, region, access key, secret key, bucket name, SSL toggle, and force-path-style (required for MinIO). |
| FR-09.3 | The client MUST provide an `Upload` method accepting an `io.Reader`, object key, and content type. |
| FR-09.4 | The client MUST provide a `PresignedGetURL` method that generates a time-limited download URL for a given object key. |
| FR-09.5 | The client MUST provide a `Delete` method for removing objects by key. |

---

### FR-10: Error Handling

**Package:** `errors`

| ID | Requirement |
|----|-------------|
| FR-10.1 | The library MUST define typed error constructors for the following categories: `Validation`, `Unauthorized`, `Forbidden`, `NotFound`, `Conflict`, `Internal`, `Database`. |
| FR-10.2 | Each error type MUST map to a standard HTTP status code. |
| FR-10.3 | All errors MUST serialise to a JSON body conforming to RFC 7807 Problem Details: `{ "type", "title", "detail", "status" }`. |
| FR-10.4 | `Validation` errors MUST include a field-level `errors` array listing each invalid field and its reason. |
| FR-10.5 | A Chi middleware (`errors.Handler()`) MUST be provided that catches typed errors returned from handlers and writes the correct HTTP response. |
| FR-10.6 | Untyped errors (unexpected panics or generic `error` values) MUST be caught and returned as `500 Internal Server Error` without leaking internal stack traces to the response body. |

---

### FR-11: Response Formatting

**Package:** `response`

| ID | Requirement |
|----|-------------|
| FR-11.1 | The library MUST provide `OK(w, data)` and `Created(w, data)` helpers that write `200` / `201` responses with a standard JSON envelope. |
| FR-11.2 | The library MUST provide `OKWithPagination(w, items, pagination)` that includes `limit`, `offset`, and `total` fields alongside the result array. |
| FR-11.3 | A generic `DxResponse[T]` type MUST be defined for type-safe response construction. |
| FR-11.4 | All success responses MUST set `Content-Type: application/json`. |

---

### FR-12: Configuration Loading

**Package:** `config`

| ID | Requirement |
|----|-------------|
| FR-12.1 | The library MUST support loading configuration from a YAML file. |
| FR-12.2 | Environment variables MUST override YAML file values. The override format MUST be: uppercase, prefix-separated by `_`, with `.` in the key replaced by `_` (e.g., `server.port` → `PREFIX_SERVER_PORT`). |
| FR-12.3 | The calling service MUST be able to specify its own env prefix. |
| FR-12.4 | Missing required configuration fields MUST result in a startup error with a clear message identifying the missing key. |

---

### FR-13: OpenAPI Tooling

**Package:** `openapi`

| ID | Requirement |
|----|-------------|
| FR-13.1 | The library MUST support loading and caching an OpenAPI 3.1 specification from a file path. |
| FR-13.2 | A Chi middleware MUST be provided that validates incoming HTTP requests against the loaded spec and returns `400 Bad Request` with detailed validation errors on mismatch. |
| FR-13.3 | The library MUST provide a function to mount a Swagger UI at a configurable path (e.g., `/docs`). |

---

## 3. Non-Functional Requirements

| ID | Requirement |
|----|-------------|
| NFR-01 | The library MUST NOT import or depend on any service-specific business logic or domain models. |
| NFR-02 | All exported symbols MUST have Go doc comments. |
| NFR-03 | Unit test coverage MUST be maintained for all exported functions. |
| NFR-04 | All client creation functions (Postgres, Redis, RabbitMQ, S3) MUST validate connectivity at initialisation time and return a descriptive error on failure. |
| NFR-05 | All clients MUST be safe for concurrent use from multiple goroutines. |
| NFR-06 | The module MUST target Go 1.22 or later. |
| NFR-07 | Logging MUST use structured JSON format via `go.uber.org/zap`. |

---

## 4. Dependencies

| Library | Version | Purpose |
|---------|---------|---------|
| `go-chi/chi/v5` | v5.x | HTTP routing and middleware |
| `jackc/pgx/v5` | v5.x | PostgreSQL driver |
| `redis/go-redis/v9` | v9.x | Redis client |
| `rabbitmq/amqp091-go` | v1.x | RabbitMQ AMQP client |
| `aws/aws-sdk-go-v2` | v2.x | AWS S3 and STS |
| `golang-jwt/jwt/v5` | v5.x | JWT parsing and validation |
| `MicahParks/keyfunc/v3` | v3.x | JWKS key fetching and caching |
| `spf13/viper` | v1.x | Configuration management |
| `go-playground/validator/v10` | v10.x | Struct validation |
| `getkin/kin-openapi` | v0.x | OpenAPI 3 parsing and validation |
| `go.uber.org/zap` | v1.x | Structured logging |
| `google/uuid` | v1.x | UUID generation |

---

## 5. Out of Scope

- Business logic of any kind
- Database schema definitions (tables, indexes) — those belong in consuming services
- HTTP route definitions — those belong in consuming services
- Service discovery or registry
- Distributed tracing (future consideration)
