package rabbitmq

import "time"

// Config holds the settings for connecting to RabbitMQ.
type Config struct {
	// URL is the AMQP connection URL, e.g. amqp://guest:guest@localhost:5672/
	URL            string        `mapstructure:"url"`
	ReconnectDelay time.Duration `mapstructure:"reconnect_delay"`
	Exchange       string        `mapstructure:"exchange"`
	ExchangeType   string        `mapstructure:"exchange_type"` // direct, fanout, topic, headers
}
