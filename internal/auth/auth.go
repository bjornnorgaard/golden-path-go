// Package auth authenticates requests and enforces "deny by default".
//
// Every route requires an authenticated principal unless it is on the explicit
// anonymous allow-list (IsAnonymousPath). Authentication itself is behind the
// Authenticator interface: production uses signed session cookies (Sessions),
// tests substitute a header-based implementation at this one seam.
package auth

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/bjornnorgaard/golden-path-go/internal/id"
)

// Principal is the authenticated caller.
type Principal struct {
	UserID id.UserID
	Email  string
}

// Authenticator resolves the principal of a request, if there is one.
type Authenticator interface {
	Authenticate(r *http.Request) (Principal, bool)
}

type ctxKey struct{}

// WithPrincipal returns a context carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the principal placed by Middleware.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// anonymousPaths is the complete allow-list of routes reachable without a session.
// Adding to it is a security decision; RouteAuthorization tests pin this list.
var anonymousPaths = map[string]struct{}{
	"/login":                {},
	"/logout":               {},
	"/access-denied":        {},
	"/auth/callback/google": {},
	"/healthz":              {},
	"/readyz":               {},
}

// IsAnonymousPath reports whether the path may be served without authentication.
func IsAnonymousPath(path string) bool {
	_, ok := anonymousPaths[path]
	return ok
}

// AnonymousPaths returns a copy of the allow-list, for tests.
func AnonymousPaths() []string {
	out := make([]string, 0, len(anonymousPaths))
	for p := range anonymousPaths {
		out = append(out, p)
	}
	return out
}

// Middleware rejects unauthenticated requests to anything not on the allow-list.
// Browser navigations (GET accepting HTML) are redirected to /login; everything
// else gets 401.
func Middleware(authn Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := authn.Authenticate(r); ok {
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
			return
		}
		if IsAnonymousPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.Redirect(w, r, "/login?returnUrl="+url.QueryEscape(r.URL.Path), http.StatusFound) //nolint:gosec // target is a fixed local path
			return
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"title":"Unauthorized","status":401,"detail":"Authentication is required."}`))
	})
}
