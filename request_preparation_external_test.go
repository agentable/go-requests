package requests_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/test-go/testify/require"

	requests "github.com/agentable/go-requests"
)

func TestLegacyPreviewContract(t *testing.T) {
	client, err := requests.New()
	require.NoError(t, err)

	builder := client.Post("https://example.test/private-path").
		Query("private-query", "query-secret").
		Header("X-Private", "header-secret").
		Cookie("private-cookie", "cookie-secret").
		Text("body-secret")

	preview, err := builder.Preview(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "POST", preview.Method())
	assert.Equal(t, "https", preview.Target().Scheme())
	assert.Equal(t, "example.test", preview.Target().Host())
	assert.Equal(t, requests.PreviewValueOmitted, preview.Target().Path().State())
	assert.Equal(t, requests.PreviewValueOmitted, previewQueryValue(preview.Query(), "private-query").State())
	assert.Equal(t, requests.PreviewValueOmitted, previewHeaderValue(preview.Headers(), "X-Private").State())
	assert.Equal(t, requests.PreviewValueOmitted, previewCookieValue(preview.Cookies(), "private-cookie").State())
	assert.Equal(t, requests.PreviewValueOmitted, preview.Body().Value().State())

	second, err := builder.Preview(t.Context())
	require.NoError(t, err)
	assert.Equal(t, preview.Method(), second.Method())
	assert.Equal(t, preview.Target().Path().State(), second.Target().Path().State())
	assert.Equal(t, preview.Body().Value().State(), second.Body().Value().State())
}

func TestRequestPreparationPublicSurface(t *testing.T) {
	client, err := requests.New(requests.WithBaseURL("https://example.test"))
	require.NoError(t, err)

	builder := client.Post("/private")
	builder.PathValue(requests.Public("/public/items"))
	builder.Query("secret", "private-query")
	builder.QueryValue("tag", requests.Public("approved"))
	builder.QueryValue("tag", requests.Public(""))
	builder.Header("X-Private", "private-header")
	builder.HeaderValue("X-Public", requests.Public("approved-header"))
	builder.Cookie("private-cookie", "private-cookie-value")
	builder.CookieValue("public-cookie", requests.Public("approved-cookie"))
	builder.TextValue(requests.Public("approved-body"))

	preparation, err := builder.Prepare(t.Context(), requests.PrepareOptions{MaxPreparedBodyBytes: 64})
	require.NoError(t, err)
	require.NotNil(t, preparation)

	assert.Equal(t, "POST", preparation.Method())
	assert.Equal(t, "https", preparation.Target().Scheme())
	assert.Equal(t, "example.test", preparation.Target().Authority())
	assert.Equal(t, requests.PreparedValuePresent, preparation.Target().Path().State())
	assert.Equal(t, "/public/items", preparation.Target().Path().Value())

	queries := preparation.Query()
	queryValues := preparedValues(queries, "tag")
	require.Len(t, queryValues, 2)
	assert.Equal(t, "approved", queryValues[0].Value())
	assert.Equal(t, requests.PreparedValuePresent, queryValues[1].State())
	assert.Empty(t, queryValues[1].Value())
	assert.Equal(t, requests.PreparedValueRedacted, preparedValues(queries, "secret")[0].State())

	assert.Equal(t, requests.PreparedValuePresent, preparedHeaderValue(preparation.Headers(), "X-Public").State())
	assert.Equal(t, requests.PreparedValueRedacted, preparedHeaderValue(preparation.Headers(), "X-Private").State())
	assert.Equal(t, requests.PreparedValuePresent, preparedCookieValue(preparation.Cookies(), "public-cookie").State())
	assert.Equal(t, requests.PreparedValueRedacted, preparedCookieValue(preparation.Cookies(), "private-cookie").State())

	body := preparation.Body()
	assert.Equal(t, requests.PreviewBodyText, body.Kind())
	assert.Equal(t, "text/plain", body.MediaType())
	assert.Equal(t, requests.PreparedValuePresent, body.Data().State())
	assert.Equal(t, []byte("approved-body"), body.Data().Bytes())

	bytes := body.Data().Bytes()
	bytes[0] = 'X'
	assert.Equal(t, []byte("approved-body"), body.Data().Bytes())
	assert.NotContains(t, fmt.Sprintf("%v", preparation), "private-query")
	assert.NotContains(t, fmt.Sprintf("%v", preparation), "private-header")
	assert.NotContains(t, fmt.Sprintf("%#v", preparation), "private-cookie-value")

	assertPreparationDTOHasNoDeliveryOrRevealSurface(t)
}

func TestRequestPreparationPublicPathContributors(t *testing.T) {
	var transportCalls atomic.Int32
	client, err := requests.New(requests.WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		transportCalls.Add(1)
		return nil, errors.New("transport must not be called")
	})))
	require.NoError(t, err)

	preparation, err := client.Get("/items/{id}/{id}").
		PathValue(requests.Public("/items/{id}/{id}")).
		PathParamValue("id", requests.Public("a/b")).
		Prepare(t.Context(), requests.PrepareOptions{})
	require.NoError(t, err)
	assert.Equal(t, requests.PreparedValuePresent, preparation.Target().Path().State())
	assert.Equal(t, "/items/a%2Fb/a%2Fb", preparation.Target().Path().Value())
	assert.Zero(t, transportCalls.Load())

	empty, err := client.Get("/items/{id}").
		PathValue(requests.Public("/items/{id}")).
		PathParamValue("id", requests.Public("")).
		Prepare(t.Context(), requests.PrepareOptions{})
	require.NoError(t, err)
	assert.Equal(t, requests.PreparedValuePresent, empty.Target().Path().State())
	assert.Equal(t, "/items/", empty.Target().Path().Value())

	legacyPrivate, err := client.Get("/items/{id}").
		PathValue(requests.Public("/items/{id}")).
		PathParam("id", "private-path-canary").
		Prepare(t.Context(), requests.PrepareOptions{})
	require.NoError(t, err)
	assert.Equal(t, requests.PreparedValueRedacted, legacyPrivate.Target().Path().State())
	assert.Empty(t, legacyPrivate.Target().Path().Value())

	privateOuter, err := client.Get("/items/{id}").
		PathParamValue("id", requests.Public("public")).
		Prepare(t.Context(), requests.PrepareOptions{})
	require.NoError(t, err)
	assert.Equal(t, requests.PreparedValueRedacted, privateOuter.Target().Path().State())

	queryOnly, err := client.Get("/items?{id}=value#fragment-{id}").
		PathValue(requests.Public("/items?{id}=value#fragment-{id}")).
		PathParamValue("id", requests.Public("query-name")).
		Prepare(t.Context(), requests.PrepareOptions{})
	require.NoError(t, err)
	assert.Equal(t, requests.PreparedValuePresent, queryOnly.Target().Path().State())
	assert.Equal(t, "/items", queryOnly.Target().Path().Value())
	queryValues := preparedValues(queryOnly.Query(), "query-name")
	require.Len(t, queryValues, 1)
	assert.Equal(t, requests.PreparedValueRedacted, queryValues[0].State())
	assert.Zero(t, transportCalls.Load())
}

func TestRequestPreparationLegacyBodyIsRedacted(t *testing.T) {
	client, err := requests.New()
	require.NoError(t, err)

	preparation, err := client.Post("https://example.test").Text("private-body").Prepare(
		t.Context(), requests.PrepareOptions{MaxPreparedBodyBytes: 1024},
	)
	require.NoError(t, err)
	assert.Equal(t, requests.PreparedValueRedacted, preparation.Body().Data().State())
	assert.Empty(t, preparation.Body().Data().Bytes())
}

func TestRequestPreparationSecretCanary(t *testing.T) {
	const canary = "private-secret-canary"
	client, err := requests.New(requests.WithBaseURL("https://example.test/base-" + canary))
	require.NoError(t, err)

	preparation, err := client.Post("/private-"+canary).
		Query("private-query", canary).
		Header("X-Private", canary).
		Cookie("private-cookie", canary).
		Text(canary).
		Prepare(t.Context(), requests.PrepareOptions{MaxPreparedBodyBytes: 1024})
	require.NoError(t, err)

	assert.NotContains(t, fmt.Sprintf("%v", preparation), canary)
	assert.NotContains(t, fmt.Sprintf("%#v", preparation), canary)
	for _, query := range preparation.Query() {
		for _, value := range query.Values() {
			assert.NotContains(t, value.Value(), canary)
		}
	}
	for _, header := range preparation.Headers() {
		for _, value := range header.Values() {
			assert.NotContains(t, value.Value(), canary)
		}
	}
	for _, cookie := range preparation.Cookies() {
		assert.NotContains(t, cookie.Value().Value(), canary)
	}
	assert.NotContains(t, string(preparation.Body().Data().Bytes()), canary)
}

func TestRequestPreparationBudgetAndZeroDelivery(t *testing.T) {
	var transportCalls atomic.Int32
	client, err := requests.New(requests.WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		transportCalls.Add(1)
		return nil, errors.New("transport must not be called")
	})))
	require.NoError(t, err)

	_, err = client.Post("https://example.test").TextValue(requests.Public("1234")).Prepare(
		t.Context(), requests.PrepareOptions{MaxPreparedBodyBytes: 3},
	)
	assert.ErrorIs(t, err, requests.ErrPreparationBodyTooLarge)
	assert.Zero(t, transportCalls.Load())

	_, err = client.Post("https://example.test").
		FormFieldValue("payload", requests.Public(strings.Repeat("x", 4096))).
		Prepare(t.Context(), requests.PrepareOptions{MaxPreparedBodyBytes: 1})
	assert.ErrorIs(t, err, requests.ErrPreparationBodyTooLarge)
	assert.Zero(t, transportCalls.Load())

	preparation, err := client.Post("https://example.test").TextValue(requests.Public("")).
		Prepare(t.Context(), requests.PrepareOptions{MaxPreparedBodyBytes: 0})
	require.NoError(t, err)
	assert.Equal(t, requests.PreparedValuePresent, preparation.Body().Data().State())
	assert.Empty(t, preparation.Body().Data().Bytes())
	assert.Zero(t, transportCalls.Load())
}

func TestRequestPreparationDoesNotReadReaderOrEncoder(t *testing.T) {
	reader := &trackingReader{}
	encoder := panicEncoder{}
	client, err := requests.New(requests.WithJSONEncoder(encoder))
	require.NoError(t, err)

	readerPreparation, err := client.Post("https://example.test").Reader(reader, "application/octet-stream").Prepare(
		t.Context(), requests.PrepareOptions{},
	)
	require.NoError(t, err)
	assert.Equal(t, requests.PreviewBodyReader, readerPreparation.Body().Kind())
	assert.Equal(t, requests.PreparedValueRedacted, readerPreparation.Body().Data().State())
	assert.Zero(t, reader.reads.Load())
	assert.Zero(t, reader.closes.Load())

	typedPreparation, err := client.Post("https://example.test").JSON(struct{ Secret string }{Secret: "private"}).Prepare(
		t.Context(), requests.PrepareOptions{},
	)
	require.NoError(t, err)
	assert.Equal(t, requests.PreviewBodyJSON, typedPreparation.Body().Kind())
	assert.Equal(t, requests.PreparedValueRedacted, typedPreparation.Body().Data().State())
}

func TestRequestPreparationAuthBoundary(t *testing.T) {
	client, err := requests.New()
	require.NoError(t, err)

	preparation, err := client.Post("https://example.test").
		Auth(requests.BearerAuth{Token: "private-token"}).
		HeaderValue("Authorization", requests.Public("fake-public-token")).
		Prepare(t.Context(), requests.PrepareOptions{})
	require.NoError(t, err)
	authorization := preparedHeaderValue(preparation.Headers(), "Authorization")
	assert.Equal(t, requests.PreparedValueRedacted, authorization.State())

	var validCalls atomic.Int32
	var applyCalls atomic.Int32
	auth := &recordingAuth{validCalls: &validCalls, applyCalls: &applyCalls}
	builder := client.Post("https://example.test").Auth(auth)
	validCalls.Store(0)
	_, err = builder.Prepare(t.Context(), requests.PrepareOptions{})
	assert.ErrorIs(t, err, requests.ErrPreparationNotPreparable)
	assert.Zero(t, validCalls.Load())
	assert.Zero(t, applyCalls.Load())
}

func TestRequestPreparationMultipartManifestDoesNotOpenParts(t *testing.T) {
	source := &trackingReader{}
	multipart := requests.NewMultipart().
		Field("z", "last").
		Field("a", "first").
		Part(requests.FilePart{Field: "upload", Filename: "private.txt", ContentType: "text/plain", Body: source})
	client, err := requests.New()
	require.NoError(t, err)

	preparation, err := client.Post("https://example.test").Multipart(multipart).Prepare(
		t.Context(), requests.PrepareOptions{},
	)
	require.NoError(t, err)
	assert.Equal(t, requests.PreviewBodyMultipart, preparation.Body().Kind())
	assert.Equal(t, requests.PreparedValueRedacted, preparation.Body().Data().State())
	manifest := preparation.Body().Multipart()
	require.NotNil(t, manifest)
	assert.Equal(t, []string{"a", "z"}, preparedOccurrenceNames(manifest.Fields()))
	assert.Equal(t, []string{"upload"}, preparedFileFields(manifest.Files()))
	assert.Zero(t, source.reads.Load())
	assert.Zero(t, source.closes.Load())
	assert.NotContains(t, fmt.Sprintf("%v", preparation), "private.txt")
}

func TestRequestPreparationPathAndQueryTaint(t *testing.T) {
	client, err := requests.New(requests.WithBaseURL("https://example.test/private"))
	require.NoError(t, err)

	preparation, err := client.Get("/public").PathValue(requests.Public("/public")).Prepare(
		t.Context(), requests.PrepareOptions{},
	)
	require.NoError(t, err)
	assert.Equal(t, requests.PreparedValueRedacted, preparation.Target().Path().State())

	_, err = client.Get("https://example.test/items/{id}?{id}=x").PathParam("id", "private").Prepare(
		t.Context(), requests.PrepareOptions{},
	)
	assert.ErrorIs(t, err, requests.ErrPreparationNotPreparable)

	ordinary, err := client.Get("https://example.test/items/{id}").PathParam("id", "private").Prepare(
		t.Context(), requests.PrepareOptions{},
	)
	require.NoError(t, err)
	assert.Equal(t, requests.PreparedValueRedacted, ordinary.Target().Path().State())
}

func TestRequestPreparationRejectsPrivateStructuralSubstitution(t *testing.T) {
	client, err := requests.New(requests.WithBaseURL("https://example.test/"))
	require.NoError(t, err)

	tests := []struct {
		name  string
		path  string
		param string
		value string
	}{
		{name: "scheme", path: "{scheme}://example.test/items", param: "scheme", value: "scheme-secret-canary"},
		{name: "authority", path: "https://{host}/items", param: "host", value: "authority-secret-canary"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.Get(test.path).PathParam(test.param, test.value).Prepare(
				t.Context(), requests.PrepareOptions{},
			)
			assert.ErrorIs(t, err, requests.ErrPreparationNotPreparable)
			assert.NotContains(t, err.Error(), test.value)
		})
	}
}

func TestRequestPreparationMultipartStaticErrorsDoNotOpenParts(t *testing.T) {
	tests := []struct {
		name    string
		build   func(*trackingReader) *requests.Multipart
		secrets []string
	}{
		{
			name: "negative replay limit",
			build: func(source *trackingReader) *requests.Multipart {
				return requests.NewMultipart().Replayable(-73421).Part(requests.FilePart{
					Field:    "upload",
					Filename: "private-filename-canary.txt",
					Body:     source,
				})
			},
			secrets: []string{"-73421", "private-filename-canary"},
		},
		{
			name: "invalid content type",
			build: func(source *trackingReader) *requests.Multipart {
				return requests.NewMultipart().Part(requests.FilePart{
					Field:       "upload",
					Filename:    "private-filename-canary.txt",
					ContentType: "private-content-type-canary invalid",
					Body:        source,
				})
			},
			secrets: []string{"private-filename-canary", "private-content-type-canary"},
		},
		{
			name: "invalid boundary",
			build: func(source *trackingReader) *requests.Multipart {
				return requests.NewMultipart().Boundary("private-boundary-canary\n").Part(requests.FilePart{
					Field:    "upload",
					Filename: "private-filename-canary.txt",
					Body:     source,
				})
			},
			secrets: []string{"private-boundary-canary", "private-filename-canary"},
		},
		{
			name: "empty field",
			build: func(source *trackingReader) *requests.Multipart {
				return requests.NewMultipart().Part(requests.FilePart{
					Filename: "private-filename-canary.txt",
					Body:     source,
				})
			},
			secrets: []string{"private-filename-canary"},
		},
		{
			name: "nil body",
			build: func(*trackingReader) *requests.Multipart {
				return requests.NewMultipart().Part(requests.FilePart{
					Field:    "upload",
					Filename: "private-filename-canary.txt",
				})
			},
			secrets: []string{"private-filename-canary"},
		},
		{
			name: "typed nil body",
			build: func(*trackingReader) *requests.Multipart {
				var typedNil *trackingReader
				return requests.NewMultipart().Part(requests.FilePart{
					Field:    "upload",
					Filename: "private-filename-canary.txt",
					Body:     typedNil,
				})
			},
			secrets: []string{"private-filename-canary"},
		},
	}

	client, err := requests.New()
	require.NoError(t, err)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &trackingReader{}
			_, err := client.Post("https://example.test").Multipart(test.build(source)).Prepare(
				t.Context(), requests.PrepareOptions{},
			)
			require.Error(t, err)
			assert.ErrorIs(t, err, requests.ErrInvalidConfigValue)
			assert.Zero(t, source.reads.Load())
			assert.Zero(t, source.closes.Load())
			assert.Zero(t, source.readAtCalls.Load())
			assert.Zero(t, source.seekCalls.Load())
			assert.Zero(t, source.sizeCalls.Load())
			for _, secret := range test.secrets {
				assert.NotContains(t, err.Error(), secret)
			}
		})
	}
}

func assertPreparationDTOHasNoDeliveryOrRevealSurface(t *testing.T) {
	t.Helper()

	types := map[reflect.Type][]string{
		reflect.TypeOf(requests.RequestPreparation{}): {
			"Body", "Cookies", "Headers", "Method", "OrderedHeaders", "Query", "Target",
		},
		reflect.TypeOf(requests.PreparedTarget{}):        {"Authority", "Path", "Scheme"},
		reflect.TypeOf(requests.PreparedOccurrence{}):    {"Name", "Values"},
		reflect.TypeOf(requests.PreparedHeader{}):        {"Name", "Values"},
		reflect.TypeOf(requests.PreparedCookie{}):        {"Name", "Value"},
		reflect.TypeOf(requests.PreparedBody{}):          {"Data", "Kind", "MediaType", "Multipart"},
		reflect.TypeOf(requests.PreparedMultipart{}):     {"Fields", "Files"},
		reflect.TypeOf(requests.PreparedMultipartFile{}): {"Field"},
		reflect.TypeOf(requests.PreparedString{}):        {"State", "Value"},
		reflect.TypeOf(requests.PreparedBytes{}):         {"Bytes", "State"},
	}
	for typ, expected := range types {
		for i := 0; i < typ.NumField(); i++ {
			assert.NotEmpty(t, typ.Field(i).PkgPath, "%s field %s must stay unexported", typ, typ.Field(i).Name)
		}
		pointer := reflect.PointerTo(typ)
		actual := make([]string, 0, pointer.NumMethod())
		for i := 0; i < pointer.NumMethod(); i++ {
			method := pointer.Method(i)
			actual = append(actual, method.Name)
		}
		slices.Sort(actual)
		slices.Sort(expected)
		assert.Equal(t, expected, actual, "%s exported method set changed", typ)
	}
}

func preparedValues(values []requests.PreparedOccurrence, name string) []requests.PreparedString {
	for _, occurrence := range values {
		if occurrence.Name() == name {
			return occurrence.Values()
		}
	}
	return nil
}

func previewQueryValue(values []requests.PreviewQuery, name string) requests.PreviewValue {
	for _, occurrence := range values {
		if occurrence.Key() == name {
			entries := occurrence.Values()
			if len(entries) > 0 {
				return entries[0]
			}
		}
	}
	return requests.PreviewValue{}
}

func previewHeaderValue(values []requests.PreviewHeader, name string) requests.PreviewValue {
	for _, header := range values {
		if strings.EqualFold(header.Key(), name) {
			entries := header.Values()
			if len(entries) > 0 {
				return entries[0]
			}
		}
	}
	return requests.PreviewValue{}
}

func previewCookieValue(values []requests.PreviewCookie, name string) requests.PreviewValue {
	for _, cookie := range values {
		if cookie.Name() == name {
			return cookie.Value()
		}
	}
	return requests.PreviewValue{}
}

func preparedHeaderValue(values []requests.PreparedHeader, name string) requests.PreparedString {
	for _, header := range values {
		if strings.EqualFold(header.Name(), name) {
			values := header.Values()
			if len(values) > 0 {
				return values[0]
			}
		}
	}
	return requests.PreparedString{}
}

func preparedCookieValue(values []requests.PreparedCookie, name string) requests.PreparedString {
	for _, cookie := range values {
		if cookie.Name() == name {
			return cookie.Value()
		}
	}
	return requests.PreparedString{}
}

func preparedOccurrenceNames(values []requests.PreparedOccurrence) []string {
	names := make([]string, len(values))
	for i, value := range values {
		names[i] = value.Name()
	}
	return names
}

func preparedFileFields(values []requests.PreparedMultipartFile) []string {
	fields := make([]string, len(values))
	for i, value := range values {
		fields[i] = value.Field()
	}
	return fields
}

type trackingReader struct {
	reads       atomic.Int32
	readAtCalls atomic.Int32
	seekCalls   atomic.Int32
	sizeCalls   atomic.Int32
	closes      atomic.Int32
}

func (r *trackingReader) Read([]byte) (int, error) {
	r.reads.Add(1)
	return 0, io.EOF
}

func (r *trackingReader) ReadAt([]byte, int64) (int, error) {
	r.readAtCalls.Add(1)
	return 0, io.EOF
}

func (r *trackingReader) Seek(int64, int) (int64, error) {
	r.seekCalls.Add(1)
	return 0, nil
}

func (r *trackingReader) Size() int64 {
	r.sizeCalls.Add(1)
	return 0
}

func (r *trackingReader) Close() error {
	r.closes.Add(1)
	return nil
}

type panicEncoder struct{}

func (panicEncoder) Encode(any) (io.Reader, error) {
	panic("Prepare called encoder")
}

type recordingAuth struct {
	validCalls *atomic.Int32
	applyCalls *atomic.Int32
}

func (a *recordingAuth) Valid() bool {
	a.validCalls.Add(1)
	return true
}

func (a *recordingAuth) Apply(*http.Request) {
	a.applyCalls.Add(1)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
