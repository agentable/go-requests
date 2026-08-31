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
