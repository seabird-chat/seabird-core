package core

import (
	"testing"

	"github.com/alecthomas/assert/v2"
)

func TestBackendIDRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		id   BackendID
		want string
	}{
		{"simple", BackendID{Scheme: "irc", ID: "chat"}, "irc://chat"},
		{"slash in id", BackendID{Scheme: "irc", ID: "a/b"}, "irc://a%2Fb"},
		{"separator in id", BackendID{Scheme: "irc", ID: "a://b"}, "irc://a%3A%2F%2Fb"},
		{"unicode", BackendID{Scheme: "irc", ID: "café"}, "irc://caf%C3%A9"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, test.id.String())

			parsed, err := ParseBackendID(test.want)
			assert.NoError(t, err)
			assert.Equal(t, test.id, parsed)
		})
	}
}

func TestFullIDRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		id   FullID
		want string
	}{
		{
			"simple",
			BackendID{Scheme: "irc", ID: "chat"}.Relative("#seabird"),
			"irc://chat/%23seabird",
		},
		{
			"slash in path",
			BackendID{Scheme: "discord", ID: "guild"}.Relative("a/b"),
			"discord://guild/a%2Fb",
		},
		{
			"empty path",
			BackendID{Scheme: "irc", ID: "chat"}.Relative(""),
			"irc://chat/",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, test.id.String())

			parsed, err := ParseFullID(test.want)
			assert.NoError(t, err)
			assert.Equal(t, test.id, parsed)
		})
	}
}

func TestParseErrors(t *testing.T) {
	_, err := ParseBackendID("irc")
	assert.Error(t, err)

	// A backend ID is not a valid full ID: it has no path separator.
	_, err = ParseFullID("irc://chat")
	assert.Error(t, err)

	_, err = ParseFullID("irc")
	assert.Error(t, err)
}

func TestPercentDecodePassesThroughInvalidEscapes(t *testing.T) {
	assert.Equal(t, "100%", percentDecode("100%"))
	assert.Equal(t, "%zz", percentDecode("%zz"))
	assert.Equal(t, "%2", percentDecode("%2"))
}
