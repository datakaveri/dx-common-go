package sql

import "context"

// AfterCommit defers fn until the current transaction commits.
//
// It exists because a retryable transaction MUST be side-effect free, and
// "just don't do that" was not enforceable: `DoRetry` re-runs its whole
// callback, so any email sent, event published or RPC made inside it happens
// once per attempt — and happens at all even when the transaction ultimately
// rolls back. `dx-acl-go` emailed a consumer about an access grant from inside
// the transaction that granted it, so a serialization failure notified them
// about a grant that never committed, then notified them again (ROADMAP P0-8 /
// review finding H-06).
//
// Registered functions run:
//   - exactly once, after a successful commit, in registration order;
//   - never, if the transaction rolls back or a retry re-runs the callback —
//     the hooks registered by a failed attempt are discarded with it.
//
// A hook's error is returned by Do, but the transaction is ALREADY COMMITTED by
// then and is not undone. Use it for effects that must follow the commit, not
// for work the commit depends on.
//
// Outside a transaction it runs fn immediately: a caller should not have to
// branch on whether it happens to be in one.
func AfterCommit(ctx context.Context, fn func(context.Context) error) error {
	h, ok := hooksFrom(ctx)
	if !ok {
		return fn(ctx)
	}
	h.add(fn)
	return nil
}

// hooks collects deferred effects for one transaction attempt.
//
// Per-attempt, not per-call: DoRetry installs a fresh set for every attempt, so
// the hooks a failed attempt registered are dropped rather than accumulating
// and firing N times on eventual success.
type hooks struct {
	fns []func(context.Context) error
}

func (h *hooks) add(fn func(context.Context) error) { h.fns = append(h.fns, fn) }

func (h *hooks) run(ctx context.Context) error {
	for _, fn := range h.fns {
		if err := fn(ctx); err != nil {
			return err
		}
	}
	return nil
}

type hooksKey struct{}

func withHooks(ctx context.Context, h *hooks) context.Context {
	return context.WithValue(ctx, hooksKey{}, h)
}

func hooksFrom(ctx context.Context) (*hooks, bool) {
	h, ok := ctx.Value(hooksKey{}).(*hooks)
	return h, ok
}
