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

func cloneHTTPClient(client *http.Client, tlsConfig *tls.Config) *http.Client {
	clone := *client
	if transport, ok := client.Transport.(*http.Transport); ok {
		clonedTransport := transport.Clone()
		if tlsConfig != nil {
			clonedTransport.TLSClientConfig = tlsConfig
		}
		clone.Transport = clonedTransport
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

	c.httpClient = httpClient
}

// setDefaultTransport replaces the underlying transport.
func (c *Client) setDefaultTransport(transport http.RoundTripper) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.httpClient.Transport = transport
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
	if c.tlsConfig != nil {
		transport.TLSClientConfig = c.tlsConfig
	}
	return configureHTTP2Transport(transport)
}

func configureHTTP2Transport(transport *http.Transport) error {
	if isHTTP2Configured(transport) {
		ensureHTTP2NextProtos(transport)
		transport.ForceAttemptHTTP2 = true
		return nil
	}

	transport.ForceAttemptHTTP2 = true
	return http2.ConfigureTransport(transport)
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
