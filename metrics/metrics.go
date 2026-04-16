package metrics

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Counter metric
type Counter struct {
	name  string
	help  string
	value int64
	mu    sync.RWMutex
	labels map[string]map[string]int64
}

// NewCounter creates a new counter metric
func NewCounter(name string, help string) *Counter {
	return &Counter{
		name:   name,
		help:   help,
		value:  0,
		labels: make(map[string]map[string]int64),
	}
}

// Inc increments the counter
func (c *Counter) Inc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value++
}

// Add adds a value to the counter
func (c *Counter) Add(delta int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value += delta
}

// WithLabel increments counter for a specific label
func (c *Counter) WithLabel(labelName string, labelValue string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.labels[labelName] == nil {
		c.labels[labelName] = make(map[string]int64)
	}
	c.labels[labelName][labelValue]++
}

// Gauge metric
type Gauge struct {
	name  string
	help  string
	value float64
	mu    sync.RWMutex
}

// NewGauge creates a new gauge metric
func NewGauge(name string, help string) *Gauge {
	return &Gauge{
		name:  name,
		help:  help,
		value: 0,
	}
}

// Set sets the gauge value
func (g *Gauge) Set(value float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.value = value
}

// Inc increments the gauge
func (g *Gauge) Inc() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.value++
}

// Dec decrements the gauge
func (g *Gauge) Dec() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.value--
}

// Histogram metric
type Histogram struct {
	name    string
	help    string
	buckets []int64
	counts  map[int64]int64
	mu      sync.RWMutex
}

// NewHistogram creates a new histogram metric
func NewHistogram(name string, help string, buckets []int64) *Histogram {
	counts := make(map[int64]int64)
	for _, bucket := range buckets {
		counts[bucket] = 0
	}

	return &Histogram{
		name:    name,
		help:    help,
		buckets: buckets,
		counts:  counts,
	}
}

// Observe records a value in the histogram
func (h *Histogram) Observe(value int64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Find the appropriate bucket
	for _, bucket := range h.buckets {
		if value <= bucket {
			h.counts[bucket]++
			return
		}
	}
	// If value exceeds all buckets, count in last bucket
	if len(h.buckets) > 0 {
		h.counts[h.buckets[len(h.buckets)-1]]++
	}
}

// Registry manages all metrics
type Registry struct {
	counters   map[string]*Counter
	gauges     map[string]*Gauge
	histograms map[string]*Histogram
	mu         sync.RWMutex
}

// NewRegistry creates a new metrics registry
func NewRegistry() *Registry {
	return &Registry{
		counters:    make(map[string]*Counter),
		gauges:      make(map[string]*Gauge),
		histograms: make(map[string]*Histogram),
	}
}

// RegisterCounter registers a new counter
func (r *Registry) RegisterCounter(name string, help string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()

	counter := NewCounter(name, help)
	r.counters[name] = counter
	return counter
}

// RegisterGauge registers a new gauge
func (r *Registry) RegisterGauge(name string, help string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()

	gauge := NewGauge(name, help)
	r.gauges[name] = gauge
	return gauge
}

// RegisterHistogram registers a new histogram
func (r *Registry) RegisterHistogram(name string, help string, buckets []int64) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()

	histogram := NewHistogram(name, help, buckets)
	r.histograms[name] = histogram
	return histogram
}

// GetCounter gets a counter by name
func (r *Registry) GetCounter(name string) *Counter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.counters[name]
}

// GetGauge gets a gauge by name
func (r *Registry) GetGauge(name string) *Gauge {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.gauges[name]
}

// GetHistogram gets a histogram by name
func (r *Registry) GetHistogram(name string) *Histogram {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.histograms[name]
}

// PrometheusFormat formats metrics in Prometheus text format
func (r *Registry) PrometheusFormat() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	output := ""

	// Counter metrics
	for name, counter := range r.counters {
		counter.mu.RLock()
		output += fmt.Sprintf("# HELP %s %s\n", name, counter.help)
		output += fmt.Sprintf("# TYPE %s counter\n", name)
		output += fmt.Sprintf("%s %d\n", name, counter.value)
		counter.mu.RUnlock()
	}

	// Gauge metrics
	for name, gauge := range r.gauges {
		gauge.mu.RLock()
		output += fmt.Sprintf("# HELP %s %s\n", name, gauge.help)
		output += fmt.Sprintf("# TYPE %s gauge\n", name)
		output += fmt.Sprintf("%s %f\n", name, gauge.value)
		gauge.mu.RUnlock()
	}

	// Histogram metrics
	for name, histogram := range r.histograms {
		histogram.mu.RLock()
		output += fmt.Sprintf("# HELP %s %s\n", name, histogram.help)
		output += fmt.Sprintf("# TYPE %s histogram\n", name)
		for _, bucket := range histogram.buckets {
			count := histogram.counts[bucket]
			output += fmt.Sprintf("%s_bucket{le=\"%d\"} %d\n", name, bucket, count)
		}
		histogram.mu.RUnlock()
	}

	return output
}

// Handler provides HTTP endpoint for Prometheus metrics
type Handler struct {
	registry *Registry
}

// NewHandler creates a new metrics handler
func NewHandler(registry *Registry) *Handler {
	return &Handler{
		registry: registry,
	}
}

// ServeHTTP serves metrics in Prometheus format
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, h.registry.PrometheusFormat())
}

// RequestMetrics tracks HTTP request metrics
type RequestMetrics struct {
	requestCount    *Counter
	requestDuration *Histogram
	errorCount      *Counter
}

// NewRequestMetrics creates request metrics
func NewRequestMetrics(registry *Registry) *RequestMetrics {
	return &RequestMetrics{
		requestCount:    registry.RegisterCounter("http_requests_total", "Total HTTP requests"),
		requestDuration: registry.RegisterHistogram("http_request_duration_ms", "HTTP request duration in milliseconds", []int64{10, 50, 100, 500, 1000, 5000}),
		errorCount:      registry.RegisterCounter("http_errors_total", "Total HTTP errors"),
	}
}

// RecordRequest records a request metric
func (rm *RequestMetrics) RecordRequest(statusCode int, duration time.Duration) {
	rm.requestCount.WithLabel("status", strconv.Itoa(statusCode))

	// Record duration in milliseconds
	durationMs := int64(duration.Milliseconds())
	rm.requestDuration.Observe(durationMs)

	// Track errors
	if statusCode >= 400 {
		rm.errorCount.WithLabel("status", strconv.Itoa(statusCode))
	}
}
