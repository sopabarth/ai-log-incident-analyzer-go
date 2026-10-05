package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/domain"
)

// outcome is one step's result and what it cost.
type outcome[T any] struct {
	value   T
	latency time.Duration
	retries int
}

// ask runs one pipeline step: it sends the prompts, decodes the reply as T, and
// retries until it gets a usable T or runs out of attempts.
//
//   - A reply that can't be used (ErrMalformedOutput) is retried at once.
//   - A transient provider error (rate limit, 5xx, network trouble) is retried
//     after n*Backoff.
//   - Out of attempts, the step returns fallback - the request still succeeds.
//   - Any other error (rejected API key, bad request) is returned as is: a retry
//     can't fix it, and a fallback would hide a broken deployment.
//   - If ctx ends, ask stops and returns ctx's error without retrying.
//
// T is both what the reply is decoded into and what the fallback is, so the
// loop needs no per-step parsing code - see domain.ParseReply.
func ask[T domain.Reply](ctx context.Context, a *Analyzer, step, system, user string, fallback T) (outcome[T], error) {
	start := time.Now()
	log := a.opts.Logger.With("step", step)

	for attempt := 1; attempt <= a.opts.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return outcome[T]{}, err
		}

		reply, err := a.completer.Complete(ctx, system, user)
		if err == nil {
			var value T
			if value, err = domain.ParseReply[T]([]byte(reply)); err == nil {
				return outcome[T]{value: value, latency: time.Since(start), retries: attempt - 1}, nil
			}
			err = fmt.Errorf("%w: %w", ErrMalformedOutput, err)
		}

		if err := ctx.Err(); err != nil {
			return outcome[T]{}, err // the caller gave up; don't retry
		}

		switch {
		case errors.Is(err, ErrMalformedOutput):
			log.Warn("attempt failed: malformed output", "attempt", attempt, "of", a.opts.MaxAttempts, "error", err)
		case isTransient(err):
			if attempt == a.opts.MaxAttempts {
				log.Warn("attempt failed: transient error", "attempt", attempt, "of", a.opts.MaxAttempts, "error", err)
				break
			}
			delay := retryDelay(err, attempt, a.opts.Backoff)
			log.Warn("attempt failed: transient error, retrying", "attempt", attempt, "of", a.opts.MaxAttempts, "retry_in", delay, "error", err)
			if err := sleep(ctx, delay); err != nil {
				return outcome[T]{}, err
			}
		default:
			return outcome[T]{}, fmt.Errorf("%s: %w", step, err)
		}
	}

	log.Warn("all attempts failed, using fallback", "attempts", a.opts.MaxAttempts)
	return outcome[T]{value: fallback, latency: time.Since(start), retries: a.opts.MaxAttempts - 1}, nil
}

// maxRetryAfter is the longest provider-requested wait we will honor. A rate
// limit that resets in a few seconds is worth waiting out; one that asks for
// minutes is not worth holding a request open for.
const maxRetryAfter = 60 * time.Second

// retryDelay is how long to wait before retry number attempt after a transient
// error: the plain linear backoff, or the provider's own Retry-After when that
// is longer (the backoff is far too short for, say, a tokens-per-minute limit
// that resets in half a minute) and reasonable.
func retryDelay(err error, attempt int, backoff time.Duration) time.Duration {
	delay := time.Duration(attempt) * backoff
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > delay && apiErr.RetryAfter <= maxRetryAfter {
		return apiErr.RetryAfter
	}
	return delay
}

// isTransient reports whether err is the kind of provider trouble that may
// clear up on its own: rate limiting, a server-side error, or a network problem
// (connection refused or reset, a timeout, a response cut short).
func isTransient(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Transient()
	}
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, io.ErrUnexpectedEOF)
}

// sleep waits for d, or returns early with ctx's error if ctx ends first.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
