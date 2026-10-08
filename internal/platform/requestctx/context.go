package requestctx

import "context"

type key uint8

const (
	idKey key = iota + 1
	adminKey
)

func WithID(ctx context.Context, id string) context.Context { return context.WithValue(ctx, idKey, id) }
func ID(ctx context.Context) string                         { value, _ := ctx.Value(idKey).(string); return value }
func WithAdmin(ctx context.Context, username string) context.Context {
	return context.WithValue(ctx, adminKey, username)
}
func Admin(ctx context.Context) string { value, _ := ctx.Value(adminKey).(string); return value }
