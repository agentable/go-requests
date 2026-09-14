package requests

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/test-go/testify/require"
)

func TestMultipartBuilder(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := r.ParseMultipartForm(10 << 20)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		fileHeader := r.MultipartForm.File["avatar"][0]
		assert.Equal(t, "avatar.txt", fileHeader.Filename)
		assert.Equal(t, "text/plain", fileHeader.Header.Get("Content-Type"))
		if diff := cmp.Diff([]string{"alice"}, r.MultipartForm.Value["user"]); diff != "" {
			t.Errorf("multipart form field mismatch (-want +got):\n%s", diff)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	body := NewMultipart().
		Field("user", "alice").
		Part(FilePart{
			Field:       "avatar",
			Filename:    "avatar.txt",
			ContentType: "text/plain",
			Body:        strings.NewReader("hello"),
		})

	client := newTestClient(t, WithBaseURL(server.URL))
	resp, err := client.Post("/").Multipart(body).Send(t.Context())
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode())
}

func TestRequestPreviewMultipartManifestIsDetachedAndNonConsuming(t *testing.T) {
	source := &previewMultipartReader{}
	body := NewMultipart().
		Field("z", "last").
		Field("a", "first").
		Field("a", "second").
		Boundary("requests-boundary").
		Part(FilePart{
			Field:       "upload",
			Filename:    "payload.txt",
			ContentType: "text/plain",
			Body:        source,
		}).
		Replayable(1024)

	preview, err := newTestClient(t).Post("https://example.com").Multipart(body).Preview(t.Context())

	require.NoError(t, err)
	assert.Equal(t, PreviewBodyMultipart, preview.Body().Kind())
	assert.Equal(t, "multipart/form-data", preview.Body().MediaType())
	assert.Equal(t, PreviewValuePresent, preview.Body().Presence())
	assert.True(t, preview.Body().Replayable())
	assert.True(t, preview.Body().ReplayabilityKnown())
	manifest := preview.Body().Multipart()
	require.NotNil(t, manifest)
	assert.Equal(t, PreviewValuePresent, manifest.Boundary().State())
	assert.Equal(t, "requests-boundary", manifest.Boundary().Value())

	fields := manifest.Fields()
	require.Len(t, fields, 2)
	assert.Equal(t, "a", fields[0].Name())
	assert.Len(t, fields[0].Values(), 2)
	assert.Equal(t, "z", fields[1].Name())
	assert.Equal(t, PreviewValueOmitted, fields[0].Values()[0].State())

	files := manifest.Files()
	require.Len(t, files, 1)
	assert.Equal(t, "upload", files[0].Field())
	assert.Equal(t, "payload.txt", files[0].Filename())
	assert.Equal(t, "text/plain", files[0].ContentType())
	assert.Zero(t, source.reads.Load())

	fields[0].values[0] = PreviewValue{state: PreviewValuePresent, value: "leak"}
	files[0].field = "mutated"
	second := preview.Body().Multipart()
	assert.Equal(t, PreviewValueOmitted, second.Fields()[0].Values()[0].State())
	assert.Equal(t, "upload", second.Files()[0].Field())
}

func TestRequestPreviewMultipartRejectsStaticInvalidityWithoutReadingParts(t *testing.T) {
	tests := []struct {
		name string
		body *Multipart
	}{
		{
			name: "negative replay limit",
			body: NewMultipart().Replayable(-1),
		},
		{
			name: "invalid boundary",
			body: NewMultipart().Boundary(strings.Repeat("x", 71)),
		},
		{
			name: "empty file field",
			body: NewMultipart().Part(FilePart{Filename: "payload.txt", Body: &previewMultipartReader{}}),
		},
		{
			name: "nil file body",
			body: NewMultipart().Part(FilePart{Field: "upload", Filename: "payload.txt"}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preview, err := newTestClient(t).Post("https://example.com").Multipart(test.body).Preview(t.Context())
			assert.Nil(t, preview)
			assert.ErrorIs(t, err, ErrInvalidConfigValue)
		})
	}

	var typedNil *previewMultipartReader
	preview, err := newTestClient(t).Post("https://example.com").
		Multipart(NewMultipart().Part(FilePart{Field: "upload", Body: typedNil})).
		Preview(t.Context())
	assert.Nil(t, preview)
	assert.ErrorIs(t, err, ErrInvalidConfigValue)
}

type previewMultipartReader struct {
	reads atomic.Int64
}

func (r *previewMultipartReader) Read([]byte) (int, error) {
	r.reads.Add(1)
	return 0, io.EOF
}

func TestMultipartDoesNotCloseBorrowedFilePartBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	source := newTrackingReadCloser("payload")
	body := NewMultipart().File("upload", "payload.txt", source)
	client := newTestClient(t, WithBaseURL(server.URL))

	resp, err := client.Post("/").Multipart(body).Send(t.Context())

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode())
	assert.False(t, source.closed.Load())
}

func TestMultipartSourceReadErrorPreservesCause(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer server.Close()

	body := NewMultipart().File("upload", "payload.txt", failingReader{})
	client := newTestClient(t, WithBaseURL(server.URL))

	resp, err := client.Post("/").Multipart(body).Send(t.Context())

	assert.Nil(t, resp)
	assert.ErrorIs(t, err, errCodecRead)
}

func TestMultipartReplayableBuffersBody(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := r.ParseMultipartForm(10 << 20)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if diff := cmp.Diff([]string{"alice"}, r.MultipartForm.Value["user"]); diff != "" {
			t.Errorf("multipart form field mismatch (-want +got):\n%s", diff)
		}
		requestCount++
		if requestCount == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	body := NewMultipart().
		Field("user", "alice").
		FileString("avatar", "avatar.txt", "hello").
		Replayable(1024)

	client := newTestClient(t,
		WithBaseURL(server.URL),
		WithRetry(RetryPolicy{Max: 1, Backoff: DefaultBackoffStrategy(0)}),
	)
	resp, err := client.Post("/").Multipart(body).Send(t.Context())
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode())
	assert.Equal(t, 2, requestCount)
	assert.Equal(t, 2, resp.Attempts())
}

func TestMultipartFileBytesUsesSnapshotAndCustomBoundary(t *testing.T) {
	t.Parallel()

	data := []byte("hello")
	body := NewMultipart().
		Boundary("requests-boundary").
		FileBytes("avatar", "avatar.txt", data).
		Replayable(1024)
	data[0] = 'j'

	reader, contentType, err := body.reader()
	require.NoError(t, err)
	assert.Contains(t, contentType, "boundary=requests-boundary")

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.com/upload", reader)
	require.NoError(t, err)
	request.Header.Set("Content-Type", contentType)
	require.NoError(t, request.ParseMultipartForm(1024))

	file, _, err := request.FormFile("avatar")
	require.NoError(t, err)
	defer file.Close() //nolint:errcheck // test cleanup closes temporary file

	got, err := io.ReadAll(file)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
}

func TestMultipartReportsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body *Multipart
	}{
		{
			name: "replay limit exceeded",
			body: NewMultipart().
				FileString("avatar", "avatar.txt", strings.Repeat("x", 20)).
				Replayable(10),
		},
		{
			name: "negative replay limit",
			body: NewMultipart().
				FileString("avatar", "avatar.txt", "hello").
				Replayable(-1),
		},
		{
			name: "missing part field",
			body: NewMultipart().
				Part(FilePart{Filename: "avatar.txt", Body: strings.NewReader("hello")}).
				Replayable(1024),
		},
		{
			name: "missing part body",
			body: NewMultipart().
				Part(FilePart{Field: "avatar", Filename: "avatar.txt"}).
				Replayable(1024),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := tc.body.reader()
			assert.ErrorIs(t, err, ErrInvalidConfigValue)
		})
	}
}

func TestMultipartReportsInvalidBoundary(t *testing.T) {
	t.Parallel()

	body := NewMultipart().
		Boundary(strings.Repeat("x", 71)).
		FileString("avatar", "avatar.txt", "hello")

	_, _, err := body.reader()
	require.Error(t, err)
}
