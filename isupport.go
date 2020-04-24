package seabird

import (
	"strings"

	irc "gopkg.in/irc.v3"
)

func (s *Server) handleISupport(msg *irc.Message) {
	if len(msg.Params) < 2 {
		return
	}

	// Check for really old servers (or servers which based 005 off of rfc2812.
	if !strings.HasSuffix(msg.Trailing(), "server") {
		// This server doesn't appear to support ISupport messages. Here there
		// be dragons.
		return
	}

	s.isupportLock.Lock()
	defer s.isupportLock.Unlock()

	for _, param := range msg.Params[1 : len(msg.Params)-1] {
		data := strings.SplitN(param, "=", 2)
		if len(data) < 2 {
			s.isupport[data[0]] = ""
			continue
		}

		s.isupport[data[0]] = data[1]
	}
}

// IsEnabled will check for boolean ISupport values
func (s *Server) isupportIsEnabled(key string) bool {
	s.isupportLock.RLock()
	defer s.isupportLock.RUnlock()

	_, ok := s.isupport[key]
	return ok
}

// GetList will check for list ISupportValues
func (s *Server) isupportList(key string) ([]string, bool) {
	s.isupportLock.RLock()
	defer s.isupportLock.RUnlock()

	data, ok := s.isupport[key]
	if !ok {
		return nil, false
	}

	return strings.Split(data, ","), true
}

// GetMap will check for map ISupport values
func (s *Server) isupportMap(key string) (map[string]string, bool) {
	s.isupportLock.RLock()
	defer s.isupportLock.RUnlock()

	data, ok := s.isupport[key]
	if !ok {
		return nil, false
	}

	ret := make(map[string]string)

	for _, v := range strings.Split(data, ",") {
		innerData := strings.SplitN(v, ":", 2)
		if len(innerData) != 2 {
			return nil, false
		}

		ret[innerData[0]] = innerData[1]
	}

	return ret, true
}

// GetRaw will get the raw ISupport values
func (s *Server) isupportRaw(key string) (string, bool) {
	s.isupportLock.RLock()
	defer s.isupportLock.RUnlock()

	ret, ok := s.isupport[key]
	return ret, ok
}

func (s *Server) isupportPrefixMap() (map[rune]rune, bool) {
	// Sample: (qaohv)~&@%+
	prefix, _ := s.isupportRaw("PREFIX")

	// We only care about the symbols
	i := strings.IndexByte(prefix, ')')
	if len(prefix) == 0 || prefix[0] != '(' || i < 0 {
		// "Invalid prefix format"
		return nil, false
	}

	// We loop through the string using range so we get bytes, then we throw the
	// two results together in the map.
	var symbols []rune // ~&@%+
	for _, r := range prefix[i+1:] {
		symbols = append(symbols, r)
	}

	var modes []rune // qaohv
	for _, r := range prefix[1:i] {
		modes = append(modes, r)
	}

	if len(modes) != len(symbols) {
		// "Mismatched modes and symbols"
		return nil, false
	}

	prefixes := make(map[rune]rune)
	for k := range symbols {
		prefixes[symbols[k]] = modes[k]
	}

	return prefixes, true
}
