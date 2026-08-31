package requests

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/test-go/testify/require"
	"golang.org/x/net/http2"
)

func assertHTTP2Configured(t *testing.T, transport *http.Transport) {
	t.Helper()

	require.True(t, transport.ForceAttemptHTTP2)
	if transport.Protocols != nil {
		assert.True(t, transport.Protocols.HTTP2())
		return
	}

	require.NotNil(t, transport.TLSNextProto)
	assert.Contains(t, transport.TLSNextProto, http2.NextProtoTLS)
	require.NotNil(t, transport.TLSClientConfig)
	assert.Contains(t, transport.TLSClientConfig.NextProtos, http2.NextProtoTLS)
	assert.Contains(t, transport.TLSClientConfig.NextProtos, "http/1.1")
}

type testRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f testRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestSetHTTPClient(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("X-Custom-Test-Cookie")
		if err != nil || cookie.Value != "true" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	client := newTestClient(t, WithBaseURL(mockServer.URL))
	client.setHTTPClient(&http.Client{
		Transport: testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			req.AddCookie(&http.Cookie{Name: "X-Custom-Test-Cookie", Value: "true"})
			return http.DefaultTransport.RoundTrip(req)
		}),
	})

	resp, err := client.Get("/test").Send(context.Background())
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode())
}

func TestTransportTimeoutsViaOptions(t *testing.T) {
	t.Parallel()

	client := newTestClient(t,
		WithDialTimeout(5*time.Second),
		WithTLSHandshakeTimeout(4*time.Second),
		WithResponseHeaderTimeout(3*time.Second),
	)

	transport, ok := client.httpClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.NotNil(t, transport.DialContext)
	assert.Equal(t, 4*time.Second, transport.TLSHandshakeTimeout)
	assert.Equal(t, 3*time.Second, transport.ResponseHeaderTimeout)
}

func TestConnectionPoolOptions(t *testing.T) {
	client := newTestClient(t,
		WithMaxIdleConns(50),
		WithMaxIdleConnsPerHost(10),
		WithMaxConnsPerHost(20),
		WithIdleConnTimeout(30*time.Second),
	)

	transport, ok := client.httpClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, 50, transport.MaxIdleConns)
	assert.Equal(t, 10, transport.MaxIdleConnsPerHost)
	assert.Equal(t, 20, transport.MaxConnsPerHost)
	assert.Equal(t, 30*time.Second, transport.IdleConnTimeout)
}

func TestHTTP2OptionsPreserveHTTPTransportSettings(t *testing.T) {
	t.Parallel()

	tlsConfig := &tls.Config{InsecureSkipVerify: true}
	client := newTestClient(t,
		WithHTTP2(),
		WithTLSConfig(tlsConfig),
		WithDialTimeout(5*time.Second),
		WithTLSHandshakeTimeout(4*time.Second),
		WithResponseHeaderTimeout(3*time.Second),
		WithMaxIdleConns(50),
		WithMaxIdleConnsPerHost(10),
		WithMaxConnsPerHost(20),
		WithIdleConnTimeout(30*time.Second),
	)

	transport, ok := client.httpClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.NotSame(t, tlsConfig, transport.TLSClientConfig)
	assert.True(t, transport.TLSClientConfig.InsecureSkipVerify)
	assert.NotNil(t, transport.DialContext)
	assert.Equal(t, 4*time.Second, transport.TLSHandshakeTimeout)
	assert.Equal(t, 3*time.Second, transport.ResponseHeaderTimeout)
	assert.Equal(t, 50, transport.MaxIdleConns)
	assert.Equal(t, 10, transport.MaxIdleConnsPerHost)
	assert.Equal(t, 20, transport.MaxConnsPerHost)
	assert.Equal(t, 30*time.Second, transport.IdleConnTimeout)
	assertHTTP2Configured(t, transport)
}

func TestWithHTTP2CompletesExistingProtocolConfiguration(t *testing.T) {
	protocols := new(http.Protocols)
	protocols.SetHTTP2(true)
	transport := &http.Transport{Protocols: protocols}

	client := newTestClient(t, WithTransport(transport), WithHTTP2())

	configured, ok := client.UnsafeHTTPClient().Transport.(*http.Transport)
	require.True(t, ok)
	assert.Same(t, transport, configured)
	assert.True(t, configured.Protocols.HTTP2())
	assert.True(t, configured.ForceAttemptHTTP2)
	require.NotNil(t, configured.TLSClientConfig)
	assert.Contains(t, configured.TLSClientConfig.NextProtos, http2.NextProtoTLS)
	assert.Contains(t, configured.TLSClientConfig.NextProtos, "http/1.1")
}

func TestHTTP2OptionsNegotiateHTTP2(t *testing.T) {
	t.Parallel()

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	client := newTestClient(t,
		WithHTTP2(),
		WithTLSConfig(&tls.Config{InsecureSkipVerify: true}),
	)

	resp, err := client.Get(server.URL).Send(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "HTTP/2.0", resp.Protocol())
}

func TestTransportConfigNoOpWhenNoSettings(t *testing.T) {
	client := newTestClient(t, WithBaseURL("http://example.com"))
	assert.Nil(t, client.httpClient.Transport)
}

func TestDialContextCanRestoreDefaultDialer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := newTestClient(t,
		WithDialContext(func(context.Context, string, string) (net.Conn, error) {
			return nil, assert.AnError
		}),
		WithDialContext(nil),
	)

	resp, err := client.Get(server.URL).Send(t.Context())
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode())
}

func TestTransportOptionRejectsNonstandardDefaultTransport(t *testing.T) {
	previous := http.DefaultTransport
	http.DefaultTransport = testRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, assert.AnError
	})
	t.Cleanup(func() { http.DefaultTransport = previous })

	client, err := New(WithDialTimeout(time.Second))

	assert.Nil(t, client)
	assert.ErrorIs(t, err, ErrInvalidTransportType)
}

func TestEnsureTransportInvalidType(t *testing.T) {
	_, err := New(
		WithHTTPClient(&http.Client{
			Transport: testRoundTripperFunc(func(*http.Request) (*http.Response, error) {
				return nil, nil
			}),
		}),
		WithProxy("http://proxy.example.com"),
	)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidTransportType)
}
