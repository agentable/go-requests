package requests

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/agentable/go-orderedobject"
)

// PrepareOptions controls the amount of body data retained by Prepare.
type PrepareOptions struct {
	MaxPreparedBodyBytes int64
}

// PreparedValueState describes whether a value is redacted or retained.
type PreparedValueState string

const (
	// PreparedValueRedacted indicates that the value is intentionally withheld.
	PreparedValueRedacted PreparedValueState = "redacted"
	// PreparedValuePresent indicates that policy permits retaining the value.
	PreparedValuePresent PreparedValueState = "present"
)

// PreparedString is a detached, policy-filtered string.
type PreparedString struct {
	state PreparedValueState
	value string
}

// State reports whether the string is redacted or retained.
func (v PreparedString) State() PreparedValueState {
	if v.state != PreparedValuePresent {
		return PreparedValueRedacted
	}
	return PreparedValuePresent
}

// Value returns the retained string, or an empty string when redacted.
func (v PreparedString) Value() string {
	if v.State() != PreparedValuePresent {
		return ""
	}
	return v.value
}

// PreparedBytes is a detached, policy-filtered byte sequence.
type PreparedBytes struct {
	state PreparedValueState
	value []byte
}

// State reports whether the bytes are redacted or retained.
func (v PreparedBytes) State() PreparedValueState {
	if v.state != PreparedValuePresent {
		return PreparedValueRedacted
	}
	return PreparedValuePresent
}

// Bytes returns a detached copy of retained bytes, or nil when redacted.
func (v PreparedBytes) Bytes() []byte {
	if v.State() != PreparedValuePresent {
		return nil
	}
	return bytes.Clone(v.value)
}

func (v PreparedString) clone() PreparedString {
	if v.State() != PreparedValuePresent {
		return preparedRedactedString()
	}
	return v
}

func (v PreparedBytes) clone() PreparedBytes {
	if v.State() != PreparedValuePresent {
		return preparedRedactedBytes()
	}
	return PreparedBytes{state: PreparedValuePresent, value: bytes.Clone(v.value)}
}

func preparedRedactedString() PreparedString {
	return PreparedString{state: PreparedValueRedacted}
}

func preparedPresentString(value string) PreparedString {
	return PreparedString{state: PreparedValuePresent, value: value}
}

func preparedRedactedBytes() PreparedBytes {
	return PreparedBytes{state: PreparedValueRedacted}
}

func preparedPresentBytes(value []byte) PreparedBytes {
	return PreparedBytes{state: PreparedValuePresent, value: bytes.Clone(value)}
}

// PreparedTarget contains structural target components and a whole-path
// disclosure decision.
type PreparedTarget struct {
	scheme    string
	authority string
	path      PreparedString
}

// Scheme returns the resolved structural scheme.
func (t PreparedTarget) Scheme() string { return t.scheme }

// Authority returns the resolved structural authority without userinfo.
func (t PreparedTarget) Authority() string { return t.authority }

// Path returns the whole resolved pathname, when it was explicitly approved.
func (t PreparedTarget) Path() PreparedString { return t.path.clone() }

// PreparedOccurrence contains one named set of policy-filtered values.
type PreparedOccurrence struct {
	name   string
	values []PreparedString
}

// Name returns the structural occurrence name.
func (o PreparedOccurrence) Name() string { return o.name }

// Values returns detached values in occurrence order.
func (o PreparedOccurrence) Values() []PreparedString {
	return clonePreparedStrings(o.values)
}

// PreparedHeader contains one semantic or ordered header entry.
type PreparedHeader struct {
	name   string
	values []PreparedString
}

// Name returns the header name.
func (h PreparedHeader) Name() string { return h.name }

// Values returns detached header values.
func (h PreparedHeader) Values() []PreparedString {
	return clonePreparedStrings(h.values)
}

// PreparedCookie contains one merged cookie occurrence.
type PreparedCookie struct {
	name  string
	value PreparedString
}

// Name returns the structural cookie name.
func (c PreparedCookie) Name() string { return c.name }

// Value returns the policy-filtered cookie value.
func (c PreparedCookie) Value() PreparedString { return c.value.clone() }

// PreparedBody contains selected body vocabulary and retained public bytes.
type PreparedBody struct {
	kind      PreviewBodyKind
	mediaType string
	data      PreparedBytes
	multipart *PreparedMultipart
}

// Kind returns the selected body vocabulary.
func (b PreparedBody) Kind() PreviewBodyKind { //nolint:gocritic // PreparedBody is an immutable value DTO.
	return b.kind
}

// MediaType returns the closed library-generated media type for the body kind.
func (b PreparedBody) MediaType() string { //nolint:gocritic // PreparedBody is an immutable value DTO.
	return b.mediaType
}

// Data returns detached retained body bytes.
func (b PreparedBody) Data() PreparedBytes { //nolint:gocritic // PreparedBody is an immutable value DTO.
	return b.data.clone()
}

// Multipart returns a detached structural multipart manifest.
func (b PreparedBody) Multipart() *PreparedMultipart { //nolint:gocritic // PreparedBody is an immutable value DTO.
	return clonePreparedMultipart(b.multipart)
}

// PreparedMultipart is a structural multipart manifest without raw metadata.
type PreparedMultipart struct {
	fields []PreparedOccurrence
	files  []PreparedMultipartFile
}

// Fields returns sorted detached field occurrences. Values are redacted while
// their count remains observable as schema structure.
func (m *PreparedMultipart) Fields() []PreparedOccurrence {
	if m == nil {
		return nil
	}
	return clonePreparedOccurrences(m.fields)
}

// Files returns detached file fields in source insertion order.
func (m *PreparedMultipart) Files() []PreparedMultipartFile {
	if m == nil {
		return nil
	}
	return slices.Clone(m.files)
}

// PreparedMultipartFile contains only the structural file field name.
type PreparedMultipartFile struct {
	field string
}

// Field returns the structural file field name.
func (f PreparedMultipartFile) Field() string { return f.field }

// RequestPreparation is a detached, non-sendable request projection.
type RequestPreparation struct {
	method         string
	target         PreparedTarget
	query          []PreparedOccurrence
	headers        []PreparedHeader
	orderedHeaders []PreparedHeader
	cookies        []PreparedCookie
	body           PreparedBody
}

// Method returns the effective request method.
func (p *RequestPreparation) Method() string {
	if p == nil {
		return ""
	}
	return p.method
}

// Target returns the detached target projection.
func (p *RequestPreparation) Target() PreparedTarget {
	if p == nil {
		return PreparedTarget{}
	}
	target := p.target
	target.path = target.path.clone()
	return target
}

// Query returns detached resolved query occurrences.
func (p *RequestPreparation) Query() []PreparedOccurrence {
	if p == nil {
		return nil
	}
	return clonePreparedOccurrences(p.query)
}

// Headers returns detached semantic header entries.
func (p *RequestPreparation) Headers() []PreparedHeader {
	if p == nil {
		return nil
	}
	return clonePreparedHeaders(p.headers)
}

// OrderedHeaders returns detached ordered header intent.
func (p *RequestPreparation) OrderedHeaders() []PreparedHeader {
	if p == nil {
		return nil
	}
	return clonePreparedHeaders(p.orderedHeaders)
}

// Cookies returns detached merged cookie occurrences.
func (p *RequestPreparation) Cookies() []PreparedCookie {
	if p == nil {
		return nil
	}
	return slices.Clone(p.cookies)
}

// Body returns the detached body projection.
func (p *RequestPreparation) Body() PreparedBody {
	if p == nil {
		return PreparedBody{}
	}
	return clonePreparedBody(p.body)
}

func clonePreparedStrings(values []PreparedString) []PreparedString {
	if values == nil {
		return nil
	}
	clone := make([]PreparedString, len(values))
	for i, value := range values {
		clone[i] = value.clone()
	}
	return clone
}

func clonePreparedOccurrences(values []PreparedOccurrence) []PreparedOccurrence {
	if values == nil {
		return nil
	}
	clone := make([]PreparedOccurrence, len(values))
	for i, value := range values {
		clone[i] = PreparedOccurrence{name: value.name, values: clonePreparedStrings(value.values)}
	}
	return clone
}

func clonePreparedHeaders(values []PreparedHeader) []PreparedHeader {
	if values == nil {
		return nil
	}
	clone := make([]PreparedHeader, len(values))
	for i, value := range values {
		clone[i] = PreparedHeader{name: value.name, values: clonePreparedStrings(value.values)}
	}
	return clone
}

func clonePreparedMultipart(value *PreparedMultipart) *PreparedMultipart {
	if value == nil {
		return nil
	}
	return &PreparedMultipart{
		fields: clonePreparedOccurrences(value.fields),
		files:  slices.Clone(value.files),
	}
}

func clonePreparedBody(value PreparedBody) PreparedBody { //nolint:gocritic // Keep the public DTO value-like while deep-copying nested data.
	return PreparedBody{
		kind:      value.kind,
		mediaType: value.mediaType,
		data:      value.data.clone(),
		multipart: clonePreparedMultipart(value.multipart),
	}
}

type preparationRetainedError struct {
	class preparationErrorClass
}

func (e *preparationRetainedError) Error() string { return "request preparation failed" }

func (e *preparationRetainedError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch e.class {
	case preparationErrorClassInvalidConfigValue:
		return target == ErrInvalidConfigValue
	case preparationErrorClassUnsupportedFormFieldsType:
		return target == ErrUnsupportedFormFieldsType
	default:
		return false
	}
}

// Prepare returns a detached, non-sendable request projection.
func (b *RequestBuilder) Prepare(ctx context.Context, opts PrepareOptions) (*RequestPreparation, error) {
	if b == nil || b.client == nil {
		return nil, fmt.Errorf("%w: request preparation builder", ErrInvalidConfigValue)
	}
	if b.preparationErr != nil {
		return nil, &preparationRetainedError{class: b.preparationErrClass}
	}
	if err := preparationCheckpoint(ctx); err != nil {
		return nil, err
	}
	if opts.MaxPreparedBodyBytes < 0 {
		return nil, ErrPreparationInvalidBudget
	}

	facts, err := b.compileRequestPlan()
	if err != nil {
		return nil, sanitizePreparationCompileError(err)
	}
	if err := validatePreparationBodyShape(facts); err != nil {
		return nil, err
	}
	if err := validatePreparationEligibility(ctx, facts); err != nil {
		return nil, err
	}
	return projectRequestPreparation(ctx, facts, opts.MaxPreparedBodyBytes)
}

func sanitizePreparationCompileError(err error) error {
	if err == nil {
		return nil
	}
	// Shared compilation is limited to method/URL preflight. Keep every
	// compiler failure behind the fixed preparation sentinel, including future
	// wrapper layers or newly added validation errors.
	return ErrRequestCreationFailed
}

func preparationCheckpoint(ctx context.Context) error {
	if ctx == nil {
		return ErrRequestCreationFailed
	}
	return ctx.Err()
}

func validatePreparationBodyShape(plan *requestPlan) error {
	if plan == nil {
		return ErrRequestCreationFailed
	}
	headers := plan.bodyPreflightHeaders()
	switch plan.body.kind {
	case requestBodyNone:
		return nil
	case requestBodyJSON, requestBodyXML, requestBodyYAML:
		if headers.Get("Content-Type") == "" {
			return ErrUnsupportedContentType
		}
		return nil
	case requestBodyText:
		if _, ok := plan.body.value.(string); !ok {
			return fmt.Errorf("%w: request body", ErrInvalidConfigValue)
		}
		return nil
	case requestBodyBytes:
		if _, ok := plan.body.value.([]byte); !ok {
			return fmt.Errorf("%w: request body", ErrInvalidConfigValue)
		}
		return nil
	case requestBodyReader:
		if isNilInterface(plan.body.value) {
			return fmt.Errorf("%w: request body", ErrInvalidConfigValue)
		}
		return nil
	case requestBodyForm:
		return nil
	case requestBodyMultipart:
		if plan.body.multipart == nil {
			return fmt.Errorf("%w: multipart body", ErrInvalidConfigValue)
		}
		return nil
	default:
		return fmt.Errorf("%w: request body", ErrInvalidConfigValue)
	}
}

func validatePreparationEligibility(ctx context.Context, plan *requestPlan) error {
	if plan == nil {
		return ErrRequestCreationFailed
	}
	for i, param := range plan.pathTrace.params {
		if i%64 == 0 {
			if err := preparationCheckpoint(ctx); err != nil {
				return err
			}
		}
		if param.value.isPublic() {
			continue
		}
		if param.schemeOccurrences > 0 || param.authorityOccurrences > 0 ||
			param.queryNameOccurrences > 0 {
			return ErrPreparationNotPreparable
		}
	}
	// Delivery applies client and request auth independently. Check both
	// collaborators even when request auth later overrides the header.
	for _, auth := range []AuthMethod{plan.clientAuth, plan.requestAuth} {
		if auth == nil {
			continue
		}
		_, recognized, err := builtInAuthShape(auth)
		if err != nil {
			return fmt.Errorf("%w: built-in auth", ErrInvalidConfigValue)
		}
		if !recognized {
			return ErrPreparationNotPreparable
		}
	}
	return nil
}

func projectRequestPreparation(ctx context.Context, plan *requestPlan, budget int64) (*RequestPreparation, error) {
	if err := preparationCheckpoint(ctx); err != nil {
		return nil, err
	}
	target, err := projectPreparedTarget(ctx, plan)
	if err != nil {
		return nil, err
	}
	query, err := projectPreparedQuery(ctx, plan.query)
	if err != nil {
		return nil, err
	}
	headers, headerMap, err := projectPreparedHeaders(ctx, plan)
	if err != nil {
		return nil, err
	}
	orderedHeaders, err := projectPreparedOrderedHeaders(ctx, plan.orderedHeaders, headerMap)
	if err != nil {
		return nil, err
	}
	cookies, err := projectPreparedCookies(ctx, plan.cookies)
	if err != nil {
		return nil, err
	}
	body, err := projectPreparedBody(ctx, plan.body, budget)
	if err != nil {
		return nil, err
	}
	if err := preparationCheckpoint(ctx); err != nil {
		return nil, err
	}
	return &RequestPreparation{
		method:         plan.method,
		target:         target,
		query:          query,
		headers:        headers,
		orderedHeaders: orderedHeaders,
		cookies:        cookies,
		body:           body,
	}, nil
}

func projectPreparedTarget(ctx context.Context, plan *requestPlan) (PreparedTarget, error) {
	target := PreparedTarget{}
	if plan == nil || plan.targetURL == nil {
		return target, nil
	}
	target.scheme = plan.targetURL.Scheme
	target.authority = plan.targetURL.Host
	pathPublic := plan.pathTrace.path.isPublic() && !plan.pathTrace.basePathPrivate
	for i, param := range plan.pathTrace.params {
		if i%64 == 0 {
			if err := preparationCheckpoint(ctx); err != nil {
				return PreparedTarget{}, err
			}
		}
		if param.pathOccurrences > 0 && !param.value.isPublic() {
			pathPublic = false
			break
		}
	}
	if pathPublic {
		target.path = preparedPresentString(plan.targetURL.EscapedPath())
	} else {
		target.path = preparedRedactedString()
	}
	return target, nil
}

func projectPreparedQuery(ctx context.Context, values []requestOccurrence) ([]PreparedOccurrence, error) {
	grouped := make(map[string][]requestOccurrence)
	for i, occurrence := range values {
		if i%64 == 0 {
			if err := preparationCheckpoint(ctx); err != nil {
				return nil, err
			}
		}
		grouped[occurrence.name] = append(grouped[occurrence.name], occurrence)
	}
	if err := preparationCheckpoint(ctx); err != nil {
		return nil, err
	}
	names := slices.Sorted(maps.Keys(grouped))
	if err := preparationCheckpoint(ctx); err != nil {
		return nil, err
	}
	result := make([]PreparedOccurrence, 0, len(names))
	for i, name := range names {
		if i%64 == 0 {
			if err := preparationCheckpoint(ctx); err != nil {
				return nil, err
			}
		}
		occurrences := grouped[name]
		prepared := make([]PreparedString, len(occurrences))
		for j, occurrence := range occurrences {
			if j%64 == 0 {
				if err := preparationCheckpoint(ctx); err != nil {
					return nil, err
				}
			}
			prepared[j] = preparedStringFromValue(occurrence.value)
		}
		result = append(result, PreparedOccurrence{name: name, values: prepared})
	}
	return result, nil
}

type preparedHeaderGroup struct {
	name        string
	occurrences []requestOccurrence
}

func projectPreparedHeaders(ctx context.Context, plan *requestPlan) ([]PreparedHeader, map[string]PreparedHeader, error) {
	groups := make(map[string]*preparedHeaderGroup)
	for i, occurrence := range plan.headers {
		if i%64 == 0 {
			if err := preparationCheckpoint(ctx); err != nil {
				return nil, nil, err
			}
		}
		key := strings.ToLower(occurrence.name)
		group := groups[key]
		if group == nil {
			group = &preparedHeaderGroup{name: occurrence.name}
			groups[key] = group
		}
		group.occurrences = append(group.occurrences, occurrence)
	}
	if plan.auth != nil {
		key := strings.ToLower("Authorization")
		if groups[key] == nil {
			groups[key] = &preparedHeaderGroup{name: "Authorization"}
		}
	}
	if err := preparationCheckpoint(ctx); err != nil {
		return nil, nil, err
	}
	keys := slices.Sorted(maps.Keys(groups))
	if err := preparationCheckpoint(ctx); err != nil {
		return nil, nil, err
	}
	result := make([]PreparedHeader, 0, len(keys))
	lookup := make(map[string]PreparedHeader, len(keys))
	for i, key := range keys {
		if i%64 == 0 {
			if err := preparationCheckpoint(ctx); err != nil {
				return nil, nil, err
			}
		}
		group := groups[key]
		values, err := projectPreparedHeaderValues(ctx, group.name, group.occurrences, preparedBodyMediaType(plan.body))
		if err != nil {
			return nil, nil, err
		}
		entry := PreparedHeader{name: group.name, values: values}
		result = append(result, entry)
		lookup[key] = entry
	}
	return result, lookup, nil
}

func projectPreparedHeaderValues(
	ctx context.Context,
	name string,
	occurrences []requestOccurrence,
	bodyMediaType string,
) ([]PreparedString, error) {
	if len(occurrences) == 0 {
		return []PreparedString{preparedRedactedString()}, nil
	}
	values := make([]PreparedString, len(occurrences))
	for i, occurrence := range occurrences {
		if i%64 == 0 {
			if err := preparationCheckpoint(ctx); err != nil {
				return nil, err
			}
		}
		if isCredentialRequestHeader(name) || strings.EqualFold(name, "Cookie") {
			values[i] = preparedRedactedString()
			continue
		}
		if strings.EqualFold(name, "Content-Type") {
			if i == 0 && bodyMediaType != "" {
				values[i] = preparedPresentString(bodyMediaType)
				continue
			}
			values[i] = preparedRedactedString()
			continue
		}
		values[i] = preparedStringFromValue(occurrence.value)
	}
	return values, nil
}

func projectPreparedOrderedHeaders(
	ctx context.Context,
	ordered *orderedobject.Object[[]string],
	semantic map[string]PreparedHeader,
) ([]PreparedHeader, error) {
	if ordered == nil {
		return nil, nil
	}
	if err := preparationCheckpoint(ctx); err != nil {
		return nil, err
	}
	entries := ordered.Entries()
	if err := preparationCheckpoint(ctx); err != nil {
		return nil, err
	}
	result := make([]PreparedHeader, 0, len(entries))
	for i, entry := range entries {
		if i%64 == 0 {
			if err := preparationCheckpoint(ctx); err != nil {
				return nil, err
			}
		}
		values := make([]PreparedString, len(entry.Value))
		header, hasSemantic := semantic[strings.ToLower(entry.Key)]
		for j := range values {
			if j%64 == 0 {
				if err := preparationCheckpoint(ctx); err != nil {
					return nil, err
				}
			}
			if hasSemantic && j < len(header.values) {
				values[j] = header.values[j].clone()
			} else {
				values[j] = preparedRedactedString()
			}
		}
		result = append(result, PreparedHeader{name: entry.Key, values: values})
	}
	return result, nil
}

func preparedStringFromValue(value Value) PreparedString {
	if !value.isPublic() {
		return preparedRedactedString()
	}
	return preparedPresentString(value.rawValue())
}

func projectPreparedCookies(ctx context.Context, values []requestCookieOccurrence) ([]PreparedCookie, error) {
	result := make([]PreparedCookie, 0, len(values))
	for i, occurrence := range values {
		if i%64 == 0 {
			if err := preparationCheckpoint(ctx); err != nil {
				return nil, err
			}
		}
		if !validCookieName(occurrence.name) {
			continue
		}
		result = append(result, PreparedCookie{
			name:  occurrence.name,
			value: preparedStringFromValue(occurrence.value),
		})
	}
	return result, nil
}

func projectPreparedBody(ctx context.Context, body requestBodyPlan, budget int64) (PreparedBody, error) { //nolint:gocritic // Projection reads the detached plan without mutation.
	result := PreparedBody{
		kind:      previewBodyKind(body.kind),
		mediaType: preparedBodyMediaType(body),
		data:      preparedRedactedBytes(),
	}
	switch body.kind {
	case requestBodyNone, requestBodyJSON, requestBodyXML, requestBodyYAML, requestBodyReader:
		return result, nil
	case requestBodyText:
		if !body.valuePublic {
			return result, nil
		}
		value, ok := body.value.(string)
		if !ok {
			return PreparedBody{}, fmt.Errorf("%w: request body", ErrInvalidConfigValue)
		}
		data, err := retainPreparedBytes(ctx, []byte(value), budget)
		if err != nil {
			return PreparedBody{}, err
		}
		result.data = data
	case requestBodyBytes:
		if !body.valuePublic {
			return result, nil
		}
		value, ok := body.value.([]byte)
		if !ok {
			return PreparedBody{}, fmt.Errorf("%w: request body", ErrInvalidConfigValue)
		}
		data, err := retainPreparedBytes(ctx, value, budget)
		if err != nil {
			return PreparedBody{}, err
		}
		result.data = data
	case requestBodyForm:
		allPublic, err := preparedFormAllPublic(ctx, body.formOccurrences)
		if err != nil {
			return PreparedBody{}, err
		}
		if !allPublic {
			return result, nil
		}
		if err := preparationCheckpoint(ctx); err != nil {
			return PreparedBody{}, err
		}
		values := make(url.Values, len(body.formOccurrences))
		for i, occurrence := range body.formOccurrences {
			if i%64 == 0 {
				if err := preparationCheckpoint(ctx); err != nil {
					return PreparedBody{}, err
				}
			}
			values.Add(occurrence.name, occurrence.value.rawValue())
		}
		data, err := retainPreparedBytes(ctx, []byte(values.Encode()), budget)
		if err != nil {
			return PreparedBody{}, err
		}
		result.data = data
	case requestBodyMultipart:
		manifest, err := prepareMultipartManifest(ctx, body.multipart)
		if err != nil {
			return PreparedBody{}, err
		}
		result.multipart = manifest
	}
	return result, nil
}

func preparedFormAllPublic(ctx context.Context, occurrences []requestBodyFormOccurrence) (bool, error) {
	if len(occurrences) == 0 {
		return false, nil
	}
	for i, occurrence := range occurrences {
		if i%64 == 0 {
			if err := preparationCheckpoint(ctx); err != nil {
				return false, err
			}
		}
		if !occurrence.value.isPublic() {
			return false, nil
		}
	}
	return true, nil
}

func retainPreparedBytes(ctx context.Context, data []byte, budget int64) (PreparedBytes, error) {
	if err := preparationCheckpoint(ctx); err != nil {
		return PreparedBytes{}, err
	}
	if int64(len(data)) > budget {
		return PreparedBytes{}, ErrPreparationBodyTooLarge
	}
	prepared := preparedPresentBytes(data)
	if err := preparationCheckpoint(ctx); err != nil {
		return PreparedBytes{}, err
	}
	return prepared, nil
}

func preparedBodyMediaType(body requestBodyPlan) string { //nolint:gocritic // Media type projection reads a detached plan snapshot.
	if !body.generatedContentType {
		return ""
	}
	switch body.kind {
	case requestBodyJSON:
		return "application/json"
	case requestBodyXML:
		return "application/xml"
	case requestBodyYAML:
		return "application/yaml"
	case requestBodyText:
		return "text/plain"
	case requestBodyForm:
		return "application/x-www-form-urlencoded"
	case requestBodyMultipart:
		return "multipart/form-data"
	default:
		return ""
	}
}

func prepareMultipartManifest(ctx context.Context, multipart *Multipart) (*PreparedMultipart, error) {
	if multipart == nil {
		return nil, fmt.Errorf("%w: multipart body", ErrInvalidConfigValue)
	}
	if err := preparationCheckpoint(ctx); err != nil {
		return nil, err
	}
	if multipart.canReplay && multipart.replayMaxBytes < 0 {
		return nil, fmt.Errorf("%w: multipart replay limit", ErrInvalidConfigValue)
	}
	if multipart.boundary != "" && !validMultipartBoundary(multipart.boundary) {
		return nil, fmt.Errorf("%w: multipart boundary", ErrInvalidConfigValue)
	}
	keys := slices.Sorted(maps.Keys(multipart.fields))
	if err := preparationCheckpoint(ctx); err != nil {
		return nil, err
	}
	manifest := &PreparedMultipart{fields: make([]PreparedOccurrence, 0, len(keys))}
	for i, key := range keys {
		if err := preparationCheckpoint(ctx); err != nil {
			return nil, err
		}
		values := make([]PreparedString, len(multipart.fields[key]))
		for j := range values {
			if j%64 == 0 {
				if err := preparationCheckpoint(ctx); err != nil {
					return nil, err
				}
			}
			values[j] = preparedRedactedString()
		}
		manifest.fields = append(manifest.fields, PreparedOccurrence{name: key, values: values})
		if i%64 == 63 {
			if err := preparationCheckpoint(ctx); err != nil {
				return nil, err
			}
		}
	}
	manifest.files = make([]PreparedMultipartFile, len(multipart.parts))
	for i, part := range multipart.parts {
		if err := preparationCheckpoint(ctx); err != nil {
			return nil, err
		}
		if part.Field == "" || isNilInterface(part.Body) {
			return nil, fmt.Errorf("%w: multipart structure", ErrInvalidConfigValue)
		}
		if err := validateMultipartFileMetadata(part); err != nil {
			return nil, err
		}
		manifest.files[i] = PreparedMultipartFile{field: part.Field}
	}
	if err := preparationCheckpoint(ctx); err != nil {
		return nil, err
	}
	return manifest, nil
}
