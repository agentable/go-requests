package requests

import (
	"errors"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/agentable/go-orderedobject"
	"github.com/google/go-querystring/query"
)

// RequestBuilder facilitates building and executing HTTP requests.
type RequestBuilder struct {
	client               *Client
	method               string
	path                 string
	headers              *http.Header
	orderedHeaders       *orderedobject.Object[[]string]
	cookies              []*http.Cookie
	queries              url.Values
	pathParams           map[string]string
	body                 requestBodySelection
	timeout              time.Duration
	maxResponseBodyBytes int64
	middlewares          []Middleware
	retryPolicy          RetryPolicy
	hasRetryPolicy       bool
	auth                 AuthMethod
	preparationErr       error
}

func sanitizeURLDiagnosticError(err error) error {
	_, sanitized := sanitizeURLDiagnosticErrorTree(err)
	return sanitized
}

func sanitizeURLDiagnosticErrorTree(err error) (bool, error) {
	if err == nil {
		return false, nil
	}

	switch wrapped := err.(type) { //nolint:errorlint // The concrete wrappers must be cloned, not merely located.
	case *url.Error:
		if wrapped == nil {
			return false, err
		}
		clone := *wrapped
		clone.URL = sanitizeDiagnosticURL(wrapped.URL)
		_, clone.Err = sanitizeURLDiagnosticErrorTree(wrapped.Err)
		return true, &clone
	case *net.OpError:
		if wrapped == nil {
			return false, err
		}
		changed, cause := sanitizeURLDiagnosticErrorTree(wrapped.Err)
		if !changed {
			return false, err
		}
		clone := *wrapped
		clone.Err = cause
		return true, &clone
	case interface{ Unwrap() []error }:
		causes := wrapped.Unwrap()
		sanitized := make([]error, len(causes))
		for i, cause := range causes {
			changed, child := sanitizeURLDiagnosticErrorTree(cause)
			sanitized[i] = child
			if changed {
				for j := i + 1; j < len(causes); j++ {
					_, sanitized[j] = sanitizeURLDiagnosticErrorTree(causes[j])
				}
				return true, errors.Join(sanitized...)
			}
		}
		return false, err
	case interface{ Unwrap() error }:
		cause := wrapped.Unwrap()
		changed, sanitized := sanitizeURLDiagnosticErrorTree(cause)
		if !changed {
			return false, err
		}
		diagnosticErr := &sanitizedDiagnosticError{err: sanitized, original: err}
		if netErr, ok := err.(net.Error); ok { //nolint:errorlint // Preserve Timeout only when this outer wrapper implements net.Error; errors.As would promote a nested cause.
			return true, &sanitizedDiagnosticNetError{sanitizedDiagnosticError: diagnosticErr, original: netErr}
		}
		if _, ok := err.(isError); ok {
			return true, diagnosticErr
		}
		return true, sanitized
	default:
		return false, err
	}
}

type sanitizedDiagnosticError struct {
	err      error
	original error
}

type isError interface {
	Is(error) bool
}

func (e *sanitizedDiagnosticError) Error() string {
	return e.err.Error()
}

func (e *sanitizedDiagnosticError) Unwrap() error {
	return e.err
}

func (e *sanitizedDiagnosticError) Is(target error) bool {
	original, ok := e.original.(isError)
	return ok && original.Is(target)
}

type sanitizedDiagnosticNetError struct {
	*sanitizedDiagnosticError
	original net.Error
}

func (e *sanitizedDiagnosticNetError) Timeout() bool {
	return e.original.Timeout()
}

func sanitizeDiagnosticURL(rawURL string) string {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "<redacted>"
	}

	clone := parsedURL.Clone()
	clone.User = nil
	clone.RawQuery = ""
	clone.ForceQuery = false
	clone.Fragment = ""
	clone.RawFragment = ""
	return clone.String()
}

// NewRequestBuilder creates a new RequestBuilder with default settings.
func (c *Client) NewRequestBuilder(method, path string) *RequestBuilder {
	return &RequestBuilder{
		client:  c,
		method:  method,
		path:    path,
		queries: url.Values{},
		headers: &http.Header{},
	}
}

// AddMiddleware adds a middleware to the request.
func (b *RequestBuilder) AddMiddleware(middlewares ...Middleware) {
	if err := validateMiddlewares(middlewares); err != nil {
		b.setPreparationError(err)
		return
	}
	b.middlewares = append(b.middlewares, middlewares...)
}

func (b *RequestBuilder) setPreparationError(err error) {
	if err != nil && b.preparationErr == nil {
		b.preparationErr = err
	}
}

// Method sets the HTTP method for the request.
func (b *RequestBuilder) Method(method string) *RequestBuilder {
	b.method = method
	return b
}

// Path sets the URL path for the request.
func (b *RequestBuilder) Path(path string) *RequestBuilder {
	b.path = path
	return b
}

// PathParams sets multiple path params fields and their values at one go in the RequestBuilder instance.
func (b *RequestBuilder) PathParams(params map[string]string) *RequestBuilder {
	if b.pathParams == nil {
		b.pathParams = make(map[string]string, len(params))
	}
	maps.Copy(b.pathParams, params)
	return b
}

// PathParam sets a single path param field and its value in the RequestBuilder instance.
func (b *RequestBuilder) PathParam(key, value string) *RequestBuilder {
	if b.pathParams == nil {
		b.pathParams = map[string]string{}
	}
	b.pathParams[key] = value
	return b
}

// DelPathParam removes one or more path params fields from the RequestBuilder instance.
func (b *RequestBuilder) DelPathParam(key ...string) *RequestBuilder {
	if b.pathParams == nil {
		return b
	}
	for _, k := range key {
		delete(b.pathParams, k)
	}
	return b
}

// preparePath replaces path parameters in the URL path.
func (b *RequestBuilder) preparePath() string {
	if b.pathParams == nil {
		return b.path
	}

	preparedPath := b.path
	for key, value := range b.pathParams {
		placeholder := "{" + key + "}"
		preparedPath = strings.ReplaceAll(preparedPath, placeholder, url.PathEscape(value))
	}
	return preparedPath
}

func resolveRequestURL(baseURL, requestPath string, queryValues url.Values) (*url.URL, error) {
	requestURL, err := url.Parse(requestPath)
	if err != nil {
		return nil, err
	}
	if requestURL.IsAbs() || baseURL == "" {
		addQueryValues(requestURL, queryValues)
		return requestURL, nil
	}

	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	resolved := base.Clone()
	resolved.RawQuery = requestURL.RawQuery
	resolved.ForceQuery = requestURL.ForceQuery
	resolved.Fragment = requestURL.Fragment

	path, rawPath, err := joinEscapedURLPath(base.EscapedPath(), requestURL.EscapedPath())
	if err != nil {
		return nil, err
	}
	resolved.Path = path
	resolved.RawPath = rawPath
	addQueryValues(resolved, queryValues)
	return resolved, nil
}

func joinEscapedURLPath(basePath, requestPath string) (string, string, error) {
	escaped := basePath
	if requestPath != "" {
		switch escaped {
		case "", "/":
			escaped = "/" + strings.TrimLeft(requestPath, "/")
		default:
			escaped = strings.TrimRight(escaped, "/") + "/" + strings.TrimLeft(requestPath, "/")
		}
	}
	path, err := url.PathUnescape(escaped)
	if err != nil {
		return "", "", err
	}
	if path == escaped {
		return path, "", nil
	}
	return path, escaped, nil
}

func addQueryValues(requestURL *url.URL, queryValues url.Values) {
	if len(queryValues) == 0 {
		return
	}
	query := requestURL.Query()
	for key, values := range queryValues {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	requestURL.RawQuery = query.Encode()
}

// Queries adds query parameters to the request.
func (b *RequestBuilder) Queries(params url.Values) *RequestBuilder {
	for key, values := range params {
		for _, value := range values {
			b.queries.Add(key, value)
		}
	}
	return b
}

// Query adds a single query parameter to the request.
func (b *RequestBuilder) Query(key, value string) *RequestBuilder {
	b.queries.Add(key, value)
	return b
}

// DelQuery removes one or more query parameters from the request.
func (b *RequestBuilder) DelQuery(key ...string) *RequestBuilder {
	for _, k := range key {
		b.queries.Del(k)
	}
	return b
}

// QueriesStruct adds query parameters to the request based on a struct tagged with url tags.
func (b *RequestBuilder) QueriesStruct(queryStruct any) *RequestBuilder {
	values, err := query.Values(queryStruct)
	if err != nil {
		b.setPreparationError(err)
		if b.client.logger != nil {
			b.client.logger.Errorf("Error encoding query struct: %v", err)
		}
		return b
	}
	return b.Queries(values)
}

// Headers set headers to the request.
func (b *RequestBuilder) Headers(headers http.Header) *RequestBuilder {
	for key, values := range headers {
		b.headers.Del(key)
		for _, value := range values {
			b.headers.Add(key, value)
		}
		if b.orderedHeaders != nil {
			setOrderedHeaderValues(&b.orderedHeaders, key, values)
		}
		if strings.EqualFold(key, "Content-Type") {
			b.body.generatedContentType = false
		}
	}
	return b
}

// OrderedHeaders sets ordered headers for the request.
func (b *RequestBuilder) OrderedHeaders(headers *orderedobject.Object[[]string]) *RequestBuilder {
	b.body.generatedContentType = false
	b.orderedHeaders = cloneOrderedHeaders(headers)
	if b.orderedHeaders == nil {
		b.headers = &http.Header{}
		return b
	}
	b.headers = new(headerFromOrderedHeaders(b.orderedHeaders))
	return b
}

// Header sets (or replaces) a header in the request.
func (b *RequestBuilder) Header(key, value string) *RequestBuilder {
	b.setHeader(key, value)
	if strings.EqualFold(key, "Content-Type") {
		b.body.generatedContentType = false
	}
	return b
}

func (b *RequestBuilder) setHeader(key, value string) {
	b.headers.Set(key, value)
	if b.orderedHeaders != nil {
		setOrderedHeaderValues(&b.orderedHeaders, key, []string{value})
	}
}

// AddHeader adds a header to the request.
func (b *RequestBuilder) AddHeader(key, value string) *RequestBuilder {
	b.headers.Add(key, value)
	if b.orderedHeaders != nil {
		addOrderedHeaderValue(&b.orderedHeaders, key, value)
	}
	if strings.EqualFold(key, "Content-Type") {
		b.body.generatedContentType = false
	}
	return b
}

// DelHeader removes one or more headers from the request.
func (b *RequestBuilder) DelHeader(key ...string) *RequestBuilder {
	b.delHeader(key...)
	for _, k := range key {
		if strings.EqualFold(k, "Content-Type") {
			b.body.generatedContentType = false
		}
	}
	return b
}

func (b *RequestBuilder) delHeader(key ...string) {
	for _, k := range key {
		b.headers.Del(k)
		if b.orderedHeaders != nil {
			deleteOrderedHeader(b.orderedHeaders, k)
		}
	}
}

// Cookies adds cookies from a map.
func (b *RequestBuilder) Cookies(cookies map[string]string) *RequestBuilder {
	for key, value := range cookies {
		b.Cookie(key, value)
	}
	return b
}

// Cookie adds a cookie to the request.
func (b *RequestBuilder) Cookie(key, value string) *RequestBuilder {
	b.cookies = append(b.cookies, &http.Cookie{Name: key, Value: value}) //nolint:gosec // callers control request cookie attributes
	return b
}

// DelCookie removes one or more cookies from the request.
func (b *RequestBuilder) DelCookie(key ...string) *RequestBuilder {
	if b.cookies == nil || len(key) == 0 {
		return b
	}

	deleteKeys := stringSet(key)
	b.cookies = slices.DeleteFunc(b.cookies, func(cookie *http.Cookie) bool {
		_, ok := deleteKeys[cookie.Name]
		return ok
	})

	return b
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

// ContentType sets the Content-Type header for the request.
func (b *RequestBuilder) ContentType(contentType string) *RequestBuilder {
	return b.Header("Content-Type", contentType)
}

// Accept sets the Accept header for the request.
func (b *RequestBuilder) Accept(accept string) *RequestBuilder {
	return b.Header("Accept", accept)
}

// UserAgent sets the User-Agent header for the request.
func (b *RequestBuilder) UserAgent(userAgent string) *RequestBuilder {
	return b.Header("User-Agent", userAgent)
}

// Referer sets the Referer header for the request.
func (b *RequestBuilder) Referer(referer string) *RequestBuilder {
	return b.Header("Referer", referer)
}

// Auth applies an authentication method to the request.
func (b *RequestBuilder) Auth(auth AuthMethod) *RequestBuilder {
	if err := validateAuthOption(auth); err != nil {
		b.setPreparationError(err)
		return b
	}
	b.auth = auth
	return b
}

func (b *RequestBuilder) applyAuthAndHeaders(req *http.Request, snap *clientSnapshot) {
	addHeaderValues(req.Header, snap.headers, snap.orderedHeaders)
	for _, cookie := range snap.cookies {
		req.AddCookie(cookie)
	}
	clientCookies := req.Cookies()
	if snap.auth != nil {
		snap.auth.Apply(req)
	}
	var requestCookies []*http.Cookie
	if b.headers != nil {
		overlayHeaderValues(req.Header, *b.headers, b.orderedHeaders)
		requestCookies = headerCookies(*b.headers)
	}
	requestCookies = append(requestCookies, b.cookies...)
	applyCookiePrecedence(req, clientCookies, requestCookies)
	if b.auth != nil {
		b.auth.Apply(req)
	}
}

func (b *RequestBuilder) effectiveOrderedHeaders(snap *clientSnapshot) *orderedobject.Object[[]string] {
	headers := mergeOrderedHeaders(snap.orderedHeaders, b.orderedHeaders)
	if headers == nil || b.headers == nil {
		return headers
	}
	for key := range *b.headers {
		if _, ok := orderedHeaderKey(b.orderedHeaders, key); ok {
			continue
		}
		deleteOrderedHeader(headers, key)
	}
	if headers.Len() == 0 {
		return nil
	}
	return headers
}
