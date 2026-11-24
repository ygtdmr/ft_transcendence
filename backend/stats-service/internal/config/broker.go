package config

import (
	"errors"
	"os"
	"strings"

	"github.com/melmut-42/ft_transcendence/backend/shared/rabbitmq"
)

// maxQueueName is the AMQP limit for a queue name.
const maxQueueName = 255

// loadBroker reads the settings for consuming Game Session's events.
//
// RABBITMQ_URL and STATS_EVENTS_QUEUE are required in production and must be
// set together elsewhere. The queue and its bindings belong to the broker
// topology; this service only consumes from it. Without them, finalized
// matches and penalties are not ingested.
//
// Errors:
//
//   - Only one of the two settings is present, or neither is set in production.
//   - The URL is not an amqp:// or amqps:// URL with a host, or the queue name
//     is longer than 255 bytes or contains whitespace. Errors never contain the URL.
func (cfg *Config) loadBroker() error {
	cfg.BrokerURL = strings.TrimSpace(os.Getenv("RABBITMQ_URL"))
	cfg.EventsQueue = strings.TrimSpace(os.Getenv("STATS_EVENTS_QUEUE"))

	switch {
	case cfg.BrokerURL == "" && cfg.EventsQueue == "":
		if cfg.Environment == "production" {
			return errors.New("RABBITMQ_URL and STATS_EVENTS_QUEUE are required in production")
		}
		return nil
	case cfg.BrokerURL == "" || cfg.EventsQueue == "":
		return errors.New("RABBITMQ_URL and STATS_EVENTS_QUEUE must be set together")
	}
	if err := (rabbitmq.Config{URL: cfg.BrokerURL}).Validate(); err != nil {
		return errors.New("RABBITMQ_URL must be an amqp:// or amqps:// URL with a host")
	}
	if len(cfg.EventsQueue) > maxQueueName || strings.ContainsFunc(cfg.EventsQueue, isSpace) {
		return errors.New("STATS_EVENTS_QUEUE must be at most 255 bytes without whitespace")
	}

	return nil
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}
