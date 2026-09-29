package requests

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentable/go-orderedobject"
	"github.com/stretchr/testify/assert"
	"github.com/test-go/testify/require"
)

func TestRequestPreparationOrderedHeadersAndContentTypeDisclosure(t *testing.T) {
	ordered := orderedobject.New[[]string]().
		Set(":authority", []string{"example.test"}).
		Set("X-Ordered", []string{"private-ordered"})

	generated, err := newTestClient(t).Post("https://example.test").
		TextValue(Public("approved")).
		Prepare(t.Context(), PrepareOptions{MaxPreparedBodyBytes: 64})
	require.NoError(t, err)
	assert.Equal(t, "text/plain", generated.Body().MediaType())
	assert.Equal(t, PreparedValuePresent, preparedHeaderValue(generated.Headers(), "Content-Type").State())

	explicit, err := newTestClient(t).Post("https://example.test").
		TextValue(Public("approved")).
		HeaderValue("Content-Type", Public("text/plain")).
		OrderedHeaders(ordered).
		Prepare(t.Context(), PrepareOptions{MaxPreparedBodyBytes: 64})
	require.NoError(t, err)
	assert.Equal(t, PreparedValueRedacted, preparedHeaderValue(explicit.Headers(), "Content-Type").State())
	assert.Empty(t, explicit.Body().MediaType())
	orderedHeaders := explicit.OrderedHeaders()
	assert.Equal(t, []string{":authority", "X-Ordered"}, preparedHeaderNames(orderedHeaders))
	assert.Equal(t, PreparedValueRedacted, orderedHeaders[1].Values()[0].State())
}

func TestRequestPreparationPublicFormEncodingOrder(t *testing.T) {
	preparation, err := newTestClient(t).Post("https://example.test").
		FormFieldValue("z", Public("last")).
		FormFieldValue("a", Public("space value")).
		FormFieldValue("a", Public("")).
		Prepare(t.Context(), PrepareOptions{MaxPreparedBodyBytes: 64})
	require.NoError(t, err)
	assert.Equal(t, []byte(url.Values{"a": {"space value", ""}, "z": {"last"}}.Encode()), preparation.Body().Data().Bytes())

	private, err := newTestClient(t).Post("https://example.test").
		FormFieldValue("a", Public("visible")).
		FormField("secret", "private").
		Prepare(t.Context(), PrepareOptions{MaxPreparedBodyBytes: 64})
	require.NoError(t, err)
	assert.Equal(t, PreparedValueRedacted, private.Body().Data().State())

	legacyEmpty, err := newTestClient(t).Post("https://example.test").
		Form(url.Values{}).
		Prepare(t.Context(), PrepareOptions{MaxPreparedBodyBytes: 64})
	require.NoError(t, err)
	assert.Equal(t, PreparedValueRedacted, legacyEmpty.Body().Data().State())

	publicEmpty, err := newTestClient(t).Post("https://example.test").
		FormFieldValue("empty", Public("")).
		Prepare(t.Context(), PrepareOptions{MaxPreparedBodyBytes: 64})
	require.NoError(t, err)
	assert.Equal(t, PreparedValuePresent, publicEmpty.Body().Data().State())
	assert.Equal(t, []byte("empty="), publicEmpty.Body().Data().Bytes())
}

func TestRequestPreparationPublicFormBudgetSemantics(t *testing.T) {
	invalidUTF8 := string([]byte{0xff, 0xfe})
	values := url.Values{
		"":        {""},
		"empty":   {""},
		"special": {"space + % & / \x00 雪 " + invalidUTF8, "second"},
		"unicode": {"中文"},
	}
	expected := values.Encode()

	builder := newTestClient(t).Post("https://example.test").
		FormFieldValue("special", Public("space + % & / \x00 雪 "+invalidUTF8)).
		FormFieldValue("unicode", Public("中文")).
		FormFieldValue("special", Public("second")).
		FormFieldValue("", Public(""))
	builder.FormFieldValue("empty", Public(""))

	preparation, err := builder.Prepare(t.Context(), PrepareOptions{MaxPreparedBodyBytes: int64(len(expected))})
	require.NoError(t, err)
	assert.Equal(t, []byte(expected), preparation.Body().Data().Bytes())

	_, err = builder.Prepare(t.Context(), PrepareOptions{MaxPreparedBodyBytes: int64(len(expected) - 1)})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPreparationBodyTooLarge)
	assert.NotContains(t, err.Error(), invalidUTF8)

	mixed, err := newTestClient(t).Post("https://example.test").
		FormFieldValue("public", Public("visible")).
		FormField("private", "canary-secret").
		Prepare(t.Context(), PrepareOptions{MaxPreparedBodyBytes: 1 << 20})
	require.NoError(t, err)
	assert.Equal(t, PreparedValueRedacted, mixed.Body().Data().State())
	assert.NotContains(t, string(mixed.Body().Data().Bytes()), "canary-secret")
}

func TestRequestPreparationCredentialHeadersStayPrivate(t *testing.T) {
	preparation, err := newTestClient(t).Post("https://example.test").
		HeaderValue("Authorization", Public("Bearer fake")).
		HeaderValue("Proxy-Authorization", Public("Basic fake")).
		HeaderValue("Cookie", Public("session=private")).
		Prepare(t.Context(), PrepareOptions{})
	require.NoError(t, err)
	for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie"} {
		assert.Equal(t, PreparedValueRedacted, preparedHeaderValue(preparation.Headers(), name).State(), name)
	}
}

func TestRequestPreparationTypedBodyDoesNotUseClientDefaultContentType(t *testing.T) {
	var encoderCalls atomic.Int64
	client := newTestClient(t,
		WithContentType("application/json"),
		WithJSONEncoder(preparationCountingEncoder{calls: &encoderCalls}),
	)

	_, err := client.Post("https://example.test").
		JSON(struct{}{}).
		DelHeader("Content-Type").
		Prepare(t.Context(), PrepareOptions{})

	assert.ErrorIs(t, err, ErrUnsupportedContentType)
	assert.Zero(t, encoderCalls.Load())
}

func TestRequestPreparationClientAuthBoundary(t *testing.T) {
	client, err := New(WithBearerAuth("client-secret"))
	require.NoError(t, err)
	preparation, err := client.Get("https://example.test").Prepare(t.Context(), PrepareOptions{})
	require.NoError(t, err)
	assert.Equal(t, PreparedValueRedacted, preparedHeaderValue(preparation.Headers(), "Authorization").State())

	var validCalls atomic.Int32
	var applyCalls atomic.Int32
	unknown := &preparationRecordingAuth{validCalls: &validCalls, applyCalls: &applyCalls}
	client, err = New(WithAuth(unknown))
	require.NoError(t, err)
	validCalls.Store(0)
	applyCalls.Store(0)
	_, err = client.Get("https://example.test").Prepare(t.Context(), PrepareOptions{})
	assert.ErrorIs(t, err, ErrPreparationNotPreparable)
	assert.Zero(t, validCalls.Load())
	assert.Zero(t, applyCalls.Load())

	validCalls.Store(0)
	applyCalls.Store(0)
	client, err = New(WithAuth(unknown))
	require.NoError(t, err)
	validCalls.Store(0)
	applyCalls.Store(0)
	_, err = client.Get("https://example.test").Auth(BearerAuth{Token: "request-token"}).Prepare(t.Context(), PrepareOptions{})
	assert.ErrorIs(t, err, ErrPreparationNotPreparable)
	assert.Zero(t, validCalls.Load())
	assert.Zero(t, applyCalls.Load())

	validCalls.Store(0)
	applyCalls.Store(0)
	client, err = New(WithBearerAuth("client-token"))
	require.NoError(t, err)
	builder := client.Get("https://example.test").Auth(unknown)
	validCalls.Store(0)
	applyCalls.Store(0)
	_, err = builder.Prepare(t.Context(), PrepareOptions{})
	assert.ErrorIs(t, err, ErrPreparationNotPreparable)
	assert.Zero(t, validCalls.Load())
	assert.Zero(t, applyCalls.Load())
}

func TestRequestPreparationPathTaintAndUnsafeParameter(t *testing.T) {
	rootClient, err := New(WithBaseURL("https://example.test/"))
	require.NoError(t, err)
	rootPreparation, err := rootClient.Get("/public").PathValue(Public("/public")).Prepare(t.Context(), PrepareOptions{})
	require.NoError(t, err)
	assert.Equal(t, PreparedValuePresent, rootPreparation.Target().Path().State())

	privateBase, err := New(WithBaseURL("https://example.test/private"))
	require.NoError(t, err)
	privatePreparation, err := privateBase.Get("/public").PathValue(Public("/public")).Prepare(t.Context(), PrepareOptions{})
	require.NoError(t, err)
	assert.Equal(t, PreparedValueRedacted, privatePreparation.Target().Path().State())

	_, err = rootClient.Get("https://example.test/items/{id}?{id}=value").
		PathParam("id", "private").Prepare(t.Context(), PrepareOptions{})
	assert.ErrorIs(t, err, ErrPreparationNotPreparable)

	_, err = rootClient.Get("{scheme}://example.test/items").
		PathParam("scheme", "private").Prepare(t.Context(), PrepareOptions{})
	assert.ErrorIs(t, err, ErrPreparationNotPreparable)

	_, err = rootClient.Get("https://{host}/items").
		PathParam("host", "private").Prepare(t.Context(), PrepareOptions{})
	assert.ErrorIs(t, err, ErrPreparationNotPreparable)

	userinfo, err := rootClient.Get("https://user:{id}@example.test/items").
		PathParam("id", "private").Prepare(t.Context(), PrepareOptions{})
	require.NoError(t, err)
	assert.Equal(t, "example.test", userinfo.Target().Authority())
}

func TestRequestPreparationPublicPathParamProjection(t *testing.T) {
	client, err := New(WithBaseURL("https://example.test/"))
	require.NoError(t, err)

	public, err := client.Get("/items/{id}").
		PathValue(Public("/items/{id}")).
		PathParamValue("id", Public("a/b")).
		Prepare(t.Context(), PrepareOptions{})
	require.NoError(t, err)
	assert.Equal(t, PreparedValuePresent, public.Target().Path().State())
	assert.Equal(t, "/items/a%2Fb", public.Target().Path().Value())

	private, err := client.Get("/items/{id}").
		PathValue(Public("/items/{id}")).
		PathParam("id", "private").
		Prepare(t.Context(), PrepareOptions{})
	require.NoError(t, err)
	assert.Equal(t, PreparedValueRedacted, private.Target().Path().State())

	publicEmpty, err := client.Get("/items/{id}").
		PathValue(Public("/items/{id}")).
		PathParamValue("id", Public("")).
		Prepare(t.Context(), PrepareOptions{})
	require.NoError(t, err)
	assert.Equal(t, PreparedValuePresent, publicEmpty.Target().Path().State())
	assert.Equal(t, "/items/", publicEmpty.Target().Path().Value())

	privateOuter, err := client.Get("/items/{id}").
		PathParamValue("id", Public("public")).
		Prepare(t.Context(), PrepareOptions{})
	require.NoError(t, err)
	assert.Equal(t, PreparedValueRedacted, privateOuter.Target().Path().State())

	privateBaseClient, err := New(WithBaseURL("https://example.test/private"))
	require.NoError(t, err)
	privateBase, err := privateBaseClient.Get("/items/{id}").
		PathValue(Public("/items/{id}")).
		PathParamValue("id", Public("public")).
		Prepare(t.Context(), PrepareOptions{})
	require.NoError(t, err)
	assert.Equal(t, PreparedValueRedacted, privateBase.Target().Path().State())

	abs, err := privateBaseClient.Get("https://other.example/items/{id}").
		PathValue(Public("https://other.example/items/{id}")).
		PathParamValue("id", Public("public")).
		Prepare(t.Context(), PrepareOptions{})
	require.NoError(t, err)
	assert.Equal(t, PreparedValuePresent, abs.Target().Path().State())
	assert.Equal(t, "/items/public", abs.Target().Path().Value())
}

func TestRequestPreparationChecksContextWhileProjectingPathParameters(t *testing.T) {
	client, err := New()
	require.NoError(t, err)

	ctx := &stepCancelContext{cancelAfter: 3}
	_, err = client.Get("https://example.test/items/{id}").
		PathParam("id", "private").Prepare(ctx, PrepareOptions{})
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRequestPreparationDetachedNestedValues(t *testing.T) {
	preparation, err := newTestClient(t).Post("https://example.test").
		QueryValue("q", Public("value")).
		HeaderValue("X-Value", Public("header")).
		CookieValue("cookie", Public("cookie-value")).
		BytesPayload(PublicPayload([]byte("body"))).
		Prepare(t.Context(), PrepareOptions{MaxPreparedBodyBytes: 64})
	require.NoError(t, err)

	queries := preparation.Query()
	queryValues := queries[0].Values()
	queryValues[0] = preparedStringPresent("mutated")
	queries[0].values[0] = preparedStringPresent("mutated-again")
	headers := preparation.Headers()
	headerValues := headers[0].Values()
	headerValues[0] = preparedStringPresent("mutated")
	headers[0].values[0] = preparedStringPresent("mutated-again")
	cookies := preparation.Cookies()
	cookies[0].value = preparedStringPresent("mutated")
	body := preparation.Body().Data().Bytes()
	body[0] = 'X'

	assert.Equal(t, "value", preparation.Query()[0].Values()[0].Value())
	assert.Equal(t, "header", preparation.Headers()[0].Values()[0].Value())
	assert.Equal(t, "cookie-value", preparation.Cookies()[0].Value().Value())
	assert.Equal(t, []byte("body"), preparation.Body().Data().Bytes())
}

func TestRequestPreparationBudgetFailureDoesNotChangePublicBytes(t *testing.T) {
	const approved = "approved bytes"
	builder := newTestClient(t).Post("https://example.test").
		BytesPayload(PublicPayload([]byte(approved)))

	_, err := builder.Prepare(t.Context(), PrepareOptions{MaxPreparedBodyBytes: int64(len(approved) - 1)})
	assert.ErrorIs(t, err, ErrPreparationBodyTooLarge)

	preparation, err := builder.Prepare(t.Context(), PrepareOptions{MaxPreparedBodyBytes: int64(len(approved))})
	require.NoError(t, err)
	assert.Equal(t, PreparedValuePresent, preparation.Body().Data().State())
	assert.Equal(t, []byte(approved), preparation.Body().Data().Bytes())
}

func TestRequestPreparationMultipartWalkerContext(t *testing.T) {
	t.Run("field values", func(t *testing.T) {
		multipart := NewMultipart()
		for range 64 {
			multipart.Field("field", "private")
		}
		readerCalls := new(atomic.Int64)
		reader := &preparationCountingReader{calls: readerCalls}
		multipart.Part(FilePart{Field: "upload", Filename: "private.txt", Body: reader})

		ctx := &stepCancelContext{cancelAfter: 3}
		_, err := prepareMultipartManifest(ctx, multipart)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Zero(t, readerCalls.Load())
	})

	t.Run("part occurrences", func(t *testing.T) {
		multipart := NewMultipart()
		readerCalls := new(atomic.Int64)
		reader := &preparationCountingReader{calls: readerCalls}
		for range 64 {
			multipart.Part(FilePart{Field: "upload", Filename: "private.txt", Body: reader})
		}

		ctx := &stepCancelContext{cancelAfter: 3}
		_, err := prepareMultipartManifest(ctx, multipart)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Zero(t, readerCalls.Load())
	})
}

func TestRequestPreparationPathEligibilityChecksContext(t *testing.T) {
	var path strings.Builder
	params := make(map[string]string, 128)
	for i := range 128 {
		name := "param" + strconv.Itoa(i)
		path.WriteString("/{")
		path.WriteString(name)
		path.WriteByte('}')
		params[name] = "private"
	}

	ctx := &stepCancelContext{cancelAfter: 1}
	_, err := newTestClient(t).Get(path.String()).PathParams(params).
		Prepare(ctx, PrepareOptions{})
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRequestPreparationPathProjectionChecksContext(t *testing.T) {
	var path strings.Builder
	params := make(map[string]string, 128)
	for i := range 128 {
		name := "param" + strconv.Itoa(i)
		path.WriteString("/{")
		path.WriteString(name)
		path.WriteByte('}')
		params[name] = "private"
	}

	builder := newTestClient(t).Get(path.String()).PathValue(Public(path.String())).PathParams(params)
	facts, err := builder.compileRequestPlan()
	require.NoError(t, err)
	for i := range facts.pathTrace.params {
		facts.pathTrace.params[i].value = Public("approved")
	}

	ctx := &stepCancelContext{cancelAfter: 1}
	_, err = projectRequestPreparation(ctx, facts, 0)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRequestPreparationChecksContextInsideValueGroups(t *testing.T) {
	occurrences := func(name string) []requestOccurrence {
		values := make([]requestOccurrence, 64)
		for i := range values {
			values[i] = requestOccurrence{name: name}
		}
		return values
	}

	t.Run("query values", func(t *testing.T) {
		ctx := &stepCancelContext{cancelAfter: 4}
		_, err := projectPreparedQuery(ctx, occurrences("tag"))
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("header values", func(t *testing.T) {
		ctx := &stepCancelContext{cancelAfter: 4}
		_, _, err := projectPreparedHeaders(ctx, &requestPlan{headers: occurrences("X-Many")})
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("ordered header values", func(t *testing.T) {
		ctx := &stepCancelContext{cancelAfter: 3}
		ordered := orderedobject.New[[]string]().Set("X-Many", make([]string, 64))
		semantic := map[string]PreparedHeader{
			"x-many": {name: "X-Many", values: make([]PreparedString, 64)},
		}
		_, err := projectPreparedOrderedHeaders(ctx, ordered, semantic)
		assert.ErrorIs(t, err, context.Canceled)
	})
}

func TestRequestPreparationChecksContextAcrossProjectionCollections(t *testing.T) {
	t.Run("cookies", func(t *testing.T) {
		values := make([]requestCookieOccurrence, 64)
		ctx := &stepCancelContext{cancelAfter: 0}
		_, err := projectPreparedCookies(ctx, values)
		assert.ErrorIs(t, err, context.Canceled)
	})

	formBody := func() requestBodyPlan {
		occurrences := make([]requestBodyFormOccurrence, 128)
		for i := range occurrences {
			occurrences[i] = requestBodyFormOccurrence{name: "field", value: Public("value")}
		}
		return requestBodyPlan{kind: requestBodyForm, formOccurrences: occurrences}
	}

	t.Run("form disclosure scan", func(t *testing.T) {
		ctx := &stepCancelContext{cancelAfter: 1}
		_, err := projectPreparedBody(ctx, formBody(), 1<<20)
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("form data projection", func(t *testing.T) {
		ctx := &stepCancelContext{cancelAfter: 4}
		_, err := projectPreparedBody(ctx, formBody(), 1<<20)
		assert.ErrorIs(t, err, context.Canceled)
	})
}

func TestRequestPreparationErrorSanitization(t *testing.T) {
	_, err := newTestClient(t).Get("https://user:password@example.test/items?secret=%zz").
		Prepare(t.Context(), PrepareOptions{})
	assert.ErrorIs(t, err, ErrRequestCreationFailed)
	assert.NotContains(t, err.Error(), "password")
	assert.NotContains(t, err.Error(), "secret")
	var urlErr *url.Error
	assert.False(t, errors.As(err, &urlErr))

	_, err = newTestClient(t).Get("https://example.test").Prepare(t.Context(), PrepareOptions{MaxPreparedBodyBytes: -1})
	assert.ErrorIs(t, err, ErrPreparationInvalidBudget)

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = newTestClient(t).Get("https://example.test").Prepare(canceled, PrepareOptions{})
	assert.ErrorIs(t, err, context.Canceled)
}

func preparedHeaderValue(values []PreparedHeader, name string) PreparedString {
	for _, header := range values {
		if header.name == name {
			if len(header.values) > 0 {
				return header.values[0]
			}
		}
	}
	return PreparedString{}
}

func preparedHeaderNames(values []PreparedHeader) []string {
	names := make([]string, len(values))
	for i, value := range values {
		names[i] = value.name
	}
	return names
}

func preparedStringPresent(value string) PreparedString {
	return PreparedString{state: PreparedValuePresent, value: value}
}

type stepCancelContext struct {
	cancelAfter int32
	checks      atomic.Int32
}

func (c *stepCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }

func (c *stepCancelContext) Done() <-chan struct{} { return nil }

func (c *stepCancelContext) Err() error {
	if c.checks.Add(1) > c.cancelAfter {
		return context.Canceled
	}
	return nil
}

func (c *stepCancelContext) Value(any) any { return nil }

var _ context.Context = (*stepCancelContext)(nil)

type preparationRecordingAuth struct {
	validCalls *atomic.Int32
	applyCalls *atomic.Int32
}

func (a *preparationRecordingAuth) Valid() bool {
	a.validCalls.Add(1)
	return true
}

func (a *preparationRecordingAuth) Apply(*http.Request) {
	a.applyCalls.Add(1)
}

type preparationCountingEncoder struct {
	calls *atomic.Int64
}

func (e preparationCountingEncoder) Encode(any) (io.Reader, error) {
	e.calls.Add(1)
	return strings.NewReader(""), nil
}

type preparationCountingReader struct {
	calls *atomic.Int64
}

func (r *preparationCountingReader) Read([]byte) (int, error) {
	r.calls.Add(1)
	return 0, io.EOF
}

// TestRequestPreparationParity checks that Prepare projects the same
// delivery-independent facts compiled for the other request exits. It does
// not open a delivery body, compare borrowed bytes, or inspect credential
// values and multipart framing.
func TestRequestPreparationParity(t *testing.T) {
	tests := []struct {
		name       string
		build      func(*Client) (*RequestBuilder, *atomic.Int64)
		bodyKind   requestBodyKind
		bodyMedia  string
		bodyRedact bool
		publicBody bool
		formPublic bool
	}{
		{
			name: "text",
			build: func(client *Client) (*RequestBuilder, *atomic.Int64) {
				return parityBuilder(client).TextValue(Public("approved")), nil
			},
			bodyKind:   requestBodyText,
			bodyMedia:  "text/plain",
			bodyRedact: false,
			publicBody: true,
		},
		{
			name: "bytes",
			build: func(client *Client) (*RequestBuilder, *atomic.Int64) {
				return parityBuilder(client).BytesPayload(PublicPayload([]byte("approved"))), nil
			},
			bodyKind:   requestBodyBytes,
			bodyRedact: false,
			publicBody: true,
		},
		{
			name: "form",
			build: func(client *Client) (*RequestBuilder, *atomic.Int64) {
				builder := parityBuilder(client).
					FormFieldValue("z", Public("last")).
					FormFieldValue("a", Public("first"))
				return builder, nil
			},
			bodyKind:   requestBodyForm,
			bodyMedia:  "application/x-www-form-urlencoded",
			bodyRedact: false,
			formPublic: true,
		},
		{
			name: "json",
			build: func(client *Client) (*RequestBuilder, *atomic.Int64) {
				return parityBuilder(client).JSON(struct{ Name string }{Name: "borrowed"}), nil
			},
			bodyKind:   requestBodyJSON,
			bodyMedia:  "application/json",
			bodyRedact: true,
		},
		{
			name: "reader",
			build: func(client *Client) (*RequestBuilder, *atomic.Int64) {
				var reads atomic.Int64
				return parityBuilder(client).Reader(&parityCountingReader{reads: &reads}, "application/octet-stream"), &reads
			},
			bodyKind:   requestBodyReader,
			bodyRedact: true,
		},
		{
			name: "multipart",
			build: func(client *Client) (*RequestBuilder, *atomic.Int64) {
				var reads atomic.Int64
				multipart := NewMultipart().
					Field("z", "private").
					Part(FilePart{Field: "upload", Filename: "private.txt", Body: &parityCountingReader{reads: &reads}})
				return parityBuilder(client).Multipart(multipart), &reads
			},
			bodyKind:   requestBodyMultipart,
			bodyMedia:  "multipart/form-data",
			bodyRedact: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			builder, sourceReads := test.build(newTestClient(t))
			facts, err := builder.compileRequestPlan()
			require.NoError(t, err)
			require.NotNil(t, facts)
			delivery, err := builder.compileDeliverySnapshot()
			require.NoError(t, err)
			require.NotNil(t, delivery)
			require.NotNil(t, delivery.plan)
			assertRequestPlanParity(t, facts, delivery.plan)

			preparation, err := builder.Prepare(t.Context(), PrepareOptions{MaxPreparedBodyBytes: 1 << 20})
			require.NoError(t, err)
			require.NotNil(t, preparation)

			assert.Equal(t, facts.method, preparation.Method())
			assert.Equal(t, facts.targetURL.Scheme, preparation.Target().Scheme())
			assert.Equal(t, facts.targetURL.Host, preparation.Target().Authority())
			assert.Equal(t, PreparedValuePresent, preparation.Target().Path().State())
			assert.Equal(t, facts.targetURL.EscapedPath(), preparation.Target().Path().Value())

			assertQueryFactsParity(t, facts.query, preparation.Query())
			assertHeaderPlanParity(t, facts, preparation.Headers())
			assertOrderedHeaderFactsParity(t, facts.orderedHeaders, preparation.OrderedHeaders())
			assertCookieFactsParity(t, facts.cookies, preparation.Cookies())

			body := preparation.Body()
			assert.Equal(t, previewBodyKind(test.bodyKind), body.Kind())
			assert.Equal(t, test.bodyMedia, body.MediaType())
			assert.Equal(t, test.bodyRedact, body.Data().State() == PreparedValueRedacted)
			switch test.bodyKind {
			case requestBodyNone:
			case requestBodyText, requestBodyBytes:
				assert.Equal(t, test.publicBody, facts.body.valuePublic)
			case requestBodyForm:
				assert.Equal(t, test.formPublic, facts.body.formAllPublic())
			case requestBodyJSON, requestBodyXML, requestBodyYAML:
				assert.False(t, facts.body.valuePublic)
			case requestBodyReader, requestBodyMultipart:
				assert.False(t, facts.body.valuePublic)
			}
			if sourceReads != nil {
				assert.Zero(t, sourceReads.Load())
			}
		})
	}
}

func assertRequestPlanParity(t *testing.T, facts, delivery *requestPlan) {
	t.Helper()

	assert.Equal(t, facts.method, delivery.method)
	assert.Equal(t, facts.targetURL, delivery.targetURL)
	assert.Equal(t, facts.target, delivery.target)
	assert.Equal(t, facts.pathTrace, delivery.pathTrace)
	assert.Equal(t, facts.query, delivery.query)
	assert.Equal(t, facts.headers, delivery.headers)
	assert.Equal(t, facts.orderedHeaders, delivery.orderedHeaders)
	assert.Equal(t, facts.clientMetadata, delivery.clientMetadata)
	assert.Equal(t, facts.requestMetadata, delivery.requestMetadata)
	assert.Equal(t, facts.clientOrderedHeaders, delivery.clientOrderedHeaders)
	assert.Equal(t, facts.requestOrderedHeaders, delivery.requestOrderedHeaders)
	assert.Equal(t, facts.cookies, delivery.cookies)
	assert.Equal(t, facts.clientAuth, delivery.clientAuth)
	assert.Equal(t, facts.requestAuth, delivery.requestAuth)
	assert.Equal(t, facts.auth, delivery.auth)
	assert.Equal(t, facts.body, delivery.body)
}

func parityBuilder(client *Client) *RequestBuilder {
	ordered := orderedobject.New[[]string]().
		Set(":authority", []string{"private-ordered-authority"}).
		Set("X-Ordered", []string{"private-ordered-value"})

	return client.Post("https://example.test/items").
		PathValue(Public("/items")).
		Query("private", "private-query").
		QueryValue("public", Public("public-query")).
		Header("X-Private", "private-header").
		HeaderValue("X-Public", Public("public-header")).
		Cookie("private-cookie", "private-cookie").
		CookieValue("public-cookie", Public("public-cookie")).
		OrderedHeaders(ordered).
		Auth(BearerAuth{Token: "private-token"})
}

func assertQueryFactsParity(t *testing.T, facts []requestOccurrence, prepared []PreparedOccurrence) {
	t.Helper()
	groups := groupRequestOccurrences(facts)
	require.Equal(t, len(groups), len(prepared))
	for i, group := range groups {
		assert.Equal(t, group[0].name, prepared[i].name)
		values := prepared[i].values
		require.Equal(t, len(group), len(values), group[0].name)
		for j, occurrence := range group {
			assertPreparedValueMatchesOccurrence(t, occurrence.value, values[j])
		}
	}
}

func groupRequestOccurrences(values []requestOccurrence) [][]requestOccurrence {
	groups := make([][]requestOccurrence, 0)
	for _, value := range values {
		if len(groups) == 0 || groups[len(groups)-1][0].name != value.name {
			groups = append(groups, nil)
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], value)
	}
	return groups
}

func assertHeaderPlanParity(t *testing.T, facts *requestPlan, prepared []PreparedHeader) {
	t.Helper()
	expected := make(map[string]int)
	for _, header := range facts.headers {
		expected[http.CanonicalHeaderKey(header.name)]++
	}
	if facts.auth != nil {
		expected["Authorization"]++
	}
	actual := make(map[string]int)
	for _, header := range prepared {
		actual[http.CanonicalHeaderKey(header.name)] += len(header.values)
	}
	assert.Equal(t, expected, actual)
}

func assertOrderedHeaderFactsParity(
	t *testing.T,
	facts *orderedobject.Object[[]string],
	prepared []PreparedHeader,
) {
	t.Helper()
	if facts == nil {
		assert.Empty(t, prepared)
		return
	}
	entries := facts.Entries()
	require.Len(t, prepared, len(entries))
	for i, entry := range entries {
		assert.Equal(t, entry.Key, prepared[i].name)
		assert.Len(t, prepared[i].values, len(entry.Value))
	}
}

func assertCookieFactsParity(t *testing.T, facts []requestCookieOccurrence, prepared []PreparedCookie) {
	t.Helper()
	require.Len(t, prepared, len(facts))
	for i, occurrence := range facts {
		assert.Equal(t, occurrence.name, prepared[i].name)
		assertPreparedValueMatchesOccurrence(t, occurrence.value, prepared[i].value)
	}
}

func assertPreparedValueMatchesOccurrence(t *testing.T, source Value, projected PreparedString) {
	t.Helper()
	if source.isPublic() {
		assert.Equal(t, PreparedValuePresent, projected.state)
		assert.Equal(t, source.rawValue(), projected.value)
		return
	}
	assert.Equal(t, PreparedValueRedacted, projected.state)
	assert.Empty(t, projected.value)
}

type parityCountingReader struct {
	reads *atomic.Int64
}

func (r *parityCountingReader) Read([]byte) (int, error) {
	r.reads.Add(1)
	return 0, io.EOF
}
