package rabbitmq

import (
	"fmt"
)

// Consume starts consuming messages from queue in a background goroutine.
// For each delivery, handler is called with the message body.
// On success (nil error) the message is ack'd; on failure it is nack'd with requeue=false.
// Consume returns immediately after registering the consumer; use Close to stop.
func (c *Client) Consume(queue string, handler func([]byte) error) error {
	ch := c.Channel()

	deliveries, err := ch.Consume(
		queue,
		"",    // consumer tag (auto-generated)
		false, // auto-ack — we handle ack/nack manually
		false, // exclusive
		false, // no-local
		false, // no-wait
		nil,   // args
	)
	if err != nil {
		return fmt.Errorf("rabbitmq.Consume: register consumer: %w", err)
	}

	go func() {
		for d := range deliveries {
			if err := handler(d.Body); err != nil {
				// Nack without requeue so poison messages don't loop forever.
				_ = d.Nack(false, false)
			} else {
				_ = d.Ack(false)
			}
		}
	}()

	return nil
}
