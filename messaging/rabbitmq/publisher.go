package rabbitmq

import (
	"encoding/json"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Publish sends a raw byte message to the given exchange with routingKey.
func (c *Client) Publish(exchange, routingKey string, body []byte) error {
	ch := c.Channel()
	err := ch.Publish(
		exchange,
		routingKey,
		false, // mandatory
		false, // immediate
		amqp.Publishing{
			ContentType:  "application/octet-stream",
			DeliveryMode: amqp.Persistent,
			Body:         body,
		},
	)
	if err != nil {
		return fmt.Errorf("rabbitmq.Publish: %w", err)
	}
	return nil
}

// PublishJSON marshals v as JSON and publishes it to exchange / routingKey.
func (c *Client) PublishJSON(exchange, routingKey string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("rabbitmq.PublishJSON: marshal: %w", err)
	}

	ch := c.Channel()
	err = ch.Publish(
		exchange,
		routingKey,
		false,
		false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         data,
		},
	)
	if err != nil {
		return fmt.Errorf("rabbitmq.PublishJSON: publish: %w", err)
	}
	return nil
}
