package requests

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/test-go/testify/require"
)

func createTestTLSServer() (*httptest.Server, error) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cert, err := tls.LoadX509KeyPair(".github/testdata/cert.pem", ".github/testdata/key.pem")
	if err != nil {
		server.Close()
		return nil, fmt.Errorf("load test certificate: %w", err)
	}

	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.StartTLS()
	return server, nil
}

func TestSetTLSConfig(t *testing.T) {
	server, err := createTestTLSServer()
	require.NoError(t, err)
	defer server.Close()

	client := newTestClient(t, WithBaseURL(server.URL))
	require.NoError(t, client.setTLSConfig(&tls.Config{InsecureSkipVerify: true}))

	response, err := client.Get("/").Send(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, response)
}

func TestSetTLSConfigWithCert(t *testing.T) {
	server, err := createTestTLSServer()
	require.NoError(t, err)
	defer server.Close()

	cert, err := os.ReadFile(".github/testdata/cert.pem")
	require.NoError(t, err)
	certPool := x509.NewCertPool()
	require.True(t, certPool.AppendCertsFromPEM(cert))

	client := newTestClient(t, WithBaseURL(server.URL))
	require.NoError(t, client.setTLSConfig(&tls.Config{RootCAs: certPool}))

	response, err := client.Get("/").Send(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, response)
}

func TestInsecureSkipVerify(t *testing.T) {
	server, err := createTestTLSServer()
	require.NoError(t, err)
	defer server.Close()

	client := newTestClient(t, WithBaseURL(server.URL))
	require.NoError(t, client.insecureSkipVerify())

	response, err := client.Get("/").Send(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, response)
}

func TestCreateTestTLSServerMissingCertificate(t *testing.T) {
	originalCertPath := ".github/testdata/cert.pem"
	tempCertPath := ".github/testdata/cert.pem.bak"
	require.NoError(t, os.Rename(originalCertPath, tempCertPath))
	defer func() {
		require.NoError(t, os.Rename(tempCertPath, originalCertPath))
	}()

	server, err := createTestTLSServer()
	require.Error(t, err)
	assert.Nil(t, server)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestClientCertificates(t *testing.T) {
	serverCert, err := tls.LoadX509KeyPair(".github/testdata/cert.pem", ".github/testdata/key.pem")
	require.NoError(t, err, "load server certificate failed")

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("certificate verification successful"))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("lack of client certificate"))
	}))
	clientCertPool := x509.NewCertPool()
	clientCertData, err := os.ReadFile(".github/testdata/cert.pem")
	require.NoError(t, err, "load client certificate failed")
	clientCertPool.AppendCertsFromPEM(clientCertData)

	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    clientCertPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	server.StartTLS()
	defer server.Close()

	client := newTestClient(t, WithBaseURL(server.URL))

	t.Run("use client certificate", func(t *testing.T) {
		clientCert, err := tls.LoadX509KeyPair(".github/testdata/cert.pem", ".github/testdata/key.pem")
		require.NoError(t, err, "load client certificate failed")

		require.NoError(t, client.setTLSConfig(&tls.Config{InsecureSkipVerify: true}))
		require.NoError(t, client.setCertificates(clientCert))
		resp, err := client.Get("/").Send(context.Background())
		require.NoError(t, err)
		defer resp.Close() //nolint:errcheck // test cleanup closes response body

		assert.Equal(t, http.StatusOK, resp.StatusCode(), "status code not correct")
		assert.Equal(t, "certificate verification successful", resp.String(), "response content not correct")
	})

	t.Run("do not use client certificate", func(t *testing.T) {
		clientWithoutCert := newTestClient(t, WithBaseURL(server.URL))
		require.NoError(t, clientWithoutCert.setTLSConfig(&tls.Config{InsecureSkipVerify: true}))

		_, err := clientWithoutCert.Get("/").Send(context.Background())
		assert.Error(t, err, "expect request failed")
	})
}
