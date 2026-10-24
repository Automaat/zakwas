package runner

import "context"

type operationKey struct{}

// WithOperation identifies the provider or source behind an external command.
func WithOperation(ctx context.Context, name string) context.Context {
	if parent := Operation(ctx); parent != "" {
		name = parent + " " + name
	}
	return context.WithValue(ctx, operationKey{}, name)
}

// Operation returns the command's provider or source description, when set.
func Operation(ctx context.Context) string {
	name, _ := ctx.Value(operationKey{}).(string)
	return name
}
