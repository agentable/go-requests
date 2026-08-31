// Package http3 provides optional HTTP/3 transports for requests.
//
// The package keeps QUIC dependencies outside the core requests module. Build a
// transport with [Transport], pass it to requests.WithTransport, and close the
// transport after all clients and in-flight requests using it are done.
package http3
