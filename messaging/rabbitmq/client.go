package rabbitmq

import (
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Client manages a single AMQP connection and channel with automatic reconnect.
type Client struct {
	cfg     Config
	conn    *amqp.Connection
	channel *amqp.Channel
	mu      sync.RWMutex
	done    chan struct{}
}

// NewClient dials RabbitMQ and starts the reconnect loop.
// It blocks until the initial connection succeeds or returns an error.
func NewClient(cfg Config) (*Client, error) {
	if cfg.ReconnectDelay == 0 {
		cfg.ReconnectDelay = 5 * time.Second
	}

	c := &Client{
		cfg:  cfg,
		done: make(chan struct{}),
	}

	if err := c.connect(); err != nil {
		return nil, fmt.Errorf("rabbitmq.NewClient: initial connect: %w", err)
	}

	go c.reconnectLoop()

	return c, nil
}

// connect dials the broker and opens a channel.
func (c *Client) connect() error {
	conn, err := amqp.Dial(c.cfg.URL)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return fmt.Errorf("open channel: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn = conn
	c.channel = ch
	return nil
}

// reconnectLoop watches the connection's NotifyClose channel and reconnects
// with exponential-ish backoff when the connection drops.
func (c *Client) reconnectLoop() {
	for {
		c.mu.RLock()
		notifyClose := c.conn.NotifyClose(make(chan *amqp.Error, 1))
		c.mu.RUnlock()

		select {
		case <-c.done:
			return
		case amqpErr, ok := <-notifyClose:
			if !ok {
				return
			}
			_ = amqpErr // logged by broker

			delay := c.cfg.ReconnectDelay
			for {
				select {
				case <-c.done:
					return
				case <-time.After(delay):
				}

				if err := c.connect(); err != nil {
					// Double the delay up to 60 s.
					delay *= 2
					if delay > 60*time.Second {
						delay = 60 * time.Second
					}
					continue
				}
				break
			}
		}
	}
}

// Channel returns the current AMQP channel (safe for concurrent read access).
func (c *Client) Channel() *amqp.Channel {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.channel
}

// Close gracefully closes the channel and connection.
func (c *Client) Close() error {
	close(c.done)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.channel != nil {
		c.channel.Close()
	}
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
