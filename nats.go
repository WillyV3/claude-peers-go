package main

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
)

const (
	natsDefaultURL = "nats://100.109.211.128:4222"
	streamName     = "FLEET"
)

// Fleet event subjects. All events go under fleet.>
// fleet.peer.joined, fleet.peer.left, fleet.commit, fleet.summary, fleet.message, etc.
var fleetSubjects = []string{"fleet.>"}

func natsURL() string {
	if cfg.NatsURL != "" {
		return cfg.NatsURL
	}
	if cfg.BrokerURL != "" {
		host := cfg.BrokerURL
		for _, prefix := range []string{"http://", "https://"} {
			if len(host) > len(prefix) && host[:len(prefix)] == prefix {
				host = host[len(prefix):]
			}
		}
		for i, c := range host {
			if c == ':' {
				host = host[:i]
				break
			}
		}
		return "nats://" + host + ":4222"
	}
	return natsDefaultURL
}

// FleetEvent is published to NATS when something happens in the fleet.
type FleetEvent struct {
	Type      string `json:"type"`
	PeerID    string `json:"peer_id,omitempty"`
	Machine   string `json:"machine,omitempty"`
	Summary   string `json:"summary,omitempty"`
	CWD       string `json:"cwd,omitempty"`
	Data      string `json:"data,omitempty"`
	Timestamp string `json:"timestamp"`
}

// NATSPublisher connects to NATS and publishes fleet events.
// Used by the broker to dual-write events to both SQLite and NATS.
type NATSPublisher struct {
	nc *nats.Conn
	js nats.JetStreamContext
}

func newNATSPublisher() *NATSPublisher {
	nc, err := nats.Connect(natsURL(),
		nats.Name("claude-peers-broker"),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		log.Printf("[nats] connect failed (non-fatal, events will be SQLite-only): %v", err)
		return nil
	}

	js, err := nc.JetStream()
	if err != nil {
		log.Printf("[nats] jetstream init failed: %v", err)
		nc.Close()
		return nil
	}

	// Create or update the FLEET stream
	_, err = js.AddStream(&nats.StreamConfig{
		Name:       streamName,
		Subjects:   fleetSubjects,
		Retention:  nats.LimitsPolicy,
		MaxAge:     24 * time.Hour,
		Storage:    nats.FileStorage,
		Duplicates: 5 * time.Minute,
	})
	if err != nil {
		log.Printf("[nats] stream create failed: %v", err)
		nc.Close()
		return nil
	}

	log.Printf("[nats] connected to %s, stream %s ready", natsURL(), streamName)
	return &NATSPublisher{nc: nc, js: js}
}

func (p *NATSPublisher) publish(subject string, event FleetEvent) {
	if p == nil {
		return
	}
	event.Timestamp = nowISO()
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	if _, err := p.js.Publish(subject, data); err != nil {
		log.Printf("[nats] publish %s failed: %v", subject, err)
	}
}

func (p *NATSPublisher) close() {
	if p != nil && p.nc != nil {
		p.nc.Close()
	}
}

// subscribeFleet subscribes to the FLEET JetStream with a named durable consumer.
// Each caller should use a unique consumerName to avoid conflicts.
func subscribeFleet(consumerName string, handler func(FleetEvent)) (*nats.Conn, error) {
	nc, err := nats.Connect(natsURL(),
		nats.Name("claude-peers-dream"),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream: %w", err)
	}

	// Durable consumer so we don't miss events between runs
	_, err = js.Subscribe("fleet.>", func(msg *nats.Msg) {
		var event FleetEvent
		if json.Unmarshal(msg.Data, &event) == nil {
			handler(event)
		}
		msg.Ack()
	}, nats.Durable(consumerName), nats.DeliverAll())

	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("subscribe: %w", err)
	}

	return nc, nil
}
