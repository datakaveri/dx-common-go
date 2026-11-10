// Package cache — scope notes.
//
// # What this package deliberately does NOT provide
//
// Two capabilities are commonly expected here and are absent on purpose.
//
// **Pub/sub belongs to platform/events, not here.** Redis happens to offer it,
// but that is an implementation coincidence. Putting message fan-out behind a
// cache interface would mean a service publishing domain events through
// something called "cache", and would tie the choice of broker to the choice
// of cache — the two are independent decisions, and events already needs
// retries, DLQ, idempotency, ordering and an outbox that a cache has no
// business owning.
//
// **A session store is a usage, not a module.** A session is a namespaced
// value with a TTL, which is exactly what Scope already is:
//
//	sessions := c.Namespace("sessions").TTL(30 * time.Minute)
//	sessions.Set(ctx, sid, s)
//
// A dedicated type would add a second way to express the same thing, and the
// platform's failure mode has been too many overlapping abstractions, not too
// few. If session handling later needs behaviour a Scope cannot express —
// sliding expiry, revocation-by-user — that is the moment to add it, with the
// requirement in hand rather than guessed.
package cache
