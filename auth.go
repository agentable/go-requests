package requests

import (
	"fmt"
	"net/http"
)

// AuthMethod defines the interface for applying authentication strategies to requests.
type AuthMethod interface {
	// Apply applies the authentication strategy to req.
	Apply(req *http.Request)
	// Valid reports whether the authentication strategy is configured.
	Valid() bool
}

type authShapeKind uint8

const (
	builtInAuthUnknown authShapeKind = iota
	builtInAuthBasic
	builtInAuthBearer
	builtInAuthCustom
)

// authShape is the non-materializing identity of a built-in auth method. It
// deliberately contains no credential data and never calls AuthMethod methods.
type authShape struct {
	kind authShapeKind
}

// builtInAuthShape recognizes built-in authentication by concrete shape. The
// second result distinguishes an unknown caller-defined implementation from a
// recognized built-in method. Invalid built-in fields are reported without
// invoking Valid or Apply.
func builtInAuthShape(auth AuthMethod) (authShape, bool, error) {
	if auth == nil {
		return authShape{}, false, nil
	}
	if isNilInterface(auth) {
		return authShape{}, true, fmt.Errorf("%w: auth", ErrInvalidConfigValue)
	}

	switch value := auth.(type) {
	case BasicAuth:
		if value.Username == "" || value.Password == "" {
			return authShape{}, true, fmt.Errorf("%w: auth", ErrInvalidConfigValue)
		}
		return authShape{kind: builtInAuthBasic}, true, nil
	case *BasicAuth:
		if value.Username == "" || value.Password == "" {
			return authShape{}, true, fmt.Errorf("%w: auth", ErrInvalidConfigValue)
		}
		return authShape{kind: builtInAuthBasic}, true, nil
	case BearerAuth:
		if value.Token == "" {
			return authShape{}, true, fmt.Errorf("%w: auth", ErrInvalidConfigValue)
		}
		return authShape{kind: builtInAuthBearer}, true, nil
	case *BearerAuth:
		if value.Token == "" {
			return authShape{}, true, fmt.Errorf("%w: auth", ErrInvalidConfigValue)
		}
		return authShape{kind: builtInAuthBearer}, true, nil
	case CustomAuth:
		if value.Header == "" {
			return authShape{}, true, fmt.Errorf("%w: auth", ErrInvalidConfigValue)
		}
		return authShape{kind: builtInAuthCustom}, true, nil
	case *CustomAuth:
		if value.Header == "" {
			return authShape{}, true, fmt.Errorf("%w: auth", ErrInvalidConfigValue)
		}
		return authShape{kind: builtInAuthCustom}, true, nil
	default:
		return authShape{}, false, nil
	}
}

// BasicAuth represents HTTP Basic Authentication credentials.
type BasicAuth struct {
	Username string // Username is the HTTP Basic Authentication username.
	Password string // Password is the HTTP Basic Authentication password.
}

// Apply adds the Basic Auth credentials to the request.
func (b BasicAuth) Apply(req *http.Request) {
	req.SetBasicAuth(b.Username, b.Password)
}

// Valid checks if the Basic Auth credentials are present.
func (b BasicAuth) Valid() bool {
	return b.Username != "" && b.Password != ""
}

// BearerAuth represents an OAuth 2.0 Bearer token.
type BearerAuth struct {
	Token string // Token is the bearer token value.
}

// Apply adds the Bearer token to the request's Authorization header.
func (b BearerAuth) Apply(req *http.Request) {
	if b.Valid() {
		req.Header.Set("Authorization", "Bearer "+b.Token)
	}
}

// Valid checks if the Bearer token is present.
func (b BearerAuth) Valid() bool {
	return b.Token != ""
}

// CustomAuth allows for custom Authorization header values.
type CustomAuth struct {
	Header string // Header is the Authorization header value.
}

// Apply sets a custom Authorization header value.
func (c CustomAuth) Apply(req *http.Request) {
	if c.Valid() {
		req.Header.Set("Authorization", c.Header)
	}
}

// Valid checks if the custom Authorization header value is present.
func (c CustomAuth) Valid() bool {
	return c.Header != ""
}
