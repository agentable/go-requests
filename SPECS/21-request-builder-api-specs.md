# Request Builder API Specs

## Overview

`RequestBuilder` defines one outbound HTTP request. This spec defines path, metadata, body, timeout, middleware, retry, and dispatch behavior.

## Builder Creation

A builder is created by either:

- `Client.NewRequestBuilder(method, path)`
- one of the verb helpers: `Get`, `Post`, `Delete`, `Put`, `Patch`, `Options`, `Head`, `Connect`, `Trace`, or `Request(method, path)`

A builder is mutable until `Send(ctx)` is called.

## Request Preview

`Preview(ctx)` returns `(*RequestPreview, error)` as a synchronous, detached
projection of the builder before delivery:

```go
preview, err := client.Post("/resources").
	Bytes(body).
	ContentType("application/json").
	Preview(ctx)
if err != nil {
	return err
}
```

Preview reads one client snapshot and does not send, freeze, or write back to
the builder. It is not an exact wire request, a post-middleware request, a
complete delivery validation, a prepared request, or a body that can be sent
later. Calling `Preview` does not prevent a later independent `Send` or
`SendStream` on the same builder.

`RequestPreview` exposes these facts through accessors:

- `Method` returns the effective method; an empty method is reported as `GET`.
- `Target` returns scheme and host plus a path value whose state is
  `omitted-by-policy`. `URL` returns a detached URL containing only safe scheme
  and host structure; userinfo, path, query, opaque data, and fragments are
  absent.
- `Query` returns sorted query keys and preserves each key's value multiplicity.
  Query values are `omitted-by-policy`.
- `Headers` returns semantic header names and values. Header values are
  `omitted-by-policy` except for the single `Header.Get("Content-Type")` value;
  additional same-name values remain omitted. `OrderedHeaders` preserves
  ordered-header intent and the same value policy; pseudo-headers remain
  intent metadata.
- `Cookies` returns cookie names in precedence order. Cookie values are always
  `omitted-by-policy`. Names derived from `Cookie` headers use the same
  acceptance, invalid-name/value filtering, and cookie-count behavior as a
  pure `http.Request.Cookies()` call. Explicit client and request-local
  `*http.Cookie` names are included only when `http.Cookie.Valid` accepts the
  name; Preview does not use `AddCookie` or serialization to sanitize them.
  This is intentionally not the delivery wire-valid cookie set: Preview
  validates and retains only a legal name for an explicit typed `*http.Cookie`
  and omits its value. Preview never consults a `CookieJar`; `Cookie` headers
  continue to be parsed with `http.Request.Cookies()` rules.
- `ContentType` returns the final semantic `Content-Type` header value. It is
  independent from `PreviewBody.MediaType`, which describes the selected body
  vocabulary before header precedence. When multipart's generated content type
  remains active, Preview omits this semantic value because it does not create
  the automatic boundary; client defaults and headers set before `Multipart`
  are not reported as final values. An explicit `ContentType` after `Multipart`
  remains the final semantic value.
- `Body` returns the selected body kind, presence, structural media type,
  static length/replayability facts, and an optional multipart
  manifest. Body values are `omitted-by-policy` whenever a body is selected.

`PreviewValue.State` distinguishes `unknown`, `omitted-by-policy`, and
`present`; `PreviewValue.Value` returns an empty string unless the state is
`present` (a present value itself may be empty). The
following body rules are fixed:

| Body selection | Preview result | Static validation and ownership |
| --- | --- | --- |
| No body | `PreviewBodyNone`, unknown presence | No body is opened or created |
| `Text`, `Bytes` | Kind, selection media type, present selection, omitted value; length is known from the in-memory input | Empty selections remain present; body is known replayable |
| URL-encoded form | Kind, selection media type, present selection, omitted value; length remains unknown | Preview does not execute form serialization; body is known replayable |
| `JSON`, `XML`, `YAML` | Kind, selection media type, present selection, omitted value; length is unknown and replayability is known | Encoder and input marshal hooks are not called; missing semantic `Content-Type` returns `ErrUnsupportedContentType` |
| Opaque `Reader` | Kind, optional media type, present selection, omitted value | `Read`, `ReadAt`, `Seek`, `Size`, and `Close` are not called; replayability and length remain unknown |
| `Multipart` | Selection media type and a detached manifest | No generated boundary, pipe, producer, part read, or part close; negative replay limits, invalid explicit boundaries, empty file fields, and nil/typed-nil part bodies are checked |

The multipart manifest sorts field names while preserving each field's value
multiplicity, preserves file-part order, and exposes only file field name,
filename, and explicit content type. An explicitly configured boundary is
present; an automatically generated boundary is unknown because Preview never
generates it. `Replayable(maxBytes)` is reflected as known replayability, but
replay-size overflow remains a delivery-time failure because Preview does not
read parts.

All returned slices, URLs, and nested manifests are detached. Accessors return
copies, and no result retains a builder, client, body source, auth method,
encoder, or other caller-owned collaborator.

Preview reports retained fluent preparation failures through a fixed-text,
Preview-only error before taking a client snapshot. It preserves `errors.Is`
for package-owned preparation classifications such as
`ErrInvalidConfigValue` and `ErrUnsupportedFormFieldsType`, but does not unwrap
arbitrary caller or library causes or expose them through `errors.As`. `Send`
and `SendStream` continue to return the original retained preparation cause. A
nil context returns `ErrRequestCreationFailed` without calling any context
method. A pre-canceled context or expired deadline preserves `context.Canceled`
or `context.DeadlineExceeded` and therefore the existing
`IsCanceled`/`IsTimeout` classifications. A request-local timeout is applied
only when the caller context has no deadline. Invalid method or URL preflight
returns `ErrRequestCreationFailed` without caller URL text; unsupported auth
implementations fail closed with `ErrInvalidConfigValue` without calling
`Valid` or `Apply`.

Preview never invokes middleware, cookie jars, retry or backoff callbacks,
redirect policies, proxy selectors, transports, loggers, body encoders,
readers, multipart producers, `Request.AddCookie`, or standard-library request
serialization. It does not replace the delivery lifecycle and has no caller
cleanup obligation.

## Preparation Errors

Fluent helpers that cannot return an error directly retain the first
preparation failure on the builder. This applies to `QueriesStruct`, `Form`,
`FormFields`, invalid or typed-nil request-local `Auth`, and nil entries passed
to `AddMiddleware`. It also applies to negative request-local timeout, retry
count, and buffered response limit values.

`Send` and `SendStream` return that original cause before taking a client
snapshot, deriving a context, opening or encoding a body, applying middleware,
or calling the transport. Later fluent calls do not replace the first cause.
Logging MAY report the failure but MUST NOT be its only caller-visible channel.

## Path and Query Construction

A builder MAY define:

- path replacement through `Path`, `PathParam`, `PathParams`, and `DelPathParam`
- query parameters through `Query`, `Queries`, `QueriesStruct`, and `DelQuery`

Path parameters use `{name}` placeholders and MUST be URL-path-escaped before dispatch.

Dispatch uses one resolver for base URL, request path, path params, absolute
URLs, and query composition. Base URL paths and request paths are joined without
accidental slash loss or duplication. For a relative request path, query values
from the base URL, request path, and builder are combined in that order. Values
are appended rather than replaced, so repeated keys remain repeated. An
absolute request URL overrides both the base path and base query.

Malformed request-path query syntax fails URL preflight with
`ErrRequestCreationFailed` before body preparation or transport dispatch.

## Request Metadata

A builder MAY define request-local metadata through:

- `Header`, `Headers`, `AddHeader`, `DelHeader`
- `OrderedHeaders`
- `Cookie`, `Cookies`, `DelCookie`
- `ContentType`, `Accept`, `UserAgent`, `Referer`
- `Auth`

Metadata applies in this order: client headers, client auth, request-local
headers, request-local auth. Each later layer replaces same-name values from
earlier layers, so Authorization has one unambiguous owner and value set.

Request-local headers override client default headers with the same header name, using case-insensitive header-name matching. Request-local `AddHeader` adds values within the request-local header set; it does not preserve an older client default value for that same header name.

Client and request-local cookies merge by cookie name. The request layer
replaces same-name defaults and preserves different-name defaults; repeated
cookies within one layer use the last value.

`OrderedHeaders` accepts an `orderedobject.Object[[]string]` where keys are header names and values are all values for each header. It sets request-local header values and preserves insertion order as request intent. Pseudo-headers are retained in ordered metadata for supporting HTTP/2 or HTTP/3 transports, but are not applied to `net/http` header maps.

When ordered headers are active, all request-local header helpers that mutate headers, including `Header`, `AddHeader`, `DelHeader`, `ContentType`, `Accept`, `UserAgent`, `Referer`, and body helpers that set `Content-Type`, MUST keep the ordered metadata in sync with the semantic `http.Header` values.

After auth and cookie precedence is applied, every existing non-pseudo ordered
entry is synchronized to the final semantic values. Pseudo-header entries stay
as intent and are never inserted into `http.Header`.

If a request-local plain header overrides a client ordered default without supplying request-local ordered metadata for that header, the client ordered metadata for that header is removed so supporting transports do not observe stale default values.

## Body Selection and Encoding

Each builder owns one body selection. Every body setter atomically replaces the
previous selection, so the last setter determines the outbound payload:

- `JSON`, `XML`, `YAML`, and `Text` select their named encoding.
- `Bytes` and `Reader` select a raw body.
- `Form`, `FormFields`, and `FormField` select a URL-encoded form.
- `Multipart` selects the multipart builder.

Repeated `FormField` and `FormFields` calls are additive while the current
selection is a form. The first such call after another body kind starts a new
form instead of reviving older fields.

`JSON`, `XML`, `YAML`, `Text`, form, and multipart generate their corresponding
content type. A later explicit request-header call owns `Content-Type` instead.
`Bytes` preserves an explicit caller header but removes a media type generated
by the body it replaces. `Reader` generates a media type only when its
`contentType` argument is non-empty and is one-shot unless the reader is
seekable and sized. Changing `Content-Type` after a typed setter does not change
which encoder that setter selected.

`Encoder.Encode` returns an `io.Reader` whose complete lifecycle contract is
reading. The request pipeline reads the value and MUST NOT discover or invoke
an additional `Close` method based on its dynamic type. Default and custom
encoders therefore cannot make cleanup an implicit second contract.

The builder does not infer content type from Go value shape. Non-raw encoded bodies without an explicit content type fail before dispatch.

`Form` and `FormFields` accept `url.Values`, `map[string][]string`,
`map[string]string`, or a struct encoded from `url` tags. They do not infer
multipart files from `map[string]any`.

`JSON(nil)` encodes JSON `null`. `Form(nil)` selects a real empty URL-encoded
form. `Multipart(nil)` records `ErrInvalidConfigValue` and fails before body or
transport work.

`Multipart` is the only multipart upload vocabulary. It supports fields, file readers, bytes, strings, explicit file metadata through `FilePart`, custom boundaries, and explicit retry buffering through `Replayable(maxBytes)`. Without `Replayable`, multipart bodies stream and are not replayable after the first transport attempt.

`FilePart.Body` is borrowed. The multipart writer reads it but MUST NOT close it
based on its dynamic type. The caller retains ownership of that nested source.
The outer `http.Request.Body` follows the standard transport contract and is
closed by the transport for each attempt or redirect hop.

> **Why**: One replaceable selection makes the fluent call order equal the
> payload order and prevents stale body sources or generated metadata from
> resurfacing.
>
> **Rejected**: Fixed source priority, merged multipart/form/arbitrary bodies,
> and a public generic `Body(any)` mode switch.

## Timeout and Retry Overrides

A builder MAY define request-local delivery policy through:

- `Timeout`
- `Retry`
- `NoRetry`

`Timeout` only creates a derived deadline when the provided context does not already have one.

Zero timeout means no request-local deadline. A negative timeout is invalid and
returns `ErrInvalidConfigValue` through the preparation-error contract.

`Retry(policy)` replaces the client retry policy for that request. `NoRetry()` disables retries even when the client has a positive default.

`RetryPolicy.Max` is the number of retries after the initial attempt. Zero means
no retries. A negative value is invalid and returns `ErrInvalidConfigValue`
before dispatch; private normalization remains defensive and is not the public
input contract.

Request bodies that can be replayed SHOULD be restored before each retry attempt. Non-replayable bodies MUST NOT be retried after the first attempt once delivery has started.

## Buffered Response Limit

`MaxResponseBodyBytes(maxBytes int64)` sets the ceiling for bytes buffered by
`Send` for one request. Zero means unlimited. A negative value is invalid and
follows the preparation-error contract.

The ceiling applies to the final response selected by retry and redirect
delivery. Exceeding it returns no partial `Response` and matches
`ErrResponseBodyTooLarge`. `SendStream` does not apply a positive buffering
ceiling because the caller owns streaming reads.

## Middleware and Streaming

A builder MAY attach request-local middleware with `AddMiddleware`.

`AddMiddleware` mutates the builder in place and does not return `*RequestBuilder`.

`Send(ctx)` returns a buffered `Response`. `SendStream(ctx)` returns an unbuffered `StreamResponse` whose body is owned by the caller.

## Dispatch

`Send(ctx)`:

1. returns any retained preparation error
2. snapshots client state
3. resolves the URL and asks `http.NewRequestWithContext` to validate the
   context, method, and resolved URL with no body
4. derives the request timeout context and only then opens or encodes the body
5. constructs the outbound `http.Request` and applies auth, headers, and cookies
6. executes middleware and retry policy
7. buffers the response within the request-local byte ceiling and returns a
   complete `Response`, or no response with `ErrResponseBodyTooLarge`

Invalid context, method, or resolved URL returns `ErrRequestCreationFailed`
before streaming producers start. URL-resolution and retained preparation
errors have the same no-open guarantee. The library does not read or close a
caller body source on these preflight failures.

URL preflight and terminal transport errors omit URL userinfo, query values,
and fragments from returned and logged diagnostics. `Preview` returns a fixed
`ErrRequestCreationFailed` for invalid method or URL preflight and does not
expose the underlying cause. `Send` and `SendStream` retain or wrap their
existing request-creation causes; terminal transport causes and standard
`errors.Is` / `errors.As` classifications remain inspectable.

`SendStream(ctx)` follows the same preparation and delivery path, but returns a `StreamResponse` without buffering the response body. The caller must close the stream response.

Client mutations after `Send` starts do not affect that in-flight request.

## Forbidden

- Do not chain `AddMiddleware`; it is a mutator, not a fluent builder method.
- Do not add a `Custom(path, method)` alias; arbitrary request creation is method-first through `Request(method, path)`.
- Do not assume `Timeout` overrides an existing context deadline.
- Do not add body aliases or content-type inference that obscure `JSON`, `XML`, `YAML`, `Text`, `Bytes`, `Reader`, form, and multipart ownership.

## Contract Invariants

- Builder creation and mutation boundaries are explicit.
- Body selection and generated content-type ownership are explicit.
- The retry-override rule and request body replay behavior are documented.
