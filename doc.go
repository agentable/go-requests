// Package requests provides a fluent HTTP client library for Go.
//
// # Core request model
//
// The package is built around four core objects and two detached request
// projections:
//
//   - Client owns reusable configuration: base URL, default headers and cookies,
//     auth, retry policy, codecs, logger, and transport settings.
//   - RequestBuilder owns one outbound request: method, path, request-local
//     metadata, body, timeout, retries, buffered response limit, and middleware.
//   - RequestPreview is a detached, policy-filtered projection returned before
//     delivery; it is not a prepared or sendable request.
//   - RequestPreparation is a detached, retained-byte-limited projection for
//     selected inspection before delivery; it is sanitized and not sendable.
//   - Response exposes the buffered result of one Send call.
//   - StreamResponse exposes the unbuffered result of one SendStream call.
//
// Client defaults are formed during New or Clone. State that is reused across
// requests belongs on Client; state for one request belongs on RequestBuilder.
// Preview does not consume request inputs, invoke delivery collaborators, or
// freeze the builder; callers can still send the builder independently. Prepare
// follows the same no-delivery boundary. It may retain explicitly approved,
// eligible body data in PreparedBody.Data under MaxPreparedBodyBytes; metadata
// is projected separately under the preparation disclosure policy.
//
// # Quick start
//
//	client, err := requests.New(
//	    requests.WithBaseURL("https://api.example.com"),
//	    requests.WithTimeout(30*time.Second),
//	)
//	if err != nil {
//	    return err
//	}
//
//	resp, err := client.Get("/users/{id}").
//	    PathParam("id", "1").
//	    Send(ctx)
//	if err != nil {
//	    return err
//	}
//
//	var user User
//	if err := resp.DecodeJSON(&user); err != nil {
//	    return err
//	}
//
// # Construction
//
// Use [New] with functional options. Construction validates option input and
// returns an error instead of building a partially configured client.
//
// # Body lifecycle
//
// Request body setters fall into two groups:
//
//   - Replayable: [RequestBuilder.JSON], [RequestBuilder.XML],
//     [RequestBuilder.YAML], [RequestBuilder.Text], [RequestBuilder.Bytes],
//     [RequestBuilder.Form], [RequestBuilder.FormField], and
//     [RequestBuilder.FormFields]. The body is buffered or re-readable, so
//     retries are safe.
//   - One-shot: [RequestBuilder.Reader] given a raw [io.Reader] that is not
//     seekable, and non-replayable [RequestBuilder.Multipart].
//     Such bodies cannot be replayed; if a retry is required, Send returns
//     [ErrRequestBodyNotReplayable] instead of silently re-sending or silently
//     skipping the retry. Use [Multipart.Replayable] when a multipart body must
//     be resent.
//
// Legacy body setters keep their values private in [RequestBuilder.Prepare].
// [RequestBuilder.TextValue] and [RequestBuilder.BytesPayload] can retain
// explicitly approved in-memory data through [Public] and [PublicPayload],
// subject to [PrepareOptions.MaxPreparedBodyBytes]. A form is retained only
// when all of its occurrences are explicitly public. Typed JSON, XML, and YAML
// bodies remain structural and are never encoded by Prepare. Opaque readers
// and multipart parts remain borrowed: Prepare does not read or close them.
// Multipart preparation returns only a structural manifest and redacted body
// data. [RequestPreparation] is detached and cannot be sent or converted into
// an [http.Request].
//
// # Errors
//
// Runtime failures are returned as errors; the package does not panic and does
// not expose Must-style APIs. Use [errors.Is] with the sentinels declared in
// errors.go to detect specific causes, and the helpers [IsTimeout],
// [IsCanceled], and [IsConnectionError] to classify transport-level failures.
// A request can bound buffered allocation with
// [RequestBuilder.MaxResponseBodyBytes]. Exceeding the limit returns no partial
// response and matches [ErrResponseBodyTooLarge]; [ResponseBodyLimitError]
// provides the configured and observed byte counts.
//
// # Extensions
//
// Optional protocol and identity behavior lives in extension modules so the
// core does not pull their dependencies:
//
//   - github.com/agentable/go-requests/browser     — browser-like ordered headers
//   - github.com/agentable/go-requests/fingerprint — uTLS ClientHello profiles
//   - github.com/agentable/go-requests/http3       — QUIC HTTP/3 transport
//
// The browser and fingerprint modules plug in through [Profile]. The HTTP/3
// module returns an explicit transport for [WithTransport]; callers close that
// transport after every client, clone, and request using it is done.
//
// # Specifications
//
// Contract-level rules live under SPECS/ in the repository. Start with
// SPECS/00-overview.md.
package requests
