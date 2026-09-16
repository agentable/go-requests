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
		b.setPreparationError(err, preparationErrorClassInvalidConfigValue)
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
		b.setPreparationError(invalidOptionValue("MaxResponseBodyBytes"), preparationErrorClassInvalidConfigValue)
		return b
	}
	b.maxResponseBodyBytes = maxBytes
	return b
}

// Retry sets the request-local retry policy, replacing the client policy.
// A negative Max records an ErrInvalidConfigValue returned before dispatch.
func (b *RequestBuilder) Retry(policy RetryPolicy) *RequestBuilder {
	if err := validateIntOption("Retry.Max", policy.Max); err != nil {
		b.setPreparationError(err, preparationErrorClassInvalidConfigValue)
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

// deliverySnapshot is the private handoff between request compilation and
// delivery. It contains no builder containers; all policy values are frozen
// before body materialization begins.
type deliverySnapshot struct {
	plan                 *requestPlan
	client               clientSnapshot
	middlewares          []Middleware
	retryPolicy          RetryPolicy
	timeout              time.Duration
	maxResponseBodyBytes int64
}

type openedDelivery struct {
	request              *http.Request
	client               clientSnapshot
	middlewares          []Middleware
	retryPolicy          RetryPolicy
	maxResponseBodyBytes int64
	cancel               context.CancelFunc
	start                time.Time
}

func (b *RequestBuilder) compileDeliverySnapshot() (*deliverySnapshot, error) {
	if b == nil || b.client == nil {
		return nil, fmt.Errorf("%w: request delivery", ErrInvalidConfigValue)
	}
	if b.preparationErr != nil {
		return nil, b.preparationErr
	}

	snap := b.client.snapshot()
	plan, err := b.compileRequestPlanSnapshot(snap)
	if err != nil {
		var creation *requestPlanCreationError
		if errors.As(err, &creation) {
			if snap.logger != nil {
				snap.logger.Errorf("Error creating request: %v", sanitizeURLDiagnosticError(creation.cause))
			}
			return nil, fmt.Errorf("%w: %w", ErrRequestCreationFailed, sanitizeURLDiagnosticError(creation.cause))
		}
		if snap.logger != nil {
			snap.logger.Errorf("Error preparing request plan: %v", err)
		}
		return nil, err
	}
	if err := validateDeliveryPreflightFacts(plan.body, plan.bodyPreflightHeaders()); err != nil {
		if snap.logger != nil {
			snap.logger.Errorf("Error preparing request body: %v", err)
		}
		return nil, err
	}
	if plan.body.form != nil {
		plan.body.form = plan.body.form.Clone()
	}

	retryPolicy := snap.retry
	if b.hasRetryPolicy {
		retryPolicy = b.retryPolicy
	}
	return &deliverySnapshot{
		plan:                 plan,
		client:               snap,
		middlewares:          slices.Clone(b.middlewares),
		retryPolicy:          retryPolicy.normalize(),
		timeout:              b.timeout,
		maxResponseBodyBytes: b.maxResponseBodyBytes,
	}, nil
}

// validateDeliveryPreflightFacts keeps delivery's historical error boundary.
// Multipart part metadata is consumed by the producer during materialization;
// only checks that the reader constructor itself performs synchronously belong
// before openDelivery.
func validateDeliveryPreflightFacts(body requestBodyPlan, headers http.Header) error { //nolint:gocritic // Validation consumes a detached plan snapshot without mutation.
	if body.kind != requestBodyMultipart {
		return validateDeliveryStaticFacts(body, headers)
	}
	if body.multipart == nil {
		return previewInvalidBodyError()
	}
	if body.multipart.canReplay && body.multipart.replayMaxBytes < 0 {
		return previewInvalidBodyError()
	}
	if body.multipart.boundary != "" && !validMultipartBoundary(body.multipart.boundary) {
		return previewInvalidBodyError()
	}
	return nil
}

func prepareDeliveryContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); !ok && timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return ctx, nil
}

func (s *deliverySnapshot) openDelivery(ctx context.Context) (*openedDelivery, error) {
	if s == nil || s.plan == nil {
		return nil, fmt.Errorf("%w: delivery snapshot", ErrInvalidConfigValue)
	}
	start := time.Now()
	if _, err := http.NewRequestWithContext(ctx, s.plan.method, s.plan.targetURL.String(), nil); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRequestCreationFailed, sanitizeURLDiagnosticError(err))
	}

	deliveryContext, cancel := prepareDeliveryContext(ctx, s.timeout)
	cancelOnError := func() {
		if cancel != nil {
			cancel()
		}
	}
	preparedBody, err := prepareBodyFromPlan(
		s.plan.body,
		s.plan.bodyPreflightHeaders(),
		&s.client,
	)
	if err != nil {
		cancelOnError()
		if s.client.logger != nil {
			s.client.logger.Errorf("Error preparing request body: %v", err)
		}
		return nil, err
	}

	req, err := http.NewRequestWithContext(deliveryContext, s.plan.method, s.plan.targetURL.String(), preparedBody.body)
	if err != nil {
		cancelOnError()
		if s.client.logger != nil {
			s.client.logger.Errorf("Error creating request: %v", sanitizeURLDiagnosticError(err))
		}
		return nil, fmt.Errorf("%w: %w", ErrRequestCreationFailed, sanitizeURLDiagnosticError(err))
	}
	if preparedBody.getBody != nil {
		req.GetBody = preparedBody.getBody
		req.ContentLength = preparedBody.contentLength
	}
	req = applyPlanAuthAndHeaders(req, s.plan, preparedBody.contentType)

	return &openedDelivery{
		request:              req,
		client:               s.client,
		middlewares:          slices.Clone(s.middlewares),
		retryPolicy:          s.retryPolicy,
		maxResponseBodyBytes: s.maxResponseBodyBytes,
		cancel:               cancel,
		start:                start,
	}, nil
}

func (d *openedDelivery) do() (*http.Response, int, error) {
	attempts := 0
	req := d.request
	snap := &d.client
	ctx := req.Context()

	finalHandler := MiddlewareHandlerFunc(func(req *http.Request) (*http.Response, error) {
		retry := d.retryPolicy

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

	for _, mw := range slices.Backward(d.middlewares) {
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
	if ctx == nil {
		return nil, ErrRequestCreationFailed
	}
	snapshot, err := b.compileDeliverySnapshot()
	if err != nil {
		return nil, err
	}
	opened, err := snapshot.openDelivery(ctx)
	if err != nil {
		return nil, err
	}
	if opened.cancel != nil {
		defer opened.cancel()
	}

	resp, attempts, err := opened.do()
	if err != nil {
		if opened.client.logger != nil {
			opened.client.logger.Errorf("Error executing request: %v", err)
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, err
	}

	if resp == nil {
		if opened.client.logger != nil {
			opened.client.logger.Errorf("Response is nil")
		}
		return nil, ErrResponseNil
	}

	response, err := newResponse(resp, &opened.client, opened.maxResponseBodyBytes)
	if response != nil {
		response.elapsed = time.Since(opened.start)
		response.attempts = attempts
	}
	return response, err
}

// SendStream sends the request and returns an unbuffered streaming response.
// Invalid fluent preparation input is returned before any body or transport work.
func (b *RequestBuilder) SendStream(ctx context.Context) (*StreamResponse, error) {
	if ctx == nil {
		return nil, ErrRequestCreationFailed
	}
	snapshot, err := b.compileDeliverySnapshot()
	if err != nil {
		return nil, err
	}
	opened, err := snapshot.openDelivery(ctx)
	if err != nil {
		return nil, err
	}

	resp, attempts, err := opened.do()
	if err != nil {
		if opened.cancel != nil {
			opened.cancel()
		}
		if opened.client.logger != nil {
			opened.client.logger.Errorf("Error executing request: %v", err)
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, err
	}

	if resp == nil {
		if opened.cancel != nil {
			opened.cancel()
		}
		if opened.client.logger != nil {
			opened.client.logger.Errorf("Response is nil")
		}
		return nil, ErrResponseNil
	}

	response := newStreamResponse(resp, opened.cancel)
	response.elapsed = time.Since(opened.start)
	response.attempts = attempts
	return response, nil
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
