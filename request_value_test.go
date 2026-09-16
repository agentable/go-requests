package requests

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

const disclosureCanary = "disclosure-canary-7f2d"

func TestDisclosureValueZeroAndEmpty(t *testing.T) {
	var zeroValue Value
	var zeroPayload Payload

	assertDisclosureRedacted(t, zeroValue, disclosureCanary)
	assertDisclosureRedacted(t, zeroPayload, disclosureCanary)

	publicValue := Public("")
	publicPayload := PublicPayload(nil)
	assert.True(t, publicValue.isPublic())
	assert.True(t, publicPayload.isPublic())
	assert.False(t, zeroValue.isPublic())
	assert.False(t, zeroPayload.isPublic())
	assertDisclosurePublicFormatting(t, publicValue, "Value")
	assertDisclosurePublicFormatting(t, publicPayload, "Payload")

	assertDisclosureRedacted(t, Value{value: disclosureCanary}, disclosureCanary)
	assertDisclosureRedacted(t, Payload{value: []byte(disclosureCanary)}, disclosureCanary)
}

func TestDisclosureValueFormattingDoesNotLeakPrivateData(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "value", value: Value{value: disclosureCanary}},
		{name: "payload", value: Payload{value: []byte(disclosureCanary)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, format := range []string{"%v", "%+v", "%#v", "%q", "%s"} {
				formatted := fmt.Sprintf(format, test.value)
				assert.NotContains(t, formatted, disclosureCanary, format)
				assert.Contains(t, formatted, "redacted", format)
			}

			err := fmt.Errorf("request value: %v", test.value)
			assert.NotContains(t, err.Error(), disclosureCanary)
			assert.Contains(t, err.Error(), "redacted")
		})
	}
}

func TestDisclosurePublicValuesDoNotLeakThroughFormatting(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
	}{
		{name: "value", value: Public(disclosureCanary)},
		{name: "payload", value: PublicPayload([]byte(disclosureCanary))},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertDisclosurePublicFormatting(t, test.value, test.name)
		})
	}
}

func TestDisclosurePayloadOwnsCallerBytes(t *testing.T) {
	original := []byte(disclosureCanary)
	payload := PublicPayload(original)
	original[0] = 'X'

	assert.Equal(t, []byte(disclosureCanary), payload.cloneBytes())

	snapshot := payload.cloneBytes()
	snapshot[0] = 'Y'
	assert.Equal(t, []byte(disclosureCanary), payload.cloneBytes())
}

func assertDisclosurePublicFormatting(t *testing.T, value any, name string) {
	t.Helper()
	for _, format := range []string{"%v", "%+v", "%#v", "%q", "%s"} {
		formatted := fmt.Sprintf(format, value)
		assert.NotContains(t, formatted, disclosureCanary, name)
		assert.Contains(t, formatted, "public", name)
	}
	err := fmt.Errorf("request value: %v", value)
	assert.NotContains(t, err.Error(), disclosureCanary, name)
	assert.Contains(t, err.Error(), "public", name)
}

func assertDisclosureRedacted(t *testing.T, value any, canary string) {
	t.Helper()
	for _, format := range []string{"%v", "%+v", "%#v", "%q", "%s"} {
		formatted := fmt.Sprintf(format, value)
		assert.NotContains(t, formatted, canary, format)
		assert.Contains(t, formatted, "redacted", format)
	}
}
