package requests

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"
)

// Timeout sets the request timeout. A negative duration records an
// ErrInvalidConfigValue returned by Send or SendStream before dispatch.
func (b *RequestBuilder) Timeout(timeout time.Duration) *RequestBuilder {
	if err := validateDurationOption("Timeout", timeout); err != nil {
		b.setPreparationError(err)
		return b
	}
	b.timeout = timeout
	return b
}

// MaxResponseBodyBytes limits the bytes buffered by Send for this request.
// Zero leaves the buffered response size unlimited.
// A negative value records an ErrInvalidConfigValue returned by Send or
// SendStream before body preparation or dispatch.
func (b *RequestBuilder) MaxResponseBodyBytes(maxBytes int64) *RequestBuilder {
	if maxBytes < 0 {
		b.setPreparationError(invalidOptionValue("MaxResponseBodyBytes"))
		return b
	}
	b.maxResponseBodyBytes = maxBytes
	return b
}

// Retry sets the request-local retry policy, replacing the client policy.
// A negative Max records an ErrInvalidConfigValue returned before dispatch.
func (b *RequestBuilder) Retry(policy RetryPolicy) *RequestBuilder {
	if err := validateIntOption("Retry.Max", policy.Max); err != nil {
		b.setPreparationError(err)
		return b
	}
	b.retryPolicy = policy
	b.hasRetryPolicy = true
	return b
}

// NoRetry disables retries for this request.
func (b *RequestBuilder) NoRetry() *RequestBuilder {
	return b.Retry(RetryPolicy{})
}

func (b *RequestBuilder) effectiveRetryPolicy(snap *clientSnapshot) RetryPolicy {
	policy := snap.retry
	if b.hasRetryPolicy {
		policy = b.retryPolicy
	}
	return policy.normalize()
}

func (b *RequestBuilder) do(ctx context.Context, req *http.Request, snap *clientSnapshot) (*http.Response, int, error) {
	attempts := 0

	finalHandler := MiddlewareHandlerFunc(func(req *http.Request) (*http.Response, error) {
		retry := b.effectiveRetryPolicy(snap)

		var errs []error
		var resp *http.Response
		for attempt := range retry.Max + 1 {
			if attempt > 0 {
				if err := resetRequestBody(req); err != nil {
					return resp, err
				}
			}

			var err error
			attempts++
			resp, err = snap.httpClient.Do(req)
			diagnosticErr := sanitizeURLDiagnosticError(err)

			if err != nil {
				errs = append(errs, fmt.Errorf("attempt %d/%d: %w", attempt+1, retry.Max+1, diagnosticErr))
			}

			shouldRetry := retry.ShouldRetry(req, resp, err)
			if !shouldRetry || attempt == retry.Max {
				if err != nil {
					if snap.logger != nil {
						snap.logger.Errorf("Error after %d attempts: %v", attempt+1, diagnosticErr)
					}
					if len(errs) > 1 {
						return resp, errors.Join(errs...)
					}
					return resp, diagnosticErr
				}
				break
			}

			if !canReplayRequestBody(req) {
				if snap.logger != nil {
					snap.logger.Warnf("request body cannot be replayed; failing retry after attempt %d", attempt+1)
				}
				if err != nil {
					return resp, errors.Join(diagnosticErr, ErrRequestBodyNotReplayable)
				}
				return resp, ErrRequestBodyNotReplayable
			}

			if resp != nil && err == nil {
				if err := drainAndCloseBody(resp.Body); err != nil {
					return nil, fmt.Errorf("cleaning retry response body: %w", err)
				}
			}

			if snap.logger != nil {
				snap.logger.Infof("Retrying request (attempt %d) after backoff", attempt+1)
			}

			delay := retry.delay(attempt, resp)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				if snap.logger != nil {
					snap.logger.Errorf("Request canceled or timed out: %v", ctx.Err())
				}
				return nil, ctx.Err()
			case <-timer.C:
			}
		}

		return resp, nil
	})

	for _, mw := range slices.Backward(b.middlewares) {
		finalHandler = mw(finalHandler)
	}
	for _, mw := range slices.Backward(snap.middlewares) {
		finalHandler = mw(finalHandler)
	}

	resp, err := finalHandler(req)
	if attempts == 0 && req.Body != nil {
		_ = req.Body.Close() // Match net/http ownership when middleware skips transport delivery.
	}
	return resp, attempts, err
}

const maxRetryDrainBytes = 64 << 10

func drainAndCloseBody(body io.ReadCloser) error {
	if body == nil {
		return nil
	}
	_, drainErr := io.Copy(io.Discard, io.LimitReader(body, maxRetryDrainBytes))
	return errors.Join(drainErr, body.Close())
}

// Send executes the HTTP request.
//
// Invalid fluent preparation input is returned here before client snapshot,
// body preparation, middleware, or transport dispatch.
//
// Send takes a snapshot of the client at call time; later mutations on the
// client do not affect this in-flight request.
//
// Cancellation: ctx propagates through dial, TLS handshake, request header
// read, body read, retry backoff, and stream callbacks. When ctx is canceled
// before the response arrives, Send returns ctx.Err() and any partial response
// is closed internally. On success, Send fully reads and closes the transport
// body before returning the buffered Response; caller cleanup is not required
// for connection reuse.
//
// Retries: if the request body cannot be replayed, retries that would need to
// resend the body return [ErrRequestBodyNotReplayable] instead of silently
// re-sending or silently skipping.
func (b *RequestBuilder) Send(ctx context.Context) (*Response, error) {
	req, snap, cancel, start, err := b.prepareRequest(ctx)
	if err != nil {
		return nil, err
	}
	if cancel != nil {
		defer cancel()
	}

	resp, attempts, err := b.do(req.Context(), req, &snap)
	if err != nil {
		if snap.logger != nil {
			snap.logger.Errorf("Error executing request: %v", err)
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, err
	}

	if resp == nil {
		if snap.logger != nil {
			snap.logger.Errorf("Response is nil")
		}
		return nil, ErrResponseNil
	}

	response, err := newResponse(resp, &snap, b.maxResponseBodyBytes)
	if response != nil {
		response.elapsed = time.Since(start)
		response.attempts = attempts
	}
	return response, err
}

// SendStream sends the request and returns an unbuffered streaming response.
// Invalid fluent preparation input is returned before any body or transport work.
func (b *RequestBuilder) SendStream(ctx context.Context) (*StreamResponse, error) {
	req, snap, cancel, start, err := b.prepareRequest(ctx)
	if err != nil {
		return nil, err
	}

	resp, attempts, err := b.do(req.Context(), req, &snap)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		if snap.logger != nil {
			snap.logger.Errorf("Error executing request: %v", err)
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, err
	}

	if resp == nil {
		if cancel != nil {
			cancel()
		}
		if snap.logger != nil {
			snap.logger.Errorf("Response is nil")
		}
		return nil, ErrResponseNil
	}

	response := newStreamResponse(resp, cancel)
	response.elapsed = time.Since(start)
	response.attempts = attempts
	return response, nil
}

func (b *RequestBuilder) prepareRequest(ctx context.Context) (*http.Request, clientSnapshot, context.CancelFunc, time.Time, error) {
	start := time.Now()
	if b.preparationErr != nil {
		return nil, clientSnapshot{}, nil, start, b.preparationErr
	}
	snap := b.client.snapshot()
	var cancel context.CancelFunc
	cancelOnError := func() {
		if cancel != nil {
			cancel()
		}
	}

	parsedURL, err := resolveRequestURL(snap.baseURL, b.preparePath(), b.queries)
	if err != nil {
		err = sanitizeURLDiagnosticError(err)
		if snap.logger != nil {
			snap.logger.Errorf("Error parsing URL: %v", err)
		}
		return nil, snap, nil, start, err
	}

	if _, err := http.NewRequestWithContext(ctx, b.method, parsedURL.String(), nil); err != nil {
		err = sanitizeURLDiagnosticError(err)
		if snap.logger != nil {
			snap.logger.Errorf("Error creating request: %v", err)
		}
		return nil, snap, nil, start, fmt.Errorf("%w: %w", ErrRequestCreationFailed, err)
	}

	ctx, cancel = b.prepareContext(ctx)

	preparedBody, err := b.prepareBody(&snap)
	if err != nil {
		cancelOnError()
		if snap.logger != nil {
			snap.logger.Errorf("Error preparing request body: %v", err)
		}
		return nil, snap, nil, start, err
	}

	if preparedBody.contentType != "" {
		b.setHeader("Content-Type", preparedBody.contentType)
	}

	req, err := http.NewRequestWithContext(ctx, b.method, parsedURL.String(), preparedBody.body)
	if err != nil {
		err = sanitizeURLDiagnosticError(err)
		cancelOnError()
		if snap.logger != nil {
			snap.logger.Errorf("Error creating request: %v", err)
		}
		return nil, snap, nil, start, fmt.Errorf("%w: %w", ErrRequestCreationFailed, err)
	}
	if preparedBody.getBody != nil {
		req.GetBody = preparedBody.getBody
		req.ContentLength = preparedBody.contentLength
	}

	b.applyAuthAndHeaders(req, &snap)
	orderedHeaders := b.effectiveOrderedHeaders(&snap)
	syncOrderedHeaderValues(orderedHeaders, req.Header)
	req = withOrderedHeaders(req, orderedHeaders)

	return req, snap, cancel, start, nil
}

func canReplayRequestBody(req *http.Request) bool {
	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}

func resetRequestBody(req *http.Request) error {
	if req.Body == nil || req.Body == http.NoBody {
		return nil
	}
	if req.GetBody == nil {
		return ErrRequestBodyNotReplayable
	}
	body, err := req.GetBody()
	if err != nil {
		return fmt.Errorf("reset request body: %w", err)
	}
	req.Body = body
	return nil
}

func (b *RequestBuilder) prepareContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); !ok && b.timeout > 0 {
		return context.WithTimeout(ctx, b.timeout)
	}
	return ctx, nil
}
