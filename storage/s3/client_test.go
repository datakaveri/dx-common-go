package s3

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		useSSL   bool
		want     string
	}{
		{name: "empty is AWS default", endpoint: "", useSSL: true, want: ""},
		{name: "bare host, no ssl", endpoint: "minio:9000", useSSL: false, want: "http://minio:9000"},
		{name: "bare host, ssl", endpoint: "minio:9000", useSSL: true, want: "https://minio:9000"},
		{name: "full http url kept verbatim", endpoint: "http://minio:9000", useSSL: true, want: "http://minio:9000"},
		{name: "full https url kept verbatim", endpoint: "https://s3.example.com", useSSL: false, want: "https://s3.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeEndpoint(tt.endpoint, tt.useSSL); got != tt.want {
				t.Fatalf("normalizeEndpoint(%q, %v) = %q, want %q", tt.endpoint, tt.useSSL, got, tt.want)
			}
		})
	}
}

func TestWithDelimiter(t *testing.T) {
	var o listOptions
	WithDelimiter("/")(&o)
	if o.delimiter != "/" {
		t.Fatalf("WithDelimiter did not set delimiter: got %q", o.delimiter)
	}
}

func TestIsNotFound(t *testing.T) {
	if !IsNotFound(fmt.Errorf("wrapped: %w", &types.NotFound{})) {
		t.Error("a wrapped NotFound must be recognised")
	}
	if !IsNotFound(&types.NoSuchKey{}) {
		t.Error("NoSuchKey must be recognised")
	}
	if IsNotFound(errors.New("connection refused")) || IsNotFound(nil) {
		t.Error("a transport failure is not a not-found")
	}
}
