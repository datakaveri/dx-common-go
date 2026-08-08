package events

import "context"

// DrainOnce publishes exactly one batch. Exported to the package's external
// tests only (export_test.go is not compiled into the package's consumers), so
// the concurrency properties can be driven deterministically instead of by
// racing Run's ticker.
func (d *Dispatcher) DrainOnce(ctx context.Context) (int, error) { return d.drain(ctx) }

// ClaimForTest leases up to limit rows and returns how many it took. Same
// reasoning: the lease is only observable through claim.
func (o *Outbox) ClaimForTest(ctx context.Context, limit int) (int, error) {
	rows, err := o.claim(ctx, limit)
	return len(rows), err
}
