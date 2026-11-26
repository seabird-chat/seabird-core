package core

import (
	"strings"

	"github.com/seabird-chat/seabird-core/internal/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// backendProto describes a backend for plugins.
func backendProto(b Backend) *pb.Backend {
	return &pb.Backend{Id: b.ID, Type: b.Type, Connected: b.Connected}
}

func backendUpdatedEvent(b Backend) *pb.Event {
	return &pb.Event{Inner: &pb.Event_BackendUpdated{BackendUpdated: &pb.BackendUpdatedEvent{
		Backend: backendProto(b),
	}}}
}

// idKinds names what each ID prefix refers to, for errors.
var idKinds = map[string]string{
	backendIDPrefix: "backend",
	channelIDPrefix: "channel",
	userIDPrefix:    "user",
}

// checkID checks that a plugin's request names the right kind of thing in
// field, so a user ID sent as a channel gets a clear error.
func checkID(field, id, prefix string) error {
	got, _, ok := strings.Cut(id, "_")
	if ok && got == prefix {
		return nil
	}
	return status.Errorf(codes.InvalidArgument, "invalid %s: %q isn't a %s ID", field, id, idKinds[prefix])
}
