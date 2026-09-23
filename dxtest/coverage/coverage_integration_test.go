// Package coverage_test is the CI-2 coverage-matrix boundary test
// (OBSERVABILITY_PLAN.md CI-2 / OBSERVABILITY.md §6.3, §15 S13.4): it proves,
// against REAL dependencies (a real Postgres, Redis and RabbitMQ via
// dxtest/containers; a real HTTP round trip; a real gRPC call over a live
// listener), that each documented instrumentation seam actually produces a
// span — not that the seam's own package has a unit test, but that wiring it
// the way a service does still emits the span end to end.
//
// It lives outside every package it exercises deliberately: the point is to
// catch a seam going silently unwired from ITS CALLER (bootstrap, a service's
// Wire) as much as a regression inside the seam itself, and a single
// composition point is what makes "removing any boundary fails the test"
// (the acceptance criterion this file exists to satisfy) a fact about the
// fleet's actual wiring rather than about one package in isolation.
//
// A Docker-less environment SKIPS every subtest that needs a container
// (dxtest/containers' own behavior — see its package doc) rather than
// failing, so this file is still safe to run wherever the rest of the suite
// runs; it only certifies coverage where it can actually observe it.
package coverage_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	grpcclient "github.com/datakaveri/dx-common-go/grpc/client"
	dxmq "github.com/datakaveri/dx-common-go/messaging/rabbitmq"
	httpx "github.com/datakaveri/dx-common-go/platform/http"
	"github.com/datakaveri/dx-common-go/platform/observability/health"

	dxcache "github.com/datakaveri/dx-common-go/platform/cache/redis"
	dxsql "github.com/datakaveri/dx-common-go/platform/database/sql"
	grpcserver "github.com/datakaveri/dx-common-go/platform/grpc/server"

	"github.com/datakaveri/dx-common-go/dxtest/containers"
)

// startTracing installs a fresh, synchronous (WithSyncer, no batching delay)
// TracerProvider as the OTel global for the duration of one test and restores
// whatever was there before on cleanup — global OTel state is process-wide,
// so tests in this file must not run in parallel with each other (none call
// t.Parallel) or they would observe one another's spans.
func startTracing(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))

	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return exp
}

func hasSpanKind(spans tracetest.SpanStubs, kind trace.SpanKind) bool {
	for _, s := range spans {
		if s.SpanKind == kind {
			return true
		}
	}
	return false
}

// TestCoverage_HTTPServerAndClientSpans is the httpx.NewRouter / otelhttp
// boundary: every bootstrap.Run service builds its handler through NewRouter,
// which installs otelhttp.NewMiddleware unconditionally (platform/http/stack.go)
// — this proves a real request produces both the server span (inbound) and,
// mirroring the gateway's proxy hop, a client span on the calling side.
func TestCoverage_HTTPServerAndClientSpans(t *testing.T) {
	exp := startTracing(t)

	router := httpx.NewRouter(httpx.RouterSpec{
		Base:   "/v1",
		URNs:   "coverage",
		Health: health.New(),
	}, httpx.Routes("",
		httpx.GET("/ping", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}, httpx.Public()),
	))
	srv := httptest.NewServer(router)
	defer srv.Close()

	client := &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}
	resp, err := client.Get(srv.URL + "/v1/ping")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	// otelhttp's client span does NOT end when RoundTrip returns — it ends
	// when the response body reaches EOF or is closed (transport.go wraps
	// res.Body precisely so it can span.End() there). A deferred Close would
	// run after this test already read exp.GetSpans(), missing the span
	// entirely — the same lazy-close discipline a real caller of this
	// transport must observe to get a complete trace.
	require.NoError(t, resp.Body.Close())

	spans := exp.GetSpans()
	assert.True(t, hasSpanKind(spans, trace.SpanKindServer), "no HTTP SERVER span — httpx.NewRouter's otelhttp middleware is not wired")
	assert.True(t, hasSpanKind(spans, trace.SpanKindClient), "no HTTP CLIENT span — the calling side's otelhttp transport is not wired")
}

// TestCoverage_PostgresSpan is the dxsql.Open / otelpgx boundary
// (platform/database/sql/pool.go installs otelpgx.NewTracer() unconditionally
// — every service that opens a pool through dxsql.Open gets it, with no
// per-service opt-in).
func TestCoverage_PostgresSpan(t *testing.T) {
	exp := startTracing(t)

	pg := containers.Postgres(t)
	db, err := dxsql.Open(context.Background(), dxsql.Config{DSN: pg.DSN})
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(context.Background(), "SELECT 1")
	require.NoError(t, err)

	spans := exp.GetSpans()
	require.NotEmpty(t, spans, "no span recorded for a pgx query — otelpgx is not installed on the Open path")
	assert.True(t, hasSpanKind(spans, trace.SpanKindClient), "pgx query span must be CLIENT-kind")
}

// TestCoverage_RedisSpan is the platform/cache/redis.Open / redisotel
// boundary — instrumented unconditionally at client construction (SP-5), not
// behind an opt-in flag.
func TestCoverage_RedisSpan(t *testing.T) {
	exp := startTracing(t)

	rc := containers.Redis(t)
	store, err := dxcache.Open(context.Background(), dxcache.Config{Addr: rc.Addr})
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.Set(context.Background(), "coverage-key", []byte("v"), time.Minute))
	_, err = store.Get(context.Background(), "coverage-key")
	require.NoError(t, err)

	spans := exp.GetSpans()
	require.NotEmpty(t, spans, "no span recorded for a Redis command — redisotel is not installed on platform/cache/redis.Open")
	assert.True(t, hasSpanKind(spans, trace.SpanKindClient), "redis command span must be CLIENT-kind")
}

// TestCoverage_GRPCServerSpan is the platform/grpc/server boundary: New wires
// otelgrpc.NewServerHandler() as a StatsHandler unconditionally
// (platform/grpc/server/server.go), and the standard grpc.health.v1 service it
// always registers is enough to exercise a real RPC without a service-specific
// proto. grpc/client.Dial mirrors it on the caller side (its own default,
// WithoutTracing() opts out).
func TestCoverage_GRPCServerSpan(t *testing.T) {
	exp := startTracing(t)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv, err := grpcserver.New(grpcserver.Config{Port: 1}, grpcserver.Options{})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErrc := make(chan error, 1)
	go func() { serveErrc <- srv.ServeListener(ctx, lis) }()

	conn, err := grpcclient.Dial(grpcclient.Config{Target: lis.Addr().String()})
	require.NoError(t, err)
	defer conn.Close()

	healthClient := healthpb.NewHealthClient(conn)
	// The server marks itself SERVING only once ServeListener has bound the
	// listener; retry briefly rather than sleeping a fixed guess.
	require.Eventually(t, func() bool {
		resp, err := healthClient.Check(context.Background(), &healthpb.HealthCheckRequest{})
		return err == nil && resp.GetStatus() == healthpb.HealthCheckResponse_SERVING
	}, 5*time.Second, 20*time.Millisecond, "grpc health check never reported SERVING")

	cancel()
	select {
	case <-serveErrc:
	case <-time.After(5 * time.Second):
		t.Fatal("grpc server did not shut down")
	}

	spans := exp.GetSpans()
	assert.True(t, hasSpanKind(spans, trace.SpanKindServer), "no gRPC SERVER span — platform/grpc/server's otelgrpc StatsHandler is not wired")
	assert.True(t, hasSpanKind(spans, trace.SpanKindClient), "no gRPC CLIENT span — grpc/client.Dial's otelgrpc StatsHandler is not wired")
}

// TestCoverage_RabbitMQProducerConsumerSpans is the messaging/rabbitmq
// boundary: ReliablePublisher.Publish starts a PRODUCER span and injects
// traceparent into the message headers; ConsumerRunner.dispatch extracts it
// and starts a CONSUMER span as its child — this is what makes a job's trace
// survive the hop across the broker (the exact defect this session found and
// fixed in dx-dataplane-ogc-go's worker: the spans existed, but the process
// never called observability.Init, so they went to a no-op tracer).
func TestCoverage_RabbitMQProducerConsumerSpans(t *testing.T) {
	exp := startTracing(t)

	url := containers.RabbitMQURL(t)
	queue := "coverage.rmq." + t.Name()

	received := make(chan struct{}, 1)
	runnerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner := dxmq.NewConsumerRunner(dxmq.ConsumerConfig{
		URL:         url,
		Queue:       queue,
		ConsumerTag: "coverage-test",
		Setup: func(ch *amqp.Channel) error {
			_, err := ch.QueueDeclare(queue, false, true, false, false, nil)
			return err
		},
	})
	go runner.Run(runnerCtx, func(ctx context.Context, d dxmq.Delivery) dxmq.Outcome {
		received <- struct{}{}
		return dxmq.Ack
	})

	require.Eventually(t, runner.IsConnected, 10*time.Second, 50*time.Millisecond,
		"consumer never connected — is the RabbitMQ container reachable?")

	pub, err := dxmq.NewReliablePublisher(dxmq.PublisherConfig{URL: url})
	require.NoError(t, err)

	// Default exchange, routing key == queue name: no exchange declare needed.
	require.NoError(t, pub.Publish(context.Background(), "", queue, []byte("coverage"), dxmq.PublishOptions{}))

	select {
	case <-received:
	case <-time.After(10 * time.Second):
		t.Fatal("message was never delivered to the consumer")
	}

	spans := exp.GetSpans()
	assert.True(t, hasSpanKind(spans, trace.SpanKindProducer), "no producer span — ReliablePublisher.Publish is not starting one")
	assert.True(t, hasSpanKind(spans, trace.SpanKindConsumer), "no consumer span — ConsumerRunner.dispatch is not starting one")

	var producerTID, consumerTID trace.TraceID
	for _, s := range spans {
		switch s.SpanKind {
		case trace.SpanKindProducer:
			producerTID = s.SpanContext.TraceID()
		case trace.SpanKindConsumer:
			consumerTID = s.SpanContext.TraceID()
		}
	}
	assert.Equal(t, producerTID, consumerTID, "consumer span must continue the producer's trace, not start a new one — propagation is broken")
}
