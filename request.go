package requests

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/agentable/go-orderedobject"
	"github.com/google/go-querystring/query"
)

// RequestBuilder facilitates building and executing HTTP requests.
type RequestBuilder struct {
	client               *Client
	method               string
	target               requestTarget
	metadata             requestMetadata
	orderedHeaders       *orderedobject.Object[[]string]
	body                 requestBodyPlan
	timeout              time.Duration
	maxResponseBodyBytes int64
	middlewares          []Middleware
	retryPolicy          RetryPolicy
	hasRetryPolicy       bool
	auth                 AuthMethod
	preparationErr       error
	preparationErrClass  preparationErrorClass
}

type preparationErrorClass uint8

const (
	preparationErrorClassUnknown preparationErrorClass = iota
	preparationErrorClassInvalidConfigValue
	preparationErrorClassUnsupportedFormFieldsType
)

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
		client: c,
		method: method,
		target: newRequestTarget(path),
	}
}

// AddMiddleware adds a middleware to the request.
func (b *RequestBuilder) AddMiddleware(middlewares ...Middleware) {
	if err := validateMiddlewares(middlewares); err != nil {
		b.setPreparationError(err, preparationErrorClassInvalidConfigValue)
		return
	}
	b.middlewares = append(b.middlewares, middlewares...)
}

func (b *RequestBuilder) setPreparationError(err error, class preparationErrorClass) {
	if err != nil && b.preparationErr == nil {
		b.preparationErr = err
		b.preparationErrClass = class
	}
}

// Method sets the HTTP method for the request.
func (b *RequestBuilder) Method(method string) *RequestBuilder {
	b.method = method
	return b
}

// Path sets the URL path for the request.
func (b *RequestBuilder) Path(path string) *RequestBuilder {
	b.target.setPath(privateRequestValue(path))
	return b
}

// PathValue replaces the complete request path with a disclosure-tagged value.
// The path is still resolved and escaped by the same delivery resolver.
func (b *RequestBuilder) PathValue(path Value) *RequestBuilder {
	b.target.setPath(path)
	return b
}

// PathParams sets multiple path params fields and their values at one go in the RequestBuilder instance.
func (b *RequestBuilder) PathParams(params map[string]string) *RequestBuilder {
	b.target.setPathParams(params)
	return b
}

// PathParam sets a single path param field and its value in the RequestBuilder instance.
func (b *RequestBuilder) PathParam(key, value string) *RequestBuilder {
	b.target.setPathParam(key, value)
	return b
}

// PathParamValue sets a path parameter with an explicit disclosure capability.
func (b *RequestBuilder) PathParamValue(key string, value Value) *RequestBuilder {
	b.target.setPathParamValue(key, value)
	return b
}

// DelPathParam removes one or more path params fields from the RequestBuilder instance.
func (b *RequestBuilder) DelPathParam(key ...string) *RequestBuilder {
	b.target.deletePathParams(key...)
	return b
}

func resolveRequestURL(baseURL, requestPath string, queryValues url.Values) (*url.URL, error) {
	requestURL, err := url.Parse(requestPath)
	if err != nil {
		return nil, err
	}
	if requestURL.IsAbs() || baseURL == "" {
		if len(queryValues) != 0 {
			if _, err := url.ParseQuery(requestURL.RawQuery); err != nil {
				return nil, err
			}
		}
		addQueryValues(requestURL, queryValues)
		return requestURL, nil
	}

	requestQuery, err := url.ParseQuery(requestURL.RawQuery)
	if err != nil {
		return nil, err
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	resolved := base.Clone()
	resolved.ForceQuery = base.ForceQuery || requestURL.ForceQuery
	resolved.Fragment = requestURL.Fragment
	addQueryValues(resolved, requestQuery)

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
			b.Query(key, value)
		}
	}
	return b
}

// Query adds a single query parameter to the request.
func (b *RequestBuilder) Query(key, value string) *RequestBuilder {
	b.metadata.addQuery(key, privateRequestValue(value))
	return b
}

// DelQuery removes one or more query parameters from the request.
func (b *RequestBuilder) DelQuery(key ...string) *RequestBuilder {
	b.metadata.deleteQueries(key...)
	return b
}

// QueryValue appends a disclosure-tagged query occurrence.
func (b *RequestBuilder) QueryValue(key string, value Value) *RequestBuilder {
	b.metadata.addQuery(key, value)
	return b
}

// QueriesStruct adds query parameters to the request based on a struct tagged with url tags.
func (b *RequestBuilder) QueriesStruct(queryStruct any) *RequestBuilder {
	values, err := query.Values(queryStruct)
	if err != nil {
		b.setPreparationError(err, preparationErrorClassUnknown)
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
		b.metadata.deleteHeaders(key)
		for _, value := range values {
			b.metadata.addHeader(key, privateRequestValue(value))
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
		b.metadata.headers = nil
		return b
	}
	b.metadata.headers = nil
	for _, entry := range b.orderedHeaders.Entries() {
		if isPseudoHeader(entry.Key) {
			continue
		}
		for _, value := range entry.Value {
			b.metadata.addHeader(entry.Key, privateRequestValue(value))
		}
	}
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
	b.setHeaderValue(key, privateRequestValue(value))
}

func (b *RequestBuilder) setHeaderValue(key string, value Value) {
	b.metadata.setHeader(key, value)
	if b.orderedHeaders != nil {
		setOrderedHeaderValues(&b.orderedHeaders, key, []string{value.rawValue()})
	}
}

// HeaderValue replaces a request-local header with a disclosure-tagged value.
func (b *RequestBuilder) HeaderValue(key string, value Value) *RequestBuilder {
	b.setHeaderValue(key, value)
	if strings.EqualFold(key, "Content-Type") {
		b.body.generatedContentType = false
	}
	return b
}

// AddHeader adds a header to the request.
func (b *RequestBuilder) AddHeader(key, value string) *RequestBuilder {
	b.addHeaderValue(key, privateRequestValue(value))
	if strings.EqualFold(key, "Content-Type") {
		b.body.generatedContentType = false
	}
	return b
}

func (b *RequestBuilder) addHeaderValue(key string, value Value) {
	b.metadata.addHeader(key, value)
	if b.orderedHeaders != nil {
		addOrderedHeaderValue(&b.orderedHeaders, key, value.rawValue())
	}
}

// AddHeaderValue appends a disclosure-tagged value within a header name.
func (b *RequestBuilder) AddHeaderValue(key string, value Value) *RequestBuilder {
	b.addHeaderValue(key, value)
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
	b.metadata.deleteHeaders(key...)
	for _, k := range key {
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
	b.cookieValue(key, privateRequestValue(value))
	return b
}

// CookieValue appends a disclosure-tagged request cookie occurrence.
func (b *RequestBuilder) CookieValue(key string, value Value) *RequestBuilder {
	b.cookieValue(key, value)
	return b
}

func (b *RequestBuilder) cookieValue(key string, value Value) {
	b.metadata.addCookie(key, value)
}

// DelCookie removes one or more cookies from the request.
func (b *RequestBuilder) DelCookie(key ...string) *RequestBuilder {
	if len(key) == 0 {
		return b
	}

	b.metadata.deleteCookies(key...)

	return b
}

// compatibilityHeaders exposes the detached semantic header view used by the
// legacy body preparation helper. Disclosure state remains in metadata.
func (b *RequestBuilder) compatibilityHeaders() *http.Header {
	values := b.metadata.headerValues()
	return &values
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
		b.setPreparationError(err, preparationErrorClassInvalidConfigValue)
		return b
	}
	b.auth = auth
	return b
}

func applyPlanAuthAndHeaders(
	req *http.Request,
	plan *requestPlan,
	generatedContentType string,
) *http.Request {
	orderedHeaders := cloneOrderedHeaders(plan.orderedHeaders)
	clientHeaders := requestOccurrencesHeaderValues(plan.clientMetadata.headers)
	addHeaderValues(req.Header, clientHeaders, plan.clientOrderedHeaders)
	if plan.clientAuth != nil {
		plan.clientAuth.Apply(req)
	}

	requestHeaders := requestOccurrencesHeaderValues(plan.requestMetadata.headers)
	overlayHeaderValues(req.Header, requestHeaders, plan.requestOrderedHeaders)
	if plan.body.generatedContentType && generatedContentType != "" {
		deleteHeaderValues(req.Header, "Content-Type")
		req.Header.Set("Content-Type", generatedContentType)
		if plan.requestOrderedHeaders != nil {
			setOrderedHeaderValues(&orderedHeaders, "Content-Type", []string{generatedContentType})
		} else {
			// A generated request-local value overrides a client ordered
			// default without creating request-local ordered intent. Keep the
			// detached metadata aligned with the existing precedence contract.
			deleteOrderedHeader(orderedHeaders, "Content-Type")
		}
	}
	if orderedHeaders != nil && orderedHeaders.Len() == 0 {
		orderedHeaders = nil
	}

	deleteHeaderValues(req.Header, "Cookie")
	for _, occurrence := range plan.cookies {
		if cookie := cloneCookie(occurrence.cookie); cookie != nil {
			req.AddCookie(cookie)
		}
	}
	if plan.requestAuth != nil {
		plan.requestAuth.Apply(req)
	}

	syncOrderedHeaderValues(orderedHeaders, req.Header)
	return withOrderedHeaders(req, orderedHeaders)
}
