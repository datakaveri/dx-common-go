package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Client wraps go-redis with the project Config.
type Client struct {
	rdb *goredis.Client
}

// NewClient creates a Redis client and pings the server to verify connectivity.
func NewClient(cfg Config) (*Client, error) {
	opts := &goredis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	}
	if cfg.PoolSize > 0 {
		opts.PoolSize = cfg.PoolSize
	}

	rdb := goredis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis.NewClient: ping failed: %w", err)
	}

	return &Client{rdb: rdb}, nil
}

// Close closes the underlying connection pool.
func (c *Client) Close() error { return c.rdb.Close() }

// Underlying returns the raw go-redis client for advanced operations.
func (c *Client) Underlying() *goredis.Client { return c.rdb }
