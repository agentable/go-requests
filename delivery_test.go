package requests

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/test-go/testify/require"
)

func TestRequestNilResponseError(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, WithBaseURL("https://example.com"))
	client.addMiddleware(func(MiddlewareHandlerFunc) MiddlewareHandlerFunc {
		return func(*http.Request) (*http.Response, error) {
			return nil, nil
		}
	})

	_, err := client.Get("/").Send(context.Background())
	assert.ErrorIs(t, err, ErrResponseNil)
}

func TestRetryPolicyControlsTransportErrors(t *testing.T) {
	t.Run("false policy does not retry transport error", func(t *testing.T) {
		var attempts int32
		client := newTestClient(t,
			WithHTTPClient(&http.Client{Transport: testRoundTripperFunc(func(*http.Request) (*http.Response, error) {
				atomic.AddInt32(&attempts, 1)
				return nil, assert.AnError
			})}),
			WithRetry(RetryPolicy{
				Max:     2,
				Backoff: DefaultBackoffStrategy(0),
				ShouldRetry: func(*http.Request, *http.Response, error) bool {
					return false
				},
			}),
		)

		_, err := client.Get("http://example.com").Send(t.Context())
		require.Error(t, err)
		assert.ErrorIs(t, err, assert.AnError)
		assert.Equal(t, int32(1), attempts)
	})

	t.Run("true policy retries transport error", func(t *testing.T) {
		var attempts int32
		client := newTestClient(t,
			WithHTTPClient(&http.Client{Transport: testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if atomic.AddInt32(&attempts, 1) == 1 {
					return nil, assert.AnError
				}
				return &http.Response{
					Status:     "200 OK",
					StatusCode: http.StatusOK,
					Header:     http.Header{},
					Body:       io.NopCloser(strings.NewReader("ok")),
					Request:    req,
				}, nil
			})}),
			WithRetry(RetryPolicy{
				Max:     2,
				Backoff: DefaultBackoffStrategy(0),
				ShouldRetry: func(_ *http.Request, _ *http.Response, err error) bool {
					return err != nil
				},
			}),
		)

		resp, err := client.Get("http://example.com").Send(t.Context())
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode())
		assert.Equal(t, int32(2), attempts)
		assert.Equal(t, 2, resp.Attempts())
	})
}
