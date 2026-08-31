package http3

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/quic-go/quic-go"
	qhttp3 "github.com/quic-go/quic-go/http3"
	"github.com/test-go/testify/require"

	"github.com/agentable/go-requests"
)

func TestTransportOptions(t *testing.T) {
	tlsConfig := &tls.Config{ServerName: "example.com", MinVersion: tls.VersionTLS13}
	quicConfig := &quic.Config{}
	settings := map[uint64]uint64{0x21: 1}
	logger := slog.Default()

	transport := Transport(
		WithTLSConfig(tlsConfig),
		WithQUICConfig(quicConfig),
		WithDatagrams(),
		WithAdditionalSettings(settings),
		WithMaxResponseHeaderBytes(1024),
		WithoutCompression(),
		WithLogger(logger),
	)

	require.False(t, tlsConfig == transport.TLSClientConfig)
	require.Equal(t, "example.com", transport.TLSClientConfig.ServerName)
	require.False(t, quicConfig == transport.QUICConfig)
	require.True(t, transport.QUICConfig.EnableDatagrams)
	require.True(t, transport.EnableDatagrams)
	if diff := cmp.Diff(map[uint64]uint64{0x21: 1}, transport.AdditionalSettings); diff != "" {
		t.Errorf("additional settings mismatch (-want +got):\n%s", diff)
	}
	require.Equal(t, 1024, transport.MaxResponseHeaderBytes)
	require.True(t, transport.DisableCompression)
	require.True(t, logger == transport.Logger)

	settings[0x21] = 2
	require.Equal(t, uint64(1), transport.AdditionalSettings[0x21])
}

func TestTransportOptionsIgnoreNil(t *testing.T) {
	transport := Transport(nil, WithDatagrams())
	require.NotNil(t, transport)
	require.True(t, transport.EnableDatagrams)
}

func TestTransportSendsHTTP3RequestAndClosesConcurrently(t *testing.T) {
	source := httptest.NewTLSServer(http.NotFoundHandler())
	cert := source.TLS.Certificates[0]
	source.Close()

	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)

	server := &qhttp3.Server{
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			NextProtos:   []string{qhttp3.NextProtoH3},
		},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Proto != "HTTP/3.0" {
				w.WriteHeader(http.StatusHTTPVersionNotSupported)
				return
			}
			_, _ = w.Write([]byte("h3"))
		}),
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(packetConn)
	}()
	t.Cleanup(func() {
		require.NoError(t, server.Close())
		require.NoError(t, packetConn.Close())
		require.True(t, errors.Is(<-errCh, http.ErrServerClosed))
	})

	transport := Transport(WithTLSConfig(&tls.Config{
		InsecureSkipVerify: true,
	}))
	t.Cleanup(func() { _ = transport.Close() })
	client, err := requests.New(requests.WithTransport(transport))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	resp, err := client.Get("https://" + packetConn.LocalAddr().String()).Send(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode())
	require.Equal(t, "HTTP/3.0", resp.Protocol())
	require.Equal(t, "h3", resp.String())

	closeErrors := make(chan error, 8)
	var closeGroup sync.WaitGroup
	for range 8 {
		closeGroup.Go(func() {
			closeErrors <- transport.Close()
		})
	}
	closeGroup.Wait()
	close(closeErrors)
	for closeErr := range closeErrors {
		require.NoError(t, closeErr)
	}

	resp, err = client.Get("https://" + packetConn.LocalAddr().String()).Send(ctx)
	require.Nil(t, resp)
	require.True(t, errors.Is(err, qhttp3.ErrTransportClosed))
}
