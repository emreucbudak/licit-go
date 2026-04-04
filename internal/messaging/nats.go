package messaging

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
)

type Client struct {
	conn *nats.Conn
	js   nats.JetStreamContext
}

func NewClient(url string) (*Client, error) {
	opts := []nats.Option{
		nats.Name("licit-go"),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			slog.Warn("NATS disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			slog.Info("NATS reconnected", "url", c.ConnectedUrl())
		}),
	}

	conn, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}

	js, err := conn.JetStream()
	if err != nil {
		return nil, fmt.Errorf("nats jetstream: %w", err)
	}

	if err := ensureStreams(js); err != nil {
		return nil, fmt.Errorf("nats streams: %w", err)
	}

	slog.Info("NATS connected", "url", conn.ConnectedUrl())
	return &Client{conn: conn, js: js}, nil
}

func ensureStreams(js nats.JetStreamContext) error {
	streams := []struct {
		name     string
		subjects []string
	}{
		{name: "BIDDING", subjects: []string{"licit.bid.>"}},
		{name: "AUCTION", subjects: []string{"licit.auction.>"}},
		{name: "PAYMENT", subjects: []string{"licit.payment.>"}},
	}

	for _, s := range streams {
		_, err := js.AddStream(&nats.StreamConfig{
			Name:      s.name,
			Subjects:  s.subjects,
			Retention: nats.WorkQueuePolicy,
			MaxAge:    24 * time.Hour,
			Storage:   nats.FileStorage,
		})
		if err != nil {
			return fmt.Errorf("create stream %s: %w", s.name, err)
		}
	}
	return nil
}

// Publish sends a message to a NATS subject via JetStream.
func (c *Client) Publish(subject string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	_, err = c.js.Publish(subject, payload)
	return err
}

// Subscribe creates a durable pull subscription on the given subject.
func (c *Client) Subscribe(subject string, handler func(msg *nats.Msg)) (*nats.Subscription, error) {
	return c.conn.Subscribe(subject, handler)
}

// QueueSubscribe creates a queue subscription for load-balanced consumption.
func (c *Client) QueueSubscribe(subject, queue string, handler func(msg *nats.Msg)) (*nats.Subscription, error) {
	return c.conn.QueueSubscribe(subject, queue, handler)
}

// Request sends a request and waits for a reply (request-reply pattern).
func (c *Client) Request(subject string, data any, timeout time.Duration) (*nats.Msg, error) {
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	return c.conn.Request(subject, payload, timeout)
}

func (c *Client) Conn() *nats.Conn {
	return c.conn
}

func (c *Client) Close() {
	if err := c.conn.Drain(); err != nil {
		slog.Error("NATS drain error", "error", err)
	}
}
