package core

import (
	"context"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/seabird-chat/seabird-core/internal/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// callTestRPCs makes one call of each kind (unary, server stream, and
// bidirectional stream) and returns the resulting errors by name. It is meant
// for requests that are rejected before reaching a handler, since the
// bidirectional call otherwise waits for the backend's hello. The stream call
// registers a "weather" command.
func callTestRPCs(ctx context.Context, conn *grpc.ClientConn) map[string]error {
	errs := make(map[string]error)

	_, errs["GetCoreInfo"] = pb.NewSeabirdClient(conn).GetCoreInfo(ctx, &pb.CoreInfoRequest{})

	events, err := pb.NewSeabirdClient(conn).StreamEvents(ctx,
		&pb.StreamEventsRequest{Commands: commandsNamed("weather")})
	if err == nil {
		_, err = events.Recv()
	}
	errs["StreamEvents"] = err

	ingest, err := pb.NewChatIngestClient(conn).IngestEvents(ctx)
	if err == nil {
		_, err = ingest.Recv()
	}
	errs["IngestEvents"] = err

	return errs
}

func TestAuthRejectsRequests(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)

	tests := []struct {
		name    string
		headers []string
		message string
	}{
		{name: "no header", message: "missing authorization header"},
		{name: "other scheme", headers: []string{"Basic " + testToken}, message: "unknown auth method"},
		{name: "lowercase scheme", headers: []string{"bearer " + testToken}, message: "unknown auth method"},
		{name: "scheme without token", headers: []string{"Bearer"}, message: "missing auth token"},
		{name: "empty token", headers: []string{"Bearer "}, message: "invalid auth token"},
		{name: "unknown token", headers: []string{"Bearer wrong"}, message: "invalid auth token"},
		{name: "two headers", headers: []string{"Bearer " + testToken, "Bearer " + testToken}, message: "multiple authorization headers"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for _, header := range tt.headers {
				ctx = withAuthHeader(ctx, header)
			}

			for name, err := range callTestRPCs(ctx, env.conn) {
				assert.Equal(t, codes.Unauthenticated, status.Code(err), name)
				assert.Equal(t, tt.message, status.Convert(err).Message(), name)
			}

			// A rejected plugin registers nothing.
			resp, err := env.plugin.RegisteredCommands(env.ctx, &pb.CommandsRequest{})
			assert.NoError(t, err)
			assert.Equal(t, 0, len(resp.GetCommands()))
		})
	}
}

// Failures that aren't meant for the client, like the database going away,
// reach it as a generic error without their cause.
func TestServerFaultsAreHidden(t *testing.T) {
	t.Parallel()

	env := startTestEnv(t)
	env.breakDatabase(t)

	_, err := env.plugin.GetCoreInfo(env.ctx, &pb.CoreInfoRequest{})
	assertStatus(t, err, codes.Internal, "internal error")
}
