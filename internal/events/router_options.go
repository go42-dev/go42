package events

import (
	"log/slog"
	"time"
)

type Option func(*Router)

func WithLogger(logger *slog.Logger) Option {
	return func(r *Router) {
		r.logger = logger
	}
}

// WithMaxRetries sets the number of consumer retries after the first attempt.
// Zero disables retries.
func WithMaxRetries(maxRetries int) Option {
	return func(r *Router) {
		r.maxRetries = maxRetries
	}
}

// WithInitialBackoff sets the initial backoff interval for consumer retries.
func WithInitialBackoff(backoff time.Duration) Option {
	return func(r *Router) {
		r.initialBackoff = backoff
	}
}

// WithMaxBackoff sets the maximum backoff interval for consumer retries.
func WithMaxBackoff(backoff time.Duration) Option {
	return func(r *Router) {
		r.maxBackoff = backoff
	}
}

// WithDeadLetterTopicSuffix sets the suffix appended to topics for failed messages.
func WithDeadLetterTopicSuffix(suffix string) Option {
	return func(r *Router) {
		r.deadLetterTopicSuffix = suffix
	}
}

// WithCloseTimeout sets how long shutdown waits for message handlers.
// Zero uses the default of 30 seconds.
func WithCloseTimeout(timeout time.Duration) Option {
	return func(r *Router) {
		r.closeTimeout = timeout
	}
}

// WithPublishMaxInflight sets the maximum number of active broker calls shared by
// regular and dead-letter publishing. It must be positive and defaults to 32.
// Calls that outlive their context occupy capacity until the backend returns.
func WithPublishMaxInflight(limit int) Option {
	return func(r *Router) {
		r.publishMaxInflight = limit
	}
}
