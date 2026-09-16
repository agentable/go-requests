package requests

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentable/go-orderedobject"
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

func TestRetryRejectsNonReplayableBodyAfterTransportError(t *testing.T) {
	transportErr := errors.New("delivery failed")
	client := newTestClient(t,
		WithTransport(testRoundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, transportErr
		})),
		WithRetry(RetryPolicy{
			Max:     1,
			Backoff: DefaultBackoffStrategy(0),
			ShouldRetry: func(*http.Request, *http.Response, error) bool {
				return true
			},
		}),
	)

	resp, err := client.Post("https://example.test/").
		Reader(io.LimitReader(strings.NewReader("payload"), 7), "text/plain").
		Send(t.Context())

	assert.Nil(t, resp)
	assert.ErrorIs(t, err, transportErr)
	assert.ErrorIs(t, err, ErrRequestBodyNotReplayable)
}
func TestDeliveryPlanSnapshotsPolicyAndDoesNotReadBuilderAfterOpen(t *testing.T) {
	var transportCalls atomic.Int32
	var requestMiddlewareCalls atomic.Int32
	var lateMiddlewareCalls atomic.Int32

	client := newTestClient(t,
		WithRetry(RetryPolicy{
			Max:     1,
			Backoff: DefaultBackoffStrategy(0),
			ShouldRetry: func(_ *http.Request, resp *http.Response, _ error) bool {
				return resp != nil && resp.StatusCode == http.StatusInternalServerError
			},
		}),
		WithTransport(testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			attempt := transportCalls.Add(1)
			status := http.StatusInternalServerError
			if attempt == 2 {
				status = http.StatusNoContent
			}
			return &http.Response{
				StatusCode: status,
				Header:     make(http.Header),
				Body:       http.NoBody,
				Request:    req,
			}, nil
		})),
	)

	requestMiddleware := func(next MiddlewareHandlerFunc) MiddlewareHandlerFunc {
		return MiddlewareHandlerFunc(func(req *http.Request) (*http.Response, error) {
			requestMiddlewareCalls.Add(1)
			return next(req)
		})
	}
	lateMiddleware := func(next MiddlewareHandlerFunc) MiddlewareHandlerFunc {
		return MiddlewareHandlerFunc(func(req *http.Request) (*http.Response, error) {
			lateMiddlewareCalls.Add(1)
			return next(req)
		})
	}

	builder := client.Get("https://example.test/").
		Retry(RetryPolicy{Max: 1, Backoff: DefaultBackoffStrategy(0), ShouldRetry: DefaultRetryIf}).
		Timeout(time.Second)
	builder.AddMiddleware(requestMiddleware)

	snapshot, err := builder.compileDeliverySnapshot()
	require.NoError(t, err)
	opened, err := snapshot.openDelivery(t.Context())
	require.NoError(t, err)

	// Mutations after open must not change the delivery already in flight.
	builder.NoRetry().Timeout(time.Nanosecond)
	builder.MaxResponseBodyBytes(1)
	builder.AddMiddleware(lateMiddleware)

	response, attempts, err := opened.do()
	require.NoError(t, err)
	require.NotNil(t, response)
	assert.Equal(t, 2, attempts)
	assert.Equal(t, int32(2), transportCalls.Load())
	assert.Equal(t, int32(1), requestMiddlewareCalls.Load())
	assert.Zero(t, lateMiddlewareCalls.Load())
}

func TestDeliveryPlanSnapshotsResponseLimitAndRequestFacts(t *testing.T) {
	var requestBody string
	var requestHeader string
	var authorization string
	client := newTestClient(t, WithTransport(testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		requestBody = string(body)
		requestHeader = req.Header.Get("X-Frozen")
		authorization = req.Header.Get("Authorization")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("0123456789")),
			Request:    req,
		}, nil
	})))

	builder := client.Post("https://example.test/").
		Text("before").
		Header("X-Frozen", "before").
		Auth(BearerAuth{Token: "before"}).
		MaxResponseBodyBytes(4)

	snapshot, err := builder.compileDeliverySnapshot()
	require.NoError(t, err)
	opened, err := snapshot.openDelivery(t.Context())
	require.NoError(t, err)

	builder.Text("after").
		Header("X-Frozen", "after").
		Auth(BearerAuth{Token: "after"}).
		MaxResponseBodyBytes(0)

	response, attempts, err := opened.do()
	require.NoError(t, err)
	require.NotNil(t, response)
	assert.Equal(t, 1, attempts)
	assert.Equal(t, "before", requestBody)
	assert.Equal(t, "before", requestHeader)
	assert.Equal(t, "Bearer before", authorization)

	buffered, err := newResponse(response, &opened.client, opened.maxResponseBodyBytes)
	assert.Nil(t, buffered)
	assert.ErrorIs(t, err, ErrResponseBodyTooLarge)
}

func TestDeliveryPlanSendStreamUsesOpenedSnapshot(t *testing.T) {
	opened := make(chan struct{})
	release := make(chan struct{})
	var lateMiddlewareCalls atomic.Int32
	var requestBody string
	var requestHeader string
	var authorization string

	client := newTestClient(t, WithTransport(testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		requestBody = string(body)
		requestHeader = req.Header.Get("X-Frozen")
		authorization = req.Header.Get("Authorization")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("stream")),
			Request:    req,
		}, nil
	})))

	builder := client.Post("https://example.test/").
		Text("before").
		Header("X-Frozen", "before").
		Auth(BearerAuth{Token: "before"}).
		Timeout(time.Second)
	builder.AddMiddleware(func(next MiddlewareHandlerFunc) MiddlewareHandlerFunc {
		return MiddlewareHandlerFunc(func(req *http.Request) (*http.Response, error) {
			close(opened)
			<-release
			return next(req)
		})
	})

	type result struct {
		response *StreamResponse
		err      error
	}
	results := make(chan result, 1)
	go func() {
		response, err := builder.SendStream(context.Background())
		results <- result{response: response, err: err}
	}()

	<-opened
	builder.Text("after").
		Header("X-Frozen", "after").
		Auth(BearerAuth{Token: "after"}).
		Timeout(time.Nanosecond)
	builder.AddMiddleware(func(next MiddlewareHandlerFunc) MiddlewareHandlerFunc {
		return MiddlewareHandlerFunc(func(req *http.Request) (*http.Response, error) {
			lateMiddlewareCalls.Add(1)
			return next(req)
		})
	})
	close(release)

	outcome := <-results
	require.NoError(t, outcome.err)
	require.NotNil(t, outcome.response)
	require.NoError(t, outcome.response.Close())
	assert.Equal(t, "before", requestBody)
	assert.Equal(t, "before", requestHeader)
	assert.Equal(t, "Bearer before", authorization)
	assert.Zero(t, lateMiddlewareCalls.Load())
}

func TestDeliveryPlanMultipartContentTypeParityAcrossSendExits(t *testing.T) {
	terminals := []struct {
		name string
		send func(*RequestBuilder) error
	}{
		{
			name: "Send",
			send: func(builder *RequestBuilder) error {
				_, err := builder.Send(t.Context())
				return err
			},
		},
		{
			name: "SendStream",
			send: func(builder *RequestBuilder) error {
				response, err := builder.SendStream(t.Context())
				if response != nil {
					assert.NoError(t, response.Close())
				}
				return err
			},
		},
	}

	for _, terminal := range terminals {
		t.Run(terminal.name, func(t *testing.T) {
			var contentType string
			client := newTestClient(t, WithTransport(testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				contentType = req.Header.Get("Content-Type")
				_, err := io.Copy(io.Discard, req.Body)
				if err != nil {
					return nil, err
				}
				return &http.Response{
					StatusCode: http.StatusNoContent,
					Header:     make(http.Header),
					Body:       http.NoBody,
					Request:    req,
				}, nil
			})))
			builder := client.Post("https://example.test/").
				Multipart(NewMultipart().Field("name", "value"))
			beforeHeaders := builder.metadata.headerValues()

			require.NoError(t, terminal.send(builder))
			assert.NotEmpty(t, contentType)
			afterHeaders := builder.metadata.headerValues()
			assert.Equal(t, beforeHeaders, afterHeaders)
			assert.Empty(t, afterHeaders.Get("Content-Type"))
		})
	}
}

func TestDeliveryPlanMultipartContentTypeDoesNotPolluteSecondSend(t *testing.T) {
	var contentTypes []string
	client := newTestClient(t, WithTransport(testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		contentTypes = append(contentTypes, req.Header.Get("Content-Type"))
		if _, err := io.Copy(io.Discard, req.Body); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})))
	multipart := NewMultipart().Field("name", "value")
	builder := client.Post("https://example.test/").Multipart(multipart)
	beforeHeaders := builder.metadata.headerValues()
	beforeOrdered := cloneOrderedHeaders(builder.orderedHeaders)

	_, err := builder.Send(t.Context())
	require.NoError(t, err)
	_, err = builder.Send(t.Context())
	require.NoError(t, err)

	require.Len(t, contentTypes, 2)
	for _, contentType := range contentTypes {
		mediaType, params, err := mime.ParseMediaType(contentType)
		require.NoError(t, err)
		assert.Equal(t, "multipart/form-data", mediaType)
		assert.NotEmpty(t, params["boundary"])
	}
	assert.Equal(t, beforeHeaders, builder.metadata.headerValues())
	assert.Equal(t, beforeOrdered, builder.orderedHeaders)
}

func TestDeliveryPlanDoesNotCloseEncoderReaderOnMaterializationError(t *testing.T) {
	readErr := errors.New("encoded body read failed")
	var closeCalls atomic.Int32
	client := newTestClient(t,
		WithJSONEncoder(deliveryClosingErrorEncoder{readErr: readErr, closeCalls: &closeCalls}),
		WithTransport(testRoundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("unexpected transport call")
		})),
	)

	response, err := client.Post("https://example.test/").JSON(struct{}{}).Send(t.Context())

	assert.Nil(t, response)
	assert.ErrorIs(t, err, readErr)
	assert.Zero(t, closeCalls.Load())
}

type deliveryClosingErrorEncoder struct {
	readErr    error
	closeCalls *atomic.Int32
}

func (e deliveryClosingErrorEncoder) Encode(any) (io.Reader, error) {
	return deliveryClosingErrorReader(e), nil
}

type deliveryClosingErrorReader struct {
	readErr    error
	closeCalls *atomic.Int32
}

func (r deliveryClosingErrorReader) Read([]byte) (int, error) {
	return 0, r.readErr
}

func (r deliveryClosingErrorReader) Close() error {
	r.closeCalls.Add(1)
	return nil
}

func TestDeliveryPlanDoesNotWriteBuilderForMultipartContentType(t *testing.T) {
	client := newTestClient(t)
	multipart := NewMultipart().Field("name", "value")
	builder := client.Post("https://example.test/").Multipart(multipart)

	beforeHeaders := builder.metadata.headerValues()
	beforeOrdered := cloneOrderedHeaders(builder.orderedHeaders)
	beforeGenerated := builder.body.generatedContentType

	snapshot, err := builder.compileDeliverySnapshot()
	require.NoError(t, err)
	opened, err := snapshot.openDelivery(t.Context())
	require.NoError(t, err)
	require.NotNil(t, opened.request)

	assert.NotEmpty(t, opened.request.Header.Get("Content-Type"))
	assert.Equal(t, beforeHeaders, builder.metadata.headerValues())
	assert.Equal(t, beforeGenerated, builder.body.generatedContentType)
	assert.Equal(t, beforeOrdered, builder.orderedHeaders)
}

func TestDeliveryPlanMultipartContentTypeUpdatesDetachedOrderedIntent(t *testing.T) {
	ordered := orderedobject.New[[]string]().Set("X-First", []string{"one"})
	client := newTestClient(t, WithTransport(testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		contentType := req.Header.Get("Content-Type")
		require.NotEmpty(t, contentType)
		metadata, ok := OrderedHeaders(req)
		require.True(t, ok)
		values, ok := metadata.Get("Content-Type")
		require.True(t, ok)
		assert.Equal(t, []string{contentType}, values)
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})))

	builder := client.Post("https://example.test/").
		OrderedHeaders(ordered).
		Multipart(NewMultipart().Field("name", "value"))

	_, err := builder.Send(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"X-First"}, builder.orderedHeaders.Keys())
}

func TestDeliveryPlanMultipartContentTypeDropsClientOrderedDefault(t *testing.T) {
	clientOrdered := orderedobject.New[[]string]().Set("Content-Type", []string{"application/client"})
	client := newTestClient(t,
		WithOrderedHeaders(clientOrdered),
		WithTransport(testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			contentType := req.Header.Get("Content-Type")
			require.NotEmpty(t, contentType)
			ordered, ok := OrderedHeaders(req)
			assert.False(t, ok)
			assert.Nil(t, ordered)
			return &http.Response{
				StatusCode: http.StatusNoContent,
				Header:     make(http.Header),
				Body:       http.NoBody,
				Request:    req,
			}, nil
		})),
	)

	_, err := client.Post("https://example.test/").
		Multipart(NewMultipart().Field("name", "value")).
		Send(t.Context())
	require.NoError(t, err)
}

func TestDeliveryPlanTypedBodyDoesNotUseClientDefaultContentType(t *testing.T) {
	var encoderCalls atomic.Int64
	var transportCalls atomic.Int32
	client := newTestClient(t,
		WithContentType("application/json"),
		WithJSONEncoder(countingEncoder{calls: &encoderCalls}),
		WithTransport(testRoundTripperFunc(func(*http.Request) (*http.Response, error) {
			transportCalls.Add(1)
			return nil, errors.New("unexpected transport call")
		})),
	)

	_, err := client.Post("https://example.test/").
		JSON(struct{}{}).
		DelHeader("Content-Type").
		Send(t.Context())

	assert.ErrorIs(t, err, ErrUnsupportedContentType)
	assert.Zero(t, encoderCalls.Load())
	assert.Zero(t, transportCalls.Load())
}

func TestDeliverySnapshotValidatesTypedBodyBeforeEncoding(t *testing.T) {
	var encoderCalls atomic.Int64
	builder := newTestClient(t, WithJSONEncoder(countingEncoder{calls: &encoderCalls})).
		Post("https://example.test").
		JSON(struct{}{}).
		DelHeader("Content-Type")

	snapshot, err := builder.compileDeliverySnapshot()
	assert.Nil(t, snapshot)
	assert.ErrorIs(t, err, ErrUnsupportedContentType)
	assert.Zero(t, encoderCalls.Load())
}

func TestDeliveryPlanTypedBodyContentTypeParityWithPrepare(t *testing.T) {
	var encoderCalls atomic.Int64
	client := newTestClient(t,
		WithContentType("application/json"),
		WithJSONEncoder(countingEncoder{calls: &encoderCalls}),
	)
	builder := client.Post("https://example.test/").
		JSON(struct{}{}).
		DelHeader("Content-Type")

	_, prepareErr := builder.Prepare(t.Context(), PrepareOptions{})
	_, sendErr := builder.Send(t.Context())

	assert.ErrorIs(t, prepareErr, ErrUnsupportedContentType)
	assert.ErrorIs(t, sendErr, ErrUnsupportedContentType)
	assert.Zero(t, encoderCalls.Load())
}

func TestDeliveryPlanPreflightAvoidsBodyAndTransportActivity(t *testing.T) {
	var transportCalls atomic.Int32
	var reads atomic.Int32

	client := newTestClient(t, WithTransport(testRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		transportCalls.Add(1)
		return nil, errors.New("unexpected transport call")
	})))
	reader := &deliveryTrackingReader{reads: &reads}
	builder := client.Post("https://example.test/%zz").Reader(reader, "application/octet-stream")

	snapshot, err := builder.compileDeliverySnapshot()
	assert.Nil(t, snapshot)
	assert.ErrorIs(t, err, ErrRequestCreationFailed)
	assert.Zero(t, reads.Load())
	assert.Zero(t, transportCalls.Load())
}

func TestDeliveryPlanDefersMultipartPartValidationToMaterialization(t *testing.T) {
	source := &deliveryTrackingReader{reads: new(atomic.Int32)}
	multipart := NewMultipart().Replayable(1024).
		Part(FilePart{Filename: "payload.txt", Body: source})
	builder := newTestClient(t).Post("https://example.test/").Multipart(multipart)

	snapshot, err := builder.compileDeliverySnapshot()
	require.NoError(t, err)
	opened, err := snapshot.openDelivery(t.Context())
	assert.Nil(t, opened)
	assert.ErrorIs(t, err, ErrInvalidConfigValue)
	assert.Zero(t, source.reads.Load())
}

func TestDeliveryPlanDefersMultipartPartMetadataValidationToMaterialization(t *testing.T) {
	tests := []struct {
		name string
		part FilePart
	}{
		{
			name: "invalid content type",
			part: FilePart{
				Field:       "upload",
				Filename:    "payload.txt",
				ContentType: "not a media type",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &deliveryTrackingReader{reads: new(atomic.Int32)}
			test.part.Body = source
			builder := newTestClient(t).Post("https://example.test/").Multipart(
				NewMultipart().Replayable(1024).Part(test.part),
			)

			snapshot, err := builder.compileDeliverySnapshot()
			require.NoError(t, err)
			opened, err := snapshot.openDelivery(t.Context())
			assert.Nil(t, opened)
			assert.ErrorIs(t, err, ErrInvalidConfigValue)
			assert.Zero(t, source.reads.Load())
		})
	}
}

func TestDeliveryPlanDefersStreamingMultipartPartMetadataToBodyRead(t *testing.T) {
	source := &deliveryTrackingReader{reads: new(atomic.Int32)}
	client := newTestClient(t,
		WithTransport(testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			_, err := io.Copy(io.Discard, req.Body)
			return nil, err
		})),
	)

	_, err := client.Post("https://example.test/").
		Retry(RetryPolicy{Max: 0}).
		Multipart(NewMultipart().Part(FilePart{
			Field:       "upload",
			Filename:    "payload.txt",
			ContentType: "not a media type",
			Body:        source,
		})).
		Send(t.Context())

	assert.ErrorIs(t, err, ErrInvalidConfigValue)
	assert.Zero(t, source.reads.Load())
}

func TestDeliveryPlanDefersTypedNilMultipartPartBodyValidationToMaterialization(t *testing.T) {
	var typedNil *deliveryTrackingReader
	builder := newTestClient(t).Post("https://example.test/").Multipart(
		NewMultipart().Replayable(1024).Part(FilePart{
			Field:    "upload",
			Filename: "payload.txt",
			Body:     typedNil,
		}),
	)

	snapshot, err := builder.compileDeliverySnapshot()
	require.NoError(t, err)

	var opened *openedDelivery
	assert.NotPanics(t, func() {
		opened, err = snapshot.openDelivery(t.Context())
	})
	assert.Nil(t, opened)
	assert.ErrorIs(t, err, ErrInvalidConfigValue)
}

type deliveryTrackingReader struct {
	reads *atomic.Int32
}

func (r *deliveryTrackingReader) Read([]byte) (int, error) {
	r.reads.Add(1)
	return 0, io.EOF
}

func (r *deliveryTrackingReader) Close() error {
	return nil
}

func TestPrepareContextSnapshotIsPure(t *testing.T) {
	ctx, cancel := prepareDeliveryContext(context.Background(), time.Second)
	defer cancel()
	assert.NotNil(t, ctx)
}
