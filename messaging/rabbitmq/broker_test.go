package rabbitmq

import (
	"strings"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestBrokerURL(t *testing.T) {
	cases := []struct {
		name  string
		b     Broker
		vhost string
		want  amqp.URI
	}{
		{
			name: "controlplane-style broker, internal vhost, awkward password",
			b: Broker{Host: "databroker.example.org", Port: 24568, Username: "admin",
				Password: "pa%ss#g^:C>x@/", SSL: true, InternalVhost: "IUDX-V2-INTERNAL"},
			vhost: "IUDX-V2-INTERNAL",
			want: amqp.URI{Scheme: "amqps", Host: "databroker.example.org", Port: 24568,
				Username: "admin", Password: "pa%ss#g^:C>x@/", Vhost: "IUDX-V2-INTERNAL"},
		},
		{
			name:  "vhost with a leading slash, default plain port",
			b:     Broker{Host: "rabbitmq", Username: "dx", Password: "devpass"},
			vhost: "/internal",
			want: amqp.URI{Scheme: "amqp", Host: "rabbitmq", Port: 5672,
				Username: "dx", Password: "devpass", Vhost: "/internal"},
		},
		{
			name: "connection as a URL, vhost added",
			b: Broker{URL: "amqps://admin:pa%25ss%23g%5E%3AC%3Ex%40%2F@databroker.example.org:24568",
				InternalVhost: "IUDX-V2-INTERNAL"},
			vhost: "IUDX-V2-INTERNAL",
			want: amqp.URI{Scheme: "amqps", Host: "databroker.example.org", Port: 24568,
				Username: "admin", Password: "pa%ss#g^:C>x@/", Vhost: "IUDX-V2-INTERNAL"},
		},
		{
			name:  "URL with trailing slash, slashed vhost",
			b:     Broker{URL: "amqp://dx:devpass@rabbitmq:5672/"},
			vhost: "/internal",
			want: amqp.URI{Scheme: "amqp", Host: "rabbitmq", Port: 5672,
				Username: "dx", Password: "devpass", Vhost: "/internal"},
		},
		{
			name:  "default TLS port",
			b:     Broker{Host: "rabbitmq", Username: "dx", Password: "x", SSL: true},
			vhost: "internal",
			want: amqp.URI{Scheme: "amqps", Host: "rabbitmq", Port: 5671,
				Username: "dx", Password: "x", Vhost: "internal"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := tc.b.URLFor(tc.vhost)
			if err != nil {
				t.Fatalf("URL: %v", err)
			}
			// The driver's own parser is the contract: what it reads back is
			// what it will dial.
			got, err := amqp.ParseURI(raw)
			if err != nil {
				t.Fatalf("ParseURI: %v", err)
			}
			if got.Scheme != tc.want.Scheme || got.Host != tc.want.Host || got.Port != tc.want.Port ||
				got.Username != tc.want.Username || got.Password != tc.want.Password || got.Vhost != tc.want.Vhost {
				t.Errorf("parsed %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestBrokerInternalURL(t *testing.T) {
	b := Broker{Host: "h", Username: "u", Password: "p", InternalVhost: "IUDX-V2-INTERNAL"}
	raw, err := b.InternalURL()
	if err != nil {
		t.Fatalf("InternalURL: %v", err)
	}
	if got, _ := amqp.ParseURI(raw); got.Vhost != "IUDX-V2-INTERNAL" {
		t.Fatalf("vhost = %q", got.Vhost)
	}
	b.InternalVhost = ""
	if _, err := b.InternalURL(); err == nil || !strings.Contains(err.Error(), "internal_vhost") {
		t.Fatalf("err = %v, want internal_vhost required", err)
	}
}

func TestBrokerValidate(t *testing.T) {
	cases := []struct {
		name    string
		b       Broker
		wantErr string
	}{
		{"complete fields", Broker{Host: "h", Username: "u", Password: "p"}, ""},
		{"url only", Broker{URL: "amqps://u:p@h:24568"}, ""},
		{"url and fields", Broker{URL: "amqps://u:p@h:24568", Host: "h"}, "not both"},
		{"url and a password field", Broker{URL: "amqps://u:p@h:24568", Password: "p"}, "not both"},
		{"url naming a vhost", Broker{URL: "amqps://u:p@h:24568/IUDX-V2-INTERNAL"}, "must not name a vhost"},
		{"url with another scheme", Broker{URL: "http://u:p@h:24568"}, "amqp://"},
		{"url without host", Broker{URL: "amqp://"}, "no host"},
		{"nothing", Broker{}, "url or host"},
		{"no host", Broker{Username: "u", Password: "p"}, "url or host"},
		{"no password", Broker{Host: "h", Username: "u"}, "username and password"},
		{"bad port", Broker{Host: "h", Username: "u", Password: "p", Port: 70000}, "port"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.b.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
	if (Broker{}).Configured() || !(Broker{SSL: true}).Configured() {
		t.Fatal("Configured must be false only for the zero value")
	}
}
