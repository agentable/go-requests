package requests

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/test-go/testify/require"
)

func TestSizedReaderPreservesOneShotLength(t *testing.T) {
	const payload = "reviewed mutation"
	for _, method := range []string{http.MethodPost, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			client := newTestClient(t, WithTransport(testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				assert.Equal(t, int64(len(payload)), req.ContentLength)
				assert.Nil(t, req.GetBody)
				assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
				data, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				assert.Equal(t, payload, string(data))
				require.NoError(t, req.Body.Close())
				return &http.Response{StatusCode: http.StatusAccepted, Body: http.NoBody, Request: req}, nil
			})))
			// Even a source that Reader normally snapshots must remain one-shot.
			resp, err := client.Request(method, "https://example.test").
				SizedReader(bytes.NewReader([]byte(payload)), "application/json", int64(len(payload))).
				NoRetry().SendStream(t.Context())
			require.NoError(t, err)
			require.NoError(t, resp.Close())
			assert.Equal(t, 1, calls)
		})
	}
}

func TestSizedReaderInspectionDoesNotRead(t *testing.T) {
	reader := &previewTrackingReader{}
	builder := newTestClient(t).Post("https://example.test").SizedReader(reader, "text/plain", 7)
	preview, err := builder.Preview(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(7), preview.Body().Length())
	assert.True(t, preview.Body().LengthKnown())
	assert.True(t, preview.Body().ReplayabilityKnown())
	assert.False(t, preview.Body().Replayable())
	_, err = builder.Prepare(t.Context(), PrepareOptions{})
	require.NoError(t, err)
	assert.Zero(t, reader.reads.Load())
	_, err = builder.SizedReader(reader, "text/plain", -1).SendStream(t.Context())
	assert.ErrorIs(t, err, ErrInvalidConfigValue)
	assert.Zero(t, reader.reads.Load())
}

func TestSizedReaderWireLengthAndRedirect(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, int64(7), r.ContentLength)
		data, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.Equal(t, "payload", string(data))
		w.Header().Set("Location", "/replay")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	resp, err := newTestClient(t).Post(server.URL).
		SizedReader(strings.NewReader("payload"), "text/plain", 7).SendStream(t.Context())
	require.NoError(t, err)
	assert.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode())
	require.NoError(t, resp.Close())
	assert.Equal(t, 1, calls)
}

func TestSizedReaderDoesNotReplayOnResponseLoss(t *testing.T) {
	lost := errors.New("response lost after commit")
	calls := 0
	client := newTestClient(t, WithTransport(testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		_, err := io.Copy(io.Discard, req.Body)
		require.NoError(t, err)
		require.NoError(t, req.Body.Close())
		return nil, lost
	})), WithRetry(RetryPolicy{Max: 2, ShouldRetry: func(*http.Request, *http.Response, error) bool { return true }}))
	resp, err := client.Post("https://example.test").SizedReader(strings.NewReader("payload"), "", 7).SendStream(t.Context())
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, lost)
	assert.ErrorIs(t, err, ErrRequestBodyNotReplayable)
	assert.Equal(t, 1, calls)
}

func TestSendStreamRetainsRejectedRedirectMetadata(t *testing.T) {
	veto := errors.New("redirect veto")
	closes := 0
	httpClient := &http.Client{
		Transport: testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusMovedPermanently,
				Header:     http.Header{"Location": {"/other"}, "X-Request-Id": {"observed"}},
				Body:       &countedCloseBody{Reader: strings.NewReader("redirect"), closes: &closes}, Request: req,
			}, nil
		}),
		CheckRedirect: func(*http.Request, []*http.Request) error { return veto },
	}
	resp, err := newTestClient(t, WithHTTPClient(httpClient)).Post("https://example.test/mutation").
		SizedReader(strings.NewReader("payload"), "", 7).Timeout(time.Second).SendStream(t.Context())
	assert.ErrorIs(t, err, veto)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusMovedPermanently, resp.StatusCode())
	assert.Equal(t, "observed", resp.Header().Get("X-Request-Id"))
	assert.Equal(t, "/mutation", resp.URL().Path)
	assert.Equal(t, 1, resp.Attempts())
	assert.Positive(t, resp.Elapsed())
	assert.ErrorIs(t, resp.Raw().Request.Context().Err(), context.Canceled)
	require.NoError(t, resp.Close())
	require.NoError(t, resp.Close())
	assert.Equal(t, 1, closes)
}

func TestSendStreamClosesMiddlewareErrorResponse(t *testing.T) {
	deliveryErr := errors.New("middleware response failure")
	closeErr := errors.New("response close failure")
	var body *contextBoundCloseBody
	client := newTestClient(t, WithMiddleware(func(MiddlewareHandlerFunc) MiddlewareHandlerFunc {
		return func(req *http.Request) (*http.Response, error) {
			body = &contextBoundCloseBody{done: req.Context().Done(), closeErr: closeErr}
			return &http.Response{StatusCode: http.StatusAccepted, Body: body, Request: req}, deliveryErr
		}
	}))
	resp, err := client.Get("https://example.test").Timeout(time.Second).SendStream(t.Context())
	assert.ErrorIs(t, err, deliveryErr)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode())
	assert.ErrorIs(t, resp.Close(), closeErr)
	require.NoError(t, resp.Raw().Body.Close())
	assert.Equal(t, 1, body.closes)
}

type countedCloseBody struct {
	io.Reader
	closes *int
}

func (b *countedCloseBody) Close() error {
	*b.closes++
	return nil
}
