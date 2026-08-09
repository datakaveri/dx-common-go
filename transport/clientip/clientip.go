// Package clientip derives the caller's address in a way that a caller cannot
// choose for itself.
//
// # The defect this exists to remove (ROADMAP P1-4)
//
// Two places in this library decided "who is this" from `X-Forwarded-For`:
//
//   - middleware/ratelimit.go keyed IP-based limiting on the raw header, so a
//     client that sent its own X-Forwarded-For got a FRESH BUCKET PER REQUEST.
//     Rate limiting was bypassable by anyone who read the source.
//   - auditing/middleware.go recorded RemoteAddr, which chi's RealIP has already
//     rewritten from that same client-controlled header — so the audit trail
//     recorded whatever address the caller asked it to.
//
// Both were found when chi v5.3.0 deprecated RealIP for exactly this
// (GHSA-3fxj-6jh8-hvhx and related).
//
// # Why the rightmost entry, not the leftmost
//
// X-Forwarded-For is a list, appended to by each proxy: the LEFTMOST entry is
// whatever the original client claimed, and anything can claim anything. Each
// proxy appends the address IT observed, so the RIGHTMOST entry is the one
// written by the hop closest to us — the only entry in the list that a remote
// client cannot write.
//
// So the client address is found by walking from the right, skipping one entry
// per proxy we actually run in front of the service. chi's RealIP takes the
// leftmost, which is the one entry guaranteed to be attacker-controlled.
//
// # Why the peer address alone is not the answer
//
// Behind a load balancer the transport peer is always the load balancer, so
// keying on it puts every caller in the world in one bucket. That is not
// "secure by default", it is a different outage. The header has to be used; it
// just has to be used from the correct end, with an explicit count of how many
// hops are ours.
package clientip

import (
	"context"
	"net"
	"net/http"
	"strings"
)

// DefaultTrustedHops is how many proxies are assumed to sit in front of a
// service when nothing says otherwise.
//
// One, because every deployment of this platform has exactly one: the ingress.
// It is deliberately not zero — zero means "trust no header", which behind an
// ingress collapses every caller onto the ingress's own address and makes
// per-client limiting meaningless. A wrong value here is a correctness bug in
// one direction or the other, so it is named, defaulted, and configurable
// rather than implied.
const DefaultTrustedHops = 1

type peerKey struct{}

// WithPeer records the TRANSPORT peer address — the actual TCP source — on the
// context.
//
// It exists because chi's RealIP overwrites r.RemoteAddr with a header value,
// destroying the one address that cannot be forged. Anything installing RealIP
// must install Capture BEFORE it, or the fallback path below silently becomes
// attacker-controlled too.
func WithPeer(ctx context.Context, addr string) context.Context {
	return context.WithValue(ctx, peerKey{}, addr)
}

// Peer returns the captured transport peer, or "" when Capture did not run.
func Peer(ctx context.Context) string {
	addr, _ := ctx.Value(peerKey{}).(string)
	return addr
}

// Capture is middleware that records the transport peer before anything can
// rewrite it. Install it FIRST, ahead of chi's RealIP.
func Capture(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(WithPeer(r.Context(), r.RemoteAddr)))
	})
}

// From returns the caller's IP, trusting exactly hops proxies.
//
// hops <= 0 uses DefaultTrustedHops. The result has no port and is "" only when
// nothing at all is known about the caller.
func From(r *http.Request, hops int) string {
	if hops <= 0 {
		hops = DefaultTrustedHops
	}

	// The transport peer, preferring the captured one: r.RemoteAddr may already
	// have been rewritten from a header by the time this runs.
	peer := Peer(r.Context())
	if peer == "" {
		peer = r.RemoteAddr
	}
	peer = stripPort(peer)

	entries := forwardedFor(r)
	if len(entries) == 0 {
		return peer
	}

	// Walk from the right, skipping the hops we operate. Each of those entries
	// was written by one of our own proxies; the next one left is the furthest
	// address we have any reason to believe.
	idx := len(entries) - hops
	if idx < 0 {
		// The client sent FEWER entries than we have proxies, which means the
		// list is shorter than it should be and we cannot identify a hop we
		// trust. Fall back to the peer rather than reading an entry the client
		// may have written — this is the direction to fail in, because the
		// alternative is honouring a forged address.
		return peer
	}
	if ip := normalise(entries[idx]); ip != "" {
		return ip
	}
	return peer
}

// forwardedFor returns the X-Forwarded-For entries in order, left to right.
func forwardedFor(r *http.Request) []string {
	var out []string
	// Multiple X-Forwarded-For headers are equivalent to one comma-joined
	// header, and proxies do emit them separately. Reading only Get() would
	// silently ignore every entry after the first header.
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// normalise validates an entry as an IP and strips any port.
//
// Validation matters: an unparseable entry is not a client address, and using
// it as a rate-limit key would let a caller mint arbitrary keys with arbitrary
// junk — the bypass again, one layer along.
func normalise(s string) string {
	s = stripPort(strings.TrimSpace(s))
	if net.ParseIP(s) == nil {
		return ""
	}
	return s
}

func stripPort(s string) string {
	if s == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		return host
	}
	// No port, or an IPv6 literal without brackets.
	return strings.Trim(s, "[]")
}
