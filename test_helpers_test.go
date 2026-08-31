package requests

import (
	"errors"
	"testing"

	"github.com/test-go/testify/require"
)

var errTestTimeout = errors.New("test timeout")

func newTestClient(t testing.TB, opts ...Option) *Client {
	t.Helper()

	client, err := New(opts...)
	require.NoError(t, err)
	return client
}
