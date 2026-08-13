package requests

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/test-go/testify/require"
)

type testProfile struct {
	name string
	err  error
}

type nilOptionProfile struct{}

func (nilOptionProfile) Name() string { return "nil-option" }

func (nilOptionProfile) Options() []Option {
	return []Option{nil, WithBaseURL("https://profile.example")}
}

func (p testProfile) Name() string {
	return p.name
}

func (p testProfile) Options() []Option {
	return []Option{
		func(c *Client) error {
			if p.err != nil {
				return p.err
			}
			c.setDefaultHeader("X-Profile", p.name)
			return nil
		},
	}
}

func TestWithProfile(t *testing.T) {
	client := newTestClient(t, WithProfile(testProfile{name: "option"}))

	require.NotNil(t, client.headers)
	assert.Equal(t, "option", client.headers.Get("X-Profile"))
}

func TestWithProfileReturnsOptionError(t *testing.T) {
	client, err := New(WithProfile(testProfile{name: "broken", err: assert.AnError}))

	require.Error(t, err)
	assert.Nil(t, client)
	assert.ErrorIs(t, err, assert.AnError)
	assert.Contains(t, err.Error(), `apply profile "broken"`)
}

func TestWithProfileRejectsNilProfile(t *testing.T) {
	var typedNil *testProfile
	tests := []struct {
		name    string
		profile Profile
	}{
		{name: "nil", profile: nil},
		{name: "typed nil", profile: typedNil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := New(WithProfile(test.profile))

			require.Error(t, err)
			assert.Nil(t, client)
			assert.ErrorIs(t, err, ErrInvalidConfigValue)
		})
	}
}

func TestNilOptionsComposeAcrossConstructionAndProfiles(t *testing.T) {
	client, err := New(nil, WithProfile(nilOptionProfile{}))
	require.NoError(t, err)
	assert.Equal(t, "https://profile.example", client.GetBaseURL())

	clone, err := client.Clone(nil, WithBaseURL("https://clone.example"))
	require.NoError(t, err)
	assert.Equal(t, "https://profile.example", client.GetBaseURL())
	assert.Equal(t, "https://clone.example", clone.GetBaseURL())
}

func TestEnableHTTP2(t *testing.T) {
	client := newTestClient(t, WithHTTP2())

	transport, ok := client.httpClient.Transport.(*http.Transport)
	require.True(t, ok)
	assertHTTP2Configured(t, transport)
}
