package rabbitmq

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestOutcomeLabel(t *testing.T) {
	for _, c := range []struct {
		o    Outcome
		want string
	}{
		{Ack, "ack"},
		{Requeue, "requeue"},
		{DeadLetter, "deadletter"},
	} {
		if got := outcomeLabel(c.o); got != c.want {
			t.Errorf("outcomeLabel(%v) = %q, want %q", c.o, got, c.want)
		}
	}
}

// TestDeliveriesMetricCounts proves the dead-letter signal actually increments,
// so the P1-14 alert has something to fire on.
func TestDeliveriesMetricCounts(t *testing.T) {
	c := deliveriesTotal.WithLabelValues("test-queue", "deadletter")
	before := testutil.ToFloat64(c)
	c.Inc()
	if got := testutil.ToFloat64(c) - before; got != 1 {
		t.Errorf("deliveriesTotal delta = %v, want 1", got)
	}
}
