package clientip

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ROADMAP P1-4. The property under test is one sentence: A CALLER MUST NOT BE
// ABLE TO CHOOSE THE ADDRESS IT IS IDENTIFIED BY.
//
// Everything else here is a way of trying to break that.

func request(peer string, xff ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = peer
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	// Capture normally runs as middleware; do the same thing here so the
	// fallback path is the one production uses.
	return r.WithContext(WithPeer(r.Context(), peer))
}

func TestFrom(t *testing.T) {
	tests := []struct {
		name string
		peer string
		xff  []string
		hops int
		want string
		why  string
	}{
		{
			name: "no header falls back to the transport peer",
			peer: "203.0.113.9:4321", hops: 1, want: "203.0.113.9",
			why: "a direct caller is identified by the only thing there is",
		},
		{
			name: "one proxy: the entry it appended",
			peer: "10.0.0.1:80", xff: []string{"198.51.100.7"}, hops: 1,
			want: "198.51.100.7",
			why:  "the ingress observed the client and wrote this",
		},
		{
			name: "SPOOFED left entry is ignored",
			peer: "10.0.0.1:80", xff: []string{"1.2.3.4, 198.51.100.7"}, hops: 1,
			want: "198.51.100.7",
			why: "the client prepended 1.2.3.4 to choose its own identity; the rightmost " +
				"entry is the one our own proxy wrote and the only one it cannot forge",
		},
		{
			name: "a long forged chain is still ignored",
			peer: "10.0.0.1:80",
			xff:  []string{"9.9.9.9, 8.8.8.8, 7.7.7.7, 198.51.100.7"}, hops: 1,
			want: "198.51.100.7",
			why:  "length is not evidence; only position from the right is",
		},
		{
			name: "two proxies: skip both",
			peer: "10.0.0.1:80", xff: []string{"1.2.3.4, 198.51.100.7, 10.0.0.9"}, hops: 2,
			want: "198.51.100.7",
			why:  "the last entry is the inner proxy, the one before it is the client",
		},
		{
			name: "separate header lines are one list",
			peer: "10.0.0.1:80", xff: []string{"1.2.3.4", "198.51.100.7"}, hops: 1,
			want: "198.51.100.7",
			why: "proxies may emit X-Forwarded-For as repeated headers; reading only the " +
				"first would ignore every entry the real proxies added",
		},
		{
			name: "fewer entries than proxies falls back to the peer",
			peer: "10.0.0.1:80", xff: []string{"1.2.3.4"}, hops: 3,
			want: "10.0.0.1",
			why: "the list is shorter than our own hop count, so no entry in it was " +
				"written by a proxy we trust; honouring one would honour a forgery",
		},
		{
			name: "garbage in the trusted position falls back to the peer",
			peer: "10.0.0.1:80", xff: []string{"1.2.3.4, not-an-ip"}, hops: 1,
			want: "10.0.0.1",
			why: "an unparseable entry is not an address; using it as a rate-limit key " +
				"would let a caller mint arbitrary keys",
		},
		{
			name: "ports are stripped",
			peer: "10.0.0.1:80", xff: []string{"198.51.100.7:5555"}, hops: 1,
			want: "198.51.100.7",
			why:  "otherwise one client gets a new identity per source port",
		},
		{
			name: "IPv6 peer without a port",
			peer: "2001:db8::1", hops: 1, want: "2001:db8::1",
			why: "httptest and some servers hand over a bare IPv6 literal",
		},
		{
			name: "zero hops uses the default",
			peer: "10.0.0.1:80", xff: []string{"1.2.3.4, 198.51.100.7"}, hops: 0,
			want: "198.51.100.7",
			why:  "an unset value must not mean 'trust the whole header'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := From(request(tt.peer, tt.xff...), tt.hops)
			if got != tt.want {
				t.Errorf("From = %q, want %q — %s", got, tt.want, tt.why)
			}
		})
	}
}

// TestACallerCannotChooseItsOwnIdentity is the bypass, expressed directly.
//
// One client, many self-declared addresses, behind one proxy. Every request
// must resolve to the SAME identity, or rate limiting keyed on it is a
// formality: a fresh bucket per request is no bucket at all.
func TestACallerCannotChooseItsOwnIdentity(t *testing.T) {
	const realClient = "198.51.100.7"
	forgeries := []string{
		"1.2.3.4",
		"9.9.9.9, 8.8.8.8",
		"127.0.0.1",
		"", // no forgery at all
	}

	seen := map[string]int{}
	for _, forged := range forgeries {
		xff := realClient
		if forged != "" {
			xff = forged + ", " + realClient
		}
		seen[From(request("10.0.0.1:80", xff), 1)]++
	}

	if len(seen) != 1 {
		t.Fatalf("one client resolved to %d different identities (%v) — it can choose its "+
			"own, so a per-client limit is bypassable by varying a header", len(seen), seen)
	}
	if _, ok := seen[realClient]; !ok {
		t.Errorf("resolved identities = %v, want only %q", seen, realClient)
	}
}

// TestCaptureRunsBeforeRemoteAddrIsRewritten pins the ordering the stack
// depends on.
//
// chi's RealIP overwrites RemoteAddr with a header value. If Capture runs after
// it, the "transport peer" fallback is itself attacker-controlled and every
// fallback path in this package silently becomes forgeable — without any test
// failing, because the value still looks like an address.
func TestCaptureRunsBeforeRemoteAddrIsRewritten(t *testing.T) {
	var got string
	// Stand in for RealIP: rewrite RemoteAddr from the header.
	rewrite := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.RemoteAddr = r.Header.Get("X-Forwarded-For")
			next.ServeHTTP(w, r)
		})
	}
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// hops=3 with one entry forces the peer fallback, which is the value
		// under test.
		got = From(r, 3)
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:80"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")

	Capture(rewrite(inner)).ServeHTTP(httptest.NewRecorder(), r)

	if got != "10.0.0.1" {
		t.Errorf("peer fallback = %q, want the real transport peer 10.0.0.1 — Capture must "+
			"run BEFORE anything rewrites RemoteAddr, or the fallback is forgeable too", got)
	}
}
