package requests

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/agentable/go-orderedobject"
)

// requestPlan is the delivery-independent request snapshot shared by the
// request exits. It owns structural containers and keeps caller-owned
// collaborators (auth, typed values, readers, and multipart sources) as
// borrowed identities. Delivery policy is deliberately not part of this type.
type requestPlan struct {
	method    string
	targetURL *url.URL
	target    requestTarget
	pathTrace requestPathTrace

	// query contains every resolved occurrence, including private path/base
	// query values. Keeping occurrences rather than a map preserves disclosure
	// precedence for a later projection.
	query []requestOccurrence

	// headers is the effective semantic header set after client/request
	// precedence. Authentication is kept separately because its value must
	// never be materialized during compile or structural projection.
	headers               []requestOccurrence
	orderedHeaders        *orderedobject.Object[[]string]
	clientMetadata        requestMetadata
	requestMetadata       requestMetadata
	clientOrderedHeaders  *orderedobject.Object[[]string]
	requestOrderedHeaders *orderedobject.Object[[]string]
	cookies               []requestCookieOccurrence
	clientAuth            AuthMethod
	requestAuth           AuthMethod
	auth                  AuthMethod

	body requestBodyPlan
}

// requestPathTrace keeps the tagged source contributions that produced the
// request path. Prepare uses this trace to make disclosure decisions before
// inspecting a resolved URL; a resolved string alone cannot tell whether a
// private parameter was substituted into a safe-looking component.
type requestPathTrace struct {
	path            Value
	requestAbsolute bool
	basePathActive  bool
	basePathPrivate bool
	params          []requestPathParamTrace
}

type requestPathParamTrace struct {
	name                 string
	value                Value
	schemeOccurrences    int
	authorityOccurrences int
	userinfoOccurrences  int
	pathOccurrences      int
	queryNameOccurrences int
	fragmentOccurrences  int
}

// compileRequestPlan builds shared facts using one client snapshot. It defers
// delivery-specific body validation and snapshotting to the delivery exit.
func (b *RequestBuilder) compileRequestPlan() (*requestPlan, error) {
	if b == nil || b.client == nil {
		return nil, fmt.Errorf("%w: request plan client", ErrInvalidConfigValue)
	}
	return b.compileRequestPlanSnapshot(b.client.snapshot())
}

// compileRequestPlanSnapshot compiles shared facts from the client snapshot
// already captured by the caller, keeping each exit's snapshot boundary intact.
func (b *RequestBuilder) compileRequestPlanSnapshot(snap clientSnapshot) (*requestPlan, error) { //nolint:gocritic // A client snapshot is intentionally copied at the request boundary.
	builderMetadata := snapshotBuilderMetadata(b)
	targetSnapshot := snapshotRequestTarget(b)
	requestPath := targetSnapshot.resolvedPath()

	parsedURL, err := resolveRequestURL(snap.baseURL, requestPath, builderMetadata.queryValues())
	if err != nil {
		return nil, newRequestPlanCreationError(err)
	}
	request, err := http.NewRequestWithContext(context.Background(), b.method, parsedURL.String(), nil)
	if err != nil {
		return nil, newRequestPlanCreationError(err)
	}

	clientMetadata := snap.metadata.clone()
	query, err := compileQueryOccurrences(snap.baseURL, requestPath, builderMetadata.queries)
	if err != nil {
		return nil, newRequestPlanCreationError(err)
	}

	headers := mergePlanHeaderOccurrences(clientMetadata.headers, builderMetadata.headers)
	for i := range headers {
		if isCredentialRequestHeader(headers[i].name) {
			headers[i].value = privateRequestValue(headers[i].value.rawValue())
		}
	}
	orderedHeaders := compileOrderedHeaders(snap.orderedHeaders, b.orderedHeaders, builderMetadata.headers)
	semanticHeaders := requestOccurrencesHeaderValues(headers)
	syncOrderedHeaderValues(orderedHeaders, semanticHeaders)
	clientOrderedHeaders := cloneOrderedHeaders(snap.orderedHeaders)
	requestOrderedHeaders := cloneOrderedHeaders(b.orderedHeaders)

	cookies := compileCookieOccurrences(clientMetadata, builderMetadata)
	body := cloneRequestBodyPlan(b.body)

	return &requestPlan{
		method:                request.Method,
		targetURL:             parsedURL.Clone(),
		target:                targetSnapshot,
		pathTrace:             traceRequestTarget(snap.baseURL, requestPath, targetSnapshot),
		query:                 query,
		headers:               headers,
		orderedHeaders:        orderedHeaders,
		clientMetadata:        clientMetadata,
		requestMetadata:       builderMetadata,
		clientOrderedHeaders:  clientOrderedHeaders,
		requestOrderedHeaders: requestOrderedHeaders,
		cookies:               cookies,
		clientAuth:            snap.auth,
		requestAuth:           b.auth,
		auth:                  effectiveAuth(snap.auth, b.auth),
		body:                  body,
	}, nil
}

// bodyPreflightHeaders is the request-local header view historically consumed
// by body materialization. Client defaults remain part of the effective
// metadata projection, but do not satisfy a typed body's request-local
// Content-Type after the builder removes its generated header.
func (p *requestPlan) bodyPreflightHeaders() http.Header {
	if p == nil {
		return nil
	}
	return requestOccurrencesHeaderValues(p.requestMetadata.headers)
}

type requestPlanCreationError struct {
	cause error
}

func newRequestPlanCreationError(cause error) error {
	if cause == nil {
		return ErrRequestCreationFailed
	}
	return &requestPlanCreationError{cause: cause}
}

func (e *requestPlanCreationError) Error() string {
	return "request plan creation failed"
}

func (e *requestPlanCreationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *requestPlanCreationError) Is(target error) bool {
	return target == ErrRequestCreationFailed
}

func snapshotRequestTarget(b *RequestBuilder) requestTarget {
	target := b.target
	if target.pathParams != nil {
		target.pathParams = make(map[string]Value, len(target.pathParams))
		for name, value := range b.target.pathParams {
			target.pathParams[name] = value
		}
	}
	return target
}

func traceRequestTarget(baseURL, requestPath string, target requestTarget) requestPathTrace {
	trace := requestPathTrace{path: target.path}
	if parsed, err := url.Parse(requestPath); err == nil {
		trace.requestAbsolute = parsed.IsAbs()
	}
	if !trace.requestAbsolute && baseURL != "" {
		if base, err := url.Parse(baseURL); err == nil {
			escapedPath := base.EscapedPath()
			trace.basePathActive = escapedPath != "" && escapedPath != "/"
			trace.basePathPrivate = trace.basePathActive
		}
	}
	keys := make([]string, 0, len(target.pathParams))
	for name := range target.pathParams {
		keys = append(keys, name)
	}
	slices.Sort(keys)

	scheme, authority, userinfo, path, query, fragment := splitPathTraceSource(target.path.rawValue())
	for _, name := range keys {
		value := target.pathParams[name]
		token := "{" + name + "}"
		trace.params = append(trace.params, requestPathParamTrace{
			name:                 name,
			value:                value,
			schemeOccurrences:    strings.Count(scheme, token),
			authorityOccurrences: strings.Count(authority, token),
			userinfoOccurrences:  strings.Count(userinfo, token),
			pathOccurrences:      strings.Count(path, token),
			queryNameOccurrences: countQueryNameToken(query, token),
			fragmentOccurrences:  strings.Count(fragment, token),
		})
	}
	return trace
}

// splitPathTraceSource is intentionally lexical. It preserves the source
// spelling and placeholder locations instead of decoding or re-parsing the
// resolved URL. The resolver remains authoritative for delivery semantics.
func splitPathTraceSource(raw string) (scheme, authority, userinfo, path, query, fragment string) {
	rest := raw
	if colon := strings.IndexByte(rest, ':'); colon > 0 {
		candidate := rest[:colon]
		if !strings.ContainsAny(candidate, "/?#") {
			scheme = candidate
			rest = rest[colon+1:]
		}
	}
	if strings.HasPrefix(rest, "//") {
		rest = rest[2:]
		end := len(rest)
		if index := strings.IndexAny(rest, "/?#"); index >= 0 {
			end = index
		}
		authorityPart := rest[:end]
		if at := strings.LastIndexByte(authorityPart, '@'); at >= 0 {
			userinfo = authorityPart[:at]
			authority = authorityPart[at+1:]
		} else {
			authority = authorityPart
		}
		rest = rest[end:]
	}
	pathEnd := len(rest)
	if index := strings.IndexAny(rest, "?#"); index >= 0 {
		pathEnd = index
	}
	path = rest[:pathEnd]
	rest = rest[pathEnd:]
	if strings.HasPrefix(rest, "?") {
		rest = rest[1:]
		queryEnd := len(rest)
		if index := strings.IndexByte(rest, '#'); index >= 0 {
			queryEnd = index
		}
		query = rest[:queryEnd]
		rest = rest[queryEnd:]
	}
	if strings.HasPrefix(rest, "#") {
		fragment = rest[1:]
	}
	return scheme, authority, userinfo, path, query, fragment
}

func countQueryNameToken(rawQuery, token string) int {
	if rawQuery == "" {
		return 0
	}
	count := 0
	for _, occurrence := range strings.Split(rawQuery, "&") {
		name := occurrence
		if index := strings.IndexByte(name, '='); index >= 0 {
			name = name[:index]
		}
		count += strings.Count(name, token)
	}
	return count
}

func snapshotBuilderMetadata(b *RequestBuilder) requestMetadata {
	// This snapshot deliberately does not mutate the caller-owned builder.
	return b.metadata.clone()
}

func cloneRequestBodyPlan(body requestBodyPlan) requestBodyPlan { //nolint:gocritic // Cloning preserves structural value semantics while payload bytes stay borrowed.
	clone := body
	// Form values remain a borrowed delivery source in shared facts. A
	// delivery-bound compile clones the container after static validation;
	// Prepare rebuilds all-public bytes from tagged occurrences instead.
	clone.formOccurrences = slices.Clone(body.formOccurrences)
	// PublicPayload bytes remain builder-owned and are read-only in the shared plan.
	// Delivery reads them at materialization and creates a replayable body copy.
	return clone
}

// validateDeliveryStaticFacts contains static checks for non-multipart body
// selections. Multipart has a separate delivery validator because part errors
// must retain their materialization-time boundary.
func validateDeliveryStaticFacts(body requestBodyPlan, headers http.Header) error { //nolint:gocritic // Static validation reads a detached plan snapshot only.
	switch body.kind {
	case requestBodyNone:
		return nil
	case requestBodyJSON, requestBodyXML, requestBodyYAML:
		if headers.Get("Content-Type") == "" {
			return ErrUnsupportedContentType
		}
		return nil
	case requestBodyText:
		if _, ok := body.value.(string); !ok {
			return previewInvalidBodyError()
		}
		return nil
	case requestBodyBytes:
		if _, ok := body.value.([]byte); !ok {
			return previewInvalidBodyError()
		}
		return nil
	case requestBodyReader:
		if isNilInterface(body.value) {
			return previewInvalidBodyError()
		}
		return nil
	case requestBodyForm:
		if body.form == nil {
			return previewInvalidBodyError()
		}
		return nil
	default:
		return previewInvalidBodyError()
	}
}

func effectiveAuth(client, request AuthMethod) AuthMethod {
	if request != nil {
		return request
	}
	return client
}

func compileQueryOccurrences(baseURL, requestPath string, builder []requestOccurrence) ([]requestOccurrence, error) {
	requestURL, err := url.Parse(requestPath)
	if err != nil {
		return nil, err
	}
	requestQuery, err := url.ParseQuery(requestURL.RawQuery)
	if err != nil {
		return nil, err
	}

	baseQuery := url.Values(nil)
	if !requestURL.IsAbs() && baseURL != "" {
		base, err := url.Parse(baseURL)
		if err != nil {
			return nil, err
		}
		baseQuery = base.Query()
	}
	occurrences := make([]requestOccurrence, 0, len(baseQuery)+len(requestQuery)+len(builder))
	if requestURL.IsAbs() || baseURL == "" {
		appendPrivateQueryValues(&occurrences, requestQuery)
	} else {
		appendPrivateQueryValues(&occurrences, baseQuery)
		appendPrivateQueryValues(&occurrences, requestQuery)
	}
	occurrences = append(occurrences, cloneRequestOccurrences(builder)...)
	// resolveRequestURL ultimately serializes the combined values through
	// url.Values.Encode, which sorts query keys globally. Keep the source-layer
	// append order stable within each key so disclosure tags still line up with
	// base -> request-path -> builder precedence and repeated values.
	slices.SortStableFunc(occurrences, func(left, right requestOccurrence) int {
		return strings.Compare(left.name, right.name)
	})
	return occurrences, nil
}

func appendPrivateQueryValues(dst *[]requestOccurrence, values url.Values) {
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	slices.Sort(keys)
	for _, name := range keys {
		entries := values[name]
		for _, value := range entries {
			*dst = append(*dst, requestOccurrence{name: name, value: privateRequestValue(value)})
		}
	}
}

func cloneRequestOccurrences(values []requestOccurrence) []requestOccurrence {
	return slices.Clone(values)
}

func mergePlanHeaderOccurrences(client, request []requestOccurrence) []requestOccurrence {
	merged := cloneRequestOccurrences(client)
	requestNames := make(map[string]struct{}, len(request))
	for _, occurrence := range request {
		requestNames[strings.ToLower(occurrence.name)] = struct{}{}
	}
	if len(requestNames) > 0 {
		kept := merged[:0]
		for _, occurrence := range merged {
			if _, overridden := requestNames[strings.ToLower(occurrence.name)]; !overridden {
				kept = append(kept, occurrence)
			}
		}
		merged = kept
	}
	return append(merged, cloneRequestOccurrences(request)...)
}

func requestOccurrencesHeaderValues(occurrences []requestOccurrence) http.Header {
	headers := make(http.Header, len(occurrences))
	for _, occurrence := range occurrences {
		headers.Add(occurrence.name, occurrence.value.rawValue())
	}
	return headers
}

func compileOrderedHeaders(
	client, request *orderedobject.Object[[]string], requestHeaders []requestOccurrence,
) *orderedobject.Object[[]string] {
	ordered := mergeOrderedHeaders(client, request)
	if ordered == nil || len(requestHeaders) == 0 {
		return ordered
	}
	for name := range distinctOccurrenceNames(requestHeaders) {
		if _, explicitlyOrdered := orderedHeaderKey(request, name); !explicitlyOrdered {
			deleteOrderedHeader(ordered, name)
		}
	}
	if ordered.Len() == 0 {
		return nil
	}
	return ordered
}

func distinctOccurrenceNames(occurrences []requestOccurrence) map[string]struct{} {
	names := make(map[string]struct{}, len(occurrences))
	for _, occurrence := range occurrences {
		names[occurrence.name] = struct{}{}
	}
	return names
}

func headerCookies(headers http.Header) []*http.Cookie {
	return (&http.Request{Header: headers}).Cookies()
}

func compileCookieOccurrences(client, request requestMetadata) []requestCookieOccurrence {
	clientHeaders := client.headers
	requestHeaders := request.headers
	result := make([]requestCookieOccurrence, 0)
	appendCookieLayer := func(values []*http.Cookie) {
		for _, cookie := range values {
			if cookie == nil {
				continue
			}
			result = append(result, requestCookieOccurrence{
				name:   cookie.Name,
				value:  privateRequestValue(cookie.Value),
				cookie: cloneCookie(cookie),
			})
		}
	}
	appendTaggedCookieLayer := func(values []requestCookieOccurrence) {
		for _, occurrence := range values {
			cookie := cloneCookie(occurrence.cookie) //nolint:gosec // This is an outbound request cookie clone.
			if cookie == nil {
				cookie = &http.Cookie{}
			}
			cookie.Name = occurrence.name
			cookie.Value = occurrence.value.rawValue()
			result = append(result, requestCookieOccurrence{
				name:   occurrence.name,
				value:  occurrence.value,
				cookie: cookie,
			})
		}
	}
	appendCookieLayer(headerCookies(requestOccurrencesHeaderValues(clientHeaders)))
	appendTaggedCookieLayer(client.cookies)
	appendCookieLayer(headerCookies(requestOccurrencesHeaderValues(requestHeaders)))
	appendTaggedCookieLayer(request.cookies)
	return mergeCookieOccurrences(result)
}

func mergeCookieOccurrences(values []requestCookieOccurrence) []requestCookieOccurrence {
	result := make([]requestCookieOccurrence, 0, len(values))
	positions := make(map[string]int, len(values))
	for _, occurrence := range values {
		if position, ok := positions[occurrence.name]; ok {
			result[position] = occurrence
			continue
		}
		positions[occurrence.name] = len(result)
		result = append(result, occurrence)
	}
	return result
}

func cloneCookie(cookie *http.Cookie) *http.Cookie {
	if cookie == nil {
		return nil
	}
	clone := *cookie //nolint:gosec // This is an outbound request cookie clone.
	clone.Unparsed = slices.Clone(cookie.Unparsed)
	return &clone
}

// projectStructural is the single structural projection used by Preview.
// It only consumes plan-owned containers and static body facts; auth shape
// checks intentionally happen without invoking AuthMethod methods.
func (p *requestPlan) projectStructural(ctx context.Context) (*RequestPreview, error) {
	if p == nil {
		return nil, ErrRequestCreationFailed
	}
	if err := previewCheckpoint(ctx); err != nil {
		return nil, err
	}
	clientAuth, err := previewAuthHeader(p.clientAuth)
	if err != nil {
		return nil, err
	}
	requestAuth, err := previewAuthHeader(p.requestAuth)
	if err != nil {
		return nil, err
	}

	semanticHeaders := requestOccurrencesHeaderValues(p.headers)
	if clientAuth || requestAuth {
		semanticHeaders.Set("Authorization", "preview-redacted")
	}
	orderedHeaders := cloneOrderedHeaders(p.orderedHeaders)
	syncOrderedHeaderValues(orderedHeaders, semanticHeaders)

	bodyHeaders := p.bodyPreflightHeaders()
	body, err := previewBody(ctx, &p.body, &bodyHeaders)
	if err != nil {
		return nil, err
	}
	if p.body.kind == requestBodyMultipart && p.body.generatedContentType {
		deleteHeaderValues(semanticHeaders, "Content-Type")
		deleteOrderedHeader(orderedHeaders, "Content-Type")
	}

	query := p.targetURL.Query()
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	queries := make(PreviewQueries, 0, len(keys))
	for _, key := range keys {
		values := make([]PreviewValue, len(query[key]))
		for i := range values {
			values[i] = PreviewValue{state: PreviewValueOmitted}
		}
		queries = append(queries, PreviewQuery{key: key, values: values})
	}

	safeURL := p.targetURL.Clone()
	safeURL.User = nil
	safeURL.Path = ""
	safeURL.RawPath = ""
	safeURL.RawQuery = ""
	safeURL.ForceQuery = false
	safeURL.Fragment = ""
	safeURL.RawFragment = ""
	safeURL.Opaque = ""

	return &RequestPreview{
		method: p.method,
		target: PreviewTarget{
			scheme: p.targetURL.Scheme,
			host:   p.targetURL.Host,
			path:   PreviewValue{state: PreviewValueOmitted},
		},
		url:            safeURL,
		query:          queries,
		headers:        previewHeaderEntries(semanticHeaders),
		orderedHeaders: previewOrderedHeaderEntries(orderedHeaders, semanticHeaders.Get("Content-Type")),
		cookies:        previewCookieEntries(planPreviewCookies(p.cookies)),
		contentType:    semanticHeaders.Get("Content-Type"),
		body:           body,
	}, nil
}

func planPreviewCookies(cookies []requestCookieOccurrence) []previewCookieName {
	result := make([]previewCookieName, 0, len(cookies))
	for _, cookie := range cookies {
		if validCookieName(cookie.name) {
			result = append(result, previewCookieName{name: cookie.name})
		}
	}
	return result
}
