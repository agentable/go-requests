package requests

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/test-go/testify/require"
)

func TestPrepareBodyWithFormFields(t *testing.T) {
	builder := newTestClient(t).Post("/").Form(url.Values{
		"name": {"Jane Doe"},
		"age":  {"32"},
	})

	body, err := builder.prepareBody(&clientSnapshot{})
	require.NoError(t, err)
	assert.Equal(t, "application/x-www-form-urlencoded", body.contentType)

	data, err := io.ReadAll(body.body)
	require.NoError(t, err)
	assert.Equal(t, url.Values{"name": {"Jane Doe"}, "age": {"32"}}.Encode(), string(data))
}

func TestRequestPreviewBodySelectionMatrix(t *testing.T) {
	var encoderCalls atomic.Int64
	client := newTestClient(t, WithJSONEncoder(countingEncoder{calls: &encoderCalls}))
	tests := []struct {
		name         string
		builder      func() *RequestBuilder
		wantKind     PreviewBodyKind
		wantMedia    string
		wantPresence PreviewValueState
		wantLength   int64
		wantKnown    bool
		wantReplay   bool
		wantReplayOK bool
	}{
		{
			name:         "none",
			builder:      func() *RequestBuilder { return client.Get("https://example.com") },
			wantKind:     PreviewBodyNone,
			wantPresence: PreviewValueUnknown,
			wantLength:   -1,
		},
		{
			name:         "empty text",
			builder:      func() *RequestBuilder { return client.Post("https://example.com").Text("") },
			wantKind:     PreviewBodyText,
			wantMedia:    "text/plain",
			wantPresence: PreviewValuePresent,
			wantLength:   0,
			wantKnown:    true,
			wantReplay:   true,
			wantReplayOK: true,
		},
		{
			name:         "nil bytes",
			builder:      func() *RequestBuilder { return client.Post("https://example.com").Bytes(nil) },
			wantKind:     PreviewBodyBytes,
			wantPresence: PreviewValuePresent,
			wantLength:   0,
			wantKnown:    true,
			wantReplay:   true,
			wantReplayOK: true,
		},
		{
			name:         "empty form",
			builder:      func() *RequestBuilder { return client.Post("https://example.com").Form(url.Values{}) },
			wantKind:     PreviewBodyForm,
			wantMedia:    "application/x-www-form-urlencoded",
			wantPresence: PreviewValuePresent,
			wantLength:   -1,
			wantReplay:   true,
			wantReplayOK: true,
		},
		{
			name: "form remains structural",
			builder: func() *RequestBuilder {
				return client.Post("https://example.com").Form(url.Values{"secret": {"value"}})
			},
			wantKind:     PreviewBodyForm,
			wantMedia:    "application/x-www-form-urlencoded",
			wantPresence: PreviewValuePresent,
			wantLength:   -1,
			wantReplay:   true,
			wantReplayOK: true,
		},
		{
			name: "json is structural",
			builder: func() *RequestBuilder {
				return client.Post("https://example.com").JSON(map[string]string{"secret": "value"})
			},
			wantKind:     PreviewBodyJSON,
			wantMedia:    "application/json",
			wantPresence: PreviewValuePresent,
			wantLength:   -1,
			wantReplay:   true,
			wantReplayOK: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoderCalls.Store(0)
			preview, err := test.builder().Preview(t.Context())
			require.NoError(t, err)
			body := preview.Body()
			assert.Equal(t, test.wantKind, body.Kind())
			assert.Equal(t, test.wantMedia, body.MediaType())
			assert.Equal(t, test.wantPresence, body.Presence())
			assert.Equal(t, test.wantLength, body.Length())
			assert.Equal(t, test.wantKnown, body.LengthKnown())
			assert.Equal(t, test.wantReplay, body.Replayable())
			assert.Equal(t, test.wantReplayOK, body.ReplayabilityKnown())
			if test.wantPresence == PreviewValuePresent && test.wantKind != PreviewBodyNone {
				assert.Equal(t, PreviewValueOmitted, body.Value().State())
			}
		})
	}

	assert.Zero(t, encoderCalls.Load())
}

func TestRequestPreviewDoesNotReadOpaqueReader(t *testing.T) {
	reader := &previewTrackingReader{}
	preview, err := newTestClient(t).Post("https://example.com").
		Reader(reader, "application/octet-stream").
		Preview(t.Context())

	require.NoError(t, err)
	assert.Equal(t, PreviewBodyReader, preview.Body().Kind())
	assert.Equal(t, "application/octet-stream", preview.Body().MediaType())
	assert.Equal(t, PreviewValuePresent, preview.Body().Presence())
	assert.Equal(t, PreviewValueOmitted, preview.Body().Value().State())
	assert.False(t, preview.Body().LengthKnown())
	assert.False(t, preview.Body().ReplayabilityKnown())
	assert.Zero(t, reader.reads.Load())
}

func TestRequestPreviewTypedBodiesAreStructuralForAllCodecs(t *testing.T) {
	tests := []struct {
		name      string
		option    func(*atomic.Int64) Option
		build     func(*Client) *RequestBuilder
		wantKind  PreviewBodyKind
		wantMedia string
	}{
		{
			name: "json",
			option: func(calls *atomic.Int64) Option {
				return WithJSONEncoder(countingEncoder{calls: calls})
			},
			build: func(client *Client) *RequestBuilder {
				return client.Post("https://example.com").JSON(struct{ Secret string }{Secret: "value"})
			},
			wantKind:  PreviewBodyJSON,
			wantMedia: "application/json",
		},
		{
			name: "xml",
			option: func(calls *atomic.Int64) Option {
				return WithXMLEncoder(countingEncoder{calls: calls})
			},
			build: func(client *Client) *RequestBuilder {
				return client.Post("https://example.com").XML(struct{ Secret string }{Secret: "value"})
			},
			wantKind:  PreviewBodyXML,
			wantMedia: "application/xml",
		},
		{
			name: "yaml",
			option: func(calls *atomic.Int64) Option {
				return WithYAMLEncoder(countingEncoder{calls: calls})
			},
			build: func(client *Client) *RequestBuilder {
				return client.Post("https://example.com").YAML(struct{ Secret string }{Secret: "value"})
			},
			wantKind:  PreviewBodyYAML,
			wantMedia: "application/yaml",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var encoderCalls atomic.Int64
			client := newTestClient(t, test.option(&encoderCalls))
			preview, err := test.build(client).Preview(t.Context())

			require.NoError(t, err)
			assert.Equal(t, test.wantKind, preview.Body().Kind())
			assert.Equal(t, test.wantMedia, preview.Body().MediaType())
			assert.Equal(t, PreviewValueOmitted, preview.Body().Value().State())
			assert.Zero(t, encoderCalls.Load())
		})
	}
}

func TestRequestPreviewRejectsTypedNilReader(t *testing.T) {
	var reader *previewTrackingReader
	preview, err := newTestClient(t).Post("https://example.com").
		Reader(reader, "application/octet-stream").
		Preview(t.Context())

	assert.Nil(t, preview)
	assert.ErrorIs(t, err, ErrInvalidConfigValue)
}

type previewTrackingReader struct {
	reads atomic.Int64
}

func (r *previewTrackingReader) Read([]byte) (int, error) {
	r.reads.Add(1)
	return 0, io.EOF
}

func TestFormClonesCallerValues(t *testing.T) {
	tests := []struct {
		name  string
		input func() (any, func())
	}{
		{
			name: "url values",
			input: func() (any, func()) {
				values := url.Values{"name": {"original"}}
				return values, func() { values["name"][0] = "mutated" }
			},
		},
		{
			name: "string slice map",
			input: func() (any, func()) {
				values := map[string][]string{"name": {"original"}}
				return values, func() { values["name"][0] = "mutated" }
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, mutate := test.input()
			builder := newTestClient(t).Post("/").Form(input)
			mutate()

			body, err := builder.prepareBody(&clientSnapshot{})
			require.NoError(t, err)
			data, err := io.ReadAll(body.body)
			require.NoError(t, err)
			assert.Equal(t, "name=original", string(data))
		})
	}
}

func TestMultipartExplicitContentTypeReachesTransport(t *testing.T) {
	client := newTestClient(t, WithTransport(testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		assert.Equal(t, "application/vnd.example.upload", req.Header.Get("Content-Type"))
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     http.Header{},
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})))

	resp, err := client.Post("https://example.test/").
		Multipart(NewMultipart().Field("name", "value")).
		ContentType("application/vnd.example.upload").
		Send(t.Context())

	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode())
}

func TestReplayableMultipartReadFailurePreventsDispatch(t *testing.T) {
	readErr := errors.New("multipart source failed")
	var transportCalls atomic.Int32
	client := newTestClient(t, WithTransport(testRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		transportCalls.Add(1)
		return nil, fmt.Errorf("unexpected transport call")
	})))
	body := NewMultipart().
		File("upload", "payload.txt", io.MultiReader(strings.NewReader("prefix"), errorReader{err: readErr})).
		Replayable(1024)

	resp, err := client.Post("https://example.test/").Multipart(body).Send(t.Context())

	assert.Nil(t, resp)
	assert.ErrorIs(t, err, readErr)
	assert.Zero(t, transportCalls.Load())
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

type rejectingEncoder struct {
	err error
}

func (e rejectingEncoder) Encode(any) (io.Reader, error) { return nil, e.err }

func TestBodyPreparationFailuresPreventDispatch(t *testing.T) {
	encodeErr := errors.New("encode rejected")
	tests := []struct {
		name    string
		options []Option
		build   func(*Client) *RequestBuilder
		wantErr error
	}{
		{
			name:    "encoder rejection",
			options: []Option{WithJSONEncoder(rejectingEncoder{err: encodeErr})},
			build: func(client *Client) *RequestBuilder {
				return client.Post("https://example.test/").JSON(struct{}{})
			},
			wantErr: encodeErr,
		},
		{
			name: "removed generated content type",
			build: func(client *Client) *RequestBuilder {
				return client.Post("https://example.test/").JSON(struct{}{}).DelHeader("Content-Type")
			},
			wantErr: ErrUnsupportedContentType,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var transportCalls atomic.Int32
			options := append(slices.Clone(test.options), WithTransport(testRoundTripperFunc(func(*http.Request) (*http.Response, error) {
				transportCalls.Add(1)
				return nil, fmt.Errorf("unexpected transport call")
			})))
			client := newTestClient(t, options...)

			resp, err := test.build(client).Send(t.Context())

			assert.Nil(t, resp)
			assert.ErrorIs(t, err, test.wantErr)
			assert.Zero(t, transportCalls.Load())
		})
	}
}

func TestBuiltInBodyGetBodyReturnsFreshReaders(t *testing.T) {
	client := newTestClient(t, WithTransport(testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.GetBody == nil {
			return nil, fmt.Errorf("missing GetBody")
		}
		first, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		defer first.Close() //nolint:errcheck // test transport cleanup
		second, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		defer second.Close() //nolint:errcheck // test transport cleanup

		prefix := make([]byte, 1)
		if _, err := io.ReadFull(first, prefix); err != nil {
			return nil, err
		}
		firstRest, err := io.ReadAll(first)
		if err != nil {
			return nil, err
		}
		secondBody, err := io.ReadAll(second)
		if err != nil {
			return nil, err
		}
		firstBody := make([]byte, 0, len(prefix)+len(firstRest))
		firstBody = append(firstBody, prefix...)
		firstBody = append(firstBody, firstRest...)
		if !bytes.Equal(firstBody, secondBody) {
			return nil, fmt.Errorf("GetBody readers are not independent")
		}
		_ = req.Body.Close()
		return &http.Response{
			Status:     "200 OK",
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})))

	resp, err := client.Post("https://example.com").JSON(map[string]string{"message": "hello"}).Send(t.Context())

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode())
}
