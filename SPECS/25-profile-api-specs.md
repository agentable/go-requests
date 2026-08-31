# Profile API Specs

## Overview

`Profile` expresses a coherent client identity as construction-time options. A
profile may contribute default headers, ordered headers, protocol preferences,
and fingerprint hooks.

Profiles are not request builders. They configure reusable client defaults before requests are created.

## Contract

The profile contract is:

```go
type Profile interface {
	Name() string
	Options() []Option
}
```

`WithProfile(profile)` applies the returned options in order during `New` or `Clone`. If a profile option fails, construction fails with profile context.
A nil or typed-nil profile is invalid and returns `ErrInvalidConfigValue` instead of panicking. Nil entries in extension profile option lists are ignored, matching root functional-option composition.

TLS options on fingerprint profile constructors capture a standard shallow
`tls.Config.Clone` when the profile option is applied. Delayed profile
application MUST NOT retain or mutate the caller's top-level config pointer.
Referenced collaborators retain the ownership rules defined in
`SPECS/20-client-api-specs.md`.

HTTP/3 is intentionally outside the profile contract. Its extension returns a
closable transport handle that callers pass to `WithTransport`, keeping QUIC
transport ownership visible at the call site.

## Scope

Profiles MAY contribute:

- default headers
- ordered headers
- protocol preferences such as HTTP/2
- TLS fingerprint configuration in extension modules

Profiles MUST NOT apply request-local state. Request-local headers and ordered headers continue to override profile defaults according to `SPECS/21-request-builder-api-specs.md`.

## Package Boundary

The root package defines the profile interface and option only. Concrete
profiles SHOULD live in small identity extension packages such as `browser` and
`fingerprint`, so root APIs stay stable and do not accumulate browser-version
or fingerprint dependency details. Protocol transports with an independent
close lifecycle MUST expose that lifecycle directly rather than hide it behind
a profile.

Fixed browser header profiles in the `browser` module MUST name the browser major version they emit, such as `Chrome145` or `Firefox148`. Auto-style fingerprint profiles in the `fingerprint` module may stay unversioned when the exact ClientHello version is controlled by the uTLS dependency.

## Forbidden

- Do not expose browser version details as root package methods.
- Do not use unversioned names for fixed browser header identities.
- Do not make profile application per-request.
- Do not use profile as a general mutation hook.
- Do not hide a closable transport behind a profile.
- Do not silently change TLS verification defaults from a profile.

## Contract Invariants

- Profiles are client-level only.
- Profile errors surface through `New` or `Clone`.
- `WithProfile` preserves construction-time option ordering.
- Unsupported profile/option combinations fail rather than partially applying or changing protocol.
- Request-local metadata overrides profile defaults.
