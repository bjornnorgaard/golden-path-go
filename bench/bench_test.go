// Package bench holds in-process HTTP benchmarks of the real application against a real
// PostgreSQL (Testcontainers). They call the root http.Handler directly (no sockets), so they
// measure the cost of the application itself: routing, contract validation, handlers, SQL.
//
//	go test -run '^$' -bench . -benchmem ./bench
//
// For black-box load comparisons across implementations use the k6 scripts in ../k6.
package bench

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/bjornnorgaard/golden-path-go/internal/app"
	"github.com/bjornnorgaard/golden-path-go/internal/auth"
	"github.com/bjornnorgaard/golden-path-go/internal/config"
	"github.com/bjornnorgaard/golden-path-go/internal/id"
)

var (
	handler http.Handler
	owner   = auth.Principal{UserID: id.From[id.User](uuid.MustParse("22222222-2222-2222-2222-222222222222")), Email: "bench@example.com"}
)

type fixedAuth struct{}

func (fixedAuth) Authenticate(*http.Request) (auth.Principal, bool) { return owner, true }

func TestMain(m *testing.M) {
	flag.Parse()
	if f := flag.Lookup("test.bench"); f == nil || f.Value.String() == "" {
		os.Exit(m.Run()) // plain `go test ./...`: no benchmarks requested, so do not start a database
	}
	code, err := run(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench setup failed:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	ctx := context.Background()
	pg, err := postgres.Run(ctx, "postgres:18-alpine",
		postgres.WithDatabase("golden-path"), postgres.WithUsername("golden-user"), postgres.WithPassword("golden-pass"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		return 0, err
	}
	defer func() { _ = testcontainers.TerminateContainer(pg) }()
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return 0, err
	}
	env := map[string]string{
		"DATABASE_URL": dsn, "DATABASE_MIGRATE_ON_START": "true", "JOBS_ENABLED": "false",
		"AUTH_SESSION_SECRET": strings.Repeat("b", 32),
	}
	if os.Getenv("BENCH_NO_VALIDATE") != "" { // compare with and without runtime contract validation
		env["API_VALIDATE_REQUESTS"] = "false"
	}
	cfg, err := config.LoadFrom(env)
	if err != nil {
		return 0, err
	}
	a, err := app.New(ctx, cfg, app.Options{Authenticator: fixedAuth{}, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		return 0, err
	}
	defer a.Close()
	if _, err := a.Pool().Exec(ctx, `INSERT INTO users (id, email) VALUES ($1, $2) ON CONFLICT DO NOTHING`, owner.UserID, owner.Email); err != nil {
		return 0, err
	}
	handler = a.Handler()
	return m.Run(), nil
}

func call(tb testing.TB, path, body string, want int) []byte {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != want {
		tb.Fatalf("POST %s = %d, want %d: %s", path, rec.Code, want, rec.Body.String())
	}
	return rec.Body.Bytes()
}

func createOne(tb testing.TB) string {
	out := call(tb, "/todos/create", `{"title":"benchmark todo","dueBy":"2030-01-01T00:00:00Z"}`, http.StatusOK)
	_, rest, _ := strings.Cut(string(out), `"id":"`)
	idStr, _, _ := strings.Cut(rest, `"`)
	return idStr
}

func BenchmarkRESTCreateTodo(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			call(b, "/todos/create", `{"title":"benchmark todo"}`, http.StatusOK)
		}
	})
}

func BenchmarkRESTGetTodoById(b *testing.B) {
	todoID := createOne(b)
	body := fmt.Sprintf(`{"id":%q}`, todoID)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			call(b, "/todos/get-by-id", body, http.StatusOK)
		}
	})
}

func BenchmarkRESTGetTodoList20(b *testing.B) {
	for range 50 {
		createOne(b)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			call(b, "/todos/get-list", `{"limit":20,"offset":0}`, http.StatusOK)
		}
	})
}

func BenchmarkRESTValidationError(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			call(b, "/todos/create", `{"title":"ab"}`, http.StatusBadRequest)
		}
	})
}

func BenchmarkGraphQLGetTodoById(b *testing.B) {
	todoID := createOne(b)
	body := fmt.Sprintf(`{"query":"query { getTodoById(input: { id: \"%s\" }) { id title isComplete } }"}`, todoID)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			call(b, "/graphql", body, http.StatusOK)
		}
	})
}

// BenchmarkRESTLifecycle mirrors one k6 iteration: create, get, list, update, toggle, delete.
func BenchmarkRESTLifecycle(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			todoID := createOne(b)
			call(b, "/todos/get-by-id", fmt.Sprintf(`{"id":%q}`, todoID), http.StatusOK)
			call(b, "/todos/get-list", `{"limit":20,"offset":0}`, http.StatusOK)
			call(b, "/todos/update", fmt.Sprintf(`{"id":%q,"title":"updated title","isComplete":false}`, todoID), http.StatusOK)
			call(b, "/todos/toggle", fmt.Sprintf(`{"id":%q}`, todoID), http.StatusOK)
			call(b, "/todos/delete", fmt.Sprintf(`{"id":%q}`, todoID), http.StatusOK)
		}
	})
}
