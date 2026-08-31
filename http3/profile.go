package http3

import (
	"crypto/tls"
	"log/slog"
	"maps"

	"github.com/quic-go/quic-go"
	qhttp3 "github.com/quic-go/quic-go/http3"
)

type settings struct {
	tlsConfig              *tls.Config
	quicConfig             *quic.Config
	enableDatagrams        bool
	additionalSettings     map[uint64]uint64
	maxResponseHeaderBytes int
	disableCompression     bool
	logger                 *slog.Logger
}

// Option configures an HTTP/3 transport.
type Option func(*settings)

// WithTLSConfig captures a standard shallow clone for the HTTP/3 transport.
func WithTLSConfig(tlsConfig *tls.Config) Option {
	return func(s *settings) {
		s.tlsConfig = cloneTLSConfig(tlsConfig)
	}
}

// WithQUICConfig sets the QUIC configuration for the HTTP/3 transport.
func WithQUICConfig(quicConfig *quic.Config) Option {
	return func(s *settings) {
		s.quicConfig = quicConfig
	}
}

// WithDatagrams enables HTTP/3 datagram support.
func WithDatagrams() Option {
	return func(s *settings) {
		s.enableDatagrams = true
	}
}

// WithAdditionalSettings sets additional HTTP/3 settings.
func WithAdditionalSettings(values map[uint64]uint64) Option {
	return func(s *settings) {
		s.additionalSettings = maps.Clone(values)
	}
}

// WithMaxResponseHeaderBytes sets the response header byte limit.
func WithMaxResponseHeaderBytes(n int) Option {
	return func(s *settings) {
		s.maxResponseHeaderBytes = n
	}
}

// WithoutCompression disables automatic gzip request and response handling in the HTTP/3 transport.
func WithoutCompression() Option {
	return func(s *settings) {
		s.disableCompression = true
	}
}

// WithLogger sets the HTTP/3 transport logger.
func WithLogger(logger *slog.Logger) Option {
	return func(s *settings) {
		s.logger = logger
	}
}

// Transport returns a configured HTTP/3 transport.
func Transport(opts ...Option) *qhttp3.Transport {
	return newSettings(opts...).transport()
}

func newSettings(opts ...Option) settings {
	var s settings
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt(&s)
	}
	return s
}

func (s settings) transport() *qhttp3.Transport {
	return &qhttp3.Transport{
		TLSClientConfig:        cloneTLSConfig(s.tlsConfig),
		QUICConfig:             cloneQUICConfig(s.quicConfig, s.enableDatagrams),
		EnableDatagrams:        s.enableDatagrams,
		AdditionalSettings:     maps.Clone(s.additionalSettings),
		MaxResponseHeaderBytes: s.maxResponseHeaderBytes,
		DisableCompression:     s.disableCompression,
		Logger:                 s.logger,
	}
}

func cloneTLSConfig(config *tls.Config) *tls.Config {
	if config == nil {
		return nil
	}
	return config.Clone()
}

func cloneQUICConfig(config *quic.Config, enableDatagrams bool) *quic.Config {
	if config == nil {
		return nil
	}
	clone := config.Clone()
	if enableDatagrams {
		clone.EnableDatagrams = true
	}
	return clone
}
