package requests

import (
	"io"
)

// Encoder encodes values into an io.Reader.
type Encoder interface {
	// Encode encodes v and returns a reader for the encoded payload. The reader
	// owns no resources that require an additional Close call.
	Encode(v any) (io.Reader, error)
}

// Decoder decodes data from an io.Reader into a value.
type Decoder interface {
	// Decode decodes data from r into v.
	Decode(r io.Reader, v any) error
}
