package grpc

import (
	"net/http"
	"strings"

	"google.golang.org/grpc/metadata"
)

// The two directions of one projection, kept together on purpose.
//
// Subject identity (transport/headers) and workload identity
// (platform/security/workload) were built as HTTP concepts. Rather than give
// gRPC its own canonical string, its own signer and its own enforcement policy
// — three things that would then drift — internal gRPC calls carry the SAME
// signed headers as metadata, and both ends project between the two shapes
// here. A caller signs with transport/headers.Sign exactly as it would for
// HTTP, and the server verifies with transport/headers.Verify exactly as it
// would for HTTP.
//
// If these two functions ever disagree, every internal call silently loses its
// identity while still compiling — which is why they live in one file with one
// round-trip test rather than one in the client and one in the server.

// HeaderToMD converts signed HTTP headers into outgoing gRPC metadata.
//
// Metadata keys must be lowercase; gRPC rejects uppercase ones at the
// transport, so the conversion is not merely cosmetic.
func HeaderToMD(h http.Header) metadata.MD {
	md := metadata.MD{}
	for k, vs := range h {
		for _, v := range vs {
			md.Append(strings.ToLower(k), v)
		}
	}
	return md
}

// MDToHeader converts incoming gRPC metadata into an http.Header, so the
// existing HTTP-shaped verifiers can read it unchanged.
//
// http.Header canonicalises on Add, so `x-subject-id` arrives as
// `X-Subject-Id` and Get finds it under either spelling.
func MDToHeader(md metadata.MD) http.Header {
	h := make(http.Header, len(md))
	for k, vs := range md {
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	return h
}
