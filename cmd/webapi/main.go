// Command webapi is the golden-path-go HTTP API.
//
//	webapi [serve]   run the API (default)
//	webapi migrate   apply database migrations and exit
//	webapi healthcheck  probe /healthz on HTTP_ADDR (for container HEALTHCHECK in a scratch image)
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bjornnorgaard/golden-path-go/internal/app"
	"github.com/bjornnorgaard/golden-path-go/internal/config"
	"github.com/bjornnorgaard/golden-path-go/internal/db"
	"github.com/bjornnorgaard/golden-path-go/internal/platform/telemetry"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if err := run(ctx, cmd); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cmd string) error {
	if cmd == "healthcheck" {
		return healthcheck(ctx)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := telemetry.NewLogger(os.Stdout, cfg.LogLevel)

	shutdownTelemetry, err := telemetry.Setup(ctx, cfg.TelemetryCollectorEndpoint, cfg.TelemetryServiceName)
	if err != nil {
		return err
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTelemetry(flushCtx)
	}()

	switch cmd {
	case "serve":
		a, err := app.New(ctx, cfg, app.Options{Logger: log})
		if err != nil {
			return err
		}
		defer a.Close()
		return a.Run(ctx)
	case "migrate":
		pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer pool.Close()
		if err := db.Migrate(ctx, pool); err != nil {
			return err
		}
		log.Info("migrations applied")
		return nil
	default:
		return fmt.Errorf("unknown command %q (want serve, migrate or healthcheck)", cmd)
	}
}

func healthcheck(ctx context.Context) error {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// Probes the local listener configured by HTTP_ADDR; the target is operator-supplied, not user input.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil) //nolint:gosec
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // see above
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %d", resp.StatusCode)
	}
	return nil
}
