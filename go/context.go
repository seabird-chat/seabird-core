package seabird

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

const (
	contextKeyTag      = ContextKey("tag")
	contextKeyStreamID = ContextKey("streamID")
)

type ContextKey string

func (key ContextKey) String() string {
	return fmt.Sprintf("ContextKey(%s)", string(key))
}

func WithTag(ctx context.Context, tag string) context.Context {
	return context.WithValue(ctx, contextKeyTag, tag)
}

func CtxTag(ctx context.Context) string {
	tag, ok := ctx.Value(contextKeyTag).(string)
	if !ok {
		return "<unknown>"
	}

	return tag
}

func WithStreamID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, contextKeyStreamID, id)
}

func CtxStreamID(ctx context.Context) uuid.UUID {
	streamID, ok := ctx.Value(contextKeyStreamID).(uuid.UUID)
	if !ok {
		return uuid.Nil
	}

	return streamID
}
