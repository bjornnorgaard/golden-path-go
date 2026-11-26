// Package app is the composition root: the one place where configuration,
// services, transports and background jobs are wired together, explicitly and
// in plain sight. There is no DI container and no registration by reflection;
// if a dependency is missing, this file does not compile.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/grpc"

	"github.com/bjornnorgaard/golden-path-go/internal/auth"
	"github.com/bjornnorgaard/golden-path-go/internal/config"
	"github.com/bjornnorgaard/golden-path-go/internal/db"
	"github.com/bjornnorgaard/golden-path-go/internal/jobs"
	"github.com/bjornnorgaard/golden-path-go/internal/paging"
	"github.com/bjornnorgaard/golden-path-go/internal/platform/httpx"
	"github.com/bjornnorgaard/golden-path-go/internal/platform/telemetry"
	"github.com/bjornnorgaard/golden-path-go/internal/todos"
	"github.com/bjornnorgaard/golden-path-go/internal/transport/graphql"
	"github.com/bjornnorgaard/golden-path-go/internal/transport/grpcapi"
	"github.com/bjornnorgaard/golden-path-go/internal/transport/rest/todosapi"
	"github.com/bjornnorgaard/golden-path-go/internal/transport/rest/usersapi"
	"github.com/bjornnorgaard/golden-path-go/internal/users"
)

// Options are the seams that tests (or alternative deployments) may replace.
type Options struct {
	// Authenticator replaces cookie-session authentication. Nil uses signed session cookies.
	Authenticator auth.Authenticator
	// Logger replaces the default JSON logger.
	Logger *slog.Logger
}

// App is a fully wired application.
type App struct {
	cfg     config.Config
	log     *slog.Logger
	pool    *pgxpool.Pool
	river   *river.Client[pgx.Tx]
	handler http.Handler
	grpc    *grpc.Server
	routes  []string
	Users   *users.Service
}

// New connects to the database (migrating first if configured) and wires everything.
func New(ctx context.Context, cfg config.Config, opts Options) (*App, error) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	poolCfg.ConnConfig.Tracer = otelpgx.NewTracer()
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	if cfg.DatabaseMigrateOnStart {
		if err := db.Migrate(ctx, pool); err != nil {
			pool.Close()
			return nil, err
		}
	}

	metrics, err := telemetry.NewMetrics()
	if err != nil {
		pool.Close()
		return nil, err
	}
	riverClient, err := jobs.NewClient(pool, log, metrics)
	if err != nil {
		pool.Close()
		return nil, err
	}

	pageOpts := paging.Options{DefaultPageSize: cfg.PagingDefaultPageSize, MaxPageSize: cfg.PagingMaxPageSize}
	todoSvc := todos.NewService(pool, riverClient, pageOpts, metrics)
	userSvc := users.NewService(pool, pageOpts)

	sessions := auth.NewSessions(cfg.AuthSessionSecret, cfg.AuthSessionTTL, isHTTPS(cfg.AuthPublicBaseURL))
	var authn auth.Authenticator = sessions
	if opts.Authenticator != nil {
		authn = opts.Authenticator
	}

	a := &App{cfg: cfg, log: log, pool: pool, river: riverClient, Users: userSvc}
	// gRPC: the same services behind the same authenticator. It only listens when GRPC_ADDR is set.
	a.grpc = grpcapi.NewServer(grpcapi.Options{Todos: todoSvc, Users: userSvc, Authn: authn, Log: log, Reflection: cfg.DocsEnabled})
	r := &router{mux: http.NewServeMux()}

	// Probes and login are anonymous (see auth.anonymousPaths).
	r.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.HandleFunc("GET /readyz", a.ready)
	var google *auth.GoogleConfig
	if cfg.GoogleEnabled() {
		google = &auth.GoogleConfig{ClientID: cfg.AuthGoogleClientID, ClientSecret: cfg.AuthGoogleClientSecret, BaseURL: cfg.AuthPublicBaseURL}
	}
	auth.NewLoginHandlers(sessions, provisioner{userSvc}, google, log).Register(func(p string, h http.HandlerFunc) { r.HandleFunc(p, h) })

	// REST: one generated server per contract.
	handlers := httpx.RESTHandlers{Log: log, ValidateRequests: cfg.ValidateRequests}
	todosapi.Mount(r, todosapi.NewServer(todoSvc), handlers)
	usersapi.Mount(r, usersapi.NewServer(userSvc), handlers)

	// GraphQL.
	r.Handle("POST /graphql", graphql.Handler(&graphql.Resolver{Todos: todoSvc, Users: userSvc}, log, cfg.DocsEnabled))
	if cfg.DocsEnabled {
		mountDocs(r)
		r.Handle("GET /graphql/playground", graphql.Playground("/graphql"))
	}

	a.routes = r.patterns
	a.handler = otelhttp.NewHandler(
		httpx.Recover(log, auth.Middleware(authn, r.mux)),
		"http.server",
		otelhttp.WithFilter(func(req *http.Request) bool { return req.URL.Path != "/healthz" && req.URL.Path != "/readyz" }),
	)
	return a, nil
}

// Handler is the root HTTP handler.
func (a *App) Handler() http.Handler { return a.handler }

// Routes lists every registered route pattern (for tests and diagnostics).
func (a *App) Routes() []string { return a.routes }

// GRPC is the gRPC server. It is always constructed (so tests can serve it on any
// listener); Run only listens on it when GRPC_ADDR is set.
func (a *App) GRPC() *grpc.Server { return a.grpc }

// Pool exposes the database pool (for tests and tools).
func (a *App) Pool() *pgxpool.Pool { return a.pool }

// Run serves HTTP (and runs job workers if enabled) until ctx is cancelled, then shuts down gracefully.
func (a *App) Run(ctx context.Context) error {
	var grpcLis net.Listener
	if a.cfg.GRPCAddr != "" {
		var err error
		if grpcLis, err = (&net.ListenConfig{}).Listen(ctx, "tcp", a.cfg.GRPCAddr); err != nil {
			return fmt.Errorf("listen for gRPC: %w", err)
		}
	}
	if a.cfg.JobsEnabled {
		if err := a.StartJobs(ctx); err != nil {
			if grpcLis != nil {
				_ = grpcLis.Close()
			}
			return err
		}
	}
	srv := &http.Server{Addr: a.cfg.HTTPAddr, Handler: a.handler, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 2)
	go func() { errc <- srv.ListenAndServe() }()
	if grpcLis != nil {
		go func() { errc <- a.grpc.Serve(grpcLis) }()
	}
	a.log.Info("listening", "addr", a.cfg.HTTPAddr, "grpc", a.cfg.GRPCAddr, "jobs", a.cfg.JobsEnabled)

	// Either a server failing or cancellation ends the run; in both cases everything is
	// shut down, so a failed listener never leaves the other transport or the workers running.
	var runErr error
	select {
	case runErr = <-errc:
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Drain HTTP and gRPC concurrently under one deadline.
	httpDone := make(chan error, 1)
	go func() { httpDone <- srv.Shutdown(shutdownCtx) }()
	grpcDone := make(chan struct{})
	go func() { a.grpc.GracefulStop(); close(grpcDone) }()
	select {
	case <-grpcDone:
	case <-shutdownCtx.Done():
		a.grpc.Stop()
	}
	errs := []error{ignoreClosed(runErr), <-httpDone}
	if a.cfg.JobsEnabled {
		errs = append(errs, a.StopJobs(shutdownCtx))
	}
	return errors.Join(errs...)
}

// ignoreClosed drops the benign "server closed" result of a server that was shut down on purpose.
func ignoreClosed(err error) error {
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return err
}

// StartJobs starts the background job workers in this process.
func (a *App) StartJobs(ctx context.Context) error {
	if err := a.river.Start(ctx); err != nil {
		return fmt.Errorf("start job workers: %w", err)
	}
	return nil
}

// StopJobs stops the workers, waiting for running jobs to finish or ctx to end.
func (a *App) StopJobs(ctx context.Context) error { return a.river.Stop(ctx) }

// Close releases resources (use when not calling Run).
func (a *App) Close() { a.pool.Close() }

func (a *App) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.pool.Ping(ctx); err != nil {
		httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "Not ready", "The database is unreachable.")
		return
	}
	_, _ = w.Write([]byte("ok"))
}

// router records every pattern registered on it, so the full route table can be
// inspected (the generated servers register themselves through HandleFunc).
type router struct {
	mux      *http.ServeMux
	patterns []string
}

func (r *router) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	r.patterns = append(r.patterns, pattern)
	r.mux.HandleFunc(pattern, h)
}

func (r *router) Handle(pattern string, h http.Handler) {
	r.patterns = append(r.patterns, pattern)
	r.mux.Handle(pattern, h)
}

// ServeHTTP satisfies the generated ServeMux interface.
func (r *router) ServeHTTP(w http.ResponseWriter, req *http.Request) { r.mux.ServeHTTP(w, req) }

type provisioner struct{ users *users.Service }

func (p provisioner) ProvisionUser(ctx context.Context, email string, displayName, givenName, familyName, avatarURL *string) (auth.Principal, error) {
	u, err := p.users.Provision(ctx, email, displayName, givenName, familyName, avatarURL)
	if err != nil {
		return auth.Principal{}, err
	}
	return auth.Principal{UserID: u.ID, Email: u.Email}, nil
}

func isHTTPS(base string) bool { return len(base) >= 8 && base[:8] == "https://" }
