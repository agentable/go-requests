package requests

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/test-go/testify/require"
)

func TestRequestTimeoutBoundsParentDeadline(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "Send"
		if stream {
			name = "SendStream"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				client := newTestClient(t, WithTransport(testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
					<-req.Context().Done()
					return nil, req.Context().Err()
				})))
				start := time.Now()
				builder := client.Get("http://example.com").Timeout(10 * time.Millisecond)
				var err error
				if stream {
					_, err = builder.SendStream(parent)
				} else {
					_, err = builder.Send(parent)
				}
				require.Error(t, err)
				assert.ErrorIs(t, err, context.DeadlineExceeded)
				assert.True(t, IsTimeout(err))
				assert.Equal(t, 10*time.Millisecond, time.Since(start))
				assert.NoError(t, parent.Err(), "request-local timeout must leave the parent usable")
			})
		})
	}
}

func TestRequestDeadlineSelection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		parent  time.Duration
		request time.Duration
		want    time.Duration
	}{
		{"no parent deadline", 0, 10 * time.Millisecond, 10 * time.Millisecond},
		{"earlier parent deadline", 5 * time.Millisecond, time.Second, 5 * time.Millisecond},
		{"zero timeout preserves parent", 10 * time.Millisecond, 0, 10 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent := t.Context()
				if tc.parent > 0 {
					var cancel context.CancelFunc
					parent, cancel = context.WithTimeout(parent, tc.parent)
					defer cancel()
				}
				client := newTestClient(t, WithTransport(testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
					<-req.Context().Done()
					return nil, req.Context().Err()
				})))
				start := time.Now()
				_, err := client.Get("http://example.com").Timeout(tc.request).Send(parent)
				assert.ErrorIs(t, err, context.DeadlineExceeded)
				assert.Equal(t, tc.want, time.Since(start))
			})
		})
	}
}

func TestRequestTimeoutContextOwnership(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		stream bool
		fail   bool
	}{
		{"buffered success", false, false},
		{"buffered failure", false, true},
		{"stream success", true, false},
		{"stream failure", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, cancel := context.WithTimeout(t.Context(), time.Hour)
			defer cancel()
			var delivery context.Context
			transportErr := errors.New("transport failed")
			client := newTestClient(t, WithTransport(testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				delivery = req.Context()
				if tc.fail {
					return nil, transportErr
				}
				return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
			})))
			builder := client.Get("http://example.com").Timeout(time.Minute)
			var err error
			if tc.stream {
				var response *StreamResponse
				response, err = builder.SendStream(parent)
				if !tc.fail {
					require.NoError(t, err)
					assert.NoError(t, delivery.Err(), "stream context must remain live until Close")
					_, err = io.ReadAll(response.Body())
					require.NoError(t, err)
					require.NoError(t, response.Close())
					require.NoError(t, response.Close())
				}
			} else {
				_, err = builder.Send(parent)
			}
			if tc.fail {
				assert.ErrorIs(t, err, transportErr)
			} else {
				require.NoError(t, err)
			}
			assert.ErrorIs(t, delivery.Err(), context.Canceled)
			assert.NoError(t, parent.Err())
		})
	}
}

func TestRequestTimeoutPreservesParentCancellation(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	cause := errors.New("host stopped")
	client := newTestClient(t, WithTransport(testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		cancel(cause)
		<-req.Context().Done()
		assert.ErrorIs(t, context.Cause(req.Context()), cause)
		return nil, req.Context().Err()
	})))
	_, err := client.Get("http://example.com").Timeout(time.Hour).Send(parent)
	assert.ErrorIs(t, err, context.Canceled)
	assert.True(t, IsCanceled(err))
	assert.False(t, IsTimeout(err))
	assert.ErrorIs(t, context.Cause(parent), cause)
}
