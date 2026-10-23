package middlewares

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/agentable/go-requests"
)

func TestAddHeadersPreservesNextResult(t *testing.T) {
	t.Parallel()
	req, err := http.NewRequest(http.MethodGet, "https://example.test", nil)
	assert.NoError(t, err)
	req.Header.Add("X-Value", "existing")
	wantResponse := &http.Response{StatusCode: http.StatusBadGateway}
	wantErr := errors.New("downstream failure")
	handler := AddHeaders(http.Header{"X-Value": {"first", "second"}})(func(got *http.Request) (*http.Response, error) {
		assert.Same(t, req, got)
		assert.Equal(t, []string{"existing", "first", "second"}, got.Header.Values("X-Value"))
		return wantResponse, wantErr
	})
	response, err := handler(req)
	assert.Same(t, wantResponse, response)
	assert.ErrorIs(t, err, wantErr)
}

func TestAddHeaders(t *testing.T) {
	tests := []struct {
		name           string
		headers        http.Header
		existingHeader http.Header
		wantHeader     http.Header
	}{
		{
			name: "Add new headers",
			headers: http.Header{
				"User-Agent": []string{"Custom-Agent"},
				"Accept":     []string{"application/json"},
			},
			existingHeader: http.Header{},
			wantHeader: http.Header{
				"User-Agent": []string{"Custom-Agent"},
				"Accept":     []string{"application/json"},
			},
		},
		{
			name: "Add headers with existing values",
			headers: http.Header{
				"User-Agent": []string{"Custom-Agent-2"},
				"Accept":     []string{"text/plain"},
			},
			existingHeader: http.Header{
				"User-Agent":   []string{"Custom-Agent-1"},
				"Content-Type": []string{"application/json"},
			},
			wantHeader: http.Header{
				"User-Agent":   []string{"Custom-Agent-1", "Custom-Agent-2"},
				"Accept":       []string{"text/plain"},
				"Content-Type": []string{"application/json"},
			},
		},
		{
			name: "Add multiple values for same header",
			headers: http.Header{
				"Accept": []string{"application/json", "text/plain"},
			},
			existingHeader: http.Header{},
			wantHeader: http.Header{
				"Accept": []string{"application/json", "text/plain"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test request with existing headers
			req, _ := http.NewRequest("GET", "http://example.com", nil)
			for k, v := range tt.existingHeader {
				for _, val := range v {
					req.Header.Add(k, val)
				}
			}

			// Create middleware
			middleware := AddHeaders(tt.headers)

			// Create a mock next handler
			nextHandler := requests.MiddlewareHandlerFunc(func(req *http.Request) (*http.Response, error) {
				// Check if headers were properly set
				for key, wantValues := range tt.wantHeader {
					gotValues := req.Header[key]
					assert.Equal(t, len(wantValues), len(gotValues),
						"header %s: unexpected number of values", key)

					for i, want := range wantValues {
						if i < len(gotValues) {
							assert.Equal(t, want, gotValues[i],
								"header %s[%d]: unexpected value", key, i)
						}
					}
				}
				return &http.Response{}, nil
			})

			// Execute middleware
			handler := middleware(nextHandler)
			_, err := handler(req)
			assert.NoError(t, err, "unexpected error")
		})
	}
}
