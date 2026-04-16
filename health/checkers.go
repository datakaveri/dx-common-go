package health

import (
	"context"
	"database/sql"
	"time"

	"github.com/redis/go-redis/v9"
)

// PostgreSQLChecker checks PostgreSQL database health
type PostgreSQLChecker struct {
	db *sql.DB
}

// NewPostgreSQLChecker creates a new PostgreSQL health checker
func NewPostgreSQLChecker(db *sql.DB) *PostgreSQLChecker {
	return &PostgreSQLChecker{db: db}
}

// Check verifies PostgreSQL connectivity
func (pc *PostgreSQLChecker) Check(ctx context.Context) ServiceStatus {
	start := time.Now()

	if err := pc.db.PingContext(ctx); err != nil {
		return ServiceStatus{
			Name:     "database",
			Status:   "unhealthy",
			Message:  "failed to connect: " + err.Error(),
			Duration: time.Since(start),
		}
	}

	return ServiceStatus{
		Name:     "database",
		Status:   "healthy",
		Duration: time.Since(start),
	}
}

// RedisChecker checks Redis cache health
type RedisChecker struct {
	client *redis.Client
}

// NewRedisChecker creates a new Redis health checker
func NewRedisChecker(client *redis.Client) *RedisChecker {
	return &RedisChecker{client: client}
}

// Check verifies Redis connectivity
func (rc *RedisChecker) Check(ctx context.Context) ServiceStatus {
	start := time.Now()

	if err := rc.client.Ping(ctx).Err(); err != nil {
		return ServiceStatus{
			Name:     "redis",
			Status:   "unhealthy",
			Message:  "failed to connect: " + err.Error(),
			Duration: time.Since(start),
		}
	}

	return ServiceStatus{
		Name:     "redis",
		Status:   "healthy",
		Duration: time.Since(start),
	}
}

// CustomChecker is a simple checker with a custom check function
type CustomChecker struct {
	name  string
	check func(ctx context.Context) error
}

// NewCustomChecker creates a new custom health checker
func NewCustomChecker(name string, checkFunc func(ctx context.Context) error) *CustomChecker {
	return &CustomChecker{
		name:  name,
		check: checkFunc,
	}
}

// Check runs the custom check function
func (cc *CustomChecker) Check(ctx context.Context) ServiceStatus {
	start := time.Now()

	if err := cc.check(ctx); err != nil {
		return ServiceStatus{
			Name:     cc.name,
			Status:   "unhealthy",
			Message:  err.Error(),
			Duration: time.Since(start),
		}
	}

	return ServiceStatus{
		Name:     cc.name,
		Status:   "healthy",
		Duration: time.Since(start),
	}
}

// AlwaysHealthyChecker is a checker that always returns healthy (for testing)
type AlwaysHealthyChecker struct {
	name string
}

// NewAlwaysHealthyChecker creates a checker that always reports healthy
func NewAlwaysHealthyChecker(name string) *AlwaysHealthyChecker {
	return &AlwaysHealthyChecker{name: name}
}

// Check always returns healthy status
func (ahc *AlwaysHealthyChecker) Check(ctx context.Context) ServiceStatus {
	return ServiceStatus{
		Name:   ahc.name,
		Status: "healthy",
	}
}

// MultiChecker groups multiple checkers and fails if any fail
type MultiChecker struct {
	name     string
	checkers []Checker
}

// NewMultiChecker creates a checker that combines multiple checkers
func NewMultiChecker(name string, checkers ...Checker) *MultiChecker {
	return &MultiChecker{
		name:     name,
		checkers: checkers,
	}
}

// Check returns unhealthy if any sub-checker is unhealthy
func (mc *MultiChecker) Check(ctx context.Context) ServiceStatus {
	start := time.Now()

	for _, checker := range mc.checkers {
		if status := checker.Check(ctx); status.Status != "healthy" {
			return ServiceStatus{
				Name:     mc.name,
				Status:   "unhealthy",
				Message:  status.Name + " is unhealthy",
				Duration: time.Since(start),
			}
		}
	}

	return ServiceStatus{
		Name:     mc.name,
		Status:   "healthy",
		Duration: time.Since(start),
	}
}
