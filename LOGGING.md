# Logging standard

Logs exist so that anyone — not only the author — can tell from them what the
service did and why a request failed. Every line is short, says one thing, and
carries the ids that join it to the rest.

## Levels

| Level | Use it for | Examples |
|-------|-----------|----------|
| **ERROR** | Something is broken and **a person has to act**. | a bug (500); money or data that no longer agree; a grant that gave up; a template that does not render |
| **WARN** | A failure that was **handled** — retried, degraded, skipped — and needs no action yet. | a dependency down or slow (502/503/504); a retry; an email not published; a failed audit publish |
| **INFO** | **One line per business state change**, and startup facts. | `mor application approved`, `order created`, `purchase recorded`, `policy granted`, `terms published`, `email sent` |
| **DEBUG** | Detail for troubleshooting. Off in production. | a duplicate webhook ignored; health-probe and `/metrics` request lines |

A 4xx is the client's mistake, not ours: it is neither WARN nor ERROR. The
request line already records its status.

## What the platform does for you

- **One request line per request** (`msg=request`): `method path status
  duration_ms bytes request_id user_id remote_addr` (+ `trace_id span_id`).
  `user_id` is the Keycloak subject id of an authenticated caller — an id,
  never a name or email. Health probes and `/metrics` are logged at DEBUG.
- **One cause line per 5xx** (`msg="request failed"`), on the same
  `request_id`: WARN for 502/503/504 or a dependency outage, ERROR otherwise.
- **No stack traces** except on panics (HTTP handler, gRPC handler, scheduler
  job, executor task — each logs the panic site's stack).
- **JSON** when stderr is not a terminal (containers), readable console when
  it is (`log_format: auto`, the default). Every JSON line carries `service`
  and `version`.
- **Redaction**: known secret keys (authorization, password, token …) are
  dropped or masked whatever a call site logs.
- **Scheduled jobs**: a failed run is WARN when a dependency was down (the next
  tick retries), ERROR otherwise.

## Rules for service code

1. **Return errors, don't log them.** An error returned to the HTTP layer is
   logged there once, with its cause. Attach the cause instead of logging it:

   ```go
   return dxerrors.WithCause(dxerrors.NewServiceUnavailable("could not grant the role; try again"), err)
   // or, platform errors:
   return perrors.ServiceUnavailable("could not grant the role; try again").WithCause(err)
   ```

   The client sees only the message; the log line shows the cause.

2. **Log through the request's logger**, so the line carries `request_id`:

   ```go
   logging.FromOr(ctx, s.logger).Info("order created", zap.String("order_id", id))
   ```

   Outside a request (jobs, consumers) `FromOr` falls back to `s.logger`.

3. **Tag calls to other services** with `perrors.Dependency(name, target, err)`
   and log errors with `logging.ErrorFields(err)` (instead of `zap.Error`).
   The line then has typed fields to filter on:
   `dependency=apis.user target=dx-user-go:9090 grpc_code=Unavailable`.

4. **Messages**: lowercase, past tense, `<thing> <what happened>`:
   `product created`, `join request withdrawn`, `send failed; will retry`.
   No service-name prefix (`service` is a field), no capitals for emphasis.

5. **Fields**: snake_case; ids as `<entity>_id` (`mor_id`, `order_id`,
   `event_id`); the acting user as `user_id`; durations as `duration_ms` /
   `wait_ms`. Name the entity, not its contents.

6. **Never log** secrets or tokens, full email addresses (mask them:
   `p***@acme.in`), names, or free text a user typed (rejection reasons,
   messages, notes) — it is stored in the database already.
