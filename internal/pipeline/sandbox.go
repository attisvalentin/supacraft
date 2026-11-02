package pipeline

import (
	"context"
	"net/http"
)

// Sandbox stands in for the outside world while `supacraft test` runs: a
// request whose context carries one sends its email to Mailer and its
// http.request calls to HTTP, never to the network. It is per request, so
// an API serving real traffic in the same process is unaffected.
type Sandbox struct {
	Mailer Mailer
	HTTP   http.RoundTripper
	// Errors receives what would go to the error log.
	Errors func(msg string)
}

type sandboxKey struct{}

// WithSandbox returns ctx carrying sb.
func WithSandbox(ctx context.Context, sb *Sandbox) context.Context {
	return context.WithValue(ctx, sandboxKey{}, sb)
}

// SandboxFrom returns the sandbox of ctx, or nil.
func SandboxFrom(ctx context.Context) *Sandbox {
	sb, _ := ctx.Value(sandboxKey{}).(*Sandbox)
	return sb
}
