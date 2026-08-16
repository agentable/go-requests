package requests

import (
	"crypto/tls"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentable/go-orderedobject"
	"github.com/stretchr/testify/assert"
	"github.com/test-go/testify/require"
)

func TestAsHTTPClientDoesNotReintroduceCredentialsAcrossOrigins(t *testing.T) {
	type credentials struct {
		authorization string
		cookie        string
	}

	destinationCredentials := make(chan credentials, 1)
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCredentials <- credentials{
			authorization: r.Header.Get("Authorization"),
			cookie:        r.Header.Get("Cookie"),
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer destination.Close()

	sourceCredentials := make(chan credentials, 1)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceCredentials <- credentials{
			authorization: r.Header.Get("Authorization"),
			cookie:        r.Header.Get("Cookie"),
		}
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer source.Close()

	client := newTestClient(t,
		WithBearerAuth("default-token"),
		WithCookies(map[string]string{"session": "default"}),
		WithRedirectPolicy(NewAllowRedirectPolicy(2)),
	)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, source.URL, nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer explicit-token")
	request.AddCookie(&http.Cookie{Name: "session", Value: "explicit"})

	resp, err := client.AsHTTPClient().Do(request)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	sourceGot := <-sourceCredentials
	assert.Equal(t, "Bearer explicit-token", sourceGot.authorization)
	assert.Equal(t, "session=explicit", sourceGot.cookie)
	destinationGot := <-destinationCredentials
	assert.Empty(t, destinationGot.authorization)
	assert.Empty(t, destinationGot.cookie)
}

func TestAsHTTPClientSnapshotsNetHTTPConfiguration(t *testing.T) {
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	var gotHeaders http.Header
	var gotOrderedHeaders bool
	transport := testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		gotHeaders = req.Header.Clone()
		_, gotOrderedHeaders = OrderedHeaders(req)
		return &http.Response{
			Status:     "204 No Content",
			StatusCode: http.StatusNoContent,
			Header:     http.Header{},
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})
	checkRedirect := func(*http.Request, []*http.Request) error {
		return assert.AnError
	}
	source := &http.Client{
		Transport:     &transport,
		CheckRedirect: checkRedirect,
		Jar:           jar,
		Timeout:       5 * time.Second,
	}
	ordered := orderedobject.New[[]string]().Set("X-Ordered", []string{"value"})
	var middlewareCalls atomic.Int64
	client := newTestClient(t,
		WithHTTPClient(source),
		WithHeader("X-Default", "value"),
		WithOrderedHeaders(ordered),
		WithCookies(map[string]string{"session": "default"}),
		WithBearerAuth("default-token"),
		WithMiddleware(func(next MiddlewareHandlerFunc) MiddlewareHandlerFunc {
			return func(req *http.Request) (*http.Response, error) {
				middlewareCalls.Add(1)
				return next(req)
			}
		}),
	)

	snapshot := client.AsHTTPClient()
	assert.NotSame(t, source, snapshot)
	assert.Same(t, &transport, snapshot.Transport)
	assert.Same(t, jar, snapshot.Jar)
	assert.Equal(t, 5*time.Second, snapshot.Timeout)
	redirectRequest, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com/next", nil)
	require.NoError(t, err)
	assert.ErrorIs(t, snapshot.CheckRedirect(redirectRequest, nil), assert.AnError)

	resp, err := snapshot.Get("https://example.com")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Empty(t, gotHeaders.Get("X-Default"))
	assert.Empty(t, gotHeaders.Get("X-Ordered"))
	assert.Empty(t, gotHeaders.Get("Authorization"))
	assert.Empty(t, gotHeaders.Get("Cookie"))
	assert.False(t, gotOrderedHeaders)
	assert.Zero(t, middlewareCalls.Load())

	snapshot.Timeout = time.Second
	assert.Equal(t, 5*time.Second, source.Timeout)
}

func TestAsHTTPClientClonesStandardTransportAndTLSConfig(t *testing.T) {
	sessionCache := tls.NewLRUClientSessionCache(1)
	sourceTLS := &tls.Config{
		ServerName:         "source.example.com",
		ClientSessionCache: sessionCache,
	}
	sourceTransport := &http.Transport{
		MaxIdleConns:    7,
		TLSClientConfig: sourceTLS,
	}
	source := &http.Client{Transport: sourceTransport}
	client := newTestClient(t, WithHTTPClient(source), WithTLSConfig(sourceTLS))
	sourceTLS = sourceTransport.TLSClientConfig

	snapshot := client.AsHTTPClient()
	snapshotTransport, ok := snapshot.Transport.(*http.Transport)
	require.True(t, ok)
	assert.NotSame(t, sourceTransport, snapshotTransport)
	assert.NotSame(t, sourceTLS, snapshotTransport.TLSClientConfig)
	assert.Same(t, sessionCache, snapshotTransport.TLSClientConfig.ClientSessionCache)

	snapshotTransport.MaxIdleConns = 11
	snapshotTransport.TLSClientConfig.ServerName = "snapshot.example.com"
	assert.Equal(t, 7, sourceTransport.MaxIdleConns)
	assert.Equal(t, "source.example.com", sourceTLS.ServerName)
}
