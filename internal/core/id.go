package core

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// BackendID identifies a connected chat backend. As text it is "type://id".
type BackendID struct {
	Type string
	ID   string
}

// FullID identifies a channel or user on a backend. As text it is the
// backend's ID, a slash, and the backend's own ID for the thing.
type FullID struct {
	Backend BackendID
	Path    string
}

// Relative returns the ID of something on this backend.
func (b BackendID) Relative(path string) FullID {
	return FullID{Backend: b, Path: path}
}

func (b BackendID) String() string {
	return percentEncode(b.Type) + "://" + percentEncode(b.ID)
}

func (f FullID) String() string {
	return f.Backend.String() + "/" + percentEncode(f.Path)
}

var errMissingScheme = errors.New(`missing "://" after the backend type`)

// ParseBackendID parses the text form of a BackendID.
func ParseBackendID(s string) (BackendID, error) {
	scheme, id, ok := strings.Cut(s, "://")
	if !ok {
		return BackendID{}, errMissingScheme
	}

	return decodeBackendID(scheme, id)
}

// ParseFullID parses the text form of a FullID.
func ParseFullID(s string) (FullID, error) {
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok {
		return FullID{}, errMissingScheme
	}
	id, path, ok := strings.Cut(rest, "/")
	if !ok {
		return FullID{}, errors.New(`missing "/" before the path`)
	}

	backend, err := decodeBackendID(scheme, id)
	if err != nil {
		return FullID{}, err
	}
	decodedPath, err := percentDecode(path)
	if err != nil {
		return FullID{}, err
	}

	return FullID{Backend: backend, Path: decodedPath}, nil
}

func decodeBackendID(scheme, id string) (BackendID, error) {
	decodedScheme, err := percentDecode(scheme)
	if err != nil {
		return BackendID{}, err
	}
	decodedID, err := percentDecode(id)
	if err != nil {
		return BackendID{}, err
	}

	return BackendID{Type: decodedScheme, ID: decodedID}, nil
}

const upperHex = "0123456789ABCDEF"

// percentEncode escapes every byte that isn't an ASCII letter or digit.
func percentEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isAlphanumeric(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperHex[c>>4])
		b.WriteByte(upperHex[c&0x0f])
	}
	return b.String()
}

// percentDecode reverses percentEncode. A '%' that isn't followed by two hex
// digits is kept as is, and the result must be valid UTF-8.
func percentDecode(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, hiOK := fromHex(s[i+1])
			lo, loOK := fromHex(s[i+2])
			if hiOK && loOK {
				b.WriteByte(hi<<4 | lo)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}

	decoded := b.String()
	if !utf8.ValidString(decoded) {
		return "", errors.New("not valid UTF-8 once decoded")
	}
	return decoded, nil
}

func isAlphanumeric(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func fromHex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
