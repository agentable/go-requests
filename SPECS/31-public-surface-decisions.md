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

`WithTransport` borrows a caller-supplied transport. Closable transports such
as `http3.Transport(...)` remain visible handles owned by the caller.

- **Why**: Clients, clones, and `AsHTTPClient` snapshots may share a custom
  transport by identity. Root client cleanup cannot infer a unique owner.
- **Rejected**: HTTP/3 profiles that hide the closable QUIC transport, root
  `Client.Close`, owned-transport registries, reference counting, or finalizers.
- **Contract Impact**: The caller closes a custom transport only after every
  client, clone, snapshot, and in-flight request using it is done.

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
- `GetTLSConfig` returns a standard shallow `tls.Config.Clone` so extension
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
