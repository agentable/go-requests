package requests

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
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
	assert.NotSame(t, transport, configured)
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

func TestWithTransportIsolatesConfiguration(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("origin")) }))
	defer origin.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("proxy")) }))
	defer proxy.Close()
	input := &http.Transport{MaxIdleConns: 7, ResponseHeaderTimeout: time.Second, TLSClientConfig: &tls.Config{ServerName: "original"}}
	first := newTestClient(t, WithTransport(input))
	second := newTestClient(t, WithTransport(input), WithMaxIdleConns(11), WithProxy(proxy.URL), WithTLSServerName("second"), WithHTTP2())
	defer input.CloseIdleConnections()
	defer first.UnsafeHTTPClient().CloseIdleConnections()
	defer second.UnsafeHTTPClient().CloseIdleConnections()
	assert.Equal(t, 7, input.MaxIdleConns)
	assert.Nil(t, input.Proxy)
	assert.Equal(t, "original", input.TLSClientConfig.ServerName)
	firstTransport := first.UnsafeHTTPClient().Transport.(*http.Transport)
	assert.Equal(t, 7, firstTransport.MaxIdleConns)
	assert.Equal(t, time.Second, firstTransport.ResponseHeaderTimeout)
	for _, test := range []struct {
		client *Client
		want   string
	}{{first, "origin"}, {second, "proxy"}} {
		resp, err := test.client.Get(origin.URL).Send(t.Context())
		require.NoError(t, err)
		assert.Equal(t, test.want, resp.String())
	}
	_, err := New(WithTransport(input), WithMaxIdleConns(99), WithTimeout(-time.Second))
	require.Error(t, err)
	assert.Equal(t, 7, input.MaxIdleConns)
}

func TestWithTransportTLSIsolation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	input := server.Client().Transport.(*http.Transport)
	first := newTestClient(t, WithTransport(input))
	second := newTestClient(t, WithTransport(input), WithTLSConfig(&tls.Config{RootCAs: x509.NewCertPool()}))
	defer first.UnsafeHTTPClient().CloseIdleConnections()
	defer second.UnsafeHTTPClient().CloseIdleConnections()
	_, err := second.Get(server.URL).Send(t.Context())
	require.Error(t, err)
	_, err = first.Get(server.URL).Send(t.Context())
	require.NoError(t, err)
	resp, err := server.Client().Get(server.URL)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}

func TestWithTLSHandshakeUsesEffectiveTransport(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	var calls atomic.Int32
	handshake := func(ctx context.Context, raw net.Conn, cfg *tls.Config) (net.Conn, error) {
		calls.Add(1)
		conn := tls.Client(raw, cfg)
		if err := conn.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		return conn, nil
	}
	client := newTestClient(t, WithoutProxy(), WithHTTP2(), WithTLSHandshake(handshake), WithTLSConfig(&tls.Config{RootCAs: roots}))
	resp, err := client.Get(server.URL).Send(t.Context())
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, resp.StatusCode())
	require.Equal(t, "HTTP/2.0", resp.Raw().Proto)
	require.Equal(t, int32(1), calls.Load())
}

type handshakeTestConn struct {
	net.Conn
	closes atomic.Int32
}

func (c *handshakeTestConn) Close() error { c.closes.Add(1); return c.Conn.Close() }

func TestWithTLSHandshakeClosesFailedConnection(t *testing.T) {
	rejected := errors.New("handshake rejected")
	for _, mode := range []string{"error", "connection-and-error", "nil", "typed-nil", "timeout", "cancel", "canceled-success"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				left, right := net.Pipe()
				defer func() { _ = right.Close() }()
				raw := &handshakeTestConn{Conn: left}
				defer func() { _ = left.Close() }()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				handshake := func(ctx context.Context, conn net.Conn, _ *tls.Config) (net.Conn, error) {
					switch mode {
					case "error":
						return nil, rejected
					case "connection-and-error":
						return conn, rejected
					case "nil":
						return nil, nil
					case "typed-nil":
						return (*tls.Conn)(nil), nil
					case "canceled-success":
						cancel()
						return conn, nil
					case "cancel":
						cancel()
						<-ctx.Done()
						return nil, ctx.Err()
					case "timeout":
						if _, ok := ctx.Deadline(); !ok {
							return nil, errors.New("missing handshake deadline")
						}
						<-ctx.Done()
						return nil, ctx.Err()
					default:
						t.Fatal("unexpected mode")
						return nil, nil
					}
				}
				client := newTestClient(t, WithTLSHandshake(handshake), WithTLSHandshakeTimeout(time.Second),
					WithDialContext(func(context.Context, string, string) (net.Conn, error) { return raw, nil }))
				transport := client.AsHTTPClient().Transport.(*http.Transport)
				conn, err := transport.DialTLSContext(ctx, "tcp", "example.test:443")
				require.Nil(t, conn)
				switch mode {
				case "error", "connection-and-error":
					require.True(t, errors.Is(err, rejected), "error: %v", err)
				case "nil", "typed-nil":
					require.True(t, errors.Is(err, ErrInvalidConfigValue), "error: %v", err)
				case "timeout":
					require.True(t, errors.Is(err, context.DeadlineExceeded), "error: %v", err)
					require.True(t, IsTimeout(err))
				case "cancel", "canceled-success":
					require.True(t, errors.Is(err, context.Canceled), "error: %v", err)
				}
				require.Equal(t, int32(1), raw.closes.Load())
			})
		})
	}
}

func TestWithTLSHandshakeCopies(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	for _, mode := range []string{"clone", "adapter"} {
		t.Run(mode, func(t *testing.T) {
			base := newTestClient(t, WithoutProxy(), WithTLSConfig(&tls.Config{RootCAs: x509.NewCertPool(), ServerName: "example.com"}),
				WithTLSHandshake(func(ctx context.Context, raw net.Conn, cfg *tls.Config) (net.Conn, error) {
					conn := tls.Client(raw, cfg)
					if err := conn.HandshakeContext(ctx); err != nil {
						return nil, err
					}
					return conn, nil
				}))
			cfg := &tls.Config{RootCAs: roots, ServerName: "example.com"}
			if mode == "clone" {
				derived, err := base.Clone(WithTLSConfig(cfg))
				require.NoError(t, err)
				_, err = derived.Get(server.URL).Send(t.Context())
				require.NoError(t, err)
			} else {
				hc := base.AsHTTPClient()
				hc.Transport.(*http.Transport).TLSClientConfig = cfg
				req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
				require.NoError(t, err)
				resp, err := hc.Do(req)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
			}
			_, err := base.Get(server.URL).Send(t.Context())
			require.Error(t, err)
		})
	}
}

func TestWithTLSHandshakeReplacement(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	config := &tls.Config{RootCAs: roots}
	oldErr := errors.New("old handshake")
	base := newTestClient(t, WithoutProxy(), WithTLSConfig(config), WithTLSHandshake(func(context.Context, net.Conn, *tls.Config) (net.Conn, error) { return nil, oldErr }))
	for _, mode := range []string{"clear", "transport", "client"} {
		t.Run(mode, func(t *testing.T) {
			option := WithTLSHandshake(nil)
			switch mode {
			case "transport":
				option = WithTransport(&http.Transport{TLSClientConfig: config})
			case "client":
				option = WithHTTPClient(&http.Client{Transport: &http.Transport{TLSClientConfig: config}})
			}
			derived, err := base.Clone(option)
			require.NoError(t, err)
			copied, err := derived.Clone()
			require.NoError(t, err)
			_, err = copied.Get(server.URL).Send(t.Context())
			require.NoError(t, err)
		})
	}
	_, err := base.Get(server.URL).Send(t.Context())
	require.True(t, errors.Is(err, oldErr))
	for _, handshake := range []tlsHandshakeFunc{nil, func(context.Context, net.Conn, *tls.Config) (net.Conn, error) { return nil, oldErr }} {
		_, err := New(WithTransport(testRoundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, oldErr })), WithTLSHandshake(handshake))
		require.True(t, errors.Is(err, ErrInvalidTransportType))
	}
}

func TestWithTLSHandshakeDialing(t *testing.T) {
	stopped := errors.New("dial stopped")
	for _, mode := range []string{"context", "legacy", "nil", "typed-nil"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.NotFoundHandler())
			defer server.Close()
			called := false
			input := &http.Transport{Dial: func(string, string) (net.Conn, error) { return nil, stopped }} //nolint:staticcheck // Verify injected legacy dial precedence matches net/http.
			if mode != "legacy" {
				input.DialContext = func(context.Context, string, string) (net.Conn, error) {
					switch mode {
					case "nil":
						return nil, nil
					case "typed-nil":
						return (*net.TCPConn)(nil), nil
					default:
						return nil, stopped
					}
				}
			}
			client := newTestClient(t, WithTransport(input), WithTLSHandshake(func(context.Context, net.Conn, *tls.Config) (net.Conn, error) {
				called = true
				return nil, errors.New("unexpected handshake")
			}))
			transport := client.AsHTTPClient().Transport.(*http.Transport)
			conn, err := transport.DialTLSContext(t.Context(), "tcp", server.Listener.Addr().String())
			require.Nil(t, conn)
			require.False(t, called)
			if mode == "nil" || mode == "typed-nil" {
				require.True(t, errors.Is(err, ErrInvalidConfigValue))
			} else {
				require.True(t, errors.Is(err, stopped))
			}
		})
	}
}

func TestWithTLSHandshakeTimeoutStartsAfterDial(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		left, right := net.Pipe()
		defer func() { _ = right.Close() }()
		raw := &handshakeTestConn{Conn: left}
		defer func() { _ = left.Close() }()
		start := time.Now()
		client := newTestClient(t, WithTLSHandshakeTimeout(time.Second),
			WithDialContext(func(ctx context.Context, _, _ string) (net.Conn, error) {
				time.Sleep(2 * time.Second)
				require.NoError(t, ctx.Err())
				return raw, nil
			}), WithTLSHandshake(func(ctx context.Context, _ net.Conn, _ *tls.Config) (net.Conn, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}))
		transport := client.AsHTTPClient().Transport.(*http.Transport)
		_, err := transport.DialTLSContext(t.Context(), "tcp", "example.test:443")
		require.True(t, errors.Is(err, context.DeadlineExceeded))
		require.Equal(t, 3*time.Second, time.Since(start))
		require.Equal(t, int32(1), raw.closes.Load())
	})
}
