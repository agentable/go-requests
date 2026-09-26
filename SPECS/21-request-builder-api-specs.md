# Request Builder API Specs

## Overview

`RequestBuilder` defines one outbound HTTP request. This spec defines path,
metadata, body, timeout, middleware, retry, and dispatch behavior.

## Builder Creation

A builder is created by either:

- `Client.NewRequestBuilder(method, path)`
- one of the verb helpers: `Get`, `Post`, `Delete`, `Put`, `Patch`, `Options`,
  `Head`, `Connect`, `Trace`, or `Request(method, path)`

A builder remains mutable and reusable. It is a single-owner object; callers
MUST NOT mutate a builder concurrently with `Preview`, `Prepare`, `Send`, or
`SendStream`. Each exit snapshots its inputs, and later mutations do not alter
an in-flight exit.

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
  absent. Scheme and authority are structural safety exceptions and may be
  projected by Prepare in the same resolved spelling (authority excludes
  userinfo); the no-splitting rule applies to private pathname, query, fragment,
  and userinfo data.
- `Query` returns sorted query keys and preserves each key's value multiplicity.
  Query values are `omitted-by-policy`.
- `Headers` returns semantic header names and values. Header values are
  `omitted-by-policy` except for the single `Header.Get("Content-Type")` value;
  additional same-name values remain omitted. `OrderedHeaders` preserves
  ordered-header intent and the same value policy; pseudo-headers remain intent
  metadata.
- `Cookies` returns cookie names in precedence order. Cookie values are always
  `omitted-by-policy`. Names derived from `Cookie` headers use the same
  acceptance, invalid-name/value filtering, and cookie-count behavior as a pure
  `http.Request.Cookies()` call. Explicit client and request-local
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
  static length/replayability facts, and an optional multipart manifest. Body
  values are `omitted-by-policy` whenever a body is selected.

`PreviewValue.State` distinguishes `unknown`, `omitted-by-policy`, and
`present`; `PreviewValue.Value` returns an empty string unless the state is
`present` (a present value itself may be empty). The following body rules are
fixed:

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
for package-owned preparation classifications such as `ErrInvalidConfigValue`
and `ErrUnsupportedFormFieldsType`, but does not unwrap arbitrary caller or
library causes or expose them through `errors.As`. `Send` and `SendStream`
continue to return the original retained preparation cause. A nil context
returns `ErrRequestCreationFailed` without calling any context method. A
pre-canceled context or expired deadline preserves `context.Canceled` or
`context.DeadlineExceeded`. Invalid method or URL preflight returns
`ErrRequestCreationFailed` without caller URL text; unknown/caller-defined auth
implementations fail closed with `ErrInvalidConfigValue` without calling
`Valid` or `Apply` for Preview. Prepare uses its distinct
`ErrPreparationNotPreparable` classification for the same unknown shape. Preview never calls unknown/caller-defined
`AuthMethod.Valid` or `AuthMethod.Apply`.

Preview never invokes middleware, cookie jars, retry or backoff callbacks,
redirect policies, proxy selectors, transports, loggers, body encoders,
readers, multipart producers, `Request.AddCookie`, or standard-library request
serialization. The shared facts compiler may use a no-body
`http.NewRequest` only as a method/URL preflight validator; it does not serialize
body, headers, cookies, or any caller source, and it never exposes that internal
request. Preview does not replace the delivery lifecycle and has no caller
cleanup obligation.

## Request Preparation

`Prepare(ctx, opts)` is the retained-byte-limited, no-send projection for callers that need
selected request data before deciding whether to deliver it:

```go
preparation, err := client.Post("/resources").
	TextValue(Public("already-approved body")).
	Prepare(ctx, PrepareOptions{MaxPreparedBodyBytes: 1 << 20})
```

The public shape is:

```go
type PrepareOptions struct { MaxPreparedBodyBytes int64 }

func (b *RequestBuilder) Prepare(
	ctx context.Context,
	opts PrepareOptions,
) (*RequestPreparation, error)
```

`MaxPreparedBodyBytes` MUST be non-negative; zero means that no body bytes may be
retained, and there is no unbounded preparation mode. The budget applies only to
bytes retained in a present `PreparedBody.Data`: public Text/Bytes with known
size are rejected before inspection when over the limit; an all-public form uses
the existing `url.Values.Encode` ordering and rejects the encoded result when its
length exceeds the retained budget. The budget is not a transient-allocation
limit. Legacy/private/redacted bodies and typed
JSON/XML/YAML bodies are never opened or encoded and do not consume this retained
byte budget, and multipart manifest metadata is not body data. The first version
has no unknown-size retained body source; opaque readers remain structural and
redacted without being read.
The budget does not claim to bound allocations made by the caller before
`PublicPayload`.

Preparation has four phases:

1. exit-specific preflight validates the receiver, retained errors, context, and
   options subject to the required safety order below;
2. `compile` snapshots builder/client-owned values and metadata into one private
   request plan and performs method/URL preflight first; the Prepare/Send exit
   then performs static semantic body validation without recompiling facts;
3. `optional inspect` reads only library-owned memory snapshots whose data was
   explicitly marked Public (through `TextValue` or `BytesPayload`, where the
   latter receives an owned `PublicPayload`), or all-Public form bytes, subject
   to the budget; typed JSON/XML/YAML bodies remain structural and are never
   encoded;
4. `sanitized project` returns detached values carrying their disclosure state.

Preparation has zero root-library delivery activity. It MUST NOT invoke
middleware, cookie jars, retry/backoff callbacks, redirect policies, proxy
selectors, loggers, transports, response handling, or standard-library request
serialization. Unknown/caller-defined `AuthMethod` implementations are not
preparable in this version and are rejected during Prepare before it calls
`Valid` or `Apply`. Opaque one-shot readers remain structural body selections:
Prepare returns redacted body data and calls neither `Read`, `ReadAt`, `Seek`, nor
`Close`. Prepare never calls
an encoder or typed value marshal hook. Multipart
preparation is always structural: it does not create a boundary, producer, or
part reader, and its body data is redacted. The package's `BasicAuth`,
`BearerAuth`, and `CustomAuth` are built-in auth shapes. Both their value and
non-nil pointer forms are recognized; their current non-empty field invariants
are checked directly, without calling methods. Valid shapes contribute only a
redacted Authorization occurrence without calling `Valid` or `Apply`.

`RequestPreparation` is detached and immutable. Its exact accessor surface is
defined by this specification.
It contains only method, target/path, ordered query
occurrences, semantic and ordered headers, cookies, body kind/media/data, and a
structural multipart manifest. It MUST NOT contain a raw URL, `url.URL`, URL
reassembly method, `*http.Request`, reader, `GetBody`, length, replayability,
factory, source, cleanup handle, or send capability.

The detached DTO surface is:

```go
type PreparedValueState string

const (
	PreparedValueRedacted PreparedValueState = "redacted"
	PreparedValuePresent PreparedValueState = "present"
)

type PreparedString struct { /* private */ }
func (v PreparedString) State() PreparedValueState
func (v PreparedString) Value() string

type PreparedBytes struct { /* private */ }
func (v PreparedBytes) State() PreparedValueState
func (v PreparedBytes) Bytes() []byte

type PreparedTarget struct { /* private */ }
func (t PreparedTarget) Scheme() string // empty when not applicable
func (t PreparedTarget) Authority() string // empty when not applicable; no userinfo
func (t PreparedTarget) Path() PreparedString

type PreparedOccurrence struct { /* private */ }
func (o PreparedOccurrence) Name() string
func (o PreparedOccurrence) Values() []PreparedString

type PreparedHeader struct { /* private */ }
func (h PreparedHeader) Name() string
func (h PreparedHeader) Values() []PreparedString

type PreparedCookie struct { /* private */ }
func (c PreparedCookie) Name() string
func (c PreparedCookie) Value() PreparedString

type PreparedBody struct { /* private */ }
func (b PreparedBody) Kind() PreviewBodyKind
func (b PreparedBody) MediaType() string
func (b PreparedBody) Data() PreparedBytes
func (b PreparedBody) Multipart() *PreparedMultipart

type PreparedMultipart struct { /* private */ }
func (m *PreparedMultipart) Fields() []PreparedOccurrence
func (m *PreparedMultipart) Files() []PreparedMultipartFile

type PreparedMultipartFile struct { /* private */ }
func (f PreparedMultipartFile) Field() string

type RequestPreparation struct { /* private */ }
func (p *RequestPreparation) Method() string
func (p *RequestPreparation) Target() PreparedTarget
func (p *RequestPreparation) Query() []PreparedOccurrence
func (p *RequestPreparation) Headers() []PreparedHeader
func (p *RequestPreparation) OrderedHeaders() []PreparedHeader
func (p *RequestPreparation) Cookies() []PreparedCookie
func (p *RequestPreparation) Body() PreparedBody
```

`PreparedBody.Kind` intentionally reuses `PreviewBodyKind`: both projections report
the same selected body vocabulary, while their value/disclosure contracts remain
separate. This is a shared vocabulary, not a promise that either DTO can be
converted into the other.

Zero `PreparedString`/`PreparedBytes` is redacted; present empty values remain
distinguishable through `State`. Redacted means a selected or potentially
present value was withheld; non-applicable absence is represented by its owning
structure (for example `PreparedBody.Kind()==PreviewBodyNone`, empty query/
header/cookie slices, a nil multipart manifest, or an empty string for a known
absent media type). `Value` and `Bytes` return an empty value unless present,
and `Bytes` always returns a clone. All slices and nested values are detached.
The detached DTOs retain no private raw value: redacted `PreparedString` and
`PreparedBytes` contain only their state and an empty payload, while public values
are present only because the owner supplied a capability. Their ordinary default
formatting therefore cannot expose a withheld value; input-side `Value` and
`Payload` keep the fixed safe formatter. `PreparedTarget.Path` is a whole-value
projection: it is present only when the owner supplied `PathValue(Public(...))`
for the outer path and every actual pathname parameter contribution is public
through `PathParamValue` or absent, otherwise it is redacted. An ordinary private
`PathParam` therefore keeps the resulting pathname redacted. The disclosure applies
only to the resolved pathname; query values remain separate occurrences and values
parsed from a legacy `Path` or absolute URL stay Private unless independently
owner-marked with `QueryValue(Public(...))`. A fragment is never projected.
When present, the pathname uses the resolved `url.URL.EscapedPath()` spelling,
including the resolver's existing escaping, rather than raw input text.
`Scheme` and `Authority` follow the
resolved `url.URL.Host` spelling, including explicit ports and IPv6 brackets,
with `Scheme` using the resolved scheme; authority omits userinfo and does not
add a default port or canonicalize case. If a private path parameter contributes
to scheme, authority, or a query name because it was written inside the legacy
target string, Prepare fails closed with `ErrPreparationNotPreparable` rather
than exposing that structural component. A private substitution occurring only
in userinfo remains omitted: userinfo is never part of the preparation DTO and
does not independently trigger this failure.
When a relative target has no scheme or authority, that component is
non-applicable and is returned as an empty structural string; it is not a
disclosed empty data value.

`RequestPreparation.Query()` returns resolved semantic query occurrences, not
encoded URL tokens. Occurrences are ordered by query key in the same lexical
order as `url.Values.Encode`; repeated values for one key retain the resolver's
append order after base/request-path/builder precedence is applied. The order is
not caller map iteration order or cross-key owner call order.

`PreparedMultipartFile.Field` is a structural multipart field name, like a
query or header name; callers MUST NOT put secrets in structural names.
The multipart file projection exposes only its structural field name and insertion
order. Filename and caller-supplied part content type are not retained or exposed
in the first version. Prepare may validate filename Content-Disposition parameter
syntax and part MIME syntax without opening a part, but never returns their values or copies them into error text; deeper
producer or wire failures remain delivery-owned.

Disclosure is occurrence-level. Legacy string, map, header, cookie, and form
values are Private. Typed JSON/XML/YAML values remain borrowed sources and are
never data-projected by Prepare. Text/Bytes owner setters derive an internal
public source fact from the tagged `Value`/`Payload` at the selection boundary;
that fact is not a general sensitivity map or a public raw accessor. The
owner-aware setter siblings defined by this specification use the `Public`
capability; there is no `Private`
constructor because legacy setters already provide the private operation. A
private inner occurrence always taints a composed form/body value, and a public
outer value cannot relabel it. Query occurrences remain independently tagged
within one key.
Structural names (including multipart field names), methods, and the closed set
of library-generated base media types are public schema. `PreparedBody.MediaType`
returns only that fixed set
(`application/json`, `application/xml`, `application/yaml`, `text/plain`,
`application/x-www-form-urlencoded`, or `multipart/form-data` without a
boundary) for the selected library vocabulary. It is empty for Bytes, Reader,
or a body whose only media type came from an explicit caller header; it never
echoes `Reader` content types, explicit `Content-Type` headers, client defaults,
or codec/caller strings. The package never guesses a
safe substring from a private raw URL or path. An explicit `Content-Type` header
participates in normal metadata precedence and is treated as data for taint
accounting, but Prepare never projects its value, even when it was supplied
through `Public`. A generated `Content-Type` for a library body selection may
be projected as the same closed schema value; an explicit header or Reader
content type remains redacted even when its spelling matches that set. When a
multipart boundary is generated only during delivery, the detached preparation
retains the base `multipart/form-data` media type but never exposes the final
boundary-bearing header value.
If a URL-encoded form mixes public and private
fields, its `PreparedBody.Data` is redacted as a whole because this DTO has no
field-level form-data accessor.

`Authorization` and `Proxy-Authorization` are always credential occurrences: an
explicit `HeaderValue(Public(...))` cannot make either present in Prepare, and
built-in auth only contributes a redacted Authorization occurrence. A compound
`Cookie` header is likewise treated as one private header value; Prepare never
parses it into public cookie values. `CookieValue(Public(...))` is the only owner
operation that can make an explicit cookie value present.

An empty legacy `Form` is a selected private empty body, not a public empty
payload. A form can produce present bytes only when at least one actual form
occurrence was supplied through the owner capability and every occurrence is
public; absence of occurrences does not create a public capability.

Base URL data remains private in Prepare. `PathValue(Public(...))` may publish the
complete resolved pathname only when it covers the outer path and every actual
pathname parameter contribution is public through `PathParamValue`; it does not
authorize base URL data. `PathParamValue` is the owner-aware sibling of
`PathParam`, and authorizes only that named parameter occurrence for pathname
projection. The outer path still requires `PathValue(Public(...))`; a public
parameter cannot relabel a private outer path or a private base pathname.
The path taint rules are fixed: a non-empty base pathname is a private contribution,
while an empty or `/` base pathname adds no private pathname; an absolute request URL
overrides the base pathname; a path parameter taints the pathname only when its
`{name}` placeholder in the pathname is actually replaced and its value is private,
and an unmatched parameter (or a replacement occurring only in query/fragment text)
is a no-op for pathname disclosure. A public path parameter does not taint the
pathname or add a separate disclosure capability for query values, userinfo, or
fragments; scheme, authority, and query-name exposure continues to follow the
existing structural and fail-closed rules. `DelPathParam` removes both private
and public parameter entries for the named keys.
`PathValue(Public("/x?q=v"))` declares only pathname data, so its embedded query
remains private. Any actual private pathname contribution redacts the whole resolved
pathname. Query values are projected per occurrence: a private value is redacted
without suppressing a public value with the same key. If the value is an absolute URL, scheme and authority remain structural
facts rather than a new public absolute-target capability. The resolver still applies
its existing absolute-over-base and query precedence rules; disclosure projection
never guesses a safe substring after resolution. Fragment is omitted because it is
not sent to an HTTP server.

Prepare checks the caller context after retained builder errors and before doing
projection work, and checks it between bounded projection loops. It does not
inherit request-local delivery timeout, create a child context, or retain a
cancel handle: a synchronous in-memory projection cannot be interrupted by a
deadline, while `MaxPreparedBodyBytes` is its explicit retained-result bound.
Context cancellation preserves the standard cancellation/deadline classifications.
Prepare-specific inspection failures
use three classifications: `ErrPreparationInvalidBudget`,
`ErrPreparationNotPreparable`, and `ErrPreparationBodyTooLarge`. Eligibility is
request-local: JSON/XML/YAML typed bodies
remain redacted without invoking or rejecting any encoder, while their existing
semantic `Content-Type` preflight still applies using the same request-local
header view consumed by delivery body materialization; a client default alone
does not satisfy the check after the builder removes its generated header.
The check reports `ErrUnsupportedContentType` when absent. Active unknown auth and unsafe target
substitution remain not-preparable; an opaque Reader is a redacted structural
selection, not an eligibility failure.
Retained fluent errors return fixed safe text without matching a Prepare-specific
inspection sentinel; `errors.Is` may preserve retained package-owned classifications
(`ErrInvalidConfigValue`, `ErrUnsupportedFormFieldsType`) or
`ErrUnsupportedContentType` through a sanitized wrapper, never arbitrary collaborator causes or private
data. Static invalid multipart structure uses `ErrInvalidConfigValue` with fixed
text. Nil context returns `ErrRequestCreationFailed`; method/URL preflight
returns that fixed sentinel directly without `Unwrap`/`As` access to
`*url.Error`, path, or original URL text.

The required safety ordering is: receiver, retained builder error, context, and
negative budget before projection work; method/URL preflight before body activity;
and current-plan eligibility before any encoder, reader, multipart producer, or part
method. The implementation may test the relative ordering of static semantic body
validation, eligibility, and budget overflow for parity, but those adjacent details
are not additional public compatibility promises. Error text and error chains MUST
not expose URL, body, or arbitrary collaborator causes.

Preparation does not alter the builder and does not authorize a later send.
`Send` or `SendStream` independently compiles and materializes its own delivery
path. Preparation is a point-in-time inspection result; it does not promise that
bytes produced by a later Send equal bytes inspected earlier when borrowed typed
values or encoder state can change.

## Preparation Errors

Fluent helpers that cannot return an error directly retain the first preparation
failure on the builder. This applies to `QueriesStruct`, `Form`, `FormFields`,
invalid or typed-nil request-local `Auth`, and nil entries passed to
`AddMiddleware`. It also applies to negative request-local timeout, retry count,
and buffered response limit values.

`Send` and `SendStream` return that original cause before taking a client
snapshot, deriving a context, opening or encoding a body, applying middleware,
or calling the transport. Later fluent calls do not replace the first cause.
Logging MAY report the failure but MUST NOT be its only caller-visible channel.

## Disclosure Capability

The package keeps the existing fluent language and adds only explicit owner
capabilities needed by Prepare:

```go
type Value struct { /* private */ }
type Payload struct { /* private */ }

func Public(string) Value
func PublicPayload([]byte) Payload
func (b *RequestBuilder) PathValue(Value) *RequestBuilder
func (b *RequestBuilder) PathParamValue(name string, value Value) *RequestBuilder
func (b *RequestBuilder) QueryValue(name string, value Value) *RequestBuilder
func (b *RequestBuilder) HeaderValue(name string, value Value) *RequestBuilder
func (b *RequestBuilder) AddHeaderValue(name string, value Value) *RequestBuilder
func (b *RequestBuilder) CookieValue(name string, value Value) *RequestBuilder
func (b *RequestBuilder) FormFieldValue(name string, value Value) *RequestBuilder
func (b *RequestBuilder) TextValue(Value) *RequestBuilder
func (b *RequestBuilder) BytesPayload(Payload) *RequestBuilder
```

`Value` is an input-side disclosure capability and is intentionally distinct
from the existing policy-result type `PreviewValue`.

Legacy entries remain valid and default to Private. Capability values have
private fields, a private zero value, no raw accessor, no `Reveal`, and no
value-revealing formatter or serializer; any default formatting path uses only a
fixed disclosure-safe marker. `PublicPayload` clones caller bytes. Repeated owner
values are expressed by repeated single-value calls; this version does not add
map-based capability carriers. `GetBaseURL`, `WithBaseURL`, absolute URL
resolution, and all existing verb helpers remain unchanged. Base URL disclosure,
typed-body disclosure, and a public absolute-target constructor are intentionally
deferred until a real owner callsite requires them.

Owner siblings preserve the existing mutation language: `PathValue` replaces the
complete selected path, and `PathParamValue` replaces the named path-parameter
entry just as `PathParam` does;
`QueryValue`, `CookieValue`, and `FormFieldValue` append an occurrence;
`HeaderValue` replaces the request-local header name and `AddHeaderValue`
appends within that name. Cookie occurrences still follow the existing
client/request precedence and same-name merge rules. `TextValue` and
`BytesPayload` replace the current body selection with owned in-memory data;
typed JSON/XML/YAML methods are intentionally absent. All owner header mutations
update ordered-header intent using the same synchronization rules as their legacy
counterparts.

## Path and Query Construction

A builder MAY define:

- path replacement through `Path`, `PathValue`, `PathParam`, `PathParamValue`,
  `PathParams`, and `DelPathParam`
- query parameters through `Query`, `Queries`, `QueriesStruct`, and `DelQuery`

Path parameters use `{name}` placeholders and MUST be URL-path-escaped before dispatch.
`PathParamValue` has the same replacement and `url.PathEscape` behavior as
`PathParam`, but records an owner-approved value for Prepare. `PathParam` and
`PathParams` remain private inputs. `DelPathParam` removes either kind of entry.
For preparation, only a placeholder actually replaced in the resolved pathname
participates in pathname taint; occurrences in scheme, authority, query names,
userinfo, or fragment follow their existing privacy rules, and unmatched
parameters do not affect pathname disclosure. A public parameter does not by
itself authorize the outer path: `PathValue(Public(...))` remains required for
pathname projection.

Dispatch uses one resolver for base URL, request path, path params, absolute
URLs, and query composition. Base URL paths and request paths are joined without
accidental slash loss or duplication. For a relative request path, query values
from the base URL, request path, and builder are combined in that order. Values
are appended rather than replaced, so repeated keys remain repeated. An
absolute request URL overrides both the base path and base query.

An absolute request URL without added query values preserves its raw query
string, including semicolons or percent escapes that cannot be parsed as
form values. When a relative URL or builder query values require merging,
malformed request-path query syntax fails URL preflight with
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

Request-local headers override client default headers with the same header name,
using case-insensitive header-name matching. Request-local `AddHeader` adds
values within the request-local header set; it does not preserve an older client
default value for that same header name.

Client and request-local cookies merge by cookie name. The request layer
replaces same-name defaults and preserves different-name defaults; repeated
cookies within one layer use the last value.

`OrderedHeaders` accepts an `orderedobject.Object[[]string]` where keys are
header names and values are all values for each header. It sets request-local
header values and preserves insertion order as request intent. Pseudo-headers
are retained in ordered metadata for supporting HTTP/2 or HTTP/3 transports,
but are not applied to `net/http` header maps.

When ordered headers are active, all request-local header helpers that mutate
headers, including `Header`, `AddHeader`, `DelHeader`, `ContentType`, `Accept`,
`UserAgent`, `Referer`, and body helpers that set `Content-Type`, MUST keep the
ordered metadata in sync with the semantic `http.Header` values.

After auth and cookie precedence is applied, every existing non-pseudo ordered
entry is synchronized to the final semantic values. Pseudo-header entries stay
as intent and are never inserted into `http.Header`.

If a request-local plain header overrides a client ordered default without
supplying request-local ordered metadata for that header, the client ordered
metadata for that header is removed so supporting transports do not observe
stale default values.

Authentication remains the existing mutation contract:

```go
type AuthMethod interface {
	Apply(*http.Request)
	Valid() bool
}
```

The existing construction/request-helper validation still calls `Valid` for the
configured auth and rejects nil or typed-nil auth. A non-nil caller-defined auth
that passes that existing validation remains valid for delivery, but Prepare
returns `ErrPreparationNotPreparable` before it calls `Valid` or `Apply` again.
Client auth and request-local auth are both active delivery collaborators; Prepare
checks both independently, even when request-local auth will override the final
Authorization header. An unknown shape at either layer is therefore not
preparable.
Preview also never calls unknown/caller-defined `Valid` or `Apply`. Prepare
recognizes `BasicAuth`, `BearerAuth`, and `CustomAuth` as built-in shapes without
invoking their methods; a package-private shape helper owns only that recognition
and field-qualification rule. If a caller mutates an installed built-in auth pointer
so its required field becomes empty, Prepare returns fixed `ErrInvalidConfigValue`
without calling `Valid` or `Apply`. Delivery keeps the existing order of client
headers, client auth, request-local headers, then request-local auth; therefore
request-local auth wins over an explicit request-local Authorization header, and
Prepare exposes only a redacted Authorization occurrence. Built-in `Apply` remains
the delivery formatting path. A future preparation-aware signing contract,
including any body digest semantics, requires a separate specification and must
not replace this interface.

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
`Bytes` preserves an explicit caller header but removes a media type generated by
the body it replaces. `Reader` generates a media type only when its
`contentType` argument is non-empty and is one-shot unless the reader is
seekable and sized. Changing `Content-Type` after a typed setter does not change
which encoder that setter selected.

`Encoder.Encode` returns an `io.Reader` whose complete lifecycle contract is
reading. The request pipeline reads the value and MUST NOT discover or invoke
an additional `Close` method based on its dynamic type.

For Prepare, legacy text, byte, form, typed, reader, filename, and part-content
data are Private by default. Owner siblings may mark supported in-memory data
Public; they do not change body selection or delivery serialization. Typed
JSON/XML/YAML bodies are always structural in Prepare: the configured encoder and
the caller value's marshal hooks are never called. A caller that needs public
encoded bytes must create them before entering this package and pass an owned
`PublicPayload` through `BytesPayload`. The retained-byte budget applies to the
result projection, not to allocations made before that handoff.

The builder does not infer content type from Go value shape. Non-raw encoded
bodies without an explicit content type fail before dispatch.

`Form` and `FormFields` accept `url.Values`, `map[string][]string`,
`map[string]string`, or a struct encoded from `url` tags. They do not infer
multipart files from `map[string]any`.

`JSON(nil)` encodes JSON `null`. `Form(nil)` selects a real empty URL-encoded
form. `Multipart(nil)` records `ErrInvalidConfigValue` and fails before body or
transport work.

`Multipart` is the only multipart upload vocabulary. It supports fields, file
readers, bytes, strings, explicit file metadata through `FilePart`, custom
boundaries, and explicit retry buffering through `Replayable(maxBytes)`. Without
`Replayable`, multipart bodies stream and are not replayable after the first
transport attempt.

The `Multipart` container and each `FilePart.Body` are borrowed. The multipart
writer reads nested bodies but MUST NOT close them based on their dynamic type.
Callers MUST NOT mutate the container, typed values, encoders, auth methods, or
nested parts concurrently with any request exit; the caller retains ownership
of all of those collaborators.
The outer `http.Request.Body` follows the standard transport contract and is
closed by the transport for each attempt or redirect hop.

Prepare projects an opaque `Reader` structurally without reading or closing it.
Multipart preparation is structural only: it retains field names, value counts,
and file-field order, never creates a boundary or producer, and never reads or
closes a borrowed part; its body data is redacted. It retains the existing
seekable+sized replay probe for Send, but
does not turn that probe into a new public reader source contract. A future
reopenable source must define retry and redirect behavior before being added.

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

`Timeout` only creates a derived deadline when the provided context does not
already have one.

Zero timeout means no request-local deadline. A negative timeout is invalid and
returns `ErrInvalidConfigValue` through the preparation-error contract.

`Retry(policy)` replaces the client retry policy for that request.
`NoRetry()` disables retries even when the client has a positive default.

`RetryPolicy.Max` is the number of retries after the initial attempt. Zero means
no retries. A negative value is invalid and returns `ErrInvalidConfigValue`
before dispatch; private normalization remains defensive and is not the public
input contract.

Request bodies that can be replayed SHOULD be restored before each retry attempt.
Non-replayable bodies MUST NOT be retried after the first attempt once delivery
has started.

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

`Send(ctx)` returns a buffered `Response`. `SendStream(ctx)` returns an
unbuffered `StreamResponse` whose body is owned by the caller.

## Dispatch

The package has exactly three request exits:

- `Preview`: `compile -> structural project` with no collaborator materialization;
- `Prepare`: `exit-specific preflight -> compile -> optional inspect -> sanitized
  project` with no root delivery activity;
- `Send` / `SendStream`: `compile -> delivery materialize -> delivery`.

All three begin from the same private request-plan facts. The plan freezes
library-owned values and container structure at compile time; borrowed
collaborators remain borrowed and the builder is not made concurrency-safe.

`Send(ctx)`:

1. returns any retained preparation error;
2. snapshots client state;
3. resolves the URL and asks `http.NewRequestWithContext` to validate the
   context, method, and resolved URL with no body;
4. derives the request timeout context and only then opens or encodes the body;
5. constructs the outbound `http.Request` and applies auth, headers, and cookies;
6. executes middleware and retry policy; and
7. buffers the response within the request-local byte ceiling and returns a
   complete `Response`, or no response with `ErrResponseBodyTooLarge`.

Invalid context, method, or resolved URL returns `ErrRequestCreationFailed`
before streaming producers start. URL-resolution and retained preparation errors
have the same no-open guarantee. The library does not read or close a caller
body source on these preflight failures.

URL preflight and terminal transport errors omit URL userinfo, query values, and
fragments from returned and logged diagnostics. `Preview` returns a fixed
`ErrRequestCreationFailed` for invalid method or URL preflight and does not
expose the underlying cause. `Send` and `SendStream` retain or wrap their
existing request-creation causes; terminal transport causes and standard
`errors.Is` / `errors.As` classifications remain inspectable.

`SendStream(ctx)` follows the same request-body materialization and delivery path, but returns a
`StreamResponse` without buffering the response body. The caller must close the
stream response.

Client mutations after `Send` starts do not affect that in-flight request, but
mutating a builder concurrently with any exit is outside the contract.

## Forbidden

- Do not chain `AddMiddleware`; it is a mutator, not a fluent builder method.
- Do not add a `Custom(path, method)` alias; arbitrary request creation is
  method-first through `Request(method, path)`.
- Do not assume `Timeout` overrides an existing context deadline.
- Do not add body aliases or content-type inference that obscure `JSON`, `XML`,
  `YAML`, `Text`, `Bytes`, `Reader`, form, and multipart ownership.
- Do not replace `AuthMethod` or add a preparation-time reveal switch.

## Contract Invariants

- Builder creation and mutation boundaries are explicit.
- Body selection and generated content-type ownership are explicit.
- The retry-override rule and request body replay behavior are documented.
- Preview remains structural and detached.
- Prepare is retained-byte-limited, sanitized, and not sendable.
