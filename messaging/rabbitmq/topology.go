package rabbitmq

import (
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// DeclareExchange declares a named exchange on the broker.
// kind must be one of "direct", "fanout", "topic", or "headers".
func (c *Client) DeclareExchange(name, kind string, durable bool) error {
	ch := c.Channel()
	err := ch.ExchangeDeclare(
		name,
		kind,
		durable,
		false, // autoDelete
		false, // internal
		false, // noWait
		nil,   // args
	)
	if err != nil {
		return fmt.Errorf("rabbitmq.DeclareExchange %q: %w", name, err)
	}
	return nil
}

// DeclareQueue declares a named queue on the broker and returns the Queue descriptor.
func (c *Client) DeclareQueue(name string, durable bool) (amqp.Queue, error) {
	ch := c.Channel()
	q, err := ch.QueueDeclare(
		name,
		durable,
		false, // autoDelete
		false, // exclusive
		false, // noWait
		nil,   // args
	)
	if err != nil {
		return amqp.Queue{}, fmt.Errorf("rabbitmq.DeclareQueue %q: %w", name, err)
	}
	return q, nil
}

// BindQueue binds a queue to an exchange using the supplied routing key.
func (c *Client) BindQueue(queue, exchange, routingKey string) error {
	ch := c.Channel()
	err := ch.QueueBind(
		queue,
		routingKey,
		exchange,
		false, // noWait
		nil,   // args
	)
	if err != nil {
		return fmt.Errorf("rabbitmq.BindQueue %q→%q (key=%q): %w", queue, exchange, routingKey, err)
	}
	return nil
}
