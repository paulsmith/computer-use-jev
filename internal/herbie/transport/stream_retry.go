package transport

import (
	"context"
	"net/http"

	"github.com/paulsmith/computeruser/internal/herbie/provider"
)

// StreamParser is the protocol-specific state for one HTTP/SSE attempt.
type StreamParser interface {
	Feed(string) error
	Finished() bool
	Finalize() error
	Usage() *provider.StreamUsage
	Response() *provider.StreamResponse
}

type StreamRetryOptions struct {
	Client    Client
	URL       string
	Body      []byte
	Headers   func() http.Header
	Retry     RetryPolicy
	NewParser func(provider.StreamCallback) StreamParser
}

type StreamRetryResult struct {
	Parser   StreamParser
	Response Response
}

// withParserError attaches the parser's usage and response metadata to err.
func withParserError(err error, parser StreamParser) error {
	if usage := parser.Usage(); usage != nil {
		err = provider.WithStreamUsage(err, *usage)
	}
	return provider.WithStreamResponse(err, parser.Response())
}

// withParserResponse attaches the parser's response metadata to err.
func withParserResponse(err error, parser StreamParser) error {
	return provider.WithStreamResponse(err, parser.Response())
}

// RunStreamRetry runs protocol-independent HTTP/SSE attempts until the parser
// completes, the request is not retryable, or the retry policy is exhausted.
func RunStreamRetry(ctx context.Context, o StreamRetryOptions, cb provider.StreamCallback, tick provider.TickFunc) (StreamRetryResult, error) {
	var result StreamRetryResult
	for attempt := range o.Retry.MaxAttempts {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		emitted := false
		parser := o.NewParser(func(event provider.StreamEvent) error {
			emitted = true
			return cb(event)
		})
		result.Parser = parser

		client := o.Client
		client.IdleTimeout = o.Retry.IdleTimeout
		r, err := client.SSE(ctx, o.URL, o.Headers(), o.Body, func(_, data string) error {
			return parser.Feed(data)
		}, tick)
		result.Response = r
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, withParserError(ctxErr, parser)
		}

		success := r.Status >= 200 && r.Status < 300
		if success && parser.Finished() && (err == nil || r.StreamFailure) {
			if err := parser.Finalize(); err != nil {
				return result, withParserError(err, parser)
			}
			return result, nil
		}

		streamFailure := r.StreamFailure || success && err == nil
		if !ShouldRetry(r.Status, r.Body, streamFailure, emitted) || attempt+1 == o.Retry.MaxAttempts {
			if err != nil {
				return result, withParserError(err, parser)
			}
			if success {
				if err := parser.Finalize(); err != nil {
					return result, withParserError(err, parser)
				}
			}
			return result, nil
		}

		delay := o.Retry.Delay(attempt)
		if r.RetryAfter > 0 {
			delay = r.RetryAfter
		}
		if err := ctx.Err(); err != nil {
			return result, withParserError(err, parser)
		}
		if err := cb(provider.StreamEvent{Kind: provider.EventRetry, Attempt: attempt + 1, MaxAttempts: o.Retry.MaxAttempts, Delay: delay, HTTPStatus: r.Status, Usage: parser.Usage(), Response: parser.Response()}); err != nil {
			return result, withParserResponse(err, parser)
		}
		if err := ctx.Err(); err != nil {
			return result, withParserResponse(err, parser)
		}
		if err := Sleep(ctx, delay, tick); err != nil {
			return result, withParserResponse(err, parser)
		}
	}
	return result, nil
}
