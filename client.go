package requests

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"slices"
	"sync"
	"time"

	"github.com/agentable/go-orderedobject"
	"golang.org/x/net/publicsuffix"
)

// Client represents an HTTP client.
type Client struct {
	mu             sync.RWMutex
	baseURL        string
	headers        *http.Header
	orderedHeaders *orderedobject.Object[[]string]
	cookies        []*http.Cookie
	middlewares    []Middleware
	tlsConfig      *tls.Config
	retry          RetryPolicy
	httpClient     *http.Client
	jsonEncoder    Encoder
	jsonDecoder    Decoder
	xmlEncoder     Encoder
	xmlDecoder     Decoder
	yamlEncoder    Encoder
	yamlDecoder    Decoder
	logger         Logger
	dialTimeout    time.Duration
	resolver       *net.Resolver
	localAddr      net.Addr
	dialContext    func(context.Context, string, string) (net.Conn, error)
	auth           AuthMethod
}

type clientSnapshot struct {
	baseURL        string
	headers        http.Header
	orderedHeaders *orderedobject.Object[[]string]
	cookies        []*http.Cookie
	middlewares    []Middleware
	retry          RetryPolicy
	httpClient     *http.Client
	jsonEncoder    Encoder
	jsonDecoder    Decoder
	xmlEncoder     Encoder
	xmlDecoder     Decoder
	yamlEncoder    Encoder
	yamlDecoder    Decoder
	logger         Logger
	auth           AuthMethod
}

// New creates a Client with functional options applied.
// It returns an error when any option cannot be applied.
func New(opts ...Option) (*Client, error) {
	c := newClient()
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Clone returns a new Client with the current defaults plus opts applied.
func (c *Client) Clone(opts ...Option) (*Client, error) {
	clone := c.clone()
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(clone); err != nil {
			return nil, err
		}
	}
	return clone, nil
}

func newClient() *Client {
	return &Client{
		httpClient:  &http.Client{},
		jsonEncoder: &JSONEncoder{},
		jsonDecoder: &JSONDecoder{},
		xmlEncoder:  &XMLEncoder{},
		xmlDecoder:  &XMLDecoder{},
		yamlEncoder: &YAMLEncoder{},
		yamlDecoder: &YAMLDecoder{},
		retry:       DefaultRetryPolicy(),
	}
}

func (c *Client) clone() *Client {
	c.mu.RLock()
	defer c.mu.RUnlock()

	clone := &Client{
		baseURL:        c.baseURL,
		headers:        cloneHeaderPtr(c.headers),
		orderedHeaders: cloneOrderedHeaders(c.orderedHeaders),
		cookies:        cloneCookies(c.cookies),
		middlewares:    slices.Clone(c.middlewares),
		tlsConfig:      cloneTLSConfig(c.tlsConfig),
		retry:          c.retry,
		httpClient:     nil,
		jsonEncoder:    c.jsonEncoder,
		jsonDecoder:    c.jsonDecoder,
		xmlEncoder:     c.xmlEncoder,
		xmlDecoder:     c.xmlDecoder,
		yamlEncoder:    c.yamlEncoder,
		yamlDecoder:    c.yamlDecoder,
		logger:         c.logger,
		dialTimeout:    c.dialTimeout,
		resolver:       c.resolver,
		localAddr:      c.localAddr,
		dialContext:    c.dialContext,
		auth:           c.auth,
	}
	clone.httpClient = cloneHTTPClient(c.httpClient, clone.tlsConfig)
	return clone
}

func cloneHeaderPtr(headers *http.Header) *http.Header {
	if headers == nil {
		return nil
	}
	clone := headers.Clone()
	return &clone
}

func cloneCookies(cookies []*http.Cookie) []*http.Cookie {
	if len(cookies) == 0 {
		return nil
	}
	clones := make([]*http.Cookie, len(cookies))
	for i, cookie := range cookies {
		clone := new(*cookie) //nolint:gosec // clone preserves caller-provided cookie attributes
		clone.Unparsed = slices.Clone(cookie.Unparsed)
		clones[i] = clone
	}
	return clones
}

// setBaseURL sets the base URL.
func (c *Client) setBaseURL(baseURL string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.baseURL = baseURL
}

// addMiddleware appends client-level middleware.
func (c *Client) addMiddleware(middlewares ...Middleware) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.middlewares = append(c.middlewares, middlewares...)
}

// setDefaultHeaders replaces the default semantic headers.
func (c *Client) setDefaultHeaders(headers http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()

	clone := headers.Clone()
	if clone == nil {
		clone = http.Header{}
	}
	c.headers = &clone
	c.orderedHeaders = nil
}

// setDefaultOrderedHeaders replaces ordered default headers.
func (c *Client) setDefaultOrderedHeaders(headers *orderedobject.Object[[]string]) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.orderedHeaders = cloneOrderedHeaders(headers)
	if c.orderedHeaders == nil {
		c.headers = nil
		return
	}
	c.headers = new(headerFromOrderedHeaders(c.orderedHeaders))
}

// setDefaultHeader adds or updates a default header.
func (c *Client) setDefaultHeader(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.headers == nil {
		c.headers = &http.Header{}
	}
	c.headers.Set(key, value)
	if c.orderedHeaders != nil {
		setOrderedHeaderValues(&c.orderedHeaders, key, []string{value})
	}
}

// setDefaultContentType sets the default content type.
func (c *Client) setDefaultContentType(contentType string) {
	c.setDefaultHeader("Content-Type", contentType)
}

// setDefaultAccept sets the default Accept header.
func (c *Client) setDefaultAccept(accept string) {
	c.setDefaultHeader("Accept", accept)
}

// setDefaultUserAgent sets the default User-Agent header.
func (c *Client) setDefaultUserAgent(userAgent string) {
	c.setDefaultHeader("User-Agent", userAgent)
}

// setDefaultReferer sets the default Referer header.
func (c *Client) setDefaultReferer(referer string) {
	c.setDefaultHeader("Referer", referer)
}

// setDefaultTimeout sets the underlying http.Client timeout.
func (c *Client) setDefaultTimeout(timeout time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.httpClient.Timeout = timeout
}

// setDefaultCookieJar replaces the underlying cookie jar.
func (c *Client) setDefaultCookieJar(jar http.CookieJar) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.httpClient.Jar = jar
}

// enableSession enables cookie and TLS session reuse without replacing existing session stores.
func (c *Client) enableSession() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	jar := c.httpClient.Jar
	if jar == nil {
		var err error
		jar, err = cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
		if err != nil {
			return fmt.Errorf("create session cookie jar: %w", err)
		}
	}

	if err := c.updateTLSConfigLocked(func(config *tls.Config) error {
		if config.ClientSessionCache == nil {
			config.ClientSessionCache = tls.NewLRUClientSessionCache(0)
		}
		return nil
	}); err != nil {
		return err
	}
	c.httpClient.Jar = jar
	return nil
}

// setDefaultCookies adds default cookies.
func (c *Client) setDefaultCookies(cookies map[string]string) {
	for name, value := range cookies {
		c.setDefaultCookie(name, value)
	}
}

// setDefaultCookie adds a default cookie.
func (c *Client) setDefaultCookie(name, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cookies = append(c.cookies, &http.Cookie{Name: name, Value: value}) //nolint:gosec // callers control default cookie attributes
}

func (c *Client) snapshot() clientSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()

	headers := http.Header{}
	if c.headers != nil {
		headers = c.headers.Clone()
	}

	cookies := make([]*http.Cookie, len(c.cookies))
	for i, cookie := range c.cookies {
		clone := new(*cookie) //nolint:gosec // snapshot preserves caller-provided cookie attributes
		clone.Unparsed = slices.Clone(cookie.Unparsed)
		cookies[i] = clone
	}

	middlewares := slices.Clone(c.middlewares)

	return clientSnapshot{
		baseURL:        c.baseURL,
		headers:        headers,
		orderedHeaders: cloneOrderedHeaders(c.orderedHeaders),
		cookies:        cookies,
		middlewares:    middlewares,
		retry:          c.retry,
		httpClient:     c.httpClient,
		jsonEncoder:    c.jsonEncoder,
		jsonDecoder:    c.jsonDecoder,
		xmlEncoder:     c.xmlEncoder,
		xmlDecoder:     c.xmlDecoder,
		yamlEncoder:    c.yamlEncoder,
		yamlDecoder:    c.yamlDecoder,
		logger:         c.logger,
		auth:           c.auth,
	}
}

// UnsafeHTTPClient returns the underlying HTTP client for advanced integration.
//
// The returned pointer is shared mutable client state. Callers that mutate it
// must coordinate their own synchronization and should prefer construction
// options when possible.
func (c *Client) UnsafeHTTPClient() *http.Client {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.httpClient
}

// GetBaseURL returns the configured base URL in a thread-safe way.
func (c *Client) GetBaseURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseURL
}

func (c *Client) setRetry(policy RetryPolicy) *Client {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.retry = policy
	return c
}

// setAuth configures a client-level authentication method.
func (c *Client) setAuth(auth AuthMethod) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.auth = auth
}

// setRedirectPolicy replaces the redirect policy chain.
func (c *Client) setRedirectPolicy(policies ...RedirectPolicy) *Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		for _, p := range policies {
			if err := p.Apply(req, via); err != nil {
				return err
			}
		}
		stripSensitiveHeadersOnRedirect(req, via[0])
		return nil
	}
	return c
}

// setLogger sets the client logger.
func (c *Client) setLogger(logger Logger) *Client {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.logger = logger
	return c
}

// Get initiates a GET request.
func (c *Client) Get(path string) *RequestBuilder {
	return c.NewRequestBuilder(http.MethodGet, path)
}

// Post initiates a POST request.
func (c *Client) Post(path string) *RequestBuilder {
	return c.NewRequestBuilder(http.MethodPost, path)
}

// Delete initiates a DELETE request.
func (c *Client) Delete(path string) *RequestBuilder {
	return c.NewRequestBuilder(http.MethodDelete, path)
}

// Put initiates a PUT request.
func (c *Client) Put(path string) *RequestBuilder {
	return c.NewRequestBuilder(http.MethodPut, path)
}

// Patch initiates a PATCH request.
func (c *Client) Patch(path string) *RequestBuilder {
	return c.NewRequestBuilder(http.MethodPatch, path)
}

// Options initiates an OPTIONS request.
func (c *Client) Options(path string) *RequestBuilder {
	return c.NewRequestBuilder(http.MethodOptions, path)
}

// Head initiates a HEAD request.
func (c *Client) Head(path string) *RequestBuilder {
	return c.NewRequestBuilder(http.MethodHead, path)
}

// Connect initiates a CONNECT request.
func (c *Client) Connect(path string) *RequestBuilder {
	return c.NewRequestBuilder(http.MethodConnect, path)
}

// Trace initiates a TRACE request.
func (c *Client) Trace(path string) *RequestBuilder {
	return c.NewRequestBuilder(http.MethodTrace, path)
}

// Request initiates a request with method and path.
func (c *Client) Request(method, path string) *RequestBuilder {
	return c.NewRequestBuilder(method, path)
}
