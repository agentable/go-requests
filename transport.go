package requests

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"slices"
	"time"

	"golang.org/x/net/http2"
)

func cloneHTTPClient(client *http.Client, handshake tlsHandshakeFunc) *http.Client {
	clone := *client
	if transport, ok := client.Transport.(*http.Transport); ok {
		copied := transport.Clone()
		if handshake != nil {
			bindTLSHandshake(copied, handshake)
		}
		clone.Transport = copied
	}
	return &clone
}

// ensureTransport returns the client's transport as *http.Transport, creating one if needed.
// Must be called with c.mu held.
func (c *Client) ensureTransport() (*http.Transport, error) {
	if c.httpClient.Transport == nil {
		baseline, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, fmt.Errorf("%w: expected default *http.Transport, got %T", ErrInvalidTransportType, http.DefaultTransport)
		}
		c.httpClient.Transport = baseline.Clone()
	}
	transport, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("%w: expected *http.Transport, got %T", ErrInvalidTransportType, c.httpClient.Transport)
	}
	return transport, nil
}

// setHTTPClient replaces the underlying HTTP client.
func (c *Client) setHTTPClient(httpClient *http.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.httpClient = cloneHTTPClient(httpClient, nil)
	c.tlsHandshake = nil
}

// setDefaultTransport replaces the underlying transport.
func (c *Client) setDefaultTransport(transport http.RoundTripper) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if standard, ok := transport.(*http.Transport); ok {
		transport = standard.Clone()
	}
	c.httpClient.Transport = transport
	c.tlsHandshake = nil
}

func (c *Client) configureHTTP2() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.enableHTTP2Locked()
}

func (c *Client) enableHTTP2Locked() error {
	transport, err := c.ensureTransport()
	if err != nil {
		return err
	}
	return configureHTTP2Transport(transport)
}

func configureHTTP2Transport(transport *http.Transport) error {
	if isHTTP2Configured(transport) {
		ensureHTTP2NextProtos(transport)
		transport.ForceAttemptHTTP2 = true
		return nil
	}

	if transport.Protocols == nil {
		transport.Protocols = new(http.Protocols)
		transport.Protocols.SetHTTP1(true)
	}
	transport.Protocols.SetHTTP2(true)
	ensureHTTP2NextProtos(transport)
	transport.ForceAttemptHTTP2 = true
	return nil
}

func isHTTP2Configured(transport *http.Transport) bool {
	if transport.Protocols != nil && transport.Protocols.HTTP2() {
		return true
	}
	if transport.TLSNextProto == nil {
		return false
	}
	_, ok := transport.TLSNextProto[http2.NextProtoTLS]
	return ok
}

func ensureHTTP2NextProtos(transport *http.Transport) {
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	if !slices.Contains(transport.TLSClientConfig.NextProtos, http2.NextProtoTLS) {
		transport.TLSClientConfig.NextProtos = slices.Concat(
			[]string{http2.NextProtoTLS},
			transport.TLSClientConfig.NextProtos,
		)
	}
	if !slices.Contains(transport.TLSClientConfig.NextProtos, "http/1.1") {
		transport.TLSClientConfig.NextProtos = append(transport.TLSClientConfig.NextProtos, "http/1.1")
	}
}

func (c *Client) applyTransport(fn func(*http.Transport)) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	transport, err := c.ensureTransport()
	if err != nil {
		return err
	}
	fn(transport)
	return nil
}

func (c *Client) applyDialContextLocked(transport *http.Transport) {
	if c.dialContext != nil {
		transport.DialContext = c.dialContext
		return
	}
	if c.dialTimeout == 0 && c.resolver == nil && c.localAddr == nil {
		transport.DialContext = nil
		return
	}
	dialer := &net.Dialer{
		Timeout:   c.dialTimeout,
		Resolver:  c.resolver,
		LocalAddr: c.localAddr,
	}
	transport.DialContext = dialer.DialContext
}

func (c *Client) applyDialTimeout(d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.dialTimeout = d
	transport, err := c.ensureTransport()
	if err != nil {
		return err
	}
	c.applyDialContextLocked(transport)
	return nil
}

func (c *Client) applyResolver(resolver *net.Resolver) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.resolver = resolver
	transport, err := c.ensureTransport()
	if err != nil {
		return err
	}
	c.applyDialContextLocked(transport)
	return nil
}

func (c *Client) applyDialContext(dial func(context.Context, string, string) (net.Conn, error)) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.dialContext = dial
	transport, err := c.ensureTransport()
	if err != nil {
		return err
	}
	c.applyDialContextLocked(transport)
	return nil
}

func (c *Client) applyLocalAddr(addr net.Addr) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.localAddr = addr
	transport, err := c.ensureTransport()
	if err != nil {
		return err
	}
	c.applyDialContextLocked(transport)
	return nil
}

type tlsHandshakeFunc func(context.Context, net.Conn, *tls.Config) (net.Conn, error)

func (c *Client) setTLSHandshake(handshake tlsHandshakeFunc) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	transport, err := c.ensureTransport()
	if err != nil {
		return err
	}
	c.tlsHandshake = handshake
	bindTLSHandshake(transport, handshake)
	return nil
}

func bindTLSHandshake(transport *http.Transport, handshake tlsHandshakeFunc) {
	transport.DialTLS = nil //nolint:staticcheck // Clearing both hooks prevents fallback to an old TLS dialer.
	transport.DialTLSContext = nil
	if handshake == nil {
		return
	}
	transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dial := transport.DialContext
		legacyDial := transport.Dial //nolint:staticcheck // Preserve net/http dial precedence for an injected transport.
		if dial == nil && legacyDial != nil {
			dial = func(_ context.Context, network, addr string) (net.Conn, error) { return legacyDial(network, addr) }
		}
		if dial == nil {
			dial = (&net.Dialer{}).DialContext
		}
		raw, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if isNilInterface(raw) {
			return nil, invalidOptionValue("TLSHandshake dial connection")
		}
		config := cloneTLSConfig(transport.TLSClientConfig)
		if config == nil {
			config = &tls.Config{}
		}
		if config.ServerName == "" {
			config.ServerName, _, _ = net.SplitHostPort(addr)
		}
		if timeout := transport.TLSHandshakeTimeout; timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		conn, err := handshake(ctx, raw, config)
		if err == nil {
			err = ctx.Err()
		}
		if err == nil && isNilInterface(conn) {
			err = invalidOptionValue("TLSHandshake connection")
		}
		if err != nil {
			_ = raw.Close()
			return nil, err
		}
		return conn, nil
	}
}
