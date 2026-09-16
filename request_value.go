package requests

import (
	"bytes"
	"fmt"
	"io"
)

const (
	disclosureRedacted     = "<redacted>"
	disclosurePublicMarker = "<public>"
)

type disclosureState uint8

const (
	disclosurePrivate disclosureState = iota
	disclosurePublic
)

// Value is a string request value with an explicit disclosure capability.
//
// The zero value is private. Private values remain redacted through the
// package's formatting interfaces; only code inside this package
// can inspect their original data while building a request plan.
type Value struct {
	value string
	state disclosureState
}

// Public marks value as approved for projection.
func Public(value string) Value {
	return Value{value: value, state: disclosurePublic}
}

func (v Value) isPublic() bool {
	return v.state == disclosurePublic
}

func (v Value) rawValue() string {
	return v.value
}

func (v Value) displayValue() string {
	if !v.isPublic() {
		return disclosureRedacted
	}
	return disclosurePublicMarker
}

// String returns a fixed disclosure-safe marker.
func (v Value) String() string {
	return v.displayValue()
}

// GoString returns a fixed disclosure-safe marker.
func (v Value) GoString() string {
	return v.displayValue()
}

// Format keeps every fmt path behind the disclosure boundary.
func (v Value) Format(state fmt.State, verb rune) {
	formatDisclosure(state, verb, v.displayValue())
}

// Payload is an owned byte request value with an explicit disclosure
// capability. PublicPayload clones caller bytes.
//
// The zero value is private.
type Payload struct {
	value []byte
	state disclosureState
}

// PublicPayload marks bytes as approved for projection and owns a clone.
func PublicPayload(value []byte) Payload {
	return Payload{value: bytes.Clone(value), state: disclosurePublic}
}

func (p Payload) isPublic() bool {
	return p.state == disclosurePublic
}

func (p Payload) cloneBytes() []byte {
	return bytes.Clone(p.value)
}

func (p Payload) displayValue() string {
	if !p.isPublic() {
		return disclosureRedacted
	}
	return disclosurePublicMarker
}

// String returns a fixed disclosure-safe marker.
func (p Payload) String() string {
	return p.displayValue()
}

// GoString returns a fixed disclosure-safe marker.
func (p Payload) GoString() string {
	return p.displayValue()
}

// Format keeps every fmt path behind the disclosure boundary.
func (p Payload) Format(state fmt.State, verb rune) {
	formatDisclosure(state, verb, p.displayValue())
}

func formatDisclosure(state fmt.State, verb rune, value string) {
	switch verb {
	case 'v', 's':
		_, _ = io.WriteString(state, value)
	case 'q':
		_, _ = fmt.Fprintf(state, "%q", value)
	default:
		_, _ = fmt.Fprintf(state, "%%!%c(string=%s)", verb, value)
	}
}
