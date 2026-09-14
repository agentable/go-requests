# Logging API Specs

## Overview

Logging is optional and client-scoped. This spec defines the `Logger` contract, the package log levels, and the guarantees of the default logger.

## Logger Contract

A valid logger implements all of:

- `Debugf`
- `Infof`
- `Warnf`
- `Errorf`

`WithLogger(nil)` clears logging. A typed-nil `Logger` is invalid construction
and returns `ErrInvalidConfigValue` instead of entering runtime method dispatch.

The package log levels are:

- `LevelDebug`
- `LevelInfo`
- `LevelWarn`
- `LevelError`

## Default Logger

`NewDefaultLogger(output io.Writer, level Level)` returns a `*DefaultLogger`
backed by `log/slog`.

The level argument MUST be a `requests.Level`, not a `slog.Level`.

`DefaultLogger.SetLevel` changes the active threshold after construction. Level
configuration belongs to the concrete default logger; custom `Logger`
implementations only provide the four emission methods.

> **Why**: The package consumes emission only, so callers can supply a minimal
> logger without implementing unrelated configuration. The concrete default
> logger retains runtime level control without widening the consumer interface.
>
> **Rejected**: Exposing `slog.Logger` directly as the public logging interface.

## Operational Guarantees

- Logging is opt-in. If no logger is configured, the package performs no logging.
- Log messages are operational diagnostics, not a stable parsing interface.
- File-loading helpers and retry paths may log failures or retry events when a logger is configured.
- `RequestBuilder.Preview` is silent even when a logger is configured. It does
  not enter delivery or emit a dry-run log event.
- Preview validates method and resolved URL shape without calling
  `Request.Write`, `Request.WriteProxy`, `Request.AddCookie`, or another
  standard-library request serialization path. It returns a detached,
  policy-filtered result; it is not a wire dump.
- URL-bearing construction, preflight, and transport diagnostics omit URL
  userinfo, query values, and fragments.

## Forbidden

- Do not pass `slog.Level*` values to `NewDefaultLogger`; use `requests.Level*`.
- Do not require custom loggers to expose level configuration.
- Do not parse package log messages as a stable machine-readable API.
- Do not treat Preview output or Preview silence as a replacement for delivery
  diagnostics; Preview intentionally does not observe middleware or transport
  behavior.

## Contract Invariants

- The required logger methods are explicit.
- The package level enum is explicit.
- The boundary between the public logger contract and `slog` internals is explicit.
