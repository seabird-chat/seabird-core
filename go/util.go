package seabird

import (
	"net/url"
	"strings"
)

func idParts(id string) (string, string, bool) {
	base, err := url.Parse(id)
	if err != nil {
		return "", "", false
	}

	path := base.Path
	base.Path = ""

	return base.String(), strings.TrimPrefix(path, "/"), true
}
