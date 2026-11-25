// Command mint-session creates (or finds) a user and prints a valid session cookie for
// load tests and local scripting, so no browser login is needed:
//
//	AUTH_COOKIE="$(go run ./tools/cmd/mint-session you@example.com)" k6 run k6/rest-api.js
//
// It uses the same configuration as the API (DATABASE_URL, AUTH_SESSION_SECRET,
// AUTH_SESSION_TTL), so it can only mint sessions for deployments whose secret you hold.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bjornnorgaard/golden-path-go/internal/auth"
	"github.com/bjornnorgaard/golden-path-go/internal/config"
	"github.com/bjornnorgaard/golden-path-go/internal/paging"
	"github.com/bjornnorgaard/golden-path-go/internal/users"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	email := "loadtest@example.com"
	if len(os.Args) > 1 {
		email = os.Args[1]
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	u, err := users.NewService(pool, paging.Options{DefaultPageSize: 20, MaxPageSize: 100}).Provision(ctx, email, nil, nil, nil, nil)
	if err != nil {
		return err
	}
	secure := len(cfg.AuthPublicBaseURL) > 8 && cfg.AuthPublicBaseURL[:8] == "https://"
	value := auth.NewSessions(cfg.AuthSessionSecret, cfg.AuthSessionTTL, secure).Encode(auth.Principal{UserID: u.ID, Email: u.Email})
	fmt.Printf("%s=%s\n", auth.CookieName, value)
	return nil
}
