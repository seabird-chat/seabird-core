package core

import (
	"testing"

	"github.com/alecthomas/assert/v2"
)

// The expected values below match what the Rust server produces for the same
// inputs, since IDs are visible to plugins.

var backendIDStrings = []struct {
	typ, id, want string
}{
	{"irc", "libera", "irc://libera"},
	{"irc", "irc.libera.chat", "irc://irc%2Elibera%2Echat"},
	{"discord", "1234567890", "discord://1234567890"},
	{"a b", "c d", "a%20b://c%20d"},
	{"\xc3\xa9", "\xc3\xbc", "%C3%A9://%C3%BC"},
	{"\xe6\x97\xa5\xe6\x9c\xac", "\xe8\xaa\x9e", "%E6%97%A5%E6%9C%AC://%E8%AA%9E"},
	{"", "", "://"},
	{"a/b", "c/d", "a%2Fb://c%2Fd"},
	{"-_.~", "~._-", "%2D%5F%2E%7E://%7E%2E%5F%2D"},
	{"%41", "%", "%2541://%25"},
	{"UPPER", "lower09", "UPPER://lower09"},
	{"irc://x", "y", "irc%3A%2F%2Fx://y"},
	{"\x00\x0a\x09", "\x7f", "%00%0A%09://%7F"},
	{"irc", "#chan", "irc://%23chan"},
	{"irc", "user@host:6667", "irc://user%40host%3A6667"},
	{"irc", "a+b=c&d", "irc://a%2Bb%3Dc%26d"},
	{"\xf0\x9f\x98\x80", "x", "%F0%9F%98%80://x"},
}

var fullIDStrings = []struct {
	typ, id, path, want string
}{
	{"irc", "libera", "#chan", "irc://libera/%23chan"},
	{"irc", "libera", "user name", "irc://libera/user%20name"},
	{"irc", "libera", "a/b?c#d", "irc://libera/a%2Fb%3Fc%23d"},
	{"irc", "libera", "\xc3\xa9", "irc://libera/%C3%A9"},
	{"irc", "libera", "", "irc://libera/"},
	{"irc", "libera", "/", "irc://libera/%2F"},
	{"irc", "libera", "%", "irc://libera/%25"},
	{"irc", "libera", "%23", "irc://libera/%2523"},
	{"irc", "libera", "\xe6\x97\xa5\xe6\x9c\xac", "irc://libera/%E6%97%A5%E6%9C%AC"},
	{"irc", "libera", "!@#$%^&*()", "irc://libera/%21%40%23%24%25%5E%26%2A%28%29"},
	{"irc", "libera", "..", "irc://libera/%2E%2E"},
	{"irc", "libera", "a\x00b", "irc://libera/a%00b"},
	{"irc", "libera", "UPPER lower 09", "irc://libera/UPPER%20lower%2009"},
	{"irc", "libera", "\xf0\x9f\x98\x80", "irc://libera/%F0%9F%98%80"},
	{"irc", "libera", "p/q", "irc://libera/p%2Fq"},
	{"irc", "irc.libera.chat", "p/q", "irc://irc%2Elibera%2Echat/p%2Fq"},
	{"discord", "1234567890", "p/q", "discord://1234567890/p%2Fq"},
	{"a b", "c d", "p/q", "a%20b://c%20d/p%2Fq"},
	{"\xc3\xa9", "\xc3\xbc", "p/q", "%C3%A9://%C3%BC/p%2Fq"},
	{"\xe6\x97\xa5\xe6\x9c\xac", "\xe8\xaa\x9e", "p/q", "%E6%97%A5%E6%9C%AC://%E8%AA%9E/p%2Fq"},
}

// A nil backend or full ID means the input is rejected.
var idParses = []struct {
	in      string
	backend *BackendID
	full    *FullID
}{
	{"irc://libera", &BackendID{"irc", "libera"}, nil},
	{"irc://libera/", &BackendID{"irc", "libera/"}, &FullID{BackendID{"irc", "libera"}, ""}},
	{"irc://libera/#chan", &BackendID{"irc", "libera/#chan"}, &FullID{BackendID{"irc", "libera"}, "#chan"}},
	{"irc://libera/%23chan", &BackendID{"irc", "libera/#chan"}, &FullID{BackendID{"irc", "libera"}, "#chan"}},
	{"irc://libera/%23%63han", &BackendID{"irc", "libera/#chan"}, &FullID{BackendID{"irc", "libera"}, "#chan"}},
	{"irc:/libera/x", nil, nil},
	{"irc", nil, nil},
	{"", nil, nil},
	{"://", &BackendID{"", ""}, nil},
	{"://x", &BackendID{"", "x"}, nil},
	{"irc://", &BackendID{"irc", ""}, nil},
	{"irc:///", &BackendID{"irc", "/"}, &FullID{BackendID{"irc", ""}, ""}},
	{"a://b://c/d", &BackendID{"a", "b://c/d"}, &FullID{BackendID{"a", "b:"}, "/c/d"}},
	{"irc://a%2Fb/c%2Fd", &BackendID{"irc", "a/b/c/d"}, &FullID{BackendID{"irc", "a/b"}, "c/d"}},
	{"irc://libera/%zz", &BackendID{"irc", "libera/%zz"}, &FullID{BackendID{"irc", "libera"}, "%zz"}},
	{"irc://libera/%", &BackendID{"irc", "libera/%"}, &FullID{BackendID{"irc", "libera"}, "%"}},
	{"irc://libera/%2", &BackendID{"irc", "libera/%2"}, &FullID{BackendID{"irc", "libera"}, "%2"}},
	{"irc://libera/%2g", &BackendID{"irc", "libera/%2g"}, &FullID{BackendID{"irc", "libera"}, "%2g"}},
	{"irc://libera/%C3%A9", &BackendID{"irc", "libera/\xc3\xa9"}, &FullID{BackendID{"irc", "libera"}, "\xc3\xa9"}},
	{"irc://libera/%FF", nil, nil},
	{"irc://%FF/x", nil, nil},
	{"%FF://x/y", nil, nil},
	{"irc://libera/a/b", &BackendID{"irc", "libera/a/b"}, &FullID{BackendID{"irc", "libera"}, "a/b"}},
	{"irc://libera//", &BackendID{"irc", "libera//"}, &FullID{BackendID{"irc", "libera"}, "/"}},
	{"irc%3A%2F%2Fx://y/z", &BackendID{"irc://x", "y/z"}, &FullID{BackendID{"irc://x", "y"}, "z"}},
	{"irc://libera?x=1", &BackendID{"irc", "libera?x=1"}, nil},
	{"irc://libera/#chan?x", &BackendID{"irc", "libera/#chan?x"}, &FullID{BackendID{"irc", "libera"}, "#chan?x"}},
	{"IRC://Libera/Chan", &BackendID{"IRC", "Libera/Chan"}, &FullID{BackendID{"IRC", "Libera"}, "Chan"}},
	{"irc://irc%2Elibera%2Dchat/%23chan", &BackendID{"irc", "irc.libera-chat/#chan"}, &FullID{BackendID{"irc", "irc.libera-chat"}, "#chan"}},
	{"irc://libera/%e2%82%ac", &BackendID{"irc", "libera/\xe2\x82\xac"}, &FullID{BackendID{"irc", "libera"}, "\xe2\x82\xac"}},
	{"irc://libera/%E2%82", nil, nil},
	{"irc://libera/a%00b", &BackendID{"irc", "libera/a\x00b"}, &FullID{BackendID{"irc", "libera"}, "a\x00b"}},
	{"irc://libera/+", &BackendID{"irc", "libera/+"}, &FullID{BackendID{"irc", "libera"}, "+"}},
	{"irc://li bera/x", &BackendID{"irc", "li bera/x"}, &FullID{BackendID{"irc", "li bera"}, "x"}},
	{"irc://libera/\x0a", &BackendID{"irc", "libera/\x0a"}, &FullID{BackendID{"irc", "libera"}, "\x0a"}},
	{"irc://libera/%25", &BackendID{"irc", "libera/%"}, &FullID{BackendID{"irc", "libera"}, "%"}},
	{"irc://libera/%2525", &BackendID{"irc", "libera/%25"}, &FullID{BackendID{"irc", "libera"}, "%25"}},
	{"irc://libera/%%41", &BackendID{"irc", "libera/%A"}, &FullID{BackendID{"irc", "libera"}, "%A"}},
	{"irc://libera/%4", &BackendID{"irc", "libera/%4"}, &FullID{BackendID{"irc", "libera"}, "%4"}},
	{"irc://libera/%41%", &BackendID{"irc", "libera/A%"}, &FullID{BackendID{"irc", "libera"}, "A%"}},
	{"irc://libera/%F0%9F%98%80", &BackendID{"irc", "libera/\xf0\x9f\x98\x80"}, &FullID{BackendID{"irc", "libera"}, "\xf0\x9f\x98\x80"}},
	{"irc://libera/%F0%9F%98", nil, nil},
	{"irc://lib%00era/x", &BackendID{"irc", "lib\x00era/x"}, &FullID{BackendID{"irc", "lib\x00era"}, "x"}},
}

func TestBackendIDString(t *testing.T) {
	t.Parallel()

	for _, c := range backendIDStrings {
		assert.Equal(t, c.want, BackendID{Type: c.typ, ID: c.id}.String(), c.typ+"/"+c.id)
	}
}

func TestFullIDString(t *testing.T) {
	t.Parallel()

	for _, c := range fullIDStrings {
		id := FullID{Backend: BackendID{Type: c.typ, ID: c.id}, Path: c.path}
		assert.Equal(t, c.want, id.String(), c.typ+"/"+c.id+"/"+c.path)
	}
}

func TestParseBackendID(t *testing.T) {
	t.Parallel()

	for _, c := range idParses {
		got, err := ParseBackendID(c.in)
		if c.backend == nil {
			assert.Error(t, err, c.in)
			continue
		}
		assert.NoError(t, err, c.in)
		assert.Equal(t, *c.backend, got, c.in)
	}
}

func TestParseFullID(t *testing.T) {
	t.Parallel()

	for _, c := range idParses {
		got, err := ParseFullID(c.in)
		if c.full == nil {
			assert.Error(t, err, c.in)
			continue
		}
		assert.NoError(t, err, c.in)
		assert.Equal(t, *c.full, got, c.in)
	}
}

func TestIDsRoundTrip(t *testing.T) {
	t.Parallel()

	for _, c := range backendIDStrings {
		id := BackendID{Type: c.typ, ID: c.id}
		got, err := ParseBackendID(id.String())
		assert.NoError(t, err, c.want)
		assert.Equal(t, id, got)
	}
	for _, c := range fullIDStrings {
		id := FullID{Backend: BackendID{Type: c.typ, ID: c.id}, Path: c.path}
		got, err := ParseFullID(id.String())
		assert.NoError(t, err, c.want)
		assert.Equal(t, id, got)
	}
}

func TestIDsRejectInvalidUTF8(t *testing.T) {
	t.Parallel()

	// Raw invalid bytes can't be built in Rust strings, so the shared cases
	// only cover them in escaped form.
	_, err := ParseBackendID("irc://\xff")
	assert.Error(t, err)
	_, err = ParseFullID("irc://libera/\xff")
	assert.Error(t, err)
}

func TestRelative(t *testing.T) {
	t.Parallel()

	backend := BackendID{Type: "irc", ID: "libera"}
	assert.Equal(t, FullID{Backend: backend, Path: "#chan"}, backend.Relative("#chan"))
	assert.Equal(t, "irc://libera/%23chan", backend.Relative("#chan").String())
}
