# dx-common-go

Shared Go library for the CDPG data exchange platform. Provides reusable HTTP infrastructure, authentication, database clients, messaging, storage, and observability primitives consumed by all Go-based platform services.

---

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Getting Started](#getting-started)
- [Package Reference](#package-reference)
- [Usage Examples](#usage-examples)
- [Testing](#testing)
- [Contributing](#contributing)

---

## Overview

`dx-common-go` is not a runnable service — it is a Go module imported as a library dependency by other services (`dx-files-connect-api-go`, `dx-community-layer-go`, etc.). It standardises cross-cutting concerns so that each service does not re-implement auth, error handling, HTTP server setup, and database pooling from scratch.

**Module path:** `github.com/datakaveri/dx-common-go`
**Go version:** 1.22+

---

## Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| Go | 1.22+ | Language runtime and module toolchain |
| Git | Any | Source control |
| PostgreSQL | 12+ | Required by consuming services (not this library directly) |
| Redis | 6+ | Required by consuming services |
| RabbitMQ | 3.11+ | Required by consuming services |
| MinIO / AWS S3 | Any | Required by consuming services for storage |

### Install Go (macOS)

```bash
# Using Homebrew
brew install go@1.22
echo 'export PATH="/opt/homebrew/opt/go@1.22/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc

# Verify
go version  # should print go1.22.x
```

### Install Go (Linux)

```bash
wget https://go.dev/dl/go1.22.5.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.22.5.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc
go version
```

---

## Getting Started

### 1. Clone the Repository

```bash
git clone https://github.com/datakaveri/dx-common-go.git
cd dx-common-go
```

### 2. Install Dependencies

```bash
go mod download
go mod verify
```

### 3. Use as a Dependency in Another Service

Add to your service's `go.mod`:

```bash
# From within your service directory
go get github.com/datakaveri/dx-common-go@latest
```

Or specify a local path during development:

```go
// go.mod
replace github.com/datakaveri/dx-common-go => ../dx-common-go
```

Then run:

```bash
go mod tidy
```

---

## Package Reference

### `auth/jwt`

Validates Keycloak-issued JWTs using JWKS.

```go
import "github.com/datakaveri/dx-common-go/auth/jwt"

validator, err := jwt.NewValidator(jwt.Config{
    JWKSUrl:         "http://keycloak:8180/realms/iudx/protocol/openid-connect/certs",
    Issuer:          "http://keycloak:8180/realms/iudx",
    Audience:        "account",
    CacheTTL:        5 * time.Minute,
    LeewaySeconds:   5,
})
```

**Chi middleware:**

```go
r.Use(validator.Middleware())
```

The middleware extracts the `Authorization: Bearer <token>` header, validates signature and claims, and injects the parsed claims into `context.Context`.

---

### `auth/authorization`

Role and scope-based access control.

```go
import "github.com/datakaveri/dx-common-go/auth/authorization"

// Protect a route group for providers only
r.Group(func(r chi.Router) {
    r.Use(authorization.RequireRoles(authorization.RoleProvider))
    r.Post("/upload", handler.Upload)
})

// Protect a route for multiple roles
r.Use(authorization.RequireRoles(
    authorization.RoleCosAdmin,
    authorization.RoleOrgAdmin,
))
```

**Available roles:**

| Constant | Keycloak Role |
|----------|--------------|
| `RoleProvider` | `provider` |
| `RoleConsumer` | `consumer` |
| `RoleCosAdmin` | `cos_admin` |
| `RoleOrgAdmin` | `org_admin` |

---

### `auth/context`

Retrieve the authenticated user from context after JWT middleware has run.

```go
import "github.com/datakaveri/dx-common-go/auth"

user, err := auth.UserFromContext(r.Context())
if err != nil {
    // unauthenticated
}
fmt.Println(user.Sub)          // Keycloak subject (user ID)
fmt.Println(user.Email)        // User email
fmt.Println(user.Roles)        // []string of Keycloak roles
fmt.Println(user.OrgUnitPath)  // Org hierarchy path
```

---

### `database/postgres`

PostgreSQL connection pool using `pgx/v5`.

```go
import "github.com/datakaveri/dx-common-go/database/postgres"

pool, err := postgres.NewPool(postgres.Config{
    Host:     "localhost",
    Port:     5433,
    User:     "iudx_user",
    Password: "iudx_password",
    DBName:   "mydb",
    SSLMode:  "disable",
    MaxConns: 20,
    MinConns: 2,
})
defer pool.Close()
```

**Transactions:**

```go
err = pool.WithTx(ctx, func(tx pgx.Tx) error {
    _, err := tx.Exec(ctx, "INSERT INTO ...")
    return err  // rollback on non-nil, commit on nil
})
```

---

### `database/redis`

Redis client wrapper.

```go
import "github.com/datakaveri/dx-common-go/database/redis"

client, err := redis.NewClient(redis.Config{
    Addr:     "localhost:6379",
    Password: "",
    DB:       0,
})
defer client.Close()
```

---

### `messaging/rabbitmq`

RabbitMQ publisher and consumer.

```go
import "github.com/datakaveri/dx-common-go/messaging/rabbitmq"

client, err := rabbitmq.NewClient("amqp://guest:guest@localhost:5672/")

// Publish
err = client.Publish(ctx, rabbitmq.PublishOptions{
    Exchange:   "dx.events",
    RoutingKey: "files.uploaded",
    Body:       payload,
    Persistent: true,
})

// Consume
err = client.Consume(ctx, rabbitmq.ConsumeOptions{
    Queue:   "dx.files.jobs",
    Handler: func(msg amqp091.Delivery) error { ... },
})
```

---

### `storage/s3`

S3/MinIO client for file storage.

```go
import "github.com/datakaveri/dx-common-go/storage/s3"

client, err := s3.NewClient(s3.Config{
    Endpoint:        "localhost:9002",   // empty for AWS
    Region:          "us-east-1",
    AccessKeyID:     "minioadmin",
    SecretAccessKey: "minioadmin",
    Bucket:          "dx-files",
    UseSSL:          false,
    ForcePathStyle:  true,              // required for MinIO
})

// Upload
err = client.Upload(ctx, "path/to/key", reader, contentType)

// Presigned download URL (valid for 1 hour)
url, err := client.PresignedGetURL(ctx, "path/to/key", time.Hour)
```

---

### `httpserver`

Production-grade HTTP server with graceful shutdown.

```go
import "github.com/datakaveri/dx-common-go/httpserver"

srv := httpserver.New(httpserver.Config{
    Port:            3000,
    ReadTimeout:     15 * time.Second,
    WriteTimeout:    30 * time.Second,
    IdleTimeout:     60 * time.Second,
    ShutdownTimeout: 10 * time.Second,
}, router)

if err := srv.Start(); err != nil {
    log.Fatal(err)
}
```

On `SIGTERM` or `SIGINT`, the server drains active connections within `ShutdownTimeout` before exiting.

---

### `middleware`

Chi-compatible HTTP middleware.

```go
import "github.com/datakaveri/dx-common-go/middleware"

r := chi.NewRouter()
r.Use(middleware.RequestID())    // Injects X-Request-ID header
r.Use(middleware.Logger(logger)) // Structured request logging
r.Use(middleware.Recovery())     // Panic → 500 with stack trace in logs
r.Use(middleware.CORS(origins))  // CORS with configurable allowed origins
r.Use(middleware.Timeout(30 * time.Second))
```

---

### `errors`

Standardised error types and global error handler.

```go
import "github.com/datakaveri/dx-common-go/errors"

// Create typed errors
return errors.NotFound("file not found")
return errors.Unauthorized("invalid token")
return errors.Forbidden("access denied")
return errors.Validation("invalid request", validationErrors)
return errors.Internal("unexpected error", err)

// Register the global handler on your router
r.Use(errors.Handler())
```

All errors serialize to:

```json
{
  "type": "urn:dx:as:NotFound",
  "title": "Not Found",
  "detail": "file not found",
  "status": 404
}
```

---

### `response`

Standard JSON response envelope.

```go
import "github.com/datakaveri/dx-common-go/response"

// Success with data
response.OK(w, data)
response.Created(w, data)

// Paginated list
response.OKWithPagination(w, items, response.Pagination{
    Limit:  20,
    Offset: 0,
    Total:  150,
})
```

---

### `config`

Viper-based configuration loader with environment override support.

```go
import "github.com/datakaveri/dx-common-go/config"

cfg := &MyConfig{}
err := config.Load("config.yaml", "APP", cfg)
// ENV vars with prefix APP_ override config file values
// e.g. APP_SERVER_PORT overrides server.port
```

---

### `openapi`

OpenAPI 3.1 spec loading and request validation middleware.

```go
import "github.com/datakaveri/dx-common-go/openapi"

spec, err := openapi.LoadSpec("openapi/openapi.yaml")

// Chi middleware: validate all requests against the spec
r.Use(openapi.ValidateRequest(spec))

// Mount Swagger UI at /docs
openapi.MountSwaggerUI(r, "/docs", spec)
```

---

## Testing

```bash
# Run all tests
go test ./...

# Run with race detector
go test -race ./...

# Run tests with coverage report
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

---

## Contributing

- Follow the existing package structure (one concern per package)
- Add unit tests for all exported functions
- Update `go.mod`/`go.sum` after adding dependencies (`go mod tidy`)
- Do not import service-specific packages into this library
- All exported symbols must have Go doc comments
