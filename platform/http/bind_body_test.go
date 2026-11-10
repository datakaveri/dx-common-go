package httpx

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// These tests pin ROADMAP P1-3: the operator-configured body limit is the limit
// that applies on the binding path, a custom Binder cannot silently exceed it,
// and two concatenated JSON documents are rejected.

type bodyOnlyReq struct {
	V string `json:"v"`
}

// jsonBodyOfLen builds a valid single-field JSON object of exactly n bytes, so
// a test can sit one byte either side of a limit precisely. The padding is
// plain ASCII, so it needs no escaping and the byte count is exact.
func jsonBodyOfLen(n int) string {
	const overhead = len(`{"v":""}`)
	if n < overhead {
		panic("jsonBodyOfLen: length below the fixed JSON overhead")
	}
	return `{"v":"` + strings.Repeat("a", n-overhead) + `"}`
}

// TestBodyLimitBoundary proves the configured cap is enforced at exactly the
// byte, both ways: a body at the limit binds, one byte over is rejected, and
// the error reports the limit that actually tripped rather than a constant.
func TestBodyLimitBoundary(t *testing.T) {
	const limit = 64

	atLimit := jsonBodyOfLen(limit)
	r := httptest.NewRequest("POST", "/x", strings.NewReader(atLimit))
	r = r.WithContext(withMaxBodyBytes(r.Context(), limit))
	got, err := bind[bodyOnlyReq](r)
	if err != nil {
		t.Fatalf("body of exactly the limit (%d bytes) must bind: %v", limit, err)
	}
	if want := strings.Repeat("a", limit-len(`{"v":""}`)); got.V != want {
		t.Fatalf("value not bound: got %q", got.V)
	}

	over := jsonBodyOfLen(limit + 1)
	r = httptest.NewRequest("POST", "/x", strings.NewReader(over))
	r = r.WithContext(withMaxBodyBytes(r.Context(), limit))
	_, err = bind[bodyOnlyReq](r)
	if err == nil {
		t.Fatalf("body of limit+1 (%d bytes) must be rejected", limit+1)
	}
	if !strings.Contains(err.Error(), "64 byte limit") {
		t.Errorf("error must name the effective limit, got %q", err.Error())
	}
}

// TestBodyLimitReportsRaisedLimit is the operator's-eye view: raising the cap
// on the context raises the effective limit and the message reflects it. This
// is what makes SERVER_MAX_BODY_BYTES observable rather than a hidden constant.
func TestBodyLimitReportsRaisedLimit(t *testing.T) {
	const limit = 4096

	ok := jsonBodyOfLen(2048)
	r := httptest.NewRequest("POST", "/x", strings.NewReader(ok))
	r = r.WithContext(withMaxBodyBytes(r.Context(), limit))
	if _, err := bind[bodyOnlyReq](r); err != nil {
		t.Fatalf("2 KiB body under a 4 KiB limit must bind: %v", err)
	}

	over := jsonBodyOfLen(limit + 100)
	r = httptest.NewRequest("POST", "/x", strings.NewReader(over))
	r = r.WithContext(withMaxBodyBytes(r.Context(), limit))
	_, err := bind[bodyOnlyReq](r)
	if err == nil || !strings.Contains(err.Error(), "4096 byte limit") {
		t.Errorf("want a 4096 byte limit error, got %v", err)
	}
}

// TestNegativeLimitDisablesCap proves a service that streams and bounds its own
// upload can opt out: a negative cap means unlimited, and a large body binds.
func TestNegativeLimitDisablesCap(t *testing.T) {
	big := jsonBodyOfLen(4 << 20) // 4 MiB, well over the default
	r := httptest.NewRequest("POST", "/x", strings.NewReader(big))
	r = r.WithContext(withMaxBodyBytes(r.Context(), 0)) // resolved "disabled" sentinel
	if _, err := bind[bodyOnlyReq](r); err != nil {
		t.Fatalf("a disabled cap (0) must accept a large body: %v", err)
	}
}

// TestBodyDefaultAppliesOffStack proves the fallback: a request that never
// passed through the router still gets the platform default, so a service that
// mounts a handler without NewRouter is not left unbounded.
func TestBodyDefaultAppliesOffStack(t *testing.T) {
	over := jsonBodyOfLen(DefaultMaxBodyBytes + 100)
	r := httptest.NewRequest("POST", "/x", strings.NewReader(over))
	// No withMaxBodyBytes on the context — the off-stack case.
	_, err := bind[bodyOnlyReq](r)
	if err == nil {
		t.Fatal("a body over the default must be rejected even off the router stack")
	}
	if !strings.Contains(err.Error(), "1048576 byte limit") {
		t.Errorf("want the 1 MiB default in the message, got %q", err.Error())
	}

	under := jsonBodyOfLen(1024)
	r = httptest.NewRequest("POST", "/x", strings.NewReader(under))
	if _, err := bind[bodyOnlyReq](r); err != nil {
		t.Fatalf("a small body off-stack must still bind: %v", err)
	}
}

// TestRejectsConcatenatedDocuments closes the request-smuggling shape: a
// decoder stops at the end of the first value, so without the single-document
// check `{...}{...}` and `{...} trailing junk` are silently accepted.
func TestRejectsConcatenatedDocuments(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"two objects", `{"v":"a"}{"v":"b"}`, true},
		{"object then array", `{"v":"a"}[1,2,3]`, true},
		{"object then garbage", `{"v":"a"} not-json`, true},
		{"single object", `{"v":"a"}`, false},
		{"trailing whitespace", "{\"v\":\"a\"}\n\t  ", false},
		{"empty body", ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/x", strings.NewReader(tc.body))
			_, err := bind[bodyOnlyReq](r)
			if tc.wantErr && err == nil {
				t.Fatalf("body %q must be rejected as multiple documents", tc.body)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("body %q must bind: %v", tc.body, err)
			}
			if tc.wantErr && err != nil && !strings.Contains(err.Error(), "single JSON document") {
				t.Errorf("want a single-document error, got %q", err.Error())
			}
		})
	}
}

// TestBodyReaderHonoursLimit proves the custom-Binder path: BodyReader caps at
// the configured limit, so a Binder that reads the body itself cannot exceed
// policy; a disabled cap streams unbounded; and off-stack it applies the
// default.
func TestBodyReaderHonoursLimit(t *testing.T) {
	read := func(body string, install bool, limit int64) (int, error) {
		r := httptest.NewRequest("POST", "/x", strings.NewReader(body))
		if install {
			r = r.WithContext(withMaxBodyBytes(r.Context(), limit))
		}
		n, err := io.Copy(io.Discard, BodyReader(r))
		return int(n), err
	}

	if _, err := read(strings.Repeat("a", 100), true, 32); err == nil {
		t.Error("BodyReader must reject a body over the configured limit")
	}
	if n, err := read(strings.Repeat("a", 20), true, 32); err != nil || n != 20 {
		t.Errorf("BodyReader must pass a body under the limit whole: n=%d err=%v", n, err)
	}
	if n, err := read(strings.Repeat("a", 4<<20), true, 0); err != nil || n != 4<<20 {
		t.Errorf("a disabled cap (0) must stream unbounded: n=%d err=%v", n, err)
	}
	// Off-stack: no context value, so the default applies — a small read is fine.
	if n, err := read(strings.Repeat("a", 100), false, 0); err != nil || n != 100 {
		t.Errorf("off-stack small read must succeed: n=%d err=%v", n, err)
	}
}

func TestResolveMaxBodyBytes(t *testing.T) {
	cases := []struct {
		in, want int64
	}{
		{0, DefaultMaxBodyBytes}, // omitted → default
		{-1, 0},                  // negative → disabled
		{100, 100},               // explicit → itself
	}
	for _, tc := range cases {
		if got := resolveMaxBodyBytes(tc.in); got != tc.want {
			t.Errorf("resolveMaxBodyBytes(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
