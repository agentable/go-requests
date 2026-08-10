package requests

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
