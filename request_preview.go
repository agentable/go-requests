package requests

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/agentable/go-orderedobject"
)

// PreviewValueState describes whether a value is known, intentionally omitted,
// or available in a preview.
type PreviewValueState string

const (
	// PreviewValueUnknown means the value was not available without performing
	// an unqualified operation.
	PreviewValueUnknown PreviewValueState = "unknown"
	// PreviewValueOmitted means the value was intentionally withheld by policy.
	PreviewValueOmitted PreviewValueState = "omitted-by-policy"
	// PreviewValuePresent means the value is included in the preview.
	PreviewValuePresent PreviewValueState = "present"
)

// PreviewValue is a policy-filtered scalar value.
type PreviewValue struct {
	state PreviewValueState
	value string
}

// State reports the disclosure state of the value.
func (v PreviewValue) State() PreviewValueState {
	return v.state
}

// Value returns the value when it is present. Omitted and unknown values
// return an empty string; inspect State to distinguish those cases.
func (v PreviewValue) Value() string {
	if v.state != PreviewValuePresent {
		return ""
	}
	return v.value
}

// PreviewTarget describes the non-sensitive URL target structure.
type PreviewTarget struct {
	scheme string
	host   string
	path   PreviewValue
}

// Scheme returns the URL scheme.
func (t PreviewTarget) Scheme() string {
	return t.scheme
}

// Host returns the URL host, including a port when one was supplied.
func (t PreviewTarget) Host() string {
	return t.host
}

// Path returns the policy-filtered URL path.
func (t PreviewTarget) Path() PreviewValue {
	return t.path
}

// PreviewQuery describes one query key and its value multiplicity.
type PreviewQuery struct {
	key    string
	values []PreviewValue
}

// Key returns the query key.
func (q PreviewQuery) Key() string {
	return q.key
}

// Values returns detached policy-filtered query values.
func (q PreviewQuery) Values() []PreviewValue {
	return slices.Clone(q.values)
}

// PreviewQueries is an ordered collection of query entries.
type PreviewQueries []PreviewQuery

// Entries returns a detached copy of the query entries.
func (q PreviewQueries) Entries() []PreviewQuery {
	entries := make([]PreviewQuery, len(q))
	for i := range q {
		entries[i] = PreviewQuery{key: q[i].key, values: slices.Clone(q[i].values)}
	}
	return entries
}

// PreviewHeader describes one semantic or ordered header entry.
type PreviewHeader struct {
	key    string
	values []PreviewValue
}

// Key returns the header name.
func (h PreviewHeader) Key() string {
	return h.key
}

// Values returns detached policy-filtered header values.
func (h PreviewHeader) Values() []PreviewValue {
	return slices.Clone(h.values)
}

// PreviewHeaders is an ordered collection of header entries.
type PreviewHeaders []PreviewHeader

// Entries returns a detached copy of the header entries.
func (h PreviewHeaders) Entries() []PreviewHeader {
	entries := make([]PreviewHeader, len(h))
	for i := range h {
		entries[i] = PreviewHeader{key: h[i].key, values: slices.Clone(h[i].values)}
	}
	return entries
}

// PreviewCookie describes a cookie name without exposing its value.
type PreviewCookie struct {
	name  string
	value PreviewValue
}

// Name returns the cookie name.
func (c PreviewCookie) Name() string {
	return c.name
}

// Value returns the policy-filtered cookie value.
func (c PreviewCookie) Value() PreviewValue {
	return c.value
}

// PreviewCookies is an ordered collection of cookie entries.
type PreviewCookies []PreviewCookie

// Entries returns a detached copy of the cookie entries.
func (c PreviewCookies) Entries() []PreviewCookie {
	return slices.Clone(c)
}

// PreviewBodyKind identifies the selected body vocabulary.
type PreviewBodyKind string

// Preview body kind values.
const (
	PreviewBodyNone      PreviewBodyKind = "none"
	PreviewBodyJSON      PreviewBodyKind = "json"
	PreviewBodyXML       PreviewBodyKind = "xml"
	PreviewBodyYAML      PreviewBodyKind = "yaml"
	PreviewBodyText      PreviewBodyKind = "text"
	PreviewBodyBytes     PreviewBodyKind = "bytes"
	PreviewBodyReader    PreviewBodyKind = "reader"
	PreviewBodyForm      PreviewBodyKind = "form"
	PreviewBodyMultipart PreviewBodyKind = "multipart"
)

// PreviewBody summarizes a selected request body without opening it.
type PreviewBody struct {
	kind               PreviewBodyKind
	mediaType          string
	presence           PreviewValueState
	value              PreviewValue
	length             int64
	lengthKnown        bool
	replayable         bool
	replayabilityKnown bool
	multipart          *PreviewMultipart
}

// Kind returns the selected body kind.
func (b PreviewBody) Kind() PreviewBodyKind { //nolint:gocritic // PreviewBody is an immutable value snapshot.
	return b.kind
}

// MediaType returns the selected body's structural media type.
func (b PreviewBody) MediaType() string { //nolint:gocritic // PreviewBody is an immutable value snapshot.
	return b.mediaType
}

// Presence reports whether a body selection is present. A selected empty body
// is present; a builder with no body is unknown/absent.
func (b PreviewBody) Presence() PreviewValueState { //nolint:gocritic // PreviewBody is an immutable value snapshot.
	return b.presence
}

// Value returns the policy-filtered body value.
func (b PreviewBody) Value() PreviewValue { //nolint:gocritic // PreviewBody is an immutable value snapshot.
	return b.value
}

// Length returns the known body length, or -1 when it is unknown.
func (b PreviewBody) Length() int64 { //nolint:gocritic // PreviewBody is an immutable value snapshot.
	if !b.lengthKnown {
		return -1
	}
	return b.length
}

// LengthKnown reports whether Length is known without opening the body.
func (b PreviewBody) LengthKnown() bool { //nolint:gocritic // PreviewBody is an immutable value snapshot.
	return b.lengthKnown
}

// Replayable reports whether the selected body is known to be replayable.
// ReplayabilityKnown distinguishes an unknown value from a known false value.
func (b PreviewBody) Replayable() bool { //nolint:gocritic // PreviewBody is an immutable value snapshot.
	return b.replayable
}

// ReplayabilityKnown reports whether replayability is known without opening
// the body.
func (b PreviewBody) ReplayabilityKnown() bool { //nolint:gocritic // PreviewBody is an immutable value snapshot.
	return b.replayabilityKnown
}

// Multipart returns a detached multipart manifest, or nil for other bodies.
func (b PreviewBody) Multipart() *PreviewMultipart { //nolint:gocritic // PreviewBody is an immutable value snapshot.
	return b.multipart.clone()
}

// PreviewMultipart is a detached structural multipart manifest.
type PreviewMultipart struct {
	fields   []PreviewMultipartField
	files    []PreviewMultipartFile
	boundary PreviewValue
}

// Fields returns detached form-field metadata.
func (m *PreviewMultipart) Fields() []PreviewMultipartField {
	if m == nil {
		return nil
	}
	fields := make([]PreviewMultipartField, len(m.fields))
	for i := range m.fields {
		fields[i] = PreviewMultipartField{name: m.fields[i].name, values: slices.Clone(m.fields[i].values)}
	}
	return fields
}

// Files returns detached file-part metadata in part order.
func (m *PreviewMultipart) Files() []PreviewMultipartFile {
	if m == nil {
		return nil
	}
	return slices.Clone(m.files)
}

// Boundary returns the configured boundary when one was explicitly supplied.
// Automatically generated boundaries are unknown because Preview never creates one.
func (m *PreviewMultipart) Boundary() PreviewValue {
	if m == nil {
		return PreviewValue{state: PreviewValueUnknown}
	}
	return m.boundary
}

// PreviewMultipartField describes one multipart form field.
type PreviewMultipartField struct {
	name   string
	values []PreviewValue
}

// Name returns the form field name.
func (f PreviewMultipartField) Name() string {
	return f.name
}

// Values returns detached omitted field values, preserving multiplicity.
func (f PreviewMultipartField) Values() []PreviewValue {
	return slices.Clone(f.values)
}

// PreviewMultipartFile describes one multipart file part.
type PreviewMultipartFile struct {
	field       string
	filename    string
	contentType string
}

// Field returns the multipart field name.
func (f PreviewMultipartFile) Field() string {
	return f.field
}

// Filename returns the file name metadata.
func (f PreviewMultipartFile) Filename() string {
	return f.filename
}

// ContentType returns the explicitly configured file content type.
func (f PreviewMultipartFile) ContentType() string {
	return f.contentType
}

func (m *PreviewMultipart) clone() *PreviewMultipart {
	if m == nil {
		return nil
	}
	clone := &PreviewMultipart{
		fields:   make([]PreviewMultipartField, len(m.fields)),
		files:    slices.Clone(m.files),
		boundary: m.boundary,
	}
	for i := range m.fields {
		clone.fields[i] = PreviewMultipartField{name: m.fields[i].name, values: slices.Clone(m.fields[i].values)}
	}
	return clone
}

// RequestPreview is a detached, policy-filtered pre-delivery request projection.
// It is not a prepared request and cannot be sent.
type RequestPreview struct {
	method         string
	target         PreviewTarget
	url            *url.URL
	query          PreviewQueries
	headers        PreviewHeaders
	orderedHeaders PreviewHeaders
	cookies        PreviewCookies
	contentType    string
	body           PreviewBody
}

// Method returns the effective HTTP method.
func (p *RequestPreview) Method() string {
	if p == nil {
		return ""
	}
	return p.method
}

// Target returns the safe URL target structure.
func (p *RequestPreview) Target() PreviewTarget {
	if p == nil {
		return PreviewTarget{}
	}
	return p.target
}

// URL returns a detached URL containing only safe target structure.
func (p *RequestPreview) URL() *url.URL {
	if p == nil || p.url == nil {
		return nil
	}
	return p.url.Clone()
}

// Query returns detached query-key metadata.
func (p *RequestPreview) Query() PreviewQueries {
	if p == nil {
		return nil
	}
	return PreviewQueries(p.query.Entries())
}

// Headers returns detached semantic header metadata.
func (p *RequestPreview) Headers() PreviewHeaders {
	if p == nil {
		return nil
	}
	return PreviewHeaders(p.headers.Entries())
}

// OrderedHeaders returns detached ordered-header intent metadata.
func (p *RequestPreview) OrderedHeaders() PreviewHeaders {
	if p == nil {
		return nil
	}
	return PreviewHeaders(p.orderedHeaders.Entries())
}

// Cookies returns detached cookie-name metadata.
func (p *RequestPreview) Cookies() PreviewCookies {
	if p == nil {
		return nil
	}
	return PreviewCookies(p.cookies.Entries())
}

// ContentType returns the effective semantic Content-Type header value.
func (p *RequestPreview) ContentType() string {
	if p == nil {
		return ""
	}
	return p.contentType
}

// Body returns a detached body summary.
func (p *RequestPreview) Body() PreviewBody {
	if p == nil {
		return PreviewBody{}
	}
	return p.body.clone()
}

// Preview returns a detached, pre-delivery projection of the request builder.
// It never invokes delivery, middleware, authentication, body readers, or
// configured logging.
func (b *RequestBuilder) Preview(ctx context.Context) (*RequestPreview, error) {
	if b == nil || b.client == nil {
		return nil, fmt.Errorf("%w: preview client", ErrInvalidConfigValue)
	}
	if b.preparationErr != nil {
		return nil, newPreviewPreparationError(b.preparationErrClass)
	}
	if ctx == nil {
		return nil, ErrRequestCreationFailed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	snap := b.client.snapshot()
	previewCtx, cancel := previewContext(ctx, b.timeout)
	if cancel != nil {
		defer cancel()
	}
	if err := previewCtx.Err(); err != nil {
		return nil, err
	}

	parsedURL, err := resolveRequestURL(snap.baseURL, b.preparePath(), b.queries)
	if err != nil {
		return nil, ErrRequestCreationFailed
	}
	request, err := http.NewRequestWithContext(previewCtx, b.method, parsedURL.String(), nil)
	if err != nil {
		return nil, ErrRequestCreationFailed
	}
	if err := previewCtx.Err(); err != nil {
		return nil, err
	}

	preview, err := newURLRequestPreview(previewCtx, request, parsedURL, b, &snap)
	if err != nil {
		return nil, err
	}
	return preview, nil
}

type previewPreparationError struct {
	invalidConfigValue        bool
	unsupportedFormFieldsType bool
}

func newPreviewPreparationError(class preparationErrorClass) error {
	return &previewPreparationError{
		invalidConfigValue:        class == preparationErrorClassInvalidConfigValue,
		unsupportedFormFieldsType: class == preparationErrorClassUnsupportedFormFieldsType,
	}
}

func (*previewPreparationError) Error() string {
	return "request preview preparation failed"
}

func (e *previewPreparationError) Is(target error) bool {
	switch target {
	case ErrInvalidConfigValue:
		return e.invalidConfigValue
	case ErrUnsupportedFormFieldsType:
		return e.unsupportedFormFieldsType
	default:
		return false
	}
}

func previewContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); !ok && timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return ctx, nil
}

func newURLRequestPreview(
	ctx context.Context,
	request *http.Request,
	parsedURL *url.URL,
	b *RequestBuilder,
	snap *clientSnapshot,
) (*RequestPreview, error) {
	query := parsedURL.Query()
	keys := slices.Sorted(maps.Keys(query))
	queries := make(PreviewQueries, 0, len(keys))
	for _, key := range keys {
		values := make([]PreviewValue, len(query[key]))
		for i := range values {
			values[i] = PreviewValue{state: PreviewValueOmitted}
		}
		queries = append(queries, PreviewQuery{key: key, values: values})
	}

	safeURL := parsedURL.Clone()
	safeURL.User = nil
	safeURL.Path = ""
	safeURL.RawPath = ""
	safeURL.RawQuery = ""
	safeURL.ForceQuery = false
	safeURL.Fragment = ""
	safeURL.RawFragment = ""
	safeURL.Opaque = ""

	semanticHeaders, orderedHeaders, err := previewMetadata(b, snap)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, err := previewBody(ctx, b.body, b.headers)
	if err != nil {
		return nil, err
	}
	if b.body.kind == requestBodyMultipart && b.body.generatedContentType {
		deleteHeaderValues(semanticHeaders, "Content-Type")
		deleteOrderedHeader(orderedHeaders, "Content-Type")
	}

	return &RequestPreview{
		method: request.Method,
		target: PreviewTarget{
			scheme: parsedURL.Scheme,
			host:   parsedURL.Host,
			path:   PreviewValue{state: PreviewValueOmitted},
		},
		url:            safeURL,
		query:          queries,
		headers:        previewHeaderEntries(semanticHeaders),
		orderedHeaders: previewOrderedHeaderEntries(orderedHeaders, semanticHeaders.Get("Content-Type")),
		cookies:        previewCookieEntries(previewCookies(b, snap)),
		contentType:    semanticHeaders.Get("Content-Type"),
		body:           body,
	}, nil
}

func (b PreviewBody) clone() PreviewBody { //nolint:gocritic // PreviewBody is an immutable value snapshot.
	b.multipart = b.multipart.clone()
	return b
}

func previewBody(
	ctx context.Context,
	selection requestBodySelection,
	headers *http.Header,
) (PreviewBody, error) {
	body := PreviewBody{
		kind:     previewBodyKind(selection.kind),
		presence: PreviewValueUnknown,
		value:    PreviewValue{state: PreviewValueUnknown},
	}

	switch selection.kind {
	case requestBodyNone:
		return body, nil
	case requestBodyJSON, requestBodyXML, requestBodyYAML:
		body.presence = PreviewValuePresent
		body.value = PreviewValue{state: PreviewValueOmitted}
		body.mediaType = selection.contentType
		body.replayable = true
		body.replayabilityKnown = true
		if headerValue(headers, "Content-Type") == "" {
			return PreviewBody{}, ErrUnsupportedContentType
		}
		return body, nil
	case requestBodyText:
		value, ok := selection.value.(string)
		if !ok {
			return PreviewBody{}, previewInvalidBodyError()
		}
		body.presence = PreviewValuePresent
		body.value = PreviewValue{state: PreviewValueOmitted}
		body.mediaType = selection.contentType
		body.length = int64(len(value))
		body.lengthKnown = true
		body.replayable = true
		body.replayabilityKnown = true
		return body, nil
	case requestBodyBytes:
		value, ok := selection.value.([]byte)
		if !ok {
			return PreviewBody{}, previewInvalidBodyError()
		}
		body.presence = PreviewValuePresent
		body.value = PreviewValue{state: PreviewValueOmitted}
		body.mediaType = selection.contentType
		body.length = int64(len(value))
		body.lengthKnown = true
		body.replayable = true
		body.replayabilityKnown = true
		return body, nil
	case requestBodyReader:
		if isNilInterface(selection.value) {
			return PreviewBody{}, previewInvalidBodyError()
		}
		body.presence = PreviewValuePresent
		body.value = PreviewValue{state: PreviewValueOmitted}
		body.mediaType = selection.contentType
		return body, nil
	case requestBodyForm:
		if selection.form == nil {
			return PreviewBody{}, previewInvalidBodyError()
		}
		if err := previewCheckpoint(ctx); err != nil {
			return PreviewBody{}, err
		}
		body.presence = PreviewValuePresent
		body.value = PreviewValue{state: PreviewValueOmitted}
		body.mediaType = selection.contentType
		body.replayable = true
		body.replayabilityKnown = true
		return body, nil
	case requestBodyMultipart:
		manifest, err := previewMultipart(ctx, selection.multipart)
		if err != nil {
			return PreviewBody{}, err
		}
		body.presence = PreviewValuePresent
		body.value = PreviewValue{state: PreviewValueOmitted}
		body.mediaType = "multipart/form-data"
		body.replayable = selection.multipart.canReplay
		body.replayabilityKnown = true
		body.multipart = manifest
		return body, nil
	default:
		return PreviewBody{}, previewInvalidBodyError()
	}
}

func previewBodyKind(kind requestBodyKind) PreviewBodyKind {
	switch kind {
	case requestBodyNone:
		return PreviewBodyNone
	case requestBodyJSON:
		return PreviewBodyJSON
	case requestBodyXML:
		return PreviewBodyXML
	case requestBodyYAML:
		return PreviewBodyYAML
	case requestBodyText:
		return PreviewBodyText
	case requestBodyBytes:
		return PreviewBodyBytes
	case requestBodyReader:
		return PreviewBodyReader
	case requestBodyForm:
		return PreviewBodyForm
	case requestBodyMultipart:
		return PreviewBodyMultipart
	default:
		return ""
	}
}

func previewMultipart(ctx context.Context, multipart *Multipart) (*PreviewMultipart, error) {
	if multipart == nil {
		return nil, previewInvalidBodyError()
	}
	if multipart.canReplay && multipart.replayMaxBytes < 0 {
		return nil, previewInvalidBodyError()
	}
	if multipart.boundary != "" && !validPreviewMultipartBoundary(multipart.boundary) {
		return nil, previewInvalidBodyError()
	}

	manifest := &PreviewMultipart{
		boundary: PreviewValue{state: PreviewValueUnknown},
	}
	if multipart.boundary != "" {
		manifest.boundary = PreviewValue{state: PreviewValuePresent, value: multipart.boundary}
	}

	keys := slices.Sorted(maps.Keys(multipart.fields))
	manifest.fields = make([]PreviewMultipartField, 0, len(keys))
	for i, key := range keys {
		if i%64 == 0 {
			if err := previewCheckpoint(ctx); err != nil {
				return nil, err
			}
		}
		values := multipart.fields[key]
		previewValues := make([]PreviewValue, len(values))
		for j := range previewValues {
			previewValues[j] = PreviewValue{state: PreviewValueOmitted}
		}
		manifest.fields = append(manifest.fields, PreviewMultipartField{name: key, values: previewValues})
	}

	manifest.files = make([]PreviewMultipartFile, len(multipart.parts))
	for i, part := range multipart.parts {
		if i%64 == 0 {
			if err := previewCheckpoint(ctx); err != nil {
				return nil, err
			}
		}
		if part.Field == "" || isNilInterface(part.Body) {
			return nil, previewInvalidBodyError()
		}
		manifest.files[i] = PreviewMultipartFile{
			field:       part.Field,
			filename:    part.Filename,
			contentType: part.ContentType,
		}
	}
	return manifest, nil
}

func validPreviewMultipartBoundary(boundary string) bool {
	if len(boundary) < 1 || len(boundary) > 70 {
		return false
	}
	last := len(boundary) - 1
	for i := range len(boundary) {
		value := boundary[i]
		if ('A' <= value && value <= 'Z') || ('a' <= value && value <= 'z') || ('0' <= value && value <= '9') {
			continue
		}
		switch value {
		case '\'', '(', ')', '+', '_', ',', '-', '.', '/', ':', '=', '?':
			continue
		case ' ':
			if i != last {
				continue
			}
		}
		return false
	}
	return true
}

func previewInvalidBodyError() error {
	return fmt.Errorf("%w: preview body", ErrInvalidConfigValue)
}

func previewCheckpoint(ctx context.Context) error {
	if ctx == nil {
		return ErrRequestCreationFailed
	}
	return ctx.Err()
}

func headerValue(headers *http.Header, key string) string {
	if headers == nil {
		return ""
	}
	return headers.Get(key)
}

func previewMetadata(b *RequestBuilder, snap *clientSnapshot) (http.Header, *orderedobject.Object[[]string], error) {
	clientHeaders := http.Header{}
	addHeaderValues(clientHeaders, snap.headers, snap.orderedHeaders)
	clientAuth, err := previewAuthHeader(snap.auth)
	if err != nil {
		return nil, nil, err
	}
	if clientAuth {
		clientHeaders.Set("Authorization", "preview-redacted")
	}

	semanticHeaders := clientHeaders.Clone()
	if b.headers != nil {
		overlayHeaderValues(semanticHeaders, *b.headers, b.orderedHeaders)
	}

	requestAuth, err := previewAuthHeader(b.auth)
	if err != nil {
		return nil, nil, err
	}
	if requestAuth {
		semanticHeaders.Set("Authorization", "preview-redacted")
	}

	orderedHeaders := b.effectiveOrderedHeaders(snap)
	syncOrderedHeaderValues(orderedHeaders, semanticHeaders)
	return semanticHeaders, orderedHeaders, nil
}

func previewAuthHeader(auth AuthMethod) (bool, error) {
	if auth == nil {
		return false, nil
	}
	if isNilInterface(auth) {
		return false, fmt.Errorf("%w: preview auth", ErrInvalidConfigValue)
	}

	switch value := auth.(type) {
	case BasicAuth:
		if value.Username == "" || value.Password == "" {
			return false, fmt.Errorf("%w: preview auth", ErrInvalidConfigValue)
		}
	case *BasicAuth:
		if value.Username == "" || value.Password == "" {
			return false, fmt.Errorf("%w: preview auth", ErrInvalidConfigValue)
		}
	case BearerAuth:
		if value.Token == "" {
			return false, fmt.Errorf("%w: preview auth", ErrInvalidConfigValue)
		}
	case *BearerAuth:
		if value.Token == "" {
			return false, fmt.Errorf("%w: preview auth", ErrInvalidConfigValue)
		}
	case CustomAuth:
		if value.Header == "" {
			return false, fmt.Errorf("%w: preview auth", ErrInvalidConfigValue)
		}
	case *CustomAuth:
		if value.Header == "" {
			return false, fmt.Errorf("%w: preview auth", ErrInvalidConfigValue)
		}
	default:
		return false, fmt.Errorf("%w: preview auth", ErrInvalidConfigValue)
	}
	return true, nil
}

func previewHeaderEntries(headers http.Header) PreviewHeaders {
	keys := slices.Sorted(maps.Keys(headers))
	entries := make(PreviewHeaders, 0, len(keys))
	for _, key := range keys {
		values := previewHeaderValues(key, headers[key], headers.Get(key))
		entries = append(entries, PreviewHeader{key: key, values: values})
	}
	return entries
}

func previewHeaderValues(key string, values []string, contentType string) []PreviewValue {
	projected := make([]PreviewValue, len(values))
	for i := range projected {
		projected[i] = PreviewValue{state: PreviewValueOmitted}
	}
	if strings.EqualFold(key, "Content-Type") && len(projected) > 0 {
		projected[0] = PreviewValue{state: PreviewValuePresent, value: contentType}
	}
	return projected
}

func previewOrderedHeaderEntries(
	ordered *orderedobject.Object[[]string],
	contentType string,
) PreviewHeaders {
	if ordered == nil {
		return nil
	}
	entries := ordered.Entries()
	result := make(PreviewHeaders, 0, len(entries))
	for _, entry := range entries {
		values := previewHeaderValues(entry.Key, entry.Value, contentType)
		result = append(result, PreviewHeader{key: entry.Key, values: values})
	}
	return result
}

type previewCookieName struct {
	name string
}

func previewCookies(b *RequestBuilder, snap *clientSnapshot) []previewCookieName {
	clientHeaders := http.Header{}
	addHeaderValues(clientHeaders, snap.headers, snap.orderedHeaders)
	defaults := parsePreviewCookieHeaders(clientHeaders.Values("Cookie"))
	for _, cookie := range snap.cookies {
		if cookie != nil && validPreviewCookieName(cookie.Name) {
			defaults = append(defaults, previewCookieName{name: cookie.Name})
		}
	}

	overrides := []previewCookieName{}
	if b.headers != nil {
		requestHeaders := http.Header{}
		addHeaderValues(requestHeaders, *b.headers, b.orderedHeaders)
		overrides = append(overrides, parsePreviewCookieHeaders(requestHeaders.Values("Cookie"))...)
	}
	for _, cookie := range b.cookies {
		if cookie != nil && validPreviewCookieName(cookie.Name) {
			overrides = append(overrides, previewCookieName{name: cookie.Name})
		}
	}

	result := make([]previewCookieName, 0, len(defaults)+len(overrides))
	positions := make(map[string]int, cap(result))
	for _, layer := range [][]previewCookieName{defaults, overrides} {
		for _, cookie := range layer {
			if position, ok := positions[cookie.name]; ok {
				result[position] = cookie
				continue
			}
			positions[cookie.name] = len(result)
			result = append(result, cookie)
		}
	}
	return result
}

func parsePreviewCookieHeaders(headers []string) []previewCookieName {
	cookies := headerCookies(http.Header{"Cookie": headers})
	result := make([]previewCookieName, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie != nil {
			result = append(result, previewCookieName{name: cookie.Name})
		}
	}
	return result
}

func validPreviewCookieName(name string) bool {
	return (&http.Cookie{Name: name}).Valid() == nil //nolint:gosec // G124: only validates the name; Preview never serializes or sends the cookie
}

func previewCookieEntries(cookies []previewCookieName) PreviewCookies {
	entries := make(PreviewCookies, len(cookies))
	for i, cookie := range cookies {
		entries[i] = PreviewCookie{
			name:  cookie.name,
			value: PreviewValue{state: PreviewValueOmitted},
		}
	}
	return entries
}
