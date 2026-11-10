package response

import (
	"errors"
	"net/http/httptest"
	"testing"
)

var testOffers = []Offer{
	{MediaType: "application/geo+json", Format: "geojson"},
	{MediaType: "application/json", Format: "json"},
	{MediaType: "text/html", Format: "html"},
}

func TestNegotiate_FormatOverrideWins(t *testing.T) {
	r := httptest.NewRequest("GET", "/x?f=html", nil)
	r.Header.Set("Accept", "application/json") // f= must win
	got, err := Negotiate(r, testOffers)
	if err != nil || got.Format != "html" {
		t.Fatalf("got %+v err %v", got, err)
	}

	r = httptest.NewRequest("GET", "/x?f=csv", nil)
	if _, err := Negotiate(r, testOffers); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("unsupported f must error, got %v", err)
	}
}

func TestNegotiate_AcceptHeader(t *testing.T) {
	cases := []struct {
		name   string
		accept string
		want   string
		hasErr bool
	}{
		{"empty accept defaults to first offer", "", "geojson", false},
		{"exact match", "application/json", "json", false},
		{"q-values order", "application/json;q=0.5, text/html;q=0.9", "html", false},
		{"type wildcard", "application/*", "geojson", false},
		{"full wildcard", "*/*", "geojson", false},
		{"unsatisfiable", "image/png", "", true},
		{"q=0 excludes", "text/html;q=0", "", true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/x", nil)
			if tt.accept != "" {
				r.Header.Set("Accept", tt.accept)
			}
			got, err := Negotiate(r, testOffers)
			if tt.hasErr {
				if !errors.Is(err, ErrNotAcceptable) {
					t.Fatalf("want ErrNotAcceptable, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Format != tt.want {
				t.Fatalf("format = %q, want %q", got.Format, tt.want)
			}
		})
	}
}
