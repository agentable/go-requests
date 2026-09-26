package requests

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/test-go/testify/require"
)

func TestRequestPlanSnapshotsResolvedTargetAndTaggedMetadata(t *testing.T) {
	client := newTestClient(t, WithBaseURL("https://example.test/api?base=one"))
	builder := client.Post("/items/{id}?request=two").
		PathParam("id", "a/b").
		Query("private", "secret").
		QueryValue("public", Public("approved")).
		HeaderValue("X-Request", Public("visible")).
		TextValue(Public("body"))

	plan, err := builder.compileRequestPlan()
	require.NoError(t, err)
	require.NotNil(t, plan.targetURL)
	assert.Equal(t, "POST", plan.method)
	assert.Equal(t, "/api/items/a%2Fb", plan.targetURL.EscapedPath())
	assert.Equal(t, "one", plan.targetURL.Query().Get("base"))
	assert.Equal(t, "two", plan.targetURL.Query().Get("request"))
	assert.Equal(t, "secret", plan.targetURL.Query().Get("private"))
	assert.Equal(t, "approved", plan.targetURL.Query().Get("public"))

	// Query occurrences follow url.Values.Encode's global key order; source
	// layers remain ordered only within the same key.
	assert.Equal(t, []string{"base", "private", "public", "request"}, requestOccurrenceNames(plan.query))
	for range 32 {
		repeated, err := builder.compileRequestPlan()
		require.NoError(t, err)
		assert.Equal(t, []string{"base", "private", "public", "request"}, requestOccurrenceNames(repeated.query))
	}
	assert.False(t, findRequestOccurrence(plan.query, "private").value.isPublic())
	assert.True(t, findRequestOccurrence(plan.query, "public").value.isPublic())
	assert.True(t, findRequestOccurrence(plan.headers, "X-Request").value.isPublic())
	assert.True(t, plan.body.valuePublic)

	trace := plan.pathTrace
	assert.False(t, trace.path.isPublic())
	param := findPathParamTrace(trace.params, "id")
	require.NotNil(t, param)
	assert.False(t, param.value.isPublic())
	assert.Equal(t, 1, param.pathOccurrences)
	assert.Zero(t, param.queryNameOccurrences)
	assert.Zero(t, param.schemeOccurrences)
	assert.Zero(t, param.authorityOccurrences)

	// Mutating the compiled snapshot must not write back to the builder.
	plan.target.setPath(Public("/changed"))
	plan.target.pathParams["id"] = Public("changed")
	plan.requestMetadata.queries[0].value = Public("changed")
	assert.Equal(t, "/items/{id}?request=two", builder.target.path.rawValue())
	assert.Equal(t, "a/b", builder.target.pathParams["id"].rawValue())
	assert.Equal(t, "secret", findRequestOccurrence(builder.metadata.queries, "private").value.rawValue())
	assert.False(t, findRequestOccurrence(builder.metadata.queries, "private").value.isPublic())
}

func TestRequestPlanQueryOccurrencesFollowResolvedKeyOrder(t *testing.T) {
	client := newTestClient(t, WithBaseURL(
		"https://example.test/api?b=base-b&z=base-z&a=base-a",
	))
	builder := client.Get("/items?b=request-b1&a=request-a&b=request-b2").
		QueryValue("z", Public("builder-z")).
		Query("a", "builder-a1").
		QueryValue("b", Public("builder-b")).
		Query("z", "builder-z2").
		QueryValue("a", Public("builder-a2"))

	plan, err := builder.compileRequestPlan()
	require.NoError(t, err)

	assert.Equal(t, []string{
		"a", "a", "a", "a",
		"b", "b", "b", "b",
		"z", "z", "z",
	}, requestOccurrenceNames(plan.query))
	values := make([]string, len(plan.query))
	public := make([]bool, len(plan.query))
	for i, occurrence := range plan.query {
		values[i] = occurrence.value.rawValue()
		public[i] = occurrence.value.isPublic()
	}
	assert.Equal(t, []string{
		"base-a", "request-a", "builder-a1", "builder-a2",
		"base-b", "request-b1", "request-b2", "builder-b",
		"base-z", "builder-z", "builder-z2",
	}, values)
	assert.Equal(t, []bool{
		false, false, false, true,
		false, false, false, true,
		false, true, false,
	}, public)

	// The occurrence order follows the resolved URL's url.Values.Encode
	// semantics: keys are sorted, while each key keeps base -> request-path ->
	// builder value order.
	assert.Equal(t, []string{"base-a", "request-a", "builder-a1", "builder-a2"}, plan.targetURL.Query()["a"])
	assert.Equal(t, []string{"base-b", "request-b1", "request-b2", "builder-b"}, plan.targetURL.Query()["b"])
	assert.Equal(t, []string{"base-z", "builder-z", "builder-z2"}, plan.targetURL.Query()["z"])
}

func TestRequestPlanPreservesCookieDisclosureAcrossPrecedence(t *testing.T) {
	client := newTestClient(t)
	client.metadata.addCookie("client-public", Public("client-value"))
	client.metadata.addCookie("overridden", Public("client-secret"))

	builder := client.Get("https://example.test").
		CookieValue("request-public", Public("request-value")).
		Cookie("overridden", "request-secret")

	plan, err := builder.compileRequestPlan()
	require.NoError(t, err)

	clientCookie := findCookieOccurrence(plan.cookies, "client-public")
	assert.True(t, clientCookie.value.isPublic())
	assert.Equal(t, "client-value", clientCookie.value.rawValue())

	requestCookie := findCookieOccurrence(plan.cookies, "request-public")
	assert.True(t, requestCookie.value.isPublic())
	assert.Equal(t, "request-value", requestCookie.value.rawValue())

	overriddenCookie := findCookieOccurrence(plan.cookies, "overridden")
	assert.False(t, overriddenCookie.value.isPublic())
	assert.Equal(t, "request-secret", overriddenCookie.value.rawValue())
}

func TestRequestPlanPathTraceRetainsSourceWithoutResolvedStringInference(t *testing.T) {
	builder := newTestClient(t).Get("https://example.test").
		PathValue(Public("https://{host}.example.test/items/{id}?{id}=value#fragment")).
		PathParam("id", "private-id").
		PathParam("host", "private-host")

	facts, err := builder.compileRequestPlan()
	require.NoError(t, err)

	trace := facts.pathTrace
	assert.Equal(t, "https://{host}.example.test/items/{id}?{id}=value#fragment", trace.path.rawValue())
	host := findPathParamTrace(trace.params, "host")
	require.NotNil(t, host)
	assert.Equal(t, 0, host.pathOccurrences)
	assert.Equal(t, 1, host.authorityOccurrences)
	id := findPathParamTrace(trace.params, "id")
	require.NotNil(t, id)
	assert.Equal(t, 1, id.pathOccurrences)
	assert.Equal(t, 1, id.queryNameOccurrences)
}

func TestRequestPlanPathParamValueTraceAndEscaping(t *testing.T) {
	path := "/items/{id}/{id}?q={id}#fragment-{id}"
	builder := newTestClient(t).Get(path).
		PathValue(Public(path)).
		PathParamValue("id", Public("a/b"))

	facts, err := builder.compileRequestPlan()
	require.NoError(t, err)
	assert.Equal(t, "/items/a%2Fb/a%2Fb", facts.targetURL.EscapedPath())

	param := findPathParamTrace(facts.pathTrace.params, "id")
	require.NotNil(t, param)
	assert.True(t, param.value.isPublic())
	assert.Equal(t, 2, param.pathOccurrences)
	assert.Zero(t, param.queryNameOccurrences)
	assert.Equal(t, 1, param.fragmentOccurrences)

	queryOnly := "/items?{id}=value#fragment-{id}"
	queryFacts, err := newTestClient(t).Get(queryOnly).
		PathValue(Public(queryOnly)).
		PathParamValue("id", Public("query-name")).
		compileRequestPlan()
	require.NoError(t, err)
	queryParam := findPathParamTrace(queryFacts.pathTrace.params, "id")
	require.NotNil(t, queryParam)
	assert.Zero(t, queryParam.pathOccurrences)
	assert.Equal(t, 1, queryParam.queryNameOccurrences)
	assert.Equal(t, 1, queryParam.fragmentOccurrences)

	unmatched := "/items/{missing}"
	unmatchedFacts, err := newTestClient(t).Get(unmatched).
		PathValue(Public(unmatched)).
		PathParamValue("unused", Public("ignored")).
		compileRequestPlan()
	require.NoError(t, err)
	unmatchedParam := findPathParamTrace(unmatchedFacts.pathTrace.params, "unused")
	require.NotNil(t, unmatchedParam)
	assert.Zero(t, unmatchedParam.pathOccurrences)
}

func TestRequestPlanPathTraceTracksBasePathProvenance(t *testing.T) {
	tests := []struct {
		name            string
		baseURL         string
		path            Value
		params          map[string]string
		requestAbsolute bool
		basePathActive  bool
	}{
		{
			name:           "non-empty base path taints relative target",
			baseURL:        "https://example.test/api",
			path:           Public("/items"),
			basePathActive: true,
		},
		{
			name:    "root base path does not taint",
			baseURL: "https://example.test/",
			path:    Public("/items"),
		},
		{
			name:    "empty base path does not taint",
			baseURL: "https://example.test",
			path:    Public("/items"),
		},
		{
			name:            "absolute target overrides base path",
			baseURL:         "https://example.test/api",
			path:            Public("https://other.test/items"),
			requestAbsolute: true,
		},
		{
			name:            "substitution can make target absolute",
			baseURL:         "https://example.test/api",
			path:            Public("{scheme}://other.test/items"),
			params:          map[string]string{"scheme": "https"},
			requestAbsolute: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			builder := newTestClient(t, WithBaseURL(test.baseURL)).Get(test.path.rawValue())
			builder.PathValue(test.path)
			for name, value := range test.params {
				builder.PathParam(name, value)
			}
			facts, err := builder.compileRequestPlan()
			require.NoError(t, err)
			assert.Equal(t, test.requestAbsolute, facts.pathTrace.requestAbsolute)
			assert.Equal(t, test.basePathActive, facts.pathTrace.basePathActive)
			assert.Equal(t, test.basePathActive, facts.pathTrace.basePathPrivate)
		})
	}
}

func TestDeliverySnapshotClonesFormWhileSharedFactsBorrowIt(t *testing.T) {
	form := url.Values{"private": {"secret"}}
	builder := newTestClient(t).Post("https://example.test").Form(form)
	borrowed := builder.body.form

	facts, err := builder.compileRequestPlan()
	require.NoError(t, err)
	facts.body.form.Set("private", "facts")
	assert.Equal(t, "facts", borrowed.Get("private"))
	borrowed.Set("private", "secret")

	delivery, err := builder.compileDeliverySnapshot()
	require.NoError(t, err)
	delivery.plan.body.form.Set("private", "changed")
	assert.Equal(t, "secret", borrowed.Get("private"))
}

func TestRequestPlanDoesNotCallDeliveryCollaborators(t *testing.T) {
	var authValidCalls atomic.Int64
	var authApplyCalls atomic.Int64
	var encoderCalls atomic.Int64
	var readerCalls atomic.Int64

	auth := &planPanicAuth{validCalls: &authValidCalls, applyCalls: &authApplyCalls}
	reader := &planCountingReader{calls: &readerCalls}
	encoder := countingEncoder{calls: &encoderCalls}
	client := newTestClient(t, WithJSONEncoder(encoder))
	builder := client.Post("https://example.test").
		Reader(reader, "application/octet-stream")
	// Bypass the fluent validation hook so this test proves compile itself does
	// not call an unknown AuthMethod collaborator.
	builder.auth = auth

	assert.NotPanics(t, func() {
		plan, err := builder.compileRequestPlan()
		require.NoError(t, err)
		require.NotNil(t, plan)
	})
	assert.Zero(t, authValidCalls.Load())
	assert.Zero(t, authApplyCalls.Load())
	assert.Zero(t, encoderCalls.Load())
	assert.Zero(t, readerCalls.Load())
	assert.Same(t, auth, builder.auth)
	assert.Same(t, reader, builder.body.value)
}

func TestRequestPlanMapsMethodAndURLFailuresToCreationSentinel(t *testing.T) {
	tests := []struct {
		name    string
		builder *RequestBuilder
	}{
		{
			name:    "invalid method",
			builder: newTestClient(t).Request("bad method", "https://user:password@example.test/items?secret=value"),
		},
		{
			name:    "invalid query escape with builder value",
			builder: newTestClient(t).Get("https://user:password@example.test/items?secret=%zz").Query("other", "value"),
		},
		{
			name:    "invalid relative query without base URL",
			builder: newTestClient(t).Get("/items?secret=%zz"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := test.builder.compileRequestPlan()
			assert.Nil(t, plan)
			assert.ErrorIs(t, err, ErrRequestCreationFailed)
			assert.NotContains(t, err.Error(), "password")
			assert.NotContains(t, err.Error(), "secret")
		})
	}
}

func TestRequestPlanKeepsTypedAndReaderSourcesBorrowed(t *testing.T) {
	typed := &struct{ Name string }{Name: "typed"}
	reader := &planCountingReader{}
	builder := newTestClient(t).Post("https://example.test").JSON(typed)

	plan, err := builder.compileRequestPlan()
	require.NoError(t, err)
	assert.Same(t, typed, plan.body.value)

	builder.Reader(reader, "application/octet-stream")
	plan, err = builder.compileRequestPlan()
	require.NoError(t, err)
	assert.Same(t, reader, plan.body.value)
}

func TestRequestPlanCompilationDefersStaticBodyValidation(t *testing.T) {
	builder := newTestClient(t).
		Post("https://example.test").
		JSON(struct{}{}).
		DelHeader("Content-Type")

	facts, err := builder.compileRequestPlan()
	require.NoError(t, err)
	require.NotNil(t, facts)
	assert.Equal(t, requestBodyJSON, facts.body.kind)
}

func TestRequestPreviewPreservesAuthBeforeStaticBodyError(t *testing.T) {
	var validCalls atomic.Int64
	builder := newTestClient(t).Post("https://example.test").
		JSON(struct{}{}).
		DelHeader("Content-Type")
	builder.auth = &planPanicAuth{validCalls: &validCalls}

	preview, err := builder.Preview(t.Context())
	assert.Nil(t, preview)
	assert.ErrorIs(t, err, ErrInvalidConfigValue)
	assert.NotErrorIs(t, err, ErrUnsupportedContentType)
	assert.Zero(t, validCalls.Load())
}

func TestRequestPreviewPlan(t *testing.T) {
	var readerCalls atomic.Int64
	reader := &planCountingReader{calls: &readerCalls}
	preview, err := newTestClient(t).Post("https://example.test/items?secret=value").
		QueryValue("approved", Public("yes")).
		HeaderValue("X-Approved", Public("yes")).
		Reader(reader, "application/octet-stream").
		Preview(context.Background())

	require.NoError(t, err)
	require.NotNil(t, preview)
	assert.Equal(t, "POST", preview.Method())
	assert.Equal(t, "https", preview.Target().Scheme())
	assert.Equal(t, "example.test", preview.Target().Host())
	assert.Equal(t, PreviewValueOmitted, preview.Target().Path().State())
	assert.Equal(t, []string{"approved", "secret"}, previewQueryKeys(preview.Query()))
	assert.Equal(t, PreviewValueOmitted, preview.Query().Entries()[0].Values()[0].State())
	assert.Equal(t, PreviewValueOmitted, preview.Query().Entries()[1].Values()[0].State())
	assert.Equal(t, PreviewBodyReader, preview.Body().Kind())
	assert.Equal(t, PreviewValueOmitted, preview.Body().Value().State())
	assert.Zero(t, readerCalls.Load())
}

func requestOccurrenceNames(values []requestOccurrence) []string {
	names := make([]string, len(values))
	for i, value := range values {
		names[i] = value.name
	}
	return names
}

func previewQueryKeys(values PreviewQueries) []string {
	entries := values.Entries()
	keys := make([]string, len(entries))
	for i, entry := range entries {
		keys[i] = entry.Key()
	}
	return keys
}

func findPathParamTrace(values []requestPathParamTrace, name string) *requestPathParamTrace {
	for i := range values {
		if values[i].name == name {
			return &values[i]
		}
	}
	return nil
}

type planPanicAuth struct {
	validCalls *atomic.Int64
	applyCalls *atomic.Int64
}

func (a planPanicAuth) Valid() bool {
	a.validCalls.Add(1)
	panic("request plan called AuthMethod.Valid")
}

func (a planPanicAuth) Apply(*http.Request) {
	a.applyCalls.Add(1)
	panic("request plan called AuthMethod.Apply")
}

type planCountingReader struct {
	calls *atomic.Int64
}

func (r *planCountingReader) Read([]byte) (int, error) {
	r.calls.Add(1)
	return 0, io.EOF
}
