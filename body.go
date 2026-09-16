package requests

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type requestBodyKind uint8

const (
	requestBodyNone requestBodyKind = iota
	requestBodyJSON
	requestBodyXML
	requestBodyYAML
	requestBodyText
	requestBodyBytes
	requestBodyReader
	requestBodyForm
	requestBodyMultipart
)

// requestBodyPlan is the single selected body source. The wire-oriented
// fields remain separate from disclosure facts so owner capabilities cannot
// alter delivery encoding or replay behavior.
type requestBodyPlan struct {
	kind                 requestBodyKind
	value                any
	valuePublic          bool
	form                 url.Values
	formOccurrences      []requestBodyFormOccurrence
	multipart            *Multipart
	contentType          string
	generatedContentType bool
}

type requestBodyFormOccurrence struct {
	name  string
	value Value
}

func (p requestBodyPlan) formAllPublic() bool { //nolint:gocritic // Plans are value-like snapshots at the disclosure boundary.
	if p.kind != requestBodyForm || len(p.formOccurrences) == 0 {
		return false
	}
	for _, occurrence := range p.formOccurrences {
		if !occurrence.value.isPublic() {
			return false
		}
	}
	return true
}

type preparedRequestBody struct {
	body          io.Reader
	getBody       func() (io.ReadCloser, error)
	contentLength int64
	contentType   string
}

// Form sets URL-encoded form fields from a struct, map, or url.Values.
// The resulting body is buffered and is safe to replay for retries.
func (b *RequestBuilder) Form(v any) *RequestBuilder {
	formFields, err := parseFormFields(v)

	if err != nil {
		b.setPreparationError(err, preparationErrorClassUnsupportedFormFieldsType)
		if b.client.logger != nil {
			b.client.logger.Errorf("Error parsing form: %v", err)
		}
		return b
	}

	formFields = formFields.Clone()
	if formFields == nil {
		formFields = url.Values{}
	}
	plan := requestBodyPlan{
		kind:                 requestBodyForm,
		form:                 formFields,
		contentType:          "application/x-www-form-urlencoded",
		generatedContentType: true,
	}
	for key, values := range formFields {
		for _, value := range values {
			plan.formOccurrences = append(plan.formOccurrences, requestBodyFormOccurrence{
				name:  key,
				value: privateRequestValue(value),
			})
		}
	}
	b.selectBody(plan)

	return b
}

// FormFields sets multiple form fields at once.
// The resulting body is buffered and is safe to replay for retries.
func (b *RequestBuilder) FormFields(fields any) *RequestBuilder {
	values, err := parseFormFields(fields)
	if err != nil {
		b.setPreparationError(err, preparationErrorClassUnsupportedFormFieldsType)
		if b.client.logger != nil {
			b.client.logger.Errorf("Error parsing form fields: %v", err)
		}
		return b
	}
	formFields := b.activateForm()

	for key, value := range values {
		for _, v := range value {
			b.addFormField(formFields, key, privateRequestValue(v))
		}
	}
	return b
}

// FormField adds or updates a form field.
// Without files, the resulting form body is buffered and safe to replay for retries.
func (b *RequestBuilder) FormField(key, val string) *RequestBuilder {
	b.addFormField(b.activateForm(), key, privateRequestValue(val))
	return b
}

// FormFieldValue adds a disclosure-tagged URL-encoded form occurrence.
// Public form bytes are available to Prepare only when every actual form
// occurrence in the selected form is public.
func (b *RequestBuilder) FormFieldValue(key string, value Value) *RequestBuilder {
	b.addFormField(b.activateForm(), key, value)
	return b
}

func (b *RequestBuilder) activateForm() url.Values {
	if b.body.kind != requestBodyForm {
		b.selectBody(requestBodyPlan{
			kind:                 requestBodyForm,
			form:                 url.Values{},
			contentType:          "application/x-www-form-urlencoded",
			generatedContentType: true,
		})
	}
	return b.body.form
}

func (b *RequestBuilder) addFormField(form url.Values, key string, value Value) {
	form.Add(key, value.rawValue())
	b.body.formOccurrences = append(b.body.formOccurrences, requestBodyFormOccurrence{
		name:  key,
		value: value,
	})
}

// DelFormField removes one or more form fields.
func (b *RequestBuilder) DelFormField(key ...string) *RequestBuilder {
	if b.body.kind == requestBodyForm {
		for _, k := range key {
			b.body.form.Del(k)
		}
		if len(b.body.formOccurrences) > 0 {
			kept := b.body.formOccurrences[:0]
			for _, occurrence := range b.body.formOccurrences {
				removed := false
				for _, name := range key {
					if occurrence.name == name {
						removed = true
						break
					}
				}
				if !removed {
					kept = append(kept, occurrence)
				}
			}
			b.body.formOccurrences = kept
		}
	}
	return b
}

// Multipart sets a multipart/form-data body built by [Multipart].
//
// By default the body is streamed once via an [io.Pipe] and is not replayable;
// a retry that needs to resend the body returns [ErrRequestBodyNotReplayable].
// Call m.Replayable(maxBytes) before passing the builder if retries must
// or 307/308 redirects may resend the body.
func (b *RequestBuilder) Multipart(m *Multipart) *RequestBuilder {
	if m == nil {
		b.setPreparationError(fmt.Errorf("%w: multipart body", ErrInvalidConfigValue), preparationErrorClassInvalidConfigValue)
		return b
	}
	b.selectBody(requestBodyPlan{
		kind:                 requestBodyMultipart,
		multipart:            m,
		generatedContentType: true,
	})
	return b
}

// JSON sets the request body as JSON and Content-Type to application/json.
// The encoded body is buffered and is safe to replay for retries.
func (b *RequestBuilder) JSON(v any) *RequestBuilder {
	b.selectBody(requestBodyPlan{
		kind:                 requestBodyJSON,
		value:                v,
		contentType:          "application/json",
		generatedContentType: true,
	})
	return b
}

// XML sets the request body as XML and Content-Type to application/xml.
// The encoded body is buffered and is safe to replay for retries.
func (b *RequestBuilder) XML(v any) *RequestBuilder {
	b.selectBody(requestBodyPlan{
		kind:                 requestBodyXML,
		value:                v,
		contentType:          "application/xml",
		generatedContentType: true,
	})
	return b
}

// YAML sets the request body as YAML and Content-Type to application/yaml.
// The encoded body is buffered and is safe to replay for retries.
func (b *RequestBuilder) YAML(v any) *RequestBuilder {
	b.selectBody(requestBodyPlan{
		kind:                 requestBodyYAML,
		value:                v,
		contentType:          "application/yaml",
		generatedContentType: true,
	})
	return b
}

// Text sets the request body as plain text and Content-Type to text/plain.
// The body is buffered and is safe to replay for retries.
func (b *RequestBuilder) Text(v string) *RequestBuilder {
	b.selectBody(requestBodyPlan{
		kind:                 requestBodyText,
		value:                v,
		contentType:          "text/plain",
		generatedContentType: true,
	})
	return b
}

// TextValue selects a text body with an explicit disclosure capability.
func (b *RequestBuilder) TextValue(value Value) *RequestBuilder {
	b.selectBody(requestBodyPlan{
		kind:                 requestBodyText,
		value:                value.rawValue(),
		valuePublic:          value.isPublic(),
		contentType:          "text/plain",
		generatedContentType: true,
	})
	return b
}

// Bytes sets the request body as raw bytes without changing Content-Type.
// The body is buffered and is safe to replay for retries.
func (b *RequestBuilder) Bytes(v []byte) *RequestBuilder {
	b.selectBody(requestBodyPlan{kind: requestBodyBytes, value: v})
	return b
}

// BytesPayload selects an owned byte body with an explicit disclosure
// capability. Payload already owns a clone; clone once more at the builder
// boundary so the selected plan never aliases a caller-owned slice.
func (b *RequestBuilder) BytesPayload(payload Payload) *RequestBuilder {
	b.selectBody(requestBodyPlan{
		kind:        requestBodyBytes,
		value:       payload.cloneBytes(),
		valuePublic: payload.isPublic(),
	})
	return b
}

// Reader sets a one-shot raw request body and optional Content-Type.
// The body is not replayable unless r itself is seekable and sized.
func (b *RequestBuilder) Reader(r io.Reader, contentType string) *RequestBuilder {
	b.selectBody(requestBodyPlan{
		kind:                 requestBodyReader,
		value:                r,
		contentType:          contentType,
		generatedContentType: contentType != "",
	})
	return b
}

func (b *RequestBuilder) selectBody(body requestBodyPlan) { //nolint:gocritic // Selection is a value-like replacement of builder body state.
	if b.body.generatedContentType {
		b.delHeader("Content-Type")
	}
	b.body = body
	if body.contentType != "" {
		b.setHeader("Content-Type", body.contentType)
	}
}

func (b *RequestBuilder) prepareBody(snap *clientSnapshot) (preparedRequestBody, error) {
	headers := b.compatibilityHeaders()
	if headers == nil {
		headers = &http.Header{}
	}
	return prepareBodyFromPlan(b.body, *headers, snap)
}

func prepareBodyFromPlan(body requestBodyPlan, headers http.Header, snap *clientSnapshot) (preparedRequestBody, error) { //nolint:gocritic // The plan is a detached, value-like delivery snapshot.
	contentType := headers.Get("Content-Type")
	switch body.kind {
	case requestBodyNone:
		return preparedRequestBody{}, nil
	case requestBodyJSON:
		return prepareEncodedBodyValue(body.value, contentType, snap.jsonEncoder.Encode)
	case requestBodyXML:
		return prepareEncodedBodyValue(body.value, contentType, snap.xmlEncoder.Encode)
	case requestBodyYAML:
		return prepareEncodedBodyValue(body.value, contentType, snap.yamlEncoder.Encode)
	case requestBodyText:
		return replayableRequestBody([]byte(body.value.(string)), contentType), nil
	case requestBodyBytes:
		return replayableRequestBody(body.value.([]byte), contentType), nil
	case requestBodyReader:
		reader, err := encodeRawBody(body.value)
		if err != nil {
			return preparedRequestBody{}, err
		}
		return prepareReaderBody(reader, contentType)
	case requestBodyForm:
		return replayableRequestBody([]byte(body.form.Encode()), contentType), nil
	case requestBodyMultipart:
		reader, generatedContentType, err := body.multipart.reader()
		if err != nil {
			return preparedRequestBody{}, err
		}
		if !body.generatedContentType {
			generatedContentType = ""
		}
		if !body.multipart.canReplay {
			return preparedRequestBody{body: reader, contentType: generatedContentType}, nil
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return preparedRequestBody{}, fmt.Errorf("read replayable multipart body: %w", err)
		}
		return replayableRequestBody(data, generatedContentType), nil
	default:
		return preparedRequestBody{}, fmt.Errorf("%w: unknown body selection", ErrInvalidConfigValue)
	}
}

func prepareEncodedBodyValue(
	value any,
	contentType string,
	encode func(any) (io.Reader, error),
) (preparedRequestBody, error) {
	if contentType == "" {
		return preparedRequestBody{}, fmt.Errorf("%w: missing Content-Type", ErrUnsupportedContentType)
	}
	body, err := encode(value)
	if err != nil {
		return preparedRequestBody{}, err
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return preparedRequestBody{}, fmt.Errorf("read encoded request body: %w", err)
	}
	return replayableRequestBody(data, contentType), nil
}

func prepareReaderBody(body io.Reader, contentType string) (preparedRequestBody, error) {
	prepared := preparedRequestBody{body: body, contentType: contentType}
	data, ok, err := snapshotReaderBody(body)
	if err != nil {
		return preparedRequestBody{}, err
	}
	if !ok {
		return prepared, nil
	}

	prepared.getBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	prepared.contentLength = int64(len(data))
	return prepared, nil
}

func replayableRequestBody(data []byte, contentType string) preparedRequestBody {
	data = bytes.Clone(data)
	return preparedRequestBody{
		body:          bytes.NewReader(data),
		contentLength: int64(len(data)),
		contentType:   contentType,
		getBody: func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(data)), nil
		},
	}
}

type sizedReadSeekerAt interface {
	ReadAt([]byte, int64) (int, error)
	Seek(int64, int) (int64, error)
	Size() int64
}

func snapshotReaderBody(body io.Reader) ([]byte, bool, error) {
	switch reader := body.(type) {
	case *bytes.Buffer:
		return bytes.Clone(reader.Bytes()), true, nil
	case sizedReadSeekerAt:
		data, err := readSizedReaderAt(reader)
		return data, true, err
	default:
		return nil, false, nil
	}
}

func readSizedReaderAt(reader sizedReadSeekerAt) ([]byte, error) {
	offset, err := reader.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, fmt.Errorf("read replayable request body position: %w", err)
	}
	size := reader.Size()
	if offset < 0 || offset > size {
		return nil, fmt.Errorf("%w: request body offset %d outside size %d", ErrRequestBodyReadIncomplete, offset, size)
	}
	data := make([]byte, size-offset)
	n, err := reader.ReadAt(data, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read replayable request body: %w", err)
	}
	if n != len(data) {
		return nil, fmt.Errorf("%w: read %d bytes, want %d", ErrRequestBodyReadIncomplete, n, len(data))
	}
	return data, nil
}

func encodeRawBody(value any) (io.Reader, error) {
	switch data := value.(type) {
	case string:
		return strings.NewReader(data), nil
	case []byte:
		return bytes.NewReader(data), nil
	case io.Reader:
		return data, nil
	default:
		return nil, fmt.Errorf("%w: expected string, []byte, or io.Reader", ErrUnsupportedContentType)
	}
}
