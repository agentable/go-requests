package requests

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	mimeMultipart "mime/multipart"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
)

var errMultipartBodyStopped = errors.New("multipart body stopped")

// Multipart builds a multipart/form-data request body.
type Multipart struct {
	fields         url.Values
	parts          []FilePart
	boundary       string
	canReplay      bool
	replayMaxBytes int64
}

// FilePart describes one multipart file part.
type FilePart struct {
	Field       string
	Filename    string
	ContentType string
	Body        io.Reader // Body is borrowed and is never closed by requests.
}

// NewMultipart creates an empty multipart builder.
func NewMultipart() *Multipart {
	return &Multipart{
		fields: url.Values{},
	}
}

// Field adds a form field.
func (m *Multipart) Field(name, value string) *Multipart {
	if m.fields == nil {
		m.fields = url.Values{}
	}
	m.fields.Add(name, value)
	return m
}

// File adds a file part.
func (m *Multipart) File(field, filename string, r io.Reader) *Multipart {
	return m.Part(FilePart{
		Field:    field,
		Filename: filename,
		Body:     r,
	})
}

// Part adds a file part with explicit metadata.
func (m *Multipart) Part(part FilePart) *Multipart {
	m.parts = append(m.parts, part)
	return m
}

// FileBytes adds a file part backed by bytes.
func (m *Multipart) FileBytes(field, filename string, data []byte) *Multipart {
	return m.File(field, filename, bytes.NewReader(bytes.Clone(data)))
}

// FileString adds a file part backed by a string.
func (m *Multipart) FileString(field, filename, data string) *Multipart {
	return m.File(field, filename, strings.NewReader(data))
}

// Boundary sets the multipart boundary.
func (m *Multipart) Boundary(boundary string) *Multipart {
	m.boundary = boundary
	return m
}

// Replayable buffers the multipart body up to maxBytes for retries and body-preserving redirects.
func (m *Multipart) Replayable(maxBytes int64) *Multipart {
	m.canReplay = true
	m.replayMaxBytes = maxBytes
	return m
}

func validateMultipartFileMetadata(part FilePart) error {
	if value := mime.FormatMediaType("form-data", map[string]string{
		"name":     part.Field,
		"filename": part.Filename,
	}); value == "" {
		return fmt.Errorf("%w: multipart filename", ErrInvalidConfigValue)
	}
	if part.ContentType != "" {
		if _, _, err := mime.ParseMediaType(part.ContentType); err != nil {
			return fmt.Errorf("%w: multipart content type", ErrInvalidConfigValue)
		}
	}
	return nil
}

func validMultipartBoundary(boundary string) bool {
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

func (m *Multipart) reader() (io.Reader, string, *multipartSourceLifecycle, error) {
	if m.canReplay {
		reader, contentType, err := m.bufferedReader()
		return reader, contentType, nil, err
	}
	return m.streamingReader()
}

func (m *Multipart) streamingReader() (io.Reader, string, *multipartSourceLifecycle, error) {
	reader, writer := io.Pipe()
	multipartWriter := mimeMultipart.NewWriter(writer)
	if err := m.setBoundary(multipartWriter); err != nil {
		_ = writer.CloseWithError(err)
		return nil, "", nil, err
	}
	lifecycle := &multipartSourceLifecycle{reader: reader, done: make(chan struct{})}

	go func() {
		defer close(lifecycle.done)
		err := m.writeTo(multipartWriter, lifecycle)
		if closeErr := multipartWriter.Close(); err == nil {
			err = closeErr
		}
		lifecycle.recordProducerError(err)
		_ = writer.CloseWithError(err)
	}()

	return &multipartStreamingBody{reader: reader, lifecycle: lifecycle}, multipartWriter.FormDataContentType(), lifecycle, nil
}

func (m *Multipart) bufferedReader() (io.Reader, string, error) {
	if m.replayMaxBytes < 0 {
		return nil, "", fmt.Errorf("%w: multipart replay maxBytes", ErrInvalidConfigValue)
	}

	var buf bytes.Buffer
	limited := &limitWriter{writer: &buf, max: m.replayMaxBytes}
	multipartWriter := mimeMultipart.NewWriter(limited)
	if err := m.setBoundary(multipartWriter); err != nil {
		return nil, "", err
	}
	if err := m.writeTo(multipartWriter, nil); err != nil {
		return nil, "", err
	}
	if err := multipartWriter.Close(); err != nil {
		return nil, "", err
	}
	return bytes.NewReader(buf.Bytes()), multipartWriter.FormDataContentType(), nil
}

func (m *Multipart) setBoundary(writer *mimeMultipart.Writer) error {
	if m.boundary == "" {
		return nil
	}
	if err := writer.SetBoundary(m.boundary); err != nil {
		return fmt.Errorf("setting custom boundary failed: %w", err)
	}
	return nil
}

func (m *Multipart) writeTo(writer *mimeMultipart.Writer, lifecycle *multipartSourceLifecycle) error {
	for key, values := range m.fields {
		for _, value := range values {
			if err := writer.WriteField(key, value); err != nil {
				return fmt.Errorf("writing form field failed: %w", err)
			}
		}
	}

	for _, part := range m.parts {
		if err := writeFilePart(writer, part, lifecycle); err != nil {
			return err
		}
	}

	return nil
}

func writeFilePart(writer *mimeMultipart.Writer, part FilePart, lifecycle *multipartSourceLifecycle) error {
	if part.Field == "" {
		return fmt.Errorf("%w: multipart field", ErrInvalidConfigValue)
	}
	if isNilInterface(part.Body) {
		return fmt.Errorf("%w: multipart body", ErrInvalidConfigValue)
	}
	if err := validateMultipartFileMetadata(part); err != nil {
		return err
	}

	writerPart, err := createFilePart(writer, part)
	if err != nil {
		return err
	}
	source := part.Body
	if lifecycle != nil {
		source = multipartSourceReader{source: part.Body, lifecycle: lifecycle}
	}
	if _, err := io.Copy(writerPart, source); err != nil {
		return fmt.Errorf("copying file content failed: %w", err)
	}
	return nil
}

type multipartStreamingBody struct {
	reader    *io.PipeReader
	lifecycle *multipartSourceLifecycle
}

func (b *multipartStreamingBody) Read(p []byte) (int, error) {
	return b.reader.Read(p)
}

func (b *multipartStreamingBody) Close() error {
	b.lifecycle.stop()
	return nil
}

type multipartSourceLifecycle struct {
	reader          *io.PipeReader
	done            chan struct{}
	mu              sync.Mutex
	stopped         bool
	sourceErr       error
	producerErr     error
	stopOnce        sync.Once
	contextStop     func() bool
	contextStopDone chan struct{}
	contextStopOnce sync.Once
}

func (l *multipartSourceLifecycle) read(source io.Reader, p []byte) (int, error) {
	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		return 0, io.EOF
	}
	l.mu.Unlock()

	n, err := source.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		l.mu.Lock()
		l.sourceErr = errors.Join(l.sourceErr, err)
		l.mu.Unlock()
	}
	return n, err
}

func (l *multipartSourceLifecycle) stop() {
	l.mu.Lock()
	l.stopped = true
	l.mu.Unlock()
	l.stopOnce.Do(func() {
		_ = l.reader.CloseWithError(errMultipartBodyStopped)
	})
}

func (l *multipartSourceLifecycle) recordProducerError(err error) {
	if err == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if errors.Is(err, errMultipartBodyStopped) || errors.Is(err, l.sourceErr) {
		return
	}
	l.producerErr = errors.Join(l.producerErr, err)
}

func (l *multipartSourceLifecycle) watchContext(ctx context.Context) {
	done := make(chan struct{})
	l.contextStopDone = done
	l.contextStop = context.AfterFunc(ctx, func() {
		defer close(done)
		l.stop()
	})
}

func (l *multipartSourceLifecycle) stopContextCallback() {
	l.contextStopOnce.Do(func() {
		if l.contextStop != nil && !l.contextStop() {
			<-l.contextStopDone
		}
	})
}

func (l *multipartSourceLifecycle) wait() error {
	l.stop()
	l.stopContextCallback()
	<-l.done
	l.mu.Lock()
	defer l.mu.Unlock()
	return errors.Join(l.sourceErr, l.producerErr)
}

type multipartSourceReader struct {
	source    io.Reader
	lifecycle *multipartSourceLifecycle
}

func (r multipartSourceReader) Read(p []byte) (int, error) {
	return r.lifecycle.read(r.source, p)
}

func createFilePart(writer *mimeMultipart.Writer, part FilePart) (io.Writer, error) {
	if part.ContentType == "" {
		writerPart, err := writer.CreateFormFile(part.Field, part.Filename)
		if err != nil {
			return nil, fmt.Errorf("creating form file failed: %w", err)
		}
		return writerPart, nil
	}

	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{
		"name":     part.Field,
		"filename": part.Filename,
	}))
	header.Set("Content-Type", part.ContentType)

	writerPart, err := writer.CreatePart(header)
	if err != nil {
		return nil, fmt.Errorf("creating form file failed: %w", err)
	}
	return writerPart, nil
}

type limitWriter struct {
	writer io.Writer
	max    int64
	wrote  int64
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if w.max >= 0 && w.wrote+int64(len(p)) > w.max {
		return 0, fmt.Errorf("%w: multipart replay body exceeds %d bytes", ErrInvalidConfigValue, w.max)
	}
	n, err := w.writer.Write(p)
	w.wrote += int64(n)
	return n, err
}
