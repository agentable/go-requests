package requests

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"iter"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// StreamResponse is an unbuffered HTTP response whose body is owned by the caller.
type StreamResponse struct {
	elapsed            time.Duration
	attempts           int
	cancel             context.CancelFunc
	closeOnce          sync.Once
	closeErr           error
	multipartLifecycle *multipartSourceLifecycle
	rawBody            *deliveryBody
	rawResponse        *http.Response
}

func newStreamResponse(
	resp *http.Response,
	cancel context.CancelFunc,
	multipartLifecycle *multipartSourceLifecycle,
	rawBody *deliveryBody,
) *StreamResponse {
	return &StreamResponse{
		rawResponse:        resp,
		cancel:             cancel,
		multipartLifecycle: multipartLifecycle,
		rawBody:            rawBody,
	}
}

// Raw returns the underlying HTTP response for callers that need net/http details.
func (r *StreamResponse) Raw() *http.Response {
	return r.rawResponse
}

// StatusCode returns the HTTP status code of the response.
func (r *StreamResponse) StatusCode() int {
	return r.rawResponse.StatusCode
}

// Status returns the status string of the response.
func (r *StreamResponse) Status() string {
	return r.rawResponse.Status
}

// Header returns the response headers.
func (r *StreamResponse) Header() http.Header {
	return r.rawResponse.Header.Clone()
}

// URL returns a copy of the request URL that elicited the response.
func (r *StreamResponse) URL() *url.URL {
	return r.rawResponse.Request.URL.Clone()
}

// Elapsed returns the duration from request dispatch through response setup.
func (r *StreamResponse) Elapsed() time.Duration {
	return r.elapsed
}

// Attempts returns the total number of transport attempts, including the first request.
func (r *StreamResponse) Attempts() int {
	return r.attempts
}

// Body returns the live response body. The caller must close the response.
func (r *StreamResponse) Body() io.ReadCloser {
	return r.rawResponse.Body
}

// Lines returns an iterator over streamed response body lines.
func (r *StreamResponse) Lines() iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		scanner := bufio.NewScanner(r.rawResponse.Body)
		scanBuf := make([]byte, 0, maxStreamBufferSize)
		scanner.Buffer(scanBuf, maxStreamBufferSize)

		for scanner.Scan() {
			if !yield(bytes.Clone(scanner.Bytes()), nil) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			yield(nil, err)
		}
	}
}

// Close stops a default streaming multipart producer, closes the response body,
// and releases the request context. It waits for active request source reads
// and joins multipart producer errors with the response body close error.
// A blocked source Read can delay Close. Multipart parts remain borrowed.
func (r *StreamResponse) Close() error {
	r.closeOnce.Do(func() {
		if r.multipartLifecycle != nil {
			r.multipartLifecycle.stop()
		}
		if r.cancel != nil {
			r.cancel()
		}
		if r.rawBody != nil {
			_ = r.rawBody.Close()
		}
		var bodyErr error
		if r.rawResponse != nil && r.rawResponse.Body != nil {
			bodyErr = r.rawResponse.Body.Close()
		}
		if r.multipartLifecycle != nil {
			bodyErr = errors.Join(bodyErr, r.multipartLifecycle.wait())
		}
		if r.rawBody != nil {
			r.rawBody.reads.active.Wait()
		}
		r.closeErr = bodyErr
	})
	return r.closeErr
}
