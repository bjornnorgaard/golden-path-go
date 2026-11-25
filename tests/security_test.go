package tests

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bjornnorgaard/golden-path-go/internal/app"
	"github.com/bjornnorgaard/golden-path-go/internal/auth"
	"github.com/bjornnorgaard/golden-path-go/internal/config"
)

// Deny-by-default: every registered route except the pinned allow-list rejects anonymous callers.
func TestRouteAuthorization_EveryRouteRequiresAuthenticationUnlessAllowListed(t *testing.T) {
	t.Parallel()
	c := anonymous(t)
	allowed := auth.AnonymousPaths()

	for _, pattern := range suite.app.Routes() {
		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			t.Fatalf("route %q has no method", pattern)
		}
		path = strings.TrimSuffix(path, "/") // subtree patterns
		if slices.Contains(allowed, path) {
			continue
		}
		t.Run(pattern, func(t *testing.T) {
			r := c.do(method, path+"/probe", map[string]any{})
			if method == http.MethodGet {
				r = c.do(method, path, nil)
			} else {
				r = c.do(method, path, map[string]any{})
			}
			if r.Status != http.StatusUnauthorized {
				t.Fatalf("%s as anonymous = %d, want 401", pattern, r.Status)
			}
		})
	}
}

// Pins the allow-list: widening anonymous access must be a deliberate, reviewed change to this test.
func TestRouteAuthorization_AnonymousAllowListIsPinned(t *testing.T) {
	t.Parallel()
	got := auth.AnonymousPaths()
	sort.Strings(got)
	want := []string{"/access-denied", "/auth/callback/google", "/healthz", "/login", "/logout", "/readyz"}
	if !slices.Equal(got, want) {
		t.Fatalf("anonymous paths = %v, want %v", got, want)
	}
}

func TestHealthEndpoints_AreAnonymous(t *testing.T) {
	t.Parallel()
	c := anonymous(t)
	for _, p := range []string{"/healthz", "/readyz"} {
		if r := c.do(http.MethodGet, p, nil); r.Status != http.StatusOK {
			t.Fatalf("%s = %d, want 200", p, r.Status)
		}
	}
}

func TestLogin_WithoutConfiguredProvider_IsRejected(t *testing.T) {
	t.Parallel()
	c := anonymous(t)
	for _, p := range []string{"/login", "/login?provider=google", "/login?provider=facebook"} {
		if r := c.do(http.MethodGet, p, nil); r.Status != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", p, r.Status)
		}
	}
}

func TestBrowserNavigationWithoutSession_RedirectsToLogin(t *testing.T) {
	t.Parallel()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, suite.server.URL+"/docs", nil)
	req.Header.Set("Accept", "text/html")
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatalf("status=%d location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// The production authenticator (signed session cookies) works end to end through the real app.
func TestSessionCookie_AuthenticatesAgainstTheRealApp(t *testing.T) {
	t.Parallel()
	e, err := newEnv(context.Background(), nil, func(o *app.Options) { o.Authenticator = nil })
	if err != nil {
		t.Fatal(err)
	}
	defer e.close()

	sessions := auth.NewSessions(sessionSecret, time.Hour, false)
	c := anonymous(t).on(e)

	if r := c.do(http.MethodPost, "/todos/get-list", map[string]any{}); r.Status != http.StatusUnauthorized {
		t.Fatalf("no cookie = %d, want 401", r.Status)
	}

	cookie := func(v string) http.Header { return http.Header{"Cookie": {auth.CookieName + "=" + v}} }
	good := sessions.Encode(testPrincipal)
	c.hdr = cookie(good)
	if r := c.do(http.MethodPost, "/todos/get-list", map[string]any{}); r.Status != http.StatusOK {
		t.Fatalf("valid cookie = %d, want 200; body %s", r.Status, r.Body)
	}

	c.hdr = cookie(good[:len(good)-2] + "xx") // tampered signature
	if r := c.do(http.MethodPost, "/todos/get-list", map[string]any{}); r.Status != http.StatusUnauthorized {
		t.Fatalf("tampered cookie = %d, want 401", r.Status)
	}

	expired := auth.NewSessions(sessionSecret, -time.Minute, false).Encode(testPrincipal)
	c.hdr = cookie(expired)
	if r := c.do(http.MethodPost, "/todos/get-list", map[string]any{}); r.Status != http.StatusUnauthorized {
		t.Fatalf("expired cookie = %d, want 401", r.Status)
	}

	other := auth.NewSessions("a-completely-different-secret-0123456789", time.Hour, false).Encode(testPrincipal)
	c.hdr = cookie(other)
	if r := c.do(http.MethodPost, "/todos/get-list", map[string]any{}); r.Status != http.StatusUnauthorized {
		t.Fatalf("cookie signed with another key = %d, want 401", r.Status)
	}
}

func TestDocs_AreOffByDefaultAndStillAuthenticatedWhenEnabled(t *testing.T) {
	t.Parallel()
	if r := authed(t).do(http.MethodGet, "/docs", nil); r.Status != http.StatusNotFound {
		t.Fatalf("/docs with docs disabled = %d, want 404", r.Status)
	}

	e, err := newEnv(context.Background(), func(c *config.Config) { c.DocsEnabled = true })
	if err != nil {
		t.Fatal(err)
	}
	defer e.close()
	if r := authed(t).on(e).do(http.MethodGet, "/openapi/todos.openapi.yaml", nil); r.Status != http.StatusOK || !strings.Contains(string(r.Body), "operationId: createTodo") {
		t.Fatalf("contract = %d", r.Status)
	}
	if r := anonymous(t).on(e).do(http.MethodGet, "/docs", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("anonymous /docs = %d, want 401", r.Status)
	}
	if r := authed(t).on(e).do(http.MethodGet, "/graphql/playground", nil); r.Status != http.StatusOK {
		t.Fatalf("playground = %d, want 200", r.Status)
	}
}
