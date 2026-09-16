package requests

import (
	"net/http"
	"net/url"
	"strings"
)

// requestOccurrence is the common tagged representation for named metadata.
// The name is structural; the value carries its disclosure decision.
type requestOccurrence struct {
	name  string
	value Value
}

type requestCookieOccurrence struct {
	name   string
	value  Value
	cookie *http.Cookie
}

type requestMetadata struct {
	queries []requestOccurrence
	headers []requestOccurrence
	cookies []requestCookieOccurrence
}

type requestTarget struct {
	path       Value
	pathParams map[string]Value
}

func privateRequestValue(value string) Value {
	return Value{value: value}
}

func newRequestTarget(path string) requestTarget {
	return requestTarget{path: privateRequestValue(path)}
}

func (t *requestTarget) setPath(value Value) {
	t.path = value
}

func (t *requestTarget) setPathParam(name, value string) {
	t.setPathParamValue(name, privateRequestValue(value))
}

func (t *requestTarget) setPathParamValue(name string, value Value) {
	if t.pathParams == nil {
		t.pathParams = make(map[string]Value)
	}
	t.pathParams[name] = value
}

func (t *requestTarget) setPathParams(values map[string]string) {
	if len(values) == 0 {
		return
	}
	if t.pathParams == nil {
		t.pathParams = make(map[string]Value, len(values))
	}
	for name, value := range values {
		t.pathParams[name] = privateRequestValue(value)
	}
}

func (t *requestTarget) deletePathParams(names ...string) {
	if t.pathParams == nil {
		return
	}
	for _, name := range names {
		delete(t.pathParams, name)
	}
}

func (t requestTarget) resolvedPath() string {
	path := t.path.rawValue()
	for name, value := range t.pathParams {
		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(value.rawValue()))
	}
	return path
}

func (m *requestMetadata) addQuery(name string, value Value) {
	m.queries = append(m.queries, requestOccurrence{name: name, value: value})
}

func (m *requestMetadata) deleteQueries(names ...string) {
	if len(m.queries) == 0 || len(names) == 0 {
		return
	}
	deleteNames := make(map[string]struct{}, len(names))
	for _, name := range names {
		deleteNames[name] = struct{}{}
	}
	kept := m.queries[:0]
	for _, occurrence := range m.queries {
		if _, deleted := deleteNames[occurrence.name]; !deleted {
			kept = append(kept, occurrence)
		}
	}
	m.queries = kept
}

func (m *requestMetadata) queryValues() url.Values {
	values := make(url.Values, len(m.queries))
	for _, occurrence := range m.queries {
		values[occurrence.name] = append(values[occurrence.name], occurrence.value.rawValue())
	}
	return values
}

func (m *requestMetadata) setHeader(name string, value Value) {
	m.deleteHeaders(name)
	m.headers = append(m.headers, requestOccurrence{
		name:  canonicalRequestHeaderName(name),
		value: value,
	})
}

func (m *requestMetadata) addHeader(name string, value Value) {
	m.headers = append(m.headers, requestOccurrence{
		name:  canonicalRequestHeaderName(name),
		value: value,
	})
}

func (m *requestMetadata) setHeaders(headers http.Header) {
	for name, values := range headers {
		m.deleteHeaders(name)
		for _, value := range values {
			m.addHeader(name, privateRequestValue(value))
		}
	}
}

func (m *requestMetadata) deleteHeaders(names ...string) {
	if len(m.headers) == 0 || len(names) == 0 {
		return
	}
	kept := m.headers[:0]
	for _, occurrence := range m.headers {
		deleted := false
		for _, name := range names {
			if equalHeaderName(occurrence.name, name) {
				deleted = true
				break
			}
		}
		if !deleted {
			kept = append(kept, occurrence)
		}
	}
	m.headers = kept
}

func (m *requestMetadata) headerValues() http.Header {
	values := make(http.Header, len(m.headers))
	for _, occurrence := range m.headers {
		values.Add(occurrence.name, occurrence.value.rawValue())
	}
	return values
}

func (m *requestMetadata) addCookie(name string, value Value) {
	m.cookies = append(m.cookies, requestCookieOccurrence{
		name:   name,
		value:  value,
		cookie: &http.Cookie{Name: name, Value: value.rawValue()}, //nolint:gosec // callers control request cookie attributes
	})
}

func (m *requestMetadata) deleteCookies(names ...string) {
	if len(m.cookies) == 0 || len(names) == 0 {
		return
	}
	deleteNames := stringSet(names)
	kept := m.cookies[:0]
	for _, occurrence := range m.cookies {
		if _, deleted := deleteNames[occurrence.name]; !deleted {
			kept = append(kept, occurrence)
		}
	}
	m.cookies = kept
}

func (m *requestMetadata) cookieValues() []*http.Cookie {
	if len(m.cookies) == 0 {
		return nil
	}
	values := make([]*http.Cookie, len(m.cookies))
	for i, occurrence := range m.cookies {
		cookie := new(http.Cookie) //nolint:gosec // This is an outbound request cookie; response security attributes do not apply.
		if occurrence.cookie != nil {
			*cookie = *occurrence.cookie
			cookie.Unparsed = append([]string(nil), occurrence.cookie.Unparsed...)
		}
		cookie.Name = occurrence.name
		cookie.Value = occurrence.value.rawValue()
		values[i] = cookie
	}
	return values
}

func (m *requestMetadata) clone() requestMetadata {
	clone := requestMetadata{
		queries: append([]requestOccurrence(nil), m.queries...),
		headers: append([]requestOccurrence(nil), m.headers...),
	}
	if len(m.cookies) > 0 {
		clone.cookies = make([]requestCookieOccurrence, len(m.cookies))
		for i, occurrence := range m.cookies {
			clone.cookies[i] = occurrence
			if occurrence.cookie != nil {
				cookie := *occurrence.cookie //nolint:gosec // This is an outbound request cookie clone.
				cookie.Unparsed = append([]string(nil), occurrence.cookie.Unparsed...)
				clone.cookies[i].cookie = &cookie
			}
		}
	}
	return clone
}

func canonicalRequestHeaderName(name string) string {
	if canonical := http.CanonicalHeaderKey(name); canonical != "" {
		return canonical
	}
	return name
}

func equalHeaderName(left, right string) bool {
	return strings.EqualFold(left, right)
}

// isCredentialRequestHeader identifies request headers whose values must not
// be disclosed through an owner-tagged metadata path.
func isCredentialRequestHeader(name string) bool {
	return equalHeaderName(name, "Authorization") ||
		equalHeaderName(name, "Proxy-Authorization")
}
