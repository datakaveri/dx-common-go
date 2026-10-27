package rabbitmq

import (
	"errors"
	"net"
	"net/url"
	"strconv"
)

// Broker is a RabbitMQ connection, shared by everything a service publishes
// or consumes — the controlplane's databrokerOptions.
//
// The connection is given EITHER as URL (amqp[s]://user:pass@host:port, no
// vhost) OR as the separate fields (dataBrokerIP, dataBrokerPort,
// dataBrokerUserName, dataBrokerPassword, portSsl), never both. The vhosts are
// separate in both cases, and each user of the broker picks one with URLFor.
// As a separate field the password needs no percent-encoding; URLFor escapes
// it.
type Broker struct {
	URL string `mapstructure:"url"`

	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"` // 0 = 5671 with SSL, 5672 without
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	SSL      bool   `mapstructure:"ssl"` // amqps

	InternalVhost string `mapstructure:"internal_vhost"` // auditing and other platform-internal traffic
	ProdVhost     string `mapstructure:"prod_vhost"`
	ExternalVhost string `mapstructure:"external_vhost"`
}

// Configured reports whether any field is set.
func (b Broker) Configured() bool {
	return b != Broker{}
}

func (b Broker) usesFields() bool {
	return b.Host != "" || b.Port != 0 || b.Username != "" || b.Password != "" || b.SSL
}

// Validate checks that the connection is given exactly one way.
func (b Broker) Validate() error {
	if b.URL != "" {
		if b.usesFields() {
			return errors.New("databroker: set url or host/port/username/password/ssl, not both")
		}
		u, err := url.Parse(b.URL)
		switch {
		case err != nil:
			return errors.New("databroker: url is not a valid URL")
		case u.Scheme != "amqp" && u.Scheme != "amqps":
			return errors.New("databroker: url must start with amqp:// or amqps://")
		case u.Host == "":
			return errors.New("databroker: url has no host")
		case u.Path != "" && u.Path != "/":
			return errors.New("databroker: url must not name a vhost; set internal_vhost/prod_vhost/external_vhost")
		}
		return nil
	}
	switch {
	case b.Host == "":
		return errors.New("databroker: url or host is required")
	case b.Username == "" || b.Password == "":
		return errors.New("databroker: username and password are required")
	case b.Port < 0 || b.Port > 65535:
		return errors.New("databroker: port must be between 1 and 65535")
	}
	return nil
}

// URLFor is the AMQP URL for one of the broker's vhosts, with the credentials
// and vhost escaped.
func (b Broker) URLFor(vhost string) (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	if b.URL != "" {
		u, _ := url.Parse(b.URL) // Validate parsed it
		setVhost(u, vhost)
		return u.String(), nil
	}
	scheme, port := "amqp", b.Port
	if b.SSL {
		scheme = "amqps"
	}
	if port == 0 {
		port = 5672
		if b.SSL {
			port = 5671
		}
	}
	u := url.URL{
		Scheme: scheme,
		User:   url.UserPassword(b.Username, b.Password),
		Host:   net.JoinHostPort(b.Host, strconv.Itoa(port)),
	}
	setVhost(&u, vhost)
	return u.String(), nil
}

// setVhost puts vhost in the URL path. The driver reads the vhost as the path
// after its leading "/", so "/internal" travels as "//internal" and
// "IUDX-V2-INTERNAL" as itself.
func setVhost(u *url.URL, vhost string) {
	u.Path, u.RawPath = "", ""
	if vhost != "" {
		u.Path = "/" + vhost
		u.RawPath = "/" + url.PathEscape(vhost)
	}
}

// InternalURL is the URL for the internal vhost, which auditing publishes on.
func (b Broker) InternalURL() (string, error) {
	if b.InternalVhost == "" {
		return "", errors.New("databroker: internal_vhost is required")
	}
	return b.URLFor(b.InternalVhost)
}
