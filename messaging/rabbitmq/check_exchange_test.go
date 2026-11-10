package rabbitmq

import (
	"context"
	"errors"
	"testing"
)

func TestCheckExchangeWithoutConnection(t *testing.T) {
	p := &ReliablePublisher{}
	if err := p.CheckExchange(context.Background(), "Email-go"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("err = %v, want ErrNotConnected", err)
	}
	if err := p.CheckExchange(context.Background(), ""); err != nil {
		t.Fatalf("the default exchange always exists, got %v", err)
	}
}
