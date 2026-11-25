// Package config is the only place that reads the process environment.
//
// Configuration is loaded once at startup into a typed Config, validated (all
// problems are reported together), and passed down explicitly. The reference
// table in docs/configuration.md is generated from the struct tags below by
// tools/cmd/gen-config-docs; CI fails if it is stale.
package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config is the complete runtime configuration of the web API.
//
// Tags: env = variable name; envDefault = default; doc = description for the
// generated reference; secret:"true" = never printed or logged.
type Config struct {
	HTTPAddr string `env:"HTTP_ADDR" envDefault:":8080" doc:"Listen address of the HTTP server."`
	LogLevel string `env:"LOG_LEVEL" envDefault:"info" doc:"Log level: debug, info, warn or error."`

	DatabaseURL            string `env:"DATABASE_URL,required" secret:"true" doc:"PostgreSQL connection string (URL or key/value form)."`
	DatabaseMigrateOnStart bool   `env:"DATABASE_MIGRATE_ON_START" envDefault:"false" doc:"Apply pending migrations before serving. Guarded by a Postgres advisory lock, so concurrent instances are safe. Prefer the separate 'migrate' command where the platform allows it."`

	TelemetryCollectorEndpoint string `env:"TELEMETRY_COLLECTOR_ENDPOINT" doc:"OTLP/gRPC collector endpoint (host:port). Empty disables trace and metric export."`
	TelemetryServiceName       string `env:"TELEMETRY_SERVICE_NAME" envDefault:"golden-path-go" doc:"service.name resource attribute."`

	PagingDefaultPageSize int32 `env:"PAGING_DEFAULT_PAGE_SIZE" envDefault:"20" doc:"Page size used when a list request omits limit."`
	PagingMaxPageSize     int32 `env:"PAGING_MAX_PAGE_SIZE" envDefault:"100" doc:"Largest page size ever returned; larger limits are clamped."`

	ValidateRequests bool `env:"API_VALIDATE_REQUESTS" envDefault:"true" doc:"Validate every REST request against the OpenAPI contract before it reaches an operation. Business rules are enforced by the services regardless; disabling this only skips the redundant structural checks (it costs roughly 25µs and 45KB of allocations per request)."`

	DocsEnabled bool `env:"DOCS_ENABLED" envDefault:"false" doc:"Serve the API reference (/docs, /openapi/*) and the GraphQL playground. Still requires authentication."`
	JobsEnabled bool `env:"JOBS_ENABLED" envDefault:"true" doc:"Run the background job workers in this process. Jobs can always be enqueued."`

	AuthSessionSecret string        `env:"AUTH_SESSION_SECRET,required" secret:"true" doc:"Key (min 32 bytes) used to sign session cookies."`
	AuthSessionTTL    time.Duration `env:"AUTH_SESSION_TTL" envDefault:"336h" doc:"Session lifetime."`
	AuthPublicBaseURL string        `env:"AUTH_PUBLIC_BASE_URL" envDefault:"http://localhost:8080" doc:"Externally visible base URL; used to build OAuth redirect URIs and to decide whether cookies are Secure."`

	AuthGoogleClientID     string `env:"AUTH_GOOGLE_CLIENT_ID" doc:"Google OAuth client ID. Google sign-in is enabled when both client values are set."`
	AuthGoogleClientSecret string `env:"AUTH_GOOGLE_CLIENT_SECRET" secret:"true" doc:"Google OAuth client secret."`
}

// Load reads and validates the configuration from the process environment.
func Load() (Config, error) {
	return LoadFrom(nil)
}

// LoadFrom is Load with an explicit environment (nil means the process environment).
func LoadFrom(environment map[string]string) (Config, error) {
	var c Config
	opts := env.Options{Environment: environment}
	if err := env.ParseWithOptions(&c, opts); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	if err := c.validate(); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return c, nil
}

func (c Config) validate() error {
	var errs []error
	if len(c.AuthSessionSecret) < 32 {
		errs = append(errs, errors.New("AUTH_SESSION_SECRET must be at least 32 bytes"))
	}
	if c.PagingDefaultPageSize < 1 || c.PagingMaxPageSize < c.PagingDefaultPageSize {
		errs = append(errs, errors.New("PAGING_DEFAULT_PAGE_SIZE must be >= 1 and <= PAGING_MAX_PAGE_SIZE"))
	}
	return errors.Join(errs...)
}

// GoogleEnabled reports whether Google sign-in is fully configured.
func (c Config) GoogleEnabled() bool {
	return c.AuthGoogleClientID != "" && c.AuthGoogleClientSecret != ""
}
