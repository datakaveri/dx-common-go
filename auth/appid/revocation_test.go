package appid

import (
	"testing"

	"go.uber.org/zap"

	dxmq "github.com/datakaveri/dx-common-go/messaging/rabbitmq"
)

type recorder struct{ ids []string }

func (r *recorder) Invalidate(appID string) { r.ids = append(r.ids, appID) }

// Java AppIdRevocationConsumer: {"appId": "..."} invalidates that app; a
// blank or unparseable message is logged and dropped.
func TestHandleInvalidatesTheApp(t *testing.T) {
	rec := &recorder{}
	c := NewRevocationConsumer("test.", zap.NewNop(), rec)
	for _, body := range []string{`{"appId":"app-1"}`, `{"appId":"  "}`, `{}`, `not json`} {
		if got := c.handle(t.Context(), dxmq.Delivery{Body: []byte(body)}); got != dxmq.Ack {
			t.Fatalf("%s: outcome %v", body, got)
		}
	}
	if len(rec.ids) != 1 || rec.ids[0] != "app-1" {
		t.Fatalf("invalidated %v", rec.ids)
	}
}
