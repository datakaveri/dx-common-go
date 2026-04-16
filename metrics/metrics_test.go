package metrics

import (
	"strings"
	"testing"
	"time"
)

func TestCounter_Inc(t *testing.T) {
	c := NewCounter("test_counter", "A test counter")
	c.Inc()
	c.Inc()

	if c.value != 2 {
		t.Fatalf("expected count=2, got %d", c.value)
	}
}

func TestCounter_Add(t *testing.T) {
	c := NewCounter("test_counter", "A test counter")
	c.Add(10)
	c.Add(5)

	if c.value != 15 {
		t.Fatalf("expected count=15, got %d", c.value)
	}
}

func TestCounter_WithLabel(t *testing.T) {
	c := NewCounter("test_counter", "A test counter")
	c.WithLabel("status", "200")
	c.WithLabel("status", "200")
	c.WithLabel("status", "404")

	if len(c.labels) != 1 {
		t.Fatalf("expected 1 label type, got %d", len(c.labels))
	}

	if c.labels["status"]["200"] != 2 {
		t.Fatalf("expected status:200 count=2, got %d", c.labels["status"]["200"])
	}

	if c.labels["status"]["404"] != 1 {
		t.Fatalf("expected status:404 count=1, got %d", c.labels["status"]["404"])
	}
}

func TestGauge_Set(t *testing.T) {
	g := NewGauge("test_gauge", "A test gauge")
	g.Set(42.5)

	if g.value != 42.5 {
		t.Fatalf("expected value=42.5, got %f", g.value)
	}
}

func TestGauge_Inc(t *testing.T) {
	g := NewGauge("test_gauge", "A test gauge")
	g.Set(10)
	g.Inc()

	if g.value != 11 {
		t.Fatalf("expected value=11, got %f", g.value)
	}
}

func TestGauge_Dec(t *testing.T) {
	g := NewGauge("test_gauge", "A test gauge")
	g.Set(10)
	g.Dec()

	if g.value != 9 {
		t.Fatalf("expected value=9, got %f", g.value)
	}
}

func TestHistogram_Observe(t *testing.T) {
	buckets := []int64{10, 50, 100, 500, 1000}
	h := NewHistogram("test_histogram", "A test histogram", buckets)

	h.Observe(25)
	h.Observe(75)
	h.Observe(150)

	if h.counts[50] != 1 {
		t.Fatalf("expected bucket 50 count=1, got %d", h.counts[50])
	}

	if h.counts[100] != 1 {
		t.Fatalf("expected bucket 100 count=1, got %d", h.counts[100])
	}

	if h.counts[1000] != 1 {
		t.Fatalf("expected bucket 1000 count=1, got %d", h.counts[1000])
	}
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()

	c := r.RegisterCounter("test", "test counter")
	if c == nil {
		t.Fatal("expected counter to be registered")
	}

	retrieved := r.GetCounter("test")
	if retrieved == nil {
		t.Fatal("expected to retrieve registered counter")
	}
}

func TestRegistry_GetNonexistent(t *testing.T) {
	r := NewRegistry()

	retrieved := r.GetCounter("nonexistent")
	if retrieved != nil {
		t.Fatal("expected nil for nonexistent metric")
	}
}

func TestPrometheusFormat_Counter(t *testing.T) {
	r := NewRegistry()
	c := r.RegisterCounter("test_requests", "Total requests")
	c.Inc()
	c.Inc()

	output := r.PrometheusFormat()

	if !strings.Contains(output, "test_requests") {
		t.Fatal("expected metric name in output")
	}

	if !strings.Contains(output, "2") {
		t.Fatal("expected counter value in output")
	}
}

func TestPrometheusFormat_Multiple(t *testing.T) {
	r := NewRegistry()

	r.RegisterCounter("counter1", "Counter 1").Add(5)
	r.RegisterGauge("gauge1", "Gauge 1").Set(42)

	output := r.PrometheusFormat()

	if !strings.Contains(output, "counter1") {
		t.Fatal("expected counter in output")
	}

	if !strings.Contains(output, "gauge1") {
		t.Fatal("expected gauge in output")
	}
}

func TestRequestMetrics_RecordRequest(t *testing.T) {
	r := NewRegistry()
	rm := NewRequestMetrics(r)

	rm.RecordRequest(200, 100*time.Millisecond)
	rm.RecordRequest(404, 50*time.Millisecond)
	rm.RecordRequest(500, 200*time.Millisecond)

	output := r.PrometheusFormat()

	if !strings.Contains(output, "http_requests_total") {
		t.Fatal("expected requests metric in output")
	}

	if !strings.Contains(output, "http_request_duration_ms") {
		t.Fatal("expected duration metric in output")
	}

	if !strings.Contains(output, "http_errors_total") {
		t.Fatal("expected errors metric in output")
	}
}

func TestRequestMetrics_ErrorTracking(t *testing.T) {
	r := NewRegistry()
	rm := NewRequestMetrics(r)

	// Success
	rm.RecordRequest(200, 50*time.Millisecond)

	// Errors
	rm.RecordRequest(400, 100*time.Millisecond)
	rm.RecordRequest(500, 150*time.Millisecond)

	// Should have both request and error counts
	output := r.PrometheusFormat()

	if !strings.Contains(output, "http_errors_total") {
		t.Fatal("expected error tracking")
	}
}

func TestCounter_ThreadSafety(t *testing.T) {
	c := NewCounter("test", "test")

	// Simulate concurrent increments
	done := make(chan bool)
	for i := 0; i < 100; i++ {
		go func() {
			c.Inc()
			done <- true
		}()
	}

	for i := 0; i < 100; i++ {
		<-done
	}

	if c.value != 100 {
		t.Fatalf("expected 100 increments, got %d", c.value)
	}
}

func TestHistogram_AllBuckets(t *testing.T) {
	buckets := []int64{10, 50, 100, 500}
	h := NewHistogram("test", "test", buckets)

	h.Observe(5)   // Goes to first bucket
	h.Observe(25)  // Goes to second bucket
	h.Observe(60)  // Goes to third bucket
	h.Observe(600) // Goes to last bucket

	if h.counts[10] != 1 {
		t.Fatal("bucket 10 should have 1 observation")
	}
	if h.counts[50] != 1 {
		t.Fatal("bucket 50 should have 1 observation")
	}
	if h.counts[100] != 1 {
		t.Fatal("bucket 100 should have 1 observation")
	}
	if h.counts[500] != 1 {
		t.Fatal("bucket 500 should have 1 observation")
	}
}
