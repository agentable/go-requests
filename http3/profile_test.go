package http3

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
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

func TestTransportMultipartLifecycle(t *testing.T) {
	serverRequest := make(chan struct{}, 2)
	serverCanceled := make(chan struct{}, 1)
	sourceTLS := httptest.NewTLSServer(http.NotFoundHandler())
	cert := sourceTLS.TLS.Certificates[0]
	sourceTLS.Close()

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
			serverRequest <- struct{}{}
			if r.URL.Path == "/cancel" {
				<-r.Context().Done()
				serverCanceled <- struct{}{}
				return
			}
			_, _ = w.Write([]byte("rejected"))
		}),
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(packetConn) }()
	t.Cleanup(func() {
		require.NoError(t, server.Close())
		require.NoError(t, packetConn.Close())
		require.True(t, errors.Is(<-serverErr, http.ErrServerClosed))
	})

	transport := Transport(WithTLSConfig(&tls.Config{InsecureSkipVerify: true}))
	t.Cleanup(func() { _ = transport.Close() })
	client, err := requests.New(requests.WithTransport(transport))
	require.NoError(t, err)
	url := "https://" + packetConn.LocalAddr().String()

	t.Run("early response close waits for source", func(t *testing.T) {
		release := make(chan struct{})
		source := newLifecycleBlockingReader(release)
		released := false
		t.Cleanup(func() {
			if !released {
				close(release)
			}
			select {
			case <-source.entered:
				select {
				case <-source.readDone:
				case <-time.After(5 * time.Second):
					t.Error("multipart source Read did not exit during cleanup")
				}
			default:
			}
		})
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		resultCh := make(chan struct {
			response *requests.StreamResponse
			err      error
		}, 1)
		go func() {
			response, err := client.Post(url + "/early").
				Multipart(requests.NewMultipart().File("upload", "payload.txt", source)).
				SendStream(ctx)
			resultCh <- struct {
				response *requests.StreamResponse
				err      error
			}{response, err}
		}()

		waitLifecycleSignal(t, source.entered, "multipart source read did not start")
		waitLifecycleSignal(t, serverRequest, "HTTP/3 server did not receive the request")
		var result struct {
			response *requests.StreamResponse
			err      error
		}
		select {
		case result = <-resultCh:
		case <-time.After(5 * time.Second):
			t.Fatal("SendStream did not return the early response")
		}
		require.NoError(t, result.err)
		require.Equal(t, "HTTP/3.0", result.response.Raw().Proto)
		responseBody, err := io.ReadAll(result.response.Body())
		require.NoError(t, err)
		require.Equal(t, "rejected", string(responseBody))

		closeDone := make(chan error, 1)
		go func() { closeDone <- result.response.Close() }()
		select {
		case err := <-closeDone:
			t.Fatalf("StreamResponse.Close returned while source Read was active: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		close(release)
		released = true
		select {
		case err := <-closeDone:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("StreamResponse.Close did not finish after releasing the source")
		}
		require.Equal(t, int32(0), source.active.Load())
		require.Equal(t, int32(0), source.closes.Load())
	})

	t.Run("canceled failure waits for source", func(t *testing.T) {
		release := make(chan struct{})
		source := newLifecycleBlockingReader(release)
		released := false
		t.Cleanup(func() {
			if !released {
				close(release)
			}
			select {
			case <-source.entered:
				select {
				case <-source.readDone:
				case <-time.After(5 * time.Second):
					t.Error("multipart source Read did not exit during cleanup")
				}
			default:
			}
		})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		resultCh := make(chan error, 1)
		go func() {
			_, err := client.Post(url + "/cancel").
				Multipart(requests.NewMultipart().File("upload", "payload.txt", source)).
				SendStream(ctx)
			resultCh <- err
		}()

		waitLifecycleSignal(t, source.entered, "multipart source read did not start")
		waitLifecycleSignal(t, serverRequest, "HTTP/3 server did not receive the request")
		cancel()
		waitLifecycleSignal(t, serverCanceled, "HTTP/3 server did not observe cancellation")
		select {
		case err := <-resultCh:
			t.Fatalf("SendStream returned while source Read was active: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		close(release)
		released = true
		select {
		case err := <-resultCh:
			require.True(t, errors.Is(err, context.Canceled))
		case <-time.After(5 * time.Second):
			t.Fatal("SendStream did not finish after releasing the source")
		}
		require.Equal(t, int32(0), source.active.Load())
		require.Equal(t, int32(0), source.closes.Load())
	})
}

type lifecycleBlockingReader struct {
	entered  chan struct{}
	release  <-chan struct{}
	readDone chan struct{}
	active   atomic.Int32
	closes   atomic.Int32
	once     sync.Once
	doneOnce sync.Once
}

func newLifecycleBlockingReader(release <-chan struct{}) *lifecycleBlockingReader {
	return &lifecycleBlockingReader{entered: make(chan struct{}), release: release, readDone: make(chan struct{})}
}

func (r *lifecycleBlockingReader) Read([]byte) (int, error) {
	r.active.Add(1)
	defer r.active.Add(-1)
	defer func() { r.doneOnce.Do(func() { close(r.readDone) }) }()
	r.once.Do(func() { close(r.entered) })
	<-r.release
	return 0, io.EOF
}

func (r *lifecycleBlockingReader) Close() error {
	r.closes.Add(1)
	return nil
}

func waitLifecycleSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal(message)
	}
}
