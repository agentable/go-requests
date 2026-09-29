# Public Surface Decisions

## Overview

The public API is intentionally small. A symbol belongs in the root package only
when it names a durable concept in the request language, not when it shortens one
call site or preserves an obsolete path.

This spec owns public-surface decisions that cut across client construction,
request building, response ownership, streaming, retries, and extension modules.
Feature-specific behavior still belongs in the owning specs.

## Settled Decisions

### Validated Construction

`New(opts ...Option) (*Client, error)` and `Clone(opts ...Option) (*Client, error)`
are the construction paths.

- **Why**: Construction is the point where invalid base URLs, invalid proxy
  values, certificate file failures, profile option failures, and invalid
  numeric values should become caller-visible errors.
- **Contract Impact**: Non-empty base URLs are absolute hierarchical URLs with
  a host and no fragment. Their query values are preserved during request
  composition. Standard transports require HTTP(S), while a custom transport
  may support another scheme. Typed-nil collaborators fail construction.
- **Rejected**: Parallel constructors, config structs, or best-effort public
  setters that let an invalid client exist and fail later at request time.
- **Contract Impact**: Public examples and tests use `New`, `Clone`, verb
  helpers, and `With*` options for client-level configuration.

### Explicit Body Language

Request bodies are described with `JSON`, `XML`, `YAML`, `Text`, `Bytes`,
`Reader`, form helpers, and `Multipart`.

- **Why**: The body method should reveal both encoding intent and ownership.
- **Rejected**: A generic body entry point that guesses content type from Go
  value shape.
- **Contract Impact**: Encoded body helpers set their content type explicitly.
  Raw byte and reader bodies do not imply content type unless the caller sets
  one.

### Instance-Owned Codecs

Each client owns its configured JSON, XML, and YAML encoder and decoder. The
concrete codec types and `With*Encoder` / `With*Decoder` options are public
integration points; default instances are private client state.

- **Why**: Mutable package-level defaults allow one caller to change unrelated
  clients. `Encoder` contains only `Encode` because typed body helpers, not the
  encoder collaborator, own the wire media type. A standalone form encoder
  duplicates the builder's explicit form vocabulary.
- **Rejected**: Exported mutable `Default*Encoder` / `Default*Decoder` globals
  and a standalone `FormEncoder` / `DefaultFormEncoder` path; encoder
  `ContentType` methods or optional side interfaces that create a second media
  type owner.
- **Contract Impact**: Construct codec values explicitly when customizing a
  client. Custom encoders implement only `Encode`. Build URL-encoded forms
  through `RequestBuilder` form helpers.

### Caller-Owned Streaming

`Send(ctx)` is the buffered path. `SendStream(ctx)` is the streaming path and
returns `StreamResponse`, whose body remains open until the caller closes it.

- **Why**: Buffering and streaming have different ownership models. Keeping them
  separate prevents hidden background readers and ambiguous body lifetime.
- **Rejected**: A second streaming ownership model beside `SendStream`.
- **Contract Impact**: Streaming helpers live on `StreamResponse`; buffered
  decoding, saving, and buffered line iteration live on `Response`. Buffered
  `Response` has no `Close`; caller cleanup is required only for
  `StreamResponse`.

### Explicit Custom Transport Ownership

`WithTransport` clones standard transports and borrows other caller-supplied
transports. Closable custom transports such
as `http3.Transport(...)` remain visible handles owned by the caller.

- **Why**: Clients, clones, and `AsHTTPClient` snapshots may share a custom
  transport by identity. Root client cleanup cannot infer a unique owner.
- **Rejected**: HTTP/3 profiles that hide the closable QUIC transport, root
  `Client.Close`, owned-transport registries, reference counting, or finalizers.
- **Contract Impact**: The caller closes a custom transport only after every
  client, clone, snapshot, and in-flight request using it is done.

### Managed TLS Handshake Extension

`WithTLSHandshake` is the root extension point consumed by fingerprint profiles.
Root owns the standard transport and rebinds the handshake on Clone and
AsHTTPClient. Fingerprint owns uTLS, not transport identity. Ordinary callers
continue using `WithProfile` and client-level TLS/dial options.

- **Why**: Copying DialTLSContext copies its closure, which otherwise continues
  reading the original transport's dialer and TLS config.
- **Rejected**: Replaying an entire profile, global callback registries, copying
  arbitrary closures, or wrapping every standard transport option in a second
  transport protocol.
- **Contract Impact**: Standard TLS dial-hook scope, explicit failure-close responsibility,
  context-aware handshake timeout, and replacement/clearing rules are owned by
  `SPECS/20-client-api-specs.md`. Raw standard transport copies remain ordinary
  function copies; they do not acquire hidden rebinding metadata.

### Response Escape Hatches

`Response.Raw()` and `StreamResponse.Raw()` are the raw `net/http` escape
hatches. The response structs do not expose mutable storage fields.

- **Why**: Advanced callers sometimes need standard-library details, but ordinary
  response use should read through behavior methods.
- **Rejected**: Public mutable response storage, client references, or context
  fields on response structs.
- **Contract Impact**: Raw access is explicit and narrow; callers that mutate the
  returned `*http.Response` own the consequences.

### Caller-Owned Buffered Helpers

Buffered response helpers return values owned by the caller. `Response.Bytes()`
returns a byte-slice copy, and `Response.Header()` returns a header snapshot.

- **Why**: Helper names read as values. Returning internal mutable storage makes
  ordinary reads accidentally mutate later response behavior.
- **Rejected**: A buffered `Body()` helper that exposes internal bytes.
- **Contract Impact**: Raw mutation goes through `Raw()`; value helpers return
  snapshots.

### Method-First Arbitrary Requests

The arbitrary-method entry point is `Client.Request(method, path)`.

- **Why**: Method-first order matches `NewRequestBuilder(method, path)` and
  standard HTTP language.
- **Rejected**: `Custom(path, method)` or aliases that preserve two grammars for
  one operation.
- **Contract Impact**: Public request syntax is either a verb helper such as
  `Get(path)` or `Request(method, path)`.

### Structural Request Preview

`RequestBuilder.Preview(ctx)` returns a detached `RequestPreview` projection
for callers that need to inspect request structure before deciding to send it.
The result exposes only policy-filtered method, safe target structure, query
keys, metadata shape, cookie names, selected body facts, and the semantic
`Content-Type`. It does not expose caller values, a live `*http.Request`, a
prepared or replayable body, middleware output, or delivery status.

- **Why**: SDK and CLI callers need a no-send inspection step, while ordinary
  request values and credentials must remain opaque and caller-owned streams
  must not be consumed. Keeping the result detached preserves the existing
  builder ownership model and gives Preview no second send lifecycle.
- **Rejected**: A raw or prepared `*http.Request` loses the privacy boundary
  and invites accidental dispatch; a middleware or transport interception path
  reports post-delivery behavior and can consume resources; a caller-controlled
  disclosure switch makes safety depend on the caller rather than an owning
  policy.
- **Basis**: The current `RequestPreview` implementation in
  `request_preview.go`, its focused behavior tests in `request_test.go`,
  `body_test.go`, and `form_test.go`, the existing `RequestBuilder`/delivery
  ownership split in `SPECS/00-overview.md` and
  `SPECS/21-request-builder-api-specs.md`, and the standard-library distinction
  between request construction and body/transport serialization.
- **Contract Impact**: `Preview` is structural-only. It never invokes
  middleware, cookie jars, authentication methods, encoders, readers,
  multipart producers, retry/backoff, redirect, proxy, transport, logging, or
  request serialization. Downstream full dry-run assembly remains outside this
  root package's contract.

### Capability-Aware Disclosure and Request Preparation

`RequestBuilder.Prepare(ctx, PrepareOptions)` is the one root-package no-send
operation that may inspect library-owned body data under a retained-byte limit
(zero is valid and retains no body bytes).
It returns a detached `RequestPreparation` projection. It does not call unknown
auth implementations, encoders, consume opaque readers, or open
borrowed multipart parts. Multipart is represented only by a structural manifest
with redacted body data; its first version exposes field names, value multiplicity,
and file-field order, not filename or part content type. It does not return an `http.Request`, raw URL, reader, `GetBody`,
replay handle, cleanup handle, or any other send capability.

The package keeps the ordinary fluent language small: existing string,
`http.Header`, `url.Values`, map, and typed-body inputs remain valid and enter
the preparation graph as private data. The first capability surface is only
`Public`, `PublicPayload`, `PathValue`, `PathParamValue`, `QueryValue`, `HeaderValue`,
`AddHeaderValue`, `CookieValue`, `FormFieldValue`, `TextValue`, and
`BytesPayload`, with signatures fixed by
`SPECS/21-request-builder-api-specs.md`.
No `Private*` constructor, typed-body owner API, base URL disclosure option,
absolute-target constructor, alternate `*Value`/`*Payload` name, `Reveal`, or
caller-controlled disclosure name is part of the contract. There is no
`Prepare` reveal flag or caller-controlled disclosure switch.

Disclosure is attached to each value occurrence, not stored in a parallel
sensitivity map. Zero values are private/redacted, and public empty values
remain distinguishable from redacted values. Capability wrappers have no raw
accessor, `Reveal`, general formatter, or serializer. Path disclosure is
whole-value: scheme and authority are the narrow structural exceptions, while
the library never guesses a safe pathname, query, fragment, or userinfo substring
by splitting a private raw URL. Typed bodies remain structural in
Prepare; callers provide already encoded owned bytes when they explicitly need
body data. Capability wrappers do not provide a value-revealing formatter or
serializer; their default formatting is a fixed safe marker. HTTP method,
metadata names, and the closed set of library-generated base media types are
structural schema for Prepare; caller-supplied media types remain data and are
never projected by Prepare. Structural Preview keeps its existing semantic
`Content-Type` accessor as defined in
`SPECS/21-request-builder-api-specs.md`. Callers must not put secrets in
structural names. `Public` is an input disclosure declaration, not a permission
boundary or a proof of trusted provenance; it cannot upgrade a legacy private
value already stored in a builder. `PathParamValue` is the owner-aware sibling of
`PathParam`: it replaces one named parameter entry and authorizes that value only
for the resolved pathname. `PathParam` and `PathParams` remain private, and
`DelPathParam` removes either kind of entry. The outer path still requires
`PathValue(Public(...))`; a public parameter cannot relabel a private outer path
or a private base pathname. Only an actually replaced pathname placeholder
participates in pathname taint; unmatched, query-only, and fragment-only
occurrences do not. Existing `url.PathEscape`/`EscapedPath` resolution remains
authoritative, and scheme/authority/query-name private substitutions continue to
fail closed.

`Authorization` and `Proxy-Authorization` are credential headers. Their values
remain private even when passed through an owner-marked header setter; a compound
`Cookie` header is likewise private and is never decomposed into public cookie
occurrences.

`GetBaseURL`, `WithBaseURL`, absolute URL resolution, and all existing verb
helpers remain unchanged. Base URL pathname, query, and userinfo data remain
private in Prepare when configured through legacy `WithBaseURL`; base URL
fragments are rejected during construction and never enter Prepare. The existing
getter is an explicit configuration escape hatch and is outside the preparation
projection.

- **Why**: A structural preview and a retained-byte-limited preparation projection answer
  different questions. A small capability surface keeps disclosure attached to
  the owner decision while preserving the fluent API for ordinary callers.
- **Rejected**: deleting `Preview`, making every public argument a `Value`,
  exposing raw/prepared/sendable requests, inferring safe URL substrings, or
  making safety depend on a preparation-time switch.
- **Contract Impact**: `Preview`, `Prepare`, and delivery compile the same
  private request facts but have separate collaborator and ownership
  boundaries. `Prepare` has zero root-library middleware, retry, redirect, jar,
  proxy, logger, transport, or response activity for the explicitly tested
  success/failure matrix; this is not a claim about arbitrary unknown external
  collaborators.

### Retry Policy As One Value

Retry behavior is configured through `RetryPolicy` at the client layer with
`WithRetry` or at the request layer with `Retry`.

- **Why**: Attempt count, backoff, retry condition, and Retry-After policy are one
  delivery concern.
- **Rejected**: Separate scalar setters for retry count, retry strategy, and
  retry condition.
- **Contract Impact**: Request-local retry policy replaces the client policy for
  that request. `NoRetry()` is the public way to disable a positive default.

### Extension Module Release Boundary

Extension modules remain independently consumable modules, but releases are
coordinated. Root and extension modules use one common version, their annotated
tags point to one commit, and every extension requires the root at that version.

- **Why**: One coordinated release prevents split tag histories from making the
  repository state ambiguous to consumers and maintainers.
- **Rejected**: Root-first partial publication, mixed module versions, tags on
  different commits, and local `replace` directives in published modules.
- **Contract Impact**: The complete pre-pin gate validates the currently
  resolvable graph before the unpublished common pins are written. One atomic
  push then publishes the final commit and all module tags together;
  `task verify:all` and `task test:published` validate that final commit after
  the common version is resolvable outside `go.work`. A failed post-publication
  gate requires a new common patch release, never a moved tag.

## Deliberate Public Escape Hatches

These symbols remain public because they name real integration points:

- `AsHTTPClient` exposes a caller-owned snapshot of standard-library client
  configuration without carrying requests metadata or middleware.
- `UnsafeHTTPClient` exposes the underlying client for advanced integration.
  Callers that mutate it own synchronization and consistency risk.
- `GetTLSConfig` returns a standard shallow clone of the active standard
  transport TLS settings (nil for custom transports) so extension
  modules can inherit TLS intent without receiving the client's top-level
  config pointer. Referenced collaborators keep the ownership contract defined
  by `SPECS/20-client-api-specs.md`.
- `RoundRobinProxies` and `RandomProxies` create proxy selectors for
  `WithProxySelector`.

## Forbidden

- Do not add aliases for removed construction, body, streaming, or retry names.
- Do not restore `Response.Close`, encoder `ContentType` methods,
  `ErrTestTimeout`, or an HTTP/3 `Profile` compatibility path.
- Do not restore request-builder cloning, cache middleware, standalone form
  encoders, or mutable package-level default codec instances.
- Do not add public runtime setters for client defaults; use `Clone(opts...)` to
  derive a modified client.
- Do not expose mutable response internals as fields.
- Do not add a transport adapter that reapplies requests defaults outside
  `RequestBuilder` dispatch.
- Do not add a raw/prepared/sendable request result, body rewind handle, or
  caller-controlled disclosure flag to implement Preview.
- Do not add a public symbol unless it names a durable concept that belongs in
  the request language.
- Do not publish extension modules whose root requirement differs from their
  own release version.

## Contract Invariants

- Public construction is limited to `New`, `Client.Clone`, builder creation, and verb
  helpers.
- Request body APIs are explicit about encoding and ownership.
- Request preview is a structural, detached view rather than another delivery
  path.
- Buffered and streaming response ownership remain separate.
- Public escape hatches are deliberate and named here.
- Extension module release verification is explicit.
