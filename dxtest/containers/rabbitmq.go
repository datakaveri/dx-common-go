package containers

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// RabbitMQ support, added for ROADMAP P0-6's replay tooling.
//
// The messaging tests before this were unit tests over a fake Bus, which can
// pin the handler-error → outcome mapping and nothing else. Dead-lettering,
// x-death headers and the DLQ topology are BROKER behaviour: whether a rejected
// message actually reaches "<queue>.dlq" carrying its original routing key is
// not a fact about our code, and asserting it against a fake would assert our
// assumptions back at us. P0-6's criterion is "an operator can replay
// quarantined messages and observe the projection converge", which is only
// answerable against a real broker.

var (
	rabbitOnce sync.Once
	rabbitURL  string
	rabbitErr  error
)

// RabbitMQURL returns an AMQP URL for a real RabbitMQ.
//
// If DX_TEST_AMQP_URL is set it binds to that external instance — the
// Docker-less CI path, mirroring DX_TEST_PG_DSN. Otherwise it starts one
// container shared across every call in this test binary, for the same reason
// Postgres does: one broker per test is slow, and the reaper tears it down when
// the binary exits.
//
// It SKIPS rather than fails when Docker is unavailable, so a checkout without
// Docker still runs the rest of the suite.
func RabbitMQURL(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("DX_TEST_AMQP_URL"); url != "" {
		return url
	}
	rabbitOnce.Do(func() { rabbitURL, rabbitErr = startRabbitContainer() })
	if rabbitErr != nil {
		t.Skipf("dxtest/containers: RabbitMQ unavailable (set DX_TEST_AMQP_URL to use an external broker): %v", rabbitErr)
	}
	return rabbitURL
}

func startRabbitContainer() (string, error) {
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "rabbitmq:3.13-alpine",
		ExposedPorts: []string{"5672/tcp"},
		Env: map[string]string{
			"RABBITMQ_DEFAULT_USER": "guest",
			"RABBITMQ_DEFAULT_PASS": "guest",
		},
		// Both conditions, deliberately. The port opens before the broker will
		// accept AMQP handshakes, so waiting on the port alone produces a
		// connection refused in the first test and a green run on a retry —
		// the shape of flakiness that gets tests marked as unreliable rather
		// than fixed.
		WaitingFor: wait.ForAll(
			wait.ForListeningPort("5672/tcp"),
			wait.ForLog("Server startup complete"),
		).WithDeadline(2 * time.Minute),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return "", fmt.Errorf("start rabbitmq container: %w", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		return "", fmt.Errorf("rabbitmq host: %w", err)
	}
	port, err := c.MappedPort(ctx, "5672/tcp")
	if err != nil {
		return "", fmt.Errorf("rabbitmq port: %w", err)
	}
	return fmt.Sprintf("amqp://guest:guest@%s:%s/", host, port.Port()), nil
}
