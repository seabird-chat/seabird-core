package core

import (
	"fmt"
	"strings"
)

// BackendID identifies a connected chat backend. Scheme is the backend type
// ("irc", "discord", ...) and ID distinguishes multiple instances of the same
// type. It is a comparable struct so it can be used directly as a map key.
type BackendID struct {
	Scheme string
	ID     string
}

// String renders the ID in its wire form, "scheme://id".
func (b BackendID) String() string {
	return percentEncode(b.Scheme) + "://" + percentEncode(b.ID)
}

// Relative builds a FullID for a path within this backend.
func (b BackendID) Relative(path string) FullID {
	return FullID{Backend: b, Path: path}
}

// ParseBackendID parses the wire form produced by BackendID.String.
func ParseBackendID(s string) (BackendID, error) {
	scheme, id, ok := strings.Cut(s, "://")
	if !ok {
		return BackendID{}, fmt.Errorf("backend id %q is missing the id part", s)
	}

	return BackendID{Scheme: percentDecode(scheme), ID: percentDecode(id)}, nil
}

// FullID is a backend-qualified channel or user ID. Clients always talk in
// full IDs; backends only ever see the Path portion.
type FullID struct {
	Backend BackendID
	Path    string
}

// String renders the ID in its wire form, "scheme://id/path".
func (f FullID) String() string {
	return f.Backend.String() + "/" + percentEncode(f.Path)
}

// ParseFullID parses the wire form produced by FullID.String.
func ParseFullID(s string) (FullID, error) {
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok {
		return FullID{}, fmt.Errorf("id %q is missing the id part", s)
	}

	id, path, ok := strings.Cut(rest, "/")
	if !ok {
		return FullID{}, fmt.Errorf("id %q is missing the path part", s)
	}

	return FullID{
		Backend: BackendID{Scheme: percentDecode(scheme), ID: percentDecode(id)},
		Path:    percentDecode(path),
	}, nil
}

const upperhex = "0123456789ABCDEF"

// percentEncode escapes every byte which isn't ASCII alphanumeric. IDs are
// stored in plugin configs and passed back to us verbatim, so this escape set
// has to stay exactly as wide as it is: anything narrower would stop older IDs
// from round-tripping.
func percentEncode(s string) string {
	var buf strings.Builder

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			buf.WriteByte(c)
		default:
			buf.WriteByte('%')
			buf.WriteByte(upperhex[c>>4])
			buf.WriteByte(upperhex[c&0x0f])
		}
	}

	return buf.String()
}

// percentDecode reverses percentEncode. A '%' which isn't followed by two hex
// digits is passed through literally rather than treated as an error.
func percentDecode(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}

	var buf strings.Builder

	for i := 0; i < len(s); {
		if s[i] == '%' && i+2 < len(s) {
			hi, hiOK := unhex(s[i+1])
			lo, loOK := unhex(s[i+2])
			if hiOK && loOK {
				buf.WriteByte(hi<<4 | lo)
				i += 3
				continue
			}
		}

		buf.WriteByte(s[i])
		i++
	}

	return buf.String()
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}
