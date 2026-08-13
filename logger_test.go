package requests

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/test-go/testify/require"
)

type mockLoggerRecorder struct {
	Records []string
}

func (m *mockLoggerRecorder) Write(p []byte) (n int, err error) {
	m.Records = append(m.Records, string(p))
	return len(p), nil
}

func TestDefaultLoggerLevels(t *testing.T) {
	rec := &mockLoggerRecorder{}
	logger := NewDefaultLogger(rec, LevelDebug)

	logger.Debugf("debug %s", "message")
	logger.Infof("info %s", "message")
	logger.Warnf("warn %s", "message")
	logger.Errorf("error %s", "message")

	assert.Len(t, rec.Records, 4, "Should log 4 messages")
	assert.Contains(t, rec.Records[0], "debug message", "Debug log message should match")
	assert.Contains(t, rec.Records[1], "info message", "Info log message should match")
	assert.Contains(t, rec.Records[2], "warn message", "Warn log message should match")
	assert.Contains(t, rec.Records[3], "error message", "Error log message should match")
}

func TestDefaultLoggerSetLevel(t *testing.T) {
	rec := &mockLoggerRecorder{}
	logger := NewDefaultLogger(rec, LevelInfo)

	logger.Debugf("hidden")
	logger.SetLevel(LevelDebug)
	logger.Debugf("visible")

	require.Len(t, rec.Records, 1)
	assert.Contains(t, rec.Records[0], "visible")
}

type mockLogger struct {
	Infos  []string
	Errors []string
}

func (m *mockLogger) Debugf(format string, v ...any) {}
func (m *mockLogger) Infof(format string, v ...any) {
	m.Infos = append(m.Infos, fmt.Sprintf(format, v...))
}
func (m *mockLogger) Warnf(format string, v ...any) {}
func (m *mockLogger) Errorf(format string, v ...any) {
	m.Errors = append(m.Errors, fmt.Sprintf(format, v...))
}

func TestRetryLogMessage(t *testing.T) {
	// Initialize attempt counter
	var attempts int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			// Fail initially to trigger a retry
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			// Succeed in the next attempt
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	mockLogger := &mockLogger{}
	client := newTestClient(t,
		WithBaseURL(server.URL),
		WithLogger(mockLogger),
		WithRetry(RetryPolicy{
			Max: 1,
			Backoff: func(attempt int) time.Duration {
				return 0 // No delay for testing
			},
		}),
	)

	// Making a request that should trigger a retry
	_, err := client.Get("/test").Send(context.Background())
	assert.Nil(t, err, "Did not expect an error after retry")

	// Check if the retry log message was recorded
	expectedLogMessage := "Retrying request (attempt 1) after backoff"
	found := false
	for _, logMsg := range mockLogger.Infos {
		if strings.Contains(logMsg, expectedLogMessage) {
			found = true
			break
		}
	}
	assert.True(t, found, "Expected retry log message was not recorded")
}

func TestLoggerURLDiagnosticRedaction(t *testing.T) {
	tests := []struct {
		name    string
		markers []string
		run     func(*mockLogger) error
	}{
		{
			name:    "preflight",
			markers: []string{"log-path-user-marker", "log-path-password-marker", "log-path-query-marker", "log-path-fragment-marker"},
			run: func(logger *mockLogger) error {
				client := newTestClient(t, WithLogger(logger))
				_, err := client.Get("https://log-path-user-marker:log-path-password-marker@example.com/%zz?token=log-path-query-marker#log-path-fragment-marker").Send(t.Context())
				return err
			},
		},
		{
			name:    "transport",
			markers: []string{"log-transport-user-marker", "log-transport-password-marker", "log-transport-query-marker", "log-transport-fragment-marker"},
			run: func(logger *mockLogger) error {
				client := newTestClient(t,
					WithLogger(logger),
					WithTransport(testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
						return nil, fmt.Errorf("transport wrapper: %w", &url.Error{Op: "round trip", URL: req.URL.String(), Err: assert.AnError})
					})),
				)
				_, err := client.Get("https://log-transport-user-marker:log-transport-password-marker@example.com/resource?token=log-transport-query-marker#log-transport-fragment-marker").Send(t.Context())
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logger := &mockLogger{}
			require.Error(t, test.run(logger))
			require.NotEmpty(t, logger.Errors)
			records := strings.Join(logger.Errors, "\n")
			for _, marker := range test.markers {
				assert.NotContains(t, records, marker)
			}
		})
	}
}
