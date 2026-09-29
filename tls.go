package requests

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"slices"
)

func cloneTLSConfig(config *tls.Config) *tls.Config {
	if config == nil {
		return nil
	}
	return config.Clone()
}

func cloneCertificates(certificates []tls.Certificate) []tls.Certificate {
	clones := slices.Clone(certificates)
	for i := range clones {
		clones[i].Certificate = cloneByteSlices(clones[i].Certificate)
		clones[i].SupportedSignatureAlgorithms = slices.Clone(clones[i].SupportedSignatureAlgorithms)
		clones[i].OCSPStaple = slices.Clone(clones[i].OCSPStaple)
		clones[i].SignedCertificateTimestamps = cloneByteSlices(clones[i].SignedCertificateTimestamps)
	}
	return clones
}

func cloneByteSlices(values [][]byte) [][]byte {
	if values == nil {
		return nil
	}
	cloned := make([][]byte, len(values))
	for i, value := range values {
		cloned[i] = slices.Clone(value)
	}
	return cloned
}

func setTransportTLSConfig(transport *http.Transport, config *tls.Config) {
	transport.TLSClientConfig = config
	if isHTTP2Configured(transport) {
		ensureHTTP2NextProtos(transport)
		transport.ForceAttemptHTTP2 = true
	}
}

// setTLSConfig replaces the TLS configuration.
func (c *Client) setTLSConfig(config *tls.Config) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	transport, err := c.ensureTransport()
	if err != nil {
		return err
	}
	setTransportTLSConfig(transport, cloneTLSConfig(config))
	return nil
}

func (c *Client) updateTLSConfigLocked(update func(*tls.Config) error) error {
	transport, err := c.ensureTransport()
	if err != nil {
		return err
	}
	config := cloneTLSConfig(transport.TLSClientConfig)
	if config == nil {
		config = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if err := update(config); err != nil {
		return err
	}
	setTransportTLSConfig(transport, config)
	return nil
}

// InsecureSkipVerify sets the TLS configuration to skip certificate verification.
func (c *Client) insecureSkipVerify() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.updateTLSConfigLocked(func(config *tls.Config) error {
		config.InsecureSkipVerify = true
		return nil
	})
}

// setCertificates replaces the TLS client certificates.
func (c *Client) setCertificates(certs ...tls.Certificate) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.updateTLSConfigLocked(func(config *tls.Config) error {
		config.Certificates = cloneCertificates(certs)
		return nil
	})
}

// setTLSServerName sets the TLS server name (SNI).
func (c *Client) setTLSServerName(serverName string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.updateTLSConfigLocked(func(config *tls.Config) error {
		config.ServerName = serverName
		return nil
	})
}

// setRootCertificateFromString loads root certificates from PEM text.
func (c *Client) setRootCertificateFromString(pemCerts string) error {
	return c.addRootCAs([]byte(pemCerts))
}

func (c *Client) addRootCAs(pemCerts []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.updateTLSConfigLocked(func(config *tls.Config) error {
		roots := config.RootCAs
		if roots == nil {
			roots = x509.NewCertPool()
		} else {
			roots = roots.Clone()
		}
		if !roots.AppendCertsFromPEM(pemCerts) {
			return invalidOptionValue("RootCertificate")
		}
		config.RootCAs = roots
		return nil
	})
}

// GetTLSConfig returns a shallow clone of the active standard transport's TLS
// settings, or nil when no standard transport TLS settings are available.
func (c *Client) GetTLSConfig() *tls.Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if transport, ok := c.httpClient.Transport.(*http.Transport); ok {
		return cloneTLSConfig(transport.TLSClientConfig)
	}
	return nil
}
