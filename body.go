package requests

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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

type requestBodySelection struct {
	kind                 requestBodyKind
	value                any
	form                 url.Values
	multipart            *Multipart
	contentType          string
	generatedContentType bool
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
		b.setPreparationError(err)
		if b.client.logger != nil {
			b.client.logger.Errorf("Error parsing form: %v", err)
		}
		return b
	}

	if formFields == nil {
		formFields = url.Values{}
	}
	b.selectBody(requestBodySelection{
		kind:                 requestBodyForm,
		form:                 formFields,
		contentType:          "application/x-www-form-urlencoded",
		generatedContentType: true,
	})

	return b
}

// FormFields sets multiple form fields at once.
// The resulting body is buffered and is safe to replay for retries.
func (b *RequestBuilder) FormFields(fields any) *RequestBuilder {
	values, err := parseFormFields(fields)
	if err != nil {
		b.setPreparationError(err)
		if b.client.logger != nil {
			b.client.logger.Errorf("Error parsing form fields: %v", err)
		}
		return b
	}
	formFields := b.activateForm()

	for key, value := range values {
		for _, v := range value {
			formFields.Add(key, v)
		}
	}
	return b
}

// FormField adds or updates a form field.
// Without files, the resulting form body is buffered and safe to replay for retries.
func (b *RequestBuilder) FormField(key, val string) *RequestBuilder {
	b.activateForm().Add(key, val)
	return b
}

func (b *RequestBuilder) activateForm() url.Values {
	if b.body.kind != requestBodyForm {
		b.selectBody(requestBodySelection{
			kind:                 requestBodyForm,
			form:                 url.Values{},
			contentType:          "application/x-www-form-urlencoded",
			generatedContentType: true,
		})
	}
	return b.body.form
}

// DelFormField removes one or more form fields.
func (b *RequestBuilder) DelFormField(key ...string) *RequestBuilder {
	if b.body.kind == requestBodyForm {
		for _, k := range key {
			b.body.form.Del(k)
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
		b.setPreparationError(fmt.Errorf("%w: multipart body", ErrInvalidConfigValue))
		return b
	}
	b.selectBody(requestBodySelection{
		kind:                 requestBodyMultipart,
		multipart:            m,
		generatedContentType: true,
	})
	return b
}

// JSON sets the request body as JSON and Content-Type to application/json.
// The encoded body is buffered and is safe to replay for retries.
func (b *RequestBuilder) JSON(v any) *RequestBuilder {
	b.selectBody(requestBodySelection{
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
	b.selectBody(requestBodySelection{
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
	b.selectBody(requestBodySelection{
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
	b.selectBody(requestBodySelection{
		kind:                 requestBodyText,
		value:                v,
		contentType:          "text/plain",
		generatedContentType: true,
	})
	return b
}

// Bytes sets the request body as raw bytes without changing Content-Type.
// The body is buffered and is safe to replay for retries.
func (b *RequestBuilder) Bytes(v []byte) *RequestBuilder {
	b.selectBody(requestBodySelection{kind: requestBodyBytes, value: v})
	return b
}

// Reader sets a one-shot raw request body and optional Content-Type.
// The body is not replayable unless r itself is seekable and sized.
func (b *RequestBuilder) Reader(r io.Reader, contentType string) *RequestBuilder {
	b.selectBody(requestBodySelection{
		kind:                 requestBodyReader,
		value:                r,
		contentType:          contentType,
		generatedContentType: contentType != "",
	})
	return b
}

func (b *RequestBuilder) selectBody(body requestBodySelection) {
	if b.body.generatedContentType {
		b.delHeader("Content-Type")
	}
	b.body = body
	if body.contentType != "" {
		b.setHeader("Content-Type", body.contentType)
	}
}

func (b *RequestBuilder) prepareBody(snap *clientSnapshot) (preparedRequestBody, error) {
	contentType := b.headers.Get("Content-Type")
	switch b.body.kind {
	case requestBodyNone:
		return preparedRequestBody{}, nil
	case requestBodyJSON:
		return b.prepareEncodedBody(contentType, snap.jsonEncoder.Encode)
	case requestBodyXML:
		return b.prepareEncodedBody(contentType, snap.xmlEncoder.Encode)
	case requestBodyYAML:
		return b.prepareEncodedBody(contentType, snap.yamlEncoder.Encode)
	case requestBodyText:
		return replayableRequestBody([]byte(b.body.value.(string)), contentType), nil
	case requestBodyBytes:
		return replayableRequestBody(b.body.value.([]byte), contentType), nil
	case requestBodyReader:
		body, err := encodeRawBody(b.body.value)
		if err != nil {
			return preparedRequestBody{}, err
		}
		return prepareReaderBody(body, contentType)
	case requestBodyForm:
		return replayableRequestBody([]byte(b.body.form.Encode()), contentType), nil
	case requestBodyMultipart:
		body, generatedContentType, err := b.body.multipart.reader()
		if err != nil {
			return preparedRequestBody{}, err
		}
		if !b.body.generatedContentType {
			generatedContentType = ""
		}
		if !b.body.multipart.canReplay {
			return preparedRequestBody{body: body, contentType: generatedContentType}, nil
		}
		data, err := io.ReadAll(body)
		if err != nil {
			return preparedRequestBody{}, fmt.Errorf("read replayable multipart body: %w", err)
		}
		return replayableRequestBody(data, generatedContentType), nil
	default:
		return preparedRequestBody{}, fmt.Errorf("%w: unknown body selection", ErrInvalidConfigValue)
	}
}

func (b *RequestBuilder) prepareEncodedBody(
	contentType string,
	encode func(any) (io.Reader, error),
) (preparedRequestBody, error) {
	if contentType == "" {
		return preparedRequestBody{}, fmt.Errorf("%w: missing Content-Type", ErrUnsupportedContentType)
	}
	body, err := encode(b.body.value)
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
