package rabbitmq

import "github.com/prometheus/client_golang/prometheus"

// deliveriesTotal counts consumer deliveries by queue and final outcome.
//
// A non-zero "deadletter" rate is the signal that a message was rejected
// without requeue — poison, undecodable, or over the attempt cap — which is the
// DLQ alert ROADMAP P1-14 asks for, expressed as a RATE rather than a queue
// depth so it needs no RabbitMQ management API to observe:
//
//	increase(rabbitmq_consumer_deliveries_total{outcome="deadletter"}[15m]) > 0
//
// Package-level and registered once in init(): a per-Runner metric would panic
// on the second MustRegister when a process builds more than one consumer, the
// same reasoning the scheduler's metrics carry.
var deliveriesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "rabbitmq_consumer_deliveries_total",
	Help: "Consumer deliveries by queue and final outcome (ack, requeue, deadletter).",
}, []string{"queue", "outcome"})

func init() {
	prometheus.MustRegister(deliveriesTotal)
}

// outcomeLabel renders an Outcome as a metric label value.
func outcomeLabel(o Outcome) string {
	switch o {
	case Ack:
		return "ack"
	case Requeue:
		return "requeue"
	case DeadLetter:
		return "deadletter"
	default:
		return "unknown"
	}
}
