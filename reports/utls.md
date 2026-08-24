# uTLS v1.8.2: fingerprint presets do not honor `CurvePreferences`

## Trigger

Create a preset connection with `utls.UClient`, such as
`utls.HelloChrome_Auto`, while setting a non-empty
`utls.Config.CurvePreferences` converted from `crypto/tls.Config`.

## Expected behavior

An explicit curve allowlist should either control the supported groups sent in
ClientHello or be rejected before the handshake.

## Actual behavior

The preset's `SupportedCurvesExtension` and `KeyShareExtension` control the
ClientHello groups. `Config.CurvePreferences` is not reconciled with those
extensions, so the wire fingerprint can advertise groups outside the caller's
allowlist. Go 1.27 groups such as pure `MLKEM1024`, `SecP256r1MLKEM768`, and
`SecP384r1MLKEM1024` therefore cannot be selected through that config field.
No dependency error or warning reports the mismatch.

## Non-code workaround

Leave `CurvePreferences` empty when using a uTLS preset and accept the groups
encoded by that preset. Use the standard `crypto/tls` transport when an
explicit curve allowlist is required.
