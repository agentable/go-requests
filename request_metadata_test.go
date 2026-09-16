package requests

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/agentable/go-orderedobject"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaggedMetadataLegacyAndOwnerOccurrences(t *testing.T) {
	builder := newTestClient(t).
		Get("/items/{id}").
		PathParam("id", "legacy-id").
		Query("legacy", "legacy-query").
		Header("X-Legacy", "legacy-header").
		Cookie("legacy-cookie", "legacy-value")

	assert.False(t, builder.target.path.isPublic())
	assert.False(t, builder.target.pathParams["id"].isPublic())
	assert.False(t, findRequestOccurrence(builder.metadata.queries, "legacy").value.isPublic())
	assert.False(t, findRequestOccurrence(builder.metadata.headers, "X-Legacy").value.isPublic())
	assert.False(t, findCookieOccurrence(builder.metadata.cookies, "legacy-cookie").value.isPublic())

	builder.PathValue(Public("/items/public-id")).
		QueryValue("owner-query", Public("owner-value")).
		HeaderValue("X-Owner", Public("owner-header")).
		AddHeaderValue("X-Owner", Public("owner-header-2")).
		CookieValue("owner-cookie", Public("owner-cookie-value"))

	assert.True(t, builder.target.path.isPublic())
	assert.True(t, findRequestOccurrence(builder.metadata.queries, "owner-query").value.isPublic())
	assert.True(t, findRequestOccurrence(builder.metadata.headers, "X-Owner").value.isPublic())
	assert.True(t, findRequestOccurrenceAt(builder.metadata.headers, "X-Owner", 1).value.isPublic())
	assert.True(t, findCookieOccurrence(builder.metadata.cookies, "owner-cookie").value.isPublic())
}

func TestTaggedMetadataPathParamValueAndDelete(t *testing.T) {
	builder := newTestClient(t).Get("/items/{id}/{empty}/{legacy}").
		PathParamValue("id", Public("public-id")).
		PathParamValue("empty", Public(""))

	assert.True(t, builder.target.pathParams["id"].isPublic())
	assert.Equal(t, "public-id", builder.target.pathParams["id"].rawValue())
	assert.True(t, builder.target.pathParams["empty"].isPublic())
	assert.Empty(t, builder.target.pathParams["empty"].rawValue())

	builder.PathParam("legacy", "private")
	assert.False(t, builder.target.pathParams["legacy"].isPublic())
	builder.PathParams(map[string]string{"batch": "private-batch"})
	assert.False(t, builder.target.pathParams["batch"].isPublic())

	var zero Value
	builder.PathParamValue("id", zero)
	assert.False(t, builder.target.pathParams["id"].isPublic())

	builder.PathParamValue("id", Public("replacement"))
	builder.DelPathParam("id", "empty", "legacy", "batch")
	assert.Empty(t, builder.target.pathParams)
}

func TestTaggedMetadataSetAddDeletePrecedence(t *testing.T) {
	builder := newTestClient(t).
		Get("/resource").
		HeaderValue("X-Value", Public("public-first")).
		AddHeaderValue("X-Value", Public("public-second")).
		Header("X-Value", "private-replacement").
		QueryValue("keep", Public("one")).
		Query("drop", "private").
		CookieValue("keep", Public("public-cookie")).
		Cookie("drop", "private-cookie")

	values := requestOccurrencesByName(builder.metadata.headers, "X-Value")
	require.Len(t, values, 1)
	assert.False(t, values[0].value.isPublic())
	assert.Equal(t, "private-replacement", values[0].value.rawValue())

	builder.DelQuery("drop").DelCookie("drop")
	assert.Empty(t, requestOccurrencesByName(builder.metadata.queries, "drop"))
	assert.Empty(t, requestCookiesByName(builder.metadata.cookies, "drop"))

	builder.DelHeader("X-Value")
	assert.Empty(t, requestOccurrencesByName(builder.metadata.headers, "X-Value"))
}

func TestTaggedMetadataBatchInputsArePrivateAndShareContainer(t *testing.T) {
	builder := newTestClient(t).
		Get("/resource").
		Queries(url.Values{"q": {"one", "two"}}).
		Headers(http.Header{"X-Batch": {"one", "two"}}).
		Cookies(map[string]string{"cookie-a": "a"})

	assert.Equal(t, []string{"one", "two"}, requestOccurrenceValues(builder.metadata.queries, "q"))
	assert.Equal(t, []string{"one", "two"}, requestOccurrenceValues(builder.metadata.headers, "X-Batch"))
	for _, occurrence := range builder.metadata.queries {
		assert.False(t, occurrence.value.isPublic())
	}
	for _, occurrence := range builder.metadata.headers {
		assert.False(t, occurrence.value.isPublic())
	}
	for _, occurrence := range builder.metadata.cookies {
		assert.False(t, occurrence.value.isPublic())
	}
	assert.Equal(t, "a", builder.metadata.cookieValues()[0].Value)
}

func TestTaggedClientMetadataHeaderReplacementPreservesCookies(t *testing.T) {
	client := newTestClient(t,
		WithCookies(map[string]string{"session": "cookie-secret"}),
		WithHeaders(http.Header{"X-Default": {"header-secret"}}),
	)

	snapshot := client.snapshot()
	require.Len(t, snapshot.metadata.cookies, 1)
	assert.Equal(t, "session", snapshot.metadata.cookies[0].name)
	assert.False(t, snapshot.metadata.cookies[0].value.isPublic())
	assert.Len(t, snapshot.metadata.headers, 1)
	assert.False(t, snapshot.metadata.headers[0].value.isPublic())
}

func TestTaggedOrderedHeadersKeepPseudoHeadersOutOfSemanticMetadata(t *testing.T) {
	ordered := orderedobject.New[[]string]().
		Set(":authority", []string{"example.com"}).
		Set("X-Visible", []string{"value"})

	builder := newTestClient(t).Get("https://example.com").OrderedHeaders(ordered)
	assert.Equal(t, []string{":authority", "X-Visible"}, builder.orderedHeaders.Keys())
	assert.Empty(t, builder.metadata.headerValues().Values(":authority"))
	assert.Equal(t, []string{"value"}, builder.metadata.headerValues().Values("X-Visible"))

	client := newTestClient(t, WithOrderedHeaders(ordered))
	snapshot := client.snapshot()
	assert.Equal(t, []string{":authority", "X-Visible"}, snapshot.orderedHeaders.Keys())
	assert.Empty(t, snapshot.metadata.headerValues().Values(":authority"))
	assert.Equal(t, []string{"value"}, snapshot.metadata.headerValues().Values("X-Visible"))
}

func TestTaggedMetadataSnapshotIsDetached(t *testing.T) {
	builder := newTestClient(t).Get("/resource").
		Query("legacy", "legacy-value").
		Header("X-Legacy", "legacy-value").
		Cookie("legacy", "legacy-value")

	snapshot := snapshotBuilderMetadata(builder)
	snapshot.queries[0].value = Public("snapshot-query")
	snapshot.headers[0].value = Public("snapshot-header")
	snapshot.cookies[0].value = Public("snapshot-cookie")

	assert.Equal(t, "legacy-value", builder.metadata.queries[0].value.rawValue())
	assert.False(t, builder.metadata.queries[0].value.isPublic())
	assert.Equal(t, "legacy-value", builder.metadata.headers[0].value.rawValue())
	assert.False(t, builder.metadata.headers[0].value.isPublic())
	assert.Equal(t, "legacy-value", builder.metadata.cookies[0].value.rawValue())
	assert.False(t, builder.metadata.cookies[0].value.isPublic())
}

func TestTaggedMetadataPathValueDoesNotChangeResolverSemantics(t *testing.T) {
	builder := newTestClient(t, WithBaseURL("https://example.com/api?base=one")).
		Get("/items/{id}?request=two").
		PathParam("id", "a/b").
		Query("builder", "three")

	resolved, err := resolveRequestURL(
		"https://example.com/api?base=one",
		builder.target.resolvedPath(),
		builder.metadata.queryValues(),
	)
	require.NoError(t, err)
	assert.Equal(t, "/api/items/a%2Fb", resolved.EscapedPath())
	assert.Equal(t, url.Values{
		"base":    {"one"},
		"request": {"two"},
		"builder": {"three"},
	}, resolved.Query())

	builder.PathValue(Public("/public/items")).PathParam("unused", "secret")
	assert.Equal(t, "/public/items", builder.target.resolvedPath())
}

func TestCredentialRequestHeadersRemainPrivateCandidates(t *testing.T) {
	assert.True(t, isCredentialRequestHeader("Authorization"))
	assert.True(t, isCredentialRequestHeader("authorization"))
	assert.True(t, isCredentialRequestHeader("Proxy-Authorization"))
	assert.True(t, isCredentialRequestHeader("proxy-authorization"))
	assert.False(t, isCredentialRequestHeader("X-Request-ID"))
}

func TestBuiltInAuthShapeDoesNotCallAuthMethods(t *testing.T) {
	tests := []struct {
		name string
		auth AuthMethod
		ok   bool
	}{
		{name: "basic value", auth: BasicAuth{Username: "user", Password: "pass"}, ok: true},
		{name: "basic pointer", auth: &BasicAuth{Username: "user", Password: "pass"}, ok: true},
		{name: "bearer value", auth: BearerAuth{Token: "token"}, ok: true},
		{name: "bearer pointer", auth: &BearerAuth{Token: "token"}, ok: true},
		{name: "custom value", auth: CustomAuth{Header: "Custom value"}, ok: true},
		{name: "custom pointer", auth: &CustomAuth{Header: "Custom value"}, ok: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			shape, recognized, err := builtInAuthShape(test.auth)
			require.NoError(t, err)
			assert.Equal(t, test.ok, recognized)
			assert.NotEqual(t, builtInAuthUnknown, shape.kind)
		})
	}

	shape, recognized, err := builtInAuthShape(&BasicAuth{})
	require.ErrorIs(t, err, ErrInvalidConfigValue)
	assert.True(t, recognized)
	assert.Equal(t, builtInAuthUnknown, shape.kind)
}

func findRequestOccurrence(occurrences []requestOccurrence, name string) requestOccurrence {
	return findRequestOccurrenceAt(occurrences, name, 0)
}

func findRequestOccurrenceAt(occurrences []requestOccurrence, name string, index int) requestOccurrence {
	matching := requestOccurrencesByName(occurrences, name)
	return matching[index]
}

func requestOccurrencesByName(occurrences []requestOccurrence, name string) []requestOccurrence {
	result := make([]requestOccurrence, 0)
	for _, occurrence := range occurrences {
		if equalHeaderName(occurrence.name, name) {
			result = append(result, occurrence)
		}
	}
	return result
}

func requestOccurrenceValues(occurrences []requestOccurrence, name string) []string {
	matching := requestOccurrencesByName(occurrences, name)
	values := make([]string, len(matching))
	for i, occurrence := range matching {
		values[i] = occurrence.value.rawValue()
	}
	return values
}

func findCookieOccurrence(occurrences []requestCookieOccurrence, name string) requestCookieOccurrence {
	return requestCookiesByName(occurrences, name)[0]
}

func requestCookiesByName(occurrences []requestCookieOccurrence, name string) []requestCookieOccurrence {
	result := make([]requestCookieOccurrence, 0)
	for _, occurrence := range occurrences {
		if occurrence.name == name {
			result = append(result, occurrence)
		}
	}
	return result
}
