package requests

import (
	"net/http"
)

// AsHTTPClient returns a caller-owned snapshot of the underlying net/http client.
//
// The client value, a standard http.Transport, and its top-level TLS config are
// copied. A callback installed by WithTLSHandshake is rebound to the copied
// transport. Further raw http.Transport.Clone calls cannot rebind closures;
// derive through Client.Clone before exporting when managed TLS is required.
// Custom transports, the cookie jar, redirect callback, and values
// referenced by TLS configuration retain their identity. Request headers,
// cookies outside the jar, auth, middleware, retries, codecs, and response
// helpers are not part of the snapshot.
func (c *Client) AsHTTPClient() *http.Client {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return cloneHTTPClient(c.httpClient, c.tlsHandshake)
}
