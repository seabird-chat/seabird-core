package seabird

import (
	"context"
	"net/url"
	"strings"

	"google.golang.org/grpc"
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

// wrappedServerStream allows us to replace the context on a ServerStream. This
// is currently only used to add additional values.
type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *wrappedServerStream) Context() context.Context {
	return s.ctx
}
