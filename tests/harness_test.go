// Package tests holds the black-box test suite: the real application, wired exactly
// as in production, running in-process against a real PostgreSQL (Testcontainers),
// exercised only through its public HTTP interface. Every REST response is also
// validated against the OpenAPI contract, so tests double as contract tests.
package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc/test/bufconn"

	"github.com/bjornnorgaard/golden-path-go/api"
	"github.com/bjornnorgaard/golden-path-go/internal/app"
	"github.com/bjornnorgaard/golden-path-go/internal/auth"
	"github.com/bjornnorgaard/golden-path-go/internal/config"
	"github.com/bjornnorgaard/golden-path-go/internal/id"
)

const (
	authHeader    = "X-Test-Authenticated"
	sessionSecret = "test-session-secret-0123456789abcdef-test"
)

// testPrincipal is the user every authenticated test request acts as.
var testPrincipal = auth.Principal{
	UserID: id.From[id.User](uuid.MustParse("11111111-1111-1111-1111-111111111111")),
	Email:  "test-user@example.com",
}

// headerAuth stands in for a real session: a request carrying authHeader is
// authenticated as testPrincipal; anything else is anonymous, so the real
// deny-by-default middleware is still exercised.
type headerAuth struct{}

func (headerAuth) Authenticate(r *http.Request) (auth.Principal, bool) {
	if r.Header.Get(authHeader) == "" {
		return auth.Principal{}, false
	}
	return testPrincipal, true
}

var (
	dsn      string
	suite    *env
	logs     = &syncBuffer{}
	contract = mustLoadContract()
)

type env struct {
	app     *app.App
	server  *httptest.Server
	pool    *pgxpool.Pool
	grpcLis *bufconn.Listener // gRPC is served in memory, so no port is needed
}

func TestMain(m *testing.M) {
	code, err := runSuite(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "test setup failed:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runSuite(m *testing.M) (int, error) {
	ctx := context.Background()
	pg, err := postgres.Run(ctx, "postgres:18-alpine",
		postgres.WithDatabase("golden-path"), postgres.WithUsername("golden-user"), postgres.WithPassword("golden-pass"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		return 0, fmt.Errorf("start postgres: %w", err)
	}
	defer func() { _ = testcontainers.TerminateContainer(pg) }()

	dsn, err = pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return 0, err
	}
	suite, err = newEnv(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer suite.close()
	return m.Run(), nil
}

// newEnv starts an application instance against the shared database. mutate may adjust the config.
func newEnv(ctx context.Context, mutate func(*config.Config), opts ...func(*app.Options)) (*env, error) {
	cfg, err := config.LoadFrom(map[string]string{
		"DATABASE_URL":              dsn,
		"DATABASE_MIGRATE_ON_START": "true",
		"AUTH_SESSION_SECRET":       sessionSecret,
		"JOBS_ENABLED":              "false",
	})
	if err != nil {
		return nil, err
	}
	if mutate != nil {
		mutate(&cfg)
	}
	o := app.Options{Authenticator: headerAuth{}, Logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	for _, f := range opts {
		f(&o)
	}
	a, err := app.New(ctx, cfg, o)
	if err != nil {
		return nil, err
	}
	if _, err := a.Pool().Exec(ctx, `INSERT INTO users (id, email, display_name, given_name, family_name, avatar_url)
		VALUES ($1, $2, 'test-user', 'Test', 'User', 'https://example.com/avatar.png') ON CONFLICT DO NOTHING`,
		testPrincipal.UserID, testPrincipal.Email); err != nil {
		return nil, err
	}
	lis := bufconn.Listen(1 << 20)
	go func() { _ = a.GRPC().Serve(lis) }()
	return &env{app: a, server: httptest.NewServer(a.Handler()), pool: a.Pool(), grpcLis: lis}, nil
}

func (e *env) close() {
	e.app.GRPC().Stop()
	e.server.Close()
	e.app.Close()
}

// ---- HTTP client with contract validation ----

type client struct {
	t   testing.TB
	e   *env
	hdr http.Header
}

// authed returns a client acting as the test user.
func authed(t testing.TB) *client {
	return &client{t: t, e: suite, hdr: http.Header{authHeader: {"1"}}}
}

// anonymous returns a client with no session.
func anonymous(t testing.TB) *client { return &client{t: t, e: suite, hdr: http.Header{}} }

func (c *client) on(e *env) *client { c2 := *c; c2.e = e; return &c2 }

type response struct {
	Status int
	Body   []byte
	Header http.Header
}

func (c *client) do(method, path string, body any) response {
	c.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.e.server.URL+path, rdr)
	if err != nil {
		c.t.Fatal(err)
	}
	for k, v := range c.hdr {
		req.Header[k] = v
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return response{Status: resp.StatusCode, Body: b, Header: resp.Header}
}

// post sends a JSON request, asserts the status, validates the response against the
// OpenAPI contract, and decodes it into Resp (skipped when status is not 2xx and Resp is any).
func post[Resp any](c *client, path string, body any, wantStatus int) Resp {
	c.t.Helper()
	r := c.do(http.MethodPost, path, body)
	if r.Status != wantStatus {
		c.t.Fatalf("POST %s: status = %d, want %d; body: %s", path, r.Status, wantStatus, r.Body)
	}
	validateAgainstContract(c.t, http.MethodPost, path, r)
	var out Resp
	if len(r.Body) > 0 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(r.Body, &out); err != nil {
			c.t.Fatalf("POST %s: decode response: %v; body: %s", path, err, r.Body)
		}
	}
	return out
}

// validationErrors is the documented 400 body.
type validationErrors = map[string][]string

func (c *client) graphql(query string, vars map[string]any) (data json.RawMessage, errs []gqlError) {
	c.t.Helper()
	r := c.do(http.MethodPost, "/graphql", map[string]any{"query": query, "variables": vars})
	if r.Status != http.StatusOK && r.Status != http.StatusUnprocessableEntity { // 422: schema validation failure
		c.t.Fatalf("graphql: status = %d; body: %s", r.Status, r.Body)
	}
	var out struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.Unmarshal(r.Body, &out); err != nil {
		c.t.Fatalf("graphql: decode: %v; body: %s", err, r.Body)
	}
	return out.Data, out.Errors
}

type gqlError struct {
	Message    string         `json:"message"`
	Extensions map[string]any `json:"extensions"`
}

// ---- contract validation ----

func mustLoadContract() []routers.Router {
	var out []routers.Router
	for _, name := range []string{"openapi/todos.openapi.yaml", "openapi/users.openapi.yaml"} {
		data, err := api.Contracts.ReadFile(name)
		if err != nil {
			panic(err)
		}
		doc, err := openapi3.NewLoader().LoadFromData(data)
		if err != nil {
			panic(fmt.Sprintf("%s: %v", name, err))
		}
		r, err := gorillamux.NewRouter(doc)
		if err != nil {
			panic(err)
		}
		out = append(out, r)
	}
	return out
}

func validateAgainstContract(t testing.TB, method, path string, r response) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), method, "http://localhost"+path, nil)
	for _, router := range contract {
		route, params, err := router.FindRoute(req)
		if err != nil {
			continue
		}
		in := &openapi3filter.ResponseValidationInput{
			RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route},
			Status:                 r.Status,
			Header:                 r.Header,
			Options:                &openapi3filter.Options{IncludeResponseStatus: true},
		}
		in.SetBodyBytes(r.Body)
		if err := openapi3filter.ValidateResponse(context.Background(), in); err != nil {
			t.Fatalf("response of %s %s violates the OpenAPI contract: %v\nstatus=%d body=%s", method, path, err, r.Status, r.Body)
		}
		return
	}
}

// ---- helpers ----

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *syncBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buf.String() }

// eventually polls cond until it holds or the timeout elapses.
func eventually(t testing.TB, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}
