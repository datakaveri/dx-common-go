package health

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// ServiceStatus represents the status of a single service dependency
type ServiceStatus struct {
	Name     string        `json:"name"`
	Status   string        `json:"status"` // "healthy", "degraded", "unhealthy"
	Message  string        `json:"message,omitempty"`
	Duration time.Duration `json:"duration_ms,omitempty"`
}

// HealthStatus represents the overall health status
type HealthStatus struct {
	Status   string           `json:"status"` // "healthy", "degraded", "unhealthy"
	Timestamp time.Time        `json:"timestamp"`
	Services []ServiceStatus  `json:"services"`
}

// Checker defines the interface for health checkers
type Checker interface {
	Check(ctx context.Context) ServiceStatus
}

// Handler manages health checks for dependencies
type Handler struct {
	checkers map[string]Checker
}

// NewHandler creates a new health check handler
func NewHandler() *Handler {
	return &Handler{
		checkers: make(map[string]Checker),
	}
}

// Register adds a health checker for a service
func (h *Handler) Register(name string, checker Checker) {
	h.checkers[name] = checker
}

// Health returns the overall health status
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	status := HealthStatus{
		Status:    "healthy",
		Timestamp: time.Now(),
		Services:  make([]ServiceStatus, 0, len(h.checkers)),
	}

	// Check all services
	for _, checker := range h.checkers {
		svc := checker.Check(ctx)
		status.Services = append(status.Services, svc)

		if svc.Status != "healthy" {
			status.Status = "degraded"
			if svc.Status == "unhealthy" {
				status.Status = "unhealthy"
			}
		}
	}

	// Set status code based on overall health
	statusCode := http.StatusOK
	if status.Status == "unhealthy" {
		statusCode = http.StatusServiceUnavailable
	} else if status.Status == "degraded" {
		statusCode = http.StatusOK // Still return 200 for degraded
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(status)
}

// Live health check (basic liveness probe - just checks if service is running)
func (h *Handler) Live(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "alive",
	})
}

// Ready health check (readiness probe - checks dependencies)
func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	ready := true
	statuses := make(map[string]string)

	for name, checker := range h.checkers {
		svc := checker.Check(ctx)
		statuses[name] = svc.Status
		if svc.Status != "healthy" {
			ready = false
		}
	}

	statusCode := http.StatusOK
	if !ready {
		statusCode = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ready":    ready,
		"services": statuses,
	})
}
