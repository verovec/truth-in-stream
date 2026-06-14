package mqmetrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// BrokerCreds carries the management-API host and basic-auth credentials parsed
// from the AMQP connection-URL secret.
type BrokerCreds struct {
	Host     string
	Username string
	Password string
}

// ParseBrokerURL extracts the management-API host and credentials from the AMQP
// connection URL stored in Secrets Manager, e.g.
// amqps://app:pw@b-xxx.mq.eu-west-3.amazonaws.com:5671/. The AMQP port is dropped
// because the RabbitMQ management API is served on a separate HTTPS port.
func ParseBrokerURL(raw string) (BrokerCreds, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return BrokerCreds{}, fmt.Errorf("mqmetrics: parse broker url: %w", err)
	}
	if u.Hostname() == "" {
		return BrokerCreds{}, errors.New("mqmetrics: broker url has no host")
	}
	if u.User == nil {
		return BrokerCreds{}, errors.New("mqmetrics: broker url has no credentials")
	}
	password, ok := u.User.Password()
	if !ok {
		return BrokerCreds{}, errors.New("mqmetrics: broker url has no password")
	}
	return BrokerCreds{Host: u.Hostname(), Username: u.User.Username(), Password: password}, nil
}

// Client polls a RabbitMQ management API over HTTP basic auth.
type Client struct {
	HTTP    *http.Client
	BaseURL string // scheme + host + port, e.g. https://b-xxx.mq.eu-west-3.amazonaws.com:443
	Creds   BrokerCreds
}

// FetchQueues returns every queue the management API reports across all vhosts.
func (c *Client) FetchQueues(ctx context.Context) ([]APIQueue, error) {
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/api/queues"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("mqmetrics: build request: %w", err)
	}
	req.SetBasicAuth(c.Creds.Username, c.Creds.Password)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mqmetrics: fetch queues: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("mqmetrics: management api status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var queues []APIQueue
	if err := json.NewDecoder(resp.Body).Decode(&queues); err != nil {
		return nil, fmt.Errorf("mqmetrics: decode queues: %w", err)
	}
	return queues, nil
}
