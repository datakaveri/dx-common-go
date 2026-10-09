package rabbitmq_test

import (
	"context"
	"io"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/dxtest/containers"
	dxmq "github.com/datakaveri/dx-common-go/messaging/rabbitmq"
)

// A publisher whose connection drops must come back ON ITS OWN, not on the
// next Publish. A publish-only service (dx-acl-go, dx-user-go) can go hours
// without publishing, and both gate readiness on the publisher: before this,
// one dropped connection left the pod NotReady — serving nothing — until a
// write that could no longer reach it (F38, dev cluster 2026-10-09).

const reconnectWithin = 15 * time.Second

// TestReliablePublisher_ReconnectsAfterConnectionDrop cuts the TCP connection
// under the publisher — what a NAT or load-balancer idle timeout does — and
// expects the publisher to report connected again without being asked to
// publish.
func TestReliablePublisher_ReconnectsAfterConnectionDrop(t *testing.T) {
	brokerURL := containers.RabbitMQURL(t)
	proxy := newCuttableProxy(t, brokerURL)

	pub, err := dxmq.NewReliablePublisher(dxmq.PublisherConfig{
		URL: proxy.url, Exchange: "f38.reconnect.conn", ExchangeType: "topic", Confirms: true,
	})
	if err != nil {
		t.Fatalf("NewReliablePublisher: %v", err)
	}
	t.Cleanup(pub.Close)
	waitConnected(t, pub, true, reconnectWithin, "initial connect")

	proxy.cutAll()
	waitConnected(t, pub, false, 5*time.Second, "drop to be noticed")
	waitConnected(t, pub, true, reconnectWithin, "reconnect without a publish")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pub.PublishJSONCtx(ctx, "f38.reconnect.conn", "k", map[string]string{"after": "reconnect"}); err != nil {
		t.Fatalf("publish after reconnect: %v", err)
	}
}

// TestReliablePublisher_ReconnectsAfterChannelClose has the BROKER close the
// channel (publishing to an exchange that does not exist is a channel-level
// 404) and expects a fresh channel without another publish.
func TestReliablePublisher_ReconnectsAfterChannelClose(t *testing.T) {
	brokerURL := containers.RabbitMQURL(t)

	pub, err := dxmq.NewReliablePublisher(dxmq.PublisherConfig{
		URL: brokerURL, Exchange: "f38.reconnect.chan", ExchangeType: "topic",
	})
	if err != nil {
		t.Fatalf("NewReliablePublisher: %v", err)
	}
	t.Cleanup(pub.Close)
	waitConnected(t, pub, true, reconnectWithin, "initial connect")

	// Without confirms this returns as soon as the frame is written; the
	// broker's 404 closes the channel asynchronously, after Publish returned.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = pub.PublishJSONCtx(ctx, "f38.no-such-exchange", "k", map[string]string{"x": "y"})

	waitConnected(t, pub, false, 5*time.Second, "broker channel close to be noticed")
	waitConnected(t, pub, true, reconnectWithin, "reconnect without a publish")
}

// TestReliablePublisher_CloseStopsReconnecting: Close is final — the
// supervisor must not dial the broker again behind the caller's back.
func TestReliablePublisher_CloseStopsReconnecting(t *testing.T) {
	brokerURL := containers.RabbitMQURL(t)

	pub, err := dxmq.NewReliablePublisher(dxmq.PublisherConfig{URL: brokerURL, Exchange: "f38.reconnect.close"})
	if err != nil {
		t.Fatalf("NewReliablePublisher: %v", err)
	}
	waitConnected(t, pub, true, reconnectWithin, "initial connect")
	pub.Close()

	time.Sleep(3 * time.Second) // longer than the first reconnect backoff
	if pub.IsConnected() {
		t.Fatal("publisher reconnected after Close")
	}
}

func waitConnected(t *testing.T, pub *dxmq.ReliablePublisher, want bool, within time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if pub.IsConnected() == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("waiting for %s: IsConnected() still %v after %s", what, !want, within)
}

// cuttableProxy forwards TCP to the broker and can sever every live
// connection at once, which the broker container alone cannot do without the
// management plugin.
type cuttableProxy struct {
	url string

	mu    sync.Mutex
	conns []net.Conn
}

func newCuttableProxy(t *testing.T, brokerURL string) *cuttableProxy {
	t.Helper()
	u, err := url.Parse(brokerURL)
	if err != nil {
		t.Fatalf("parse broker url: %v", err)
	}
	target := u.Host
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	p := &cuttableProxy{}
	u.Host = ln.Addr().String()
	p.url = u.String()

	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			upstream, err := net.Dial("tcp", target)
			if err != nil {
				_ = client.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, client, upstream)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(upstream, client); _ = upstream.Close() }()
			go func() { _, _ = io.Copy(client, upstream); _ = client.Close() }()
		}
	}()
	t.Cleanup(p.cutAll)
	return p
}

func (p *cuttableProxy) cutAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
}
