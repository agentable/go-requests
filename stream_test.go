package requests

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/test-go/testify/require"
)

type contextBoundCloseBody struct {
	done     <-chan struct{}
	reader   io.Reader
	closeErr error
	closes   int
}

func (b *contextBoundCloseBody) Read(p []byte) (int, error) {
	if b.reader != nil {
		return b.reader.Read(p)
	}
	return 0, io.EOF
}

func (b *contextBoundCloseBody) Close() error {
	b.closes++
	<-b.done
	return b.closeErr
}

func TestStream(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range 3 {
			_, _ = fmt.Fprintf(w, "data: Message %d\n", i)
			w.(http.Flusher).Flush()
		}
	}))
	defer server.Close()

	dataReceived := make([]string, 0, 3)

	client := newTestClient(t, WithBaseURL(server.URL))
	resp, err := client.Get("/").SendStream(t.Context())
	require.NoError(t, err)
	defer resp.Close() //nolint:errcheck // test cleanup closes response body

	for data, err := range resp.Lines() {
		require.NoError(t, err)
		dataReceived = append(dataReceived, string(data))
		assert.Contains(t, string(data), "data: Message")
	}
	assert.Equal(t, 3, len(dataReceived))
}

func TestStreamResponseRawAndAccessors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Stream", "yes")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, "hello\n")
	}))
	defer server.Close()

	client := newTestClient(t, WithBaseURL(server.URL))
	resp, err := client.Get("/events").SendStream(t.Context())
	require.NoError(t, err)
	defer resp.Close() //nolint:errcheck // test cleanup closes response body

	assert.Equal(t, http.StatusAccepted, resp.StatusCode())
	assert.Equal(t, "202 Accepted", resp.Status())
	assert.Equal(t, "yes", resp.Header().Get("X-Stream"))
	headers := resp.Header()
	headers.Set("X-Stream", "mutated")
	assert.Equal(t, "yes", resp.Raw().Header.Get("X-Stream"))
	responseURL := resp.URL()
	assert.Equal(t, "/events", responseURL.Path)
	responseURL.Path = "/mutated"
	assert.Equal(t, "/events", resp.Raw().Request.URL.Path)
	assert.Equal(t, 1, resp.Attempts())
	assert.Positive(t, resp.Elapsed())
	assert.Equal(t, http.StatusAccepted, resp.Raw().StatusCode)

	body, err := io.ReadAll(resp.Body())
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(body))
}

func TestStreamResponseCloseReleasesTimeoutContext(t *testing.T) {
	t.Parallel()

	ctxDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(ctxDone)
	}))
	defer server.Close()

	client := newTestClient(t, WithBaseURL(server.URL))
	resp, err := client.Get("/").Timeout(time.Second).SendStream(t.Context())
	require.NoError(t, err)

	require.NoError(t, resp.Close())
	require.NoError(t, resp.Close())

	select {
	case <-ctxDone:
	case <-time.After(time.Second):
		t.Fatal("stream close did not release request context")
	}
}

func TestStreamResponseCloseCancelsBeforeClosingBody(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		closeErr := errors.New("close failed")
		body := &contextBoundCloseBody{done: ctx.Done(), closeErr: closeErr}
		resp := newStreamResponse(&http.Response{Body: body}, cancel, nil, nil)
		closeDone := make(chan error, 1)

		go func() {
			closeDone <- resp.Close()
		}()
		synctest.Wait()

		select {
		case err := <-closeDone:
			assert.ErrorIs(t, err, closeErr)
		default:
			cancel()
			<-closeDone
			t.Fatal("StreamResponse.Close blocked before canceling its request context")
		}

		assert.ErrorIs(t, resp.Close(), closeErr)
		assert.Equal(t, 1, body.closes)
	})
}

func TestMultipartLifecycleStreamClose(t *testing.T) {
	sourceErr := errors.New("late multipart source failure")
	responseCloseErr := errors.New("response body close failed")
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()
		source := &multipartLifecycleReader{entered: entered, release: release, err: sourceErr}
		var responseBody *contextBoundCloseBody
		client := newTestClient(t, WithTransport(testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			consumeDone := make(chan struct{})
			go func() {
				_, _ = io.Copy(io.Discard, req.Body)
				close(consumeDone)
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				_ = req.Body.Close()
				<-consumeDone
				return nil, errors.New("multipart source read did not start")
			}
			_ = req.Body.Close()
			<-consumeDone
			responseBody = &contextBoundCloseBody{
				done:     req.Context().Done(),
				reader:   strings.NewReader("stream response"),
				closeErr: responseCloseErr,
			}
			return &http.Response{
				Status:     "413 Request Entity Too Large",
				StatusCode: http.StatusRequestEntityTooLarge,
				Header:     http.Header{},
				Body:       responseBody,
				Request:    req,
			}, nil
		})))
		type streamResult struct {
			response *StreamResponse
			err      error
		}
		resultCh := make(chan streamResult, 1)
		go func() {
			response, err := client.Post("https://example.com").
				Timeout(time.Hour).
				Multipart(NewMultipart().File("upload", "payload.txt", source)).
				SendStream(t.Context())
			resultCh <- streamResult{response: response, err: err}
		}()

		waitMultipartLifecycleSignal(t, entered, time.Second, "multipart source read did not start")
		var result streamResult
		select {
		case result = <-resultCh:
		case <-time.After(time.Second):
			t.Fatal("SendStream did not return before the multipart source was released")
		}
		require.NoError(t, result.err)
		require.NotNil(t, result.response)
		body, err := io.ReadAll(result.response.Body())
		require.NoError(t, err)
		assert.Equal(t, "stream response", string(body))
		assert.Equal(t, int32(1), source.active.Load(), "reading the response to EOF must not end the multipart borrow")

		closeResults := make(chan error, 2)
		go func() { closeResults <- result.response.Close() }()
		synctest.Wait()
		go func() { closeResults <- result.response.Close() }()
		returnedBeforeRelease := false
		var closeErr1 error
		select {
		case closeErr1 = <-closeResults:
			returnedBeforeRelease = true
		default:
		}

		close(release)
		released = true
		var closeErr2 error
		if returnedBeforeRelease {
			select {
			case closeErr2 = <-closeResults:
			case <-time.After(time.Second):
				t.Fatal("concurrent StreamResponse.Close did not finish after releasing the source")
			}
		} else {
			select {
			case closeErr1 = <-closeResults:
			case <-time.After(time.Second):
				t.Fatal("StreamResponse.Close did not finish after releasing the source")
			}
			select {
			case closeErr2 = <-closeResults:
			case <-time.After(time.Second):
				t.Fatal("concurrent StreamResponse.Close did not finish after releasing the source")
			}
		}
		assert.False(t, returnedBeforeRelease, "StreamResponse.Close returned while a multipart source Read was active")
		assert.ErrorIs(t, closeErr1, sourceErr)
		assert.ErrorIs(t, closeErr1, responseCloseErr)
		assert.ErrorIs(t, closeErr2, sourceErr)
		assert.ErrorIs(t, closeErr2, responseCloseErr)
		assert.Equal(t, closeErr1, closeErr2)
		assert.Equal(t, 1, responseBody.closes)
		assert.Zero(t, source.active.Load())
		assert.Zero(t, source.closes.Load(), "the borrowed source must remain caller-owned")
	})
}

func TestStreamResponseLinesReportsScannerError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxStreamBufferSize+1))
	}))
	defer server.Close()

	client := newTestClient(t, WithBaseURL(server.URL))
	resp, err := client.Get("/").SendStream(t.Context())
	require.NoError(t, err)
	defer resp.Close() //nolint:errcheck // test cleanup closes response body

	var gotErr error
	for _, err := range resp.Lines() {
		gotErr = err
	}
	require.Error(t, gotErr)
}
