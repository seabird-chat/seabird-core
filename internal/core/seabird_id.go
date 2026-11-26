package core

import (
	"crypto/rand"
	"encoding/base32"
)

// Seabird IDs are a prefix naming the kind of thing, an underscore, and random
// lowercase Crockford base32. Only the prefix means anything; the rest is
// compared as is.
const (
	backendIDPrefix = "sbb"
	channelIDPrefix = "sbc"
	userIDPrefix    = "sbu"
	tokenPrefix     = "sbt"
)

// seabirdIDBytes is how much randomness an ID has. IDs are looked up as whole
// strings, so making new ones longer later leaves the old ones valid.
const seabirdIDBytes = 8

// tokenBytes is how much randomness an auth token has. Tokens are secrets, so
// unlike IDs they need to be hard to guess.
const tokenBytes = 16

// crockfordEncoding is Douglas Crockford's base32 alphabet, lowercased and
// without padding. It drops i, l and o, which are easily mistaken for 1 and 0,
// and u, so IDs stay easy to read and type. It also sorts like the bytes it
// encodes. See https://www.crockford.com/base32.html.
var crockfordEncoding = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").
	WithPadding(base32.NoPadding)

// newSeabirdID returns a new random ID with the given prefix.
func newSeabirdID(prefix string) string {
	return newPrefixed(prefix, seabirdIDBytes)
}

// newToken returns a new random auth token.
func newToken() string {
	return newPrefixed(tokenPrefix, tokenBytes)
}

// newPrefixed returns prefix, an underscore, and n random bytes as Crockford
// base32.
func newPrefixed(prefix string, n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return prefix + "_" + crockfordEncoding.EncodeToString(b)
}
