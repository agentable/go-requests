# Requests Overview

## Overview

`requests` defines a fluent HTTP client around `Client`, `RequestBuilder`,
`Response`, and `StreamResponse`, with detached `RequestPreview` and
`RequestPreparation` values for two different kinds of pre-delivery
inspection. This spec defines the package boundaries and the request
lifecycle that the other `SPECS/*.md` files refine.

## Public Model

- `Client` owns reusable configuration: base URL, default headers and cookies, auth, retry policy, codecs, logger, and transport settings.
- `RequestBuilder` owns one outbound request's method, path, request-local
  metadata, body, timeout, retries, buffered response limit, and middleware.
- `RequestPreview` is a detached, policy-filtered projection of a builder before
  delivery. It is a value result, not a fifth delivery lifecycle or a prepared
  request.
- `RequestPreparation` is a detached, retained-byte-limited, policy-filtered
  projection that may inspect library-owned body data without entering delivery. Its
  exit-specific preflight runs before the shared request-plan compile. It does not
  call unknown auth implementations, consume opaque readers, or open borrowed
  multipart parts.
  Typed JSON/XML/YAML bodies remain structural and are never encoded by Prepare.
  It is not an `http.Request`, a replay handle, or a sendable request.
- `Response` exposes the buffered result of one `Send` call.
- `StreamResponse` exposes the unbuffered result of one `SendStream` call.
- Middleware, redirect policies, and proxy selection affect request delivery. They do not change the public roles of `Client`, `RequestBuilder`, `Response`, or `StreamResponse`.

> **Why**: Shared state belongs on `Client`, while request-specific state belongs on `RequestBuilder`. This keeps reuse predictable and makes the point where defaults become fixed explicit.
>
> **Rejected**: A single mutable object that mixes reusable client policy with one-shot request state.

## Request Lifecycle

1. Construct a client with `New`.
2. Create a builder with `NewRequestBuilder` or an HTTP verb helper such as `Get` or `Post`.
3. Populate request-local state on the builder.
4. Optionally call `Preview(ctx)` to obtain a detached structural projection.
   Preview does not send, consume, or freeze the builder, and it does not run
   delivery collaborators.
5. Optionally call `Prepare(ctx, opts)` to obtain a detached, retained-byte-limited
   preparation projection. Preparation may inspect only the body collaborators
   named by `SPECS/21-request-builder-api-specs.md`; it never enters delivery.
6. Call `Send(ctx)` for a buffered response or `SendStream(ctx)` for a
   caller-owned stream. These operations snapshot the client state before
   dispatch.
7. Resolve path, query, body, auth, headers, and cookies from the builder plus
   the client snapshot.
8. Execute middleware and retry policy around the transport attempt.
9. Return a `Response` with a buffered body or a `StreamResponse` with an open
   body the caller must close.

## Boundary Rules

- `SPECS/20-client-api-specs.md` defines reusable client configuration and transport policy.
- `SPECS/21-request-builder-api-specs.md` defines request construction and per-request overrides.
- `SPECS/22-response-api-specs.md` defines buffered response behavior.
- `SPECS/23-streaming-api-specs.md` defines streaming delivery.
- `SPECS/40-middleware-architecture-specs.md` defines middleware composition.
- `SPECS/41-retry-and-delivery-specs.md` defines retries and delivery timing.
- `SPECS/25-profile-api-specs.md` defines coherent client identity profiles.
- `SPECS/31-public-surface-decisions.md` defines cross-cutting public API
  surface decisions and forbidden removed-surface aliases.

`SPECS/21-request-builder-api-specs.md` is the canonical authority for the
`RequestPreview` and `RequestPreparation` results, their disclosure policy,
and their no-delivery boundaries.

## Forbidden

- Do not put one-shot request state on `Client` when it belongs on `RequestBuilder`.
- Do not assume mutating `Client` after `Send` starts can change an in-flight request.
- Do not treat `RequestPreview` as an exact wire request, a post-middleware
  request, or a sendable prepared request.
- Do not treat `RequestPreparation` as an exact wire request, a post-middleware
  request, a replayable body, or a sendable request.
- Do not define the same public rule in multiple specs; each concept belongs in exactly one file.

## Contract Invariants

- The client, builder, buffered response, and stream response roles are distinct.
- The snapshot point is explicit.
- Preview is a detached, no-delivery projection and does not alter the builder.
- Preparation is a detached, retained-byte-limited, no-delivery projection and does not alter
  the builder.
- Delivery concerns are delegated to the dedicated specs.
