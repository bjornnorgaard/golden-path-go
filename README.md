# golden-path-go

A Go reference application — a "golden path" showing how a production-shaped service fits together,
with the same goals as its sibling [golden-path-aspnet](../golden-path-aspnet): REST and GraphQL over
the same feature logic, PostgreSQL, background jobs, OpenTelemetry, and OAuth login in front of all of
it, deployed to the operator's Mimir platform.

The example domain is the same small Todo/User API (create/get/list/update/toggle/delete, plus
reminder, notification and overdue-sweep jobs). It exists to exercise the plumbing, not as a feature.
Because the HTTP and GraphQL contracts are the same as the ASP.NET version's, the same k6 scripts drive
both — see [docs/benchmarks.md](docs/benchmarks.md) for the head-to-head.

## The thesis: contracts are the source of truth, generation is the guardrail

The contract is written first, by hand, and everything mechanical is derived from it. If code and
contract disagree, **the build fails** — with an error that tells you what to do next. That matters
doubly for agents: a human can hold conventions in their head; an agent can follow a compiler error,
a failing `go generate` check, or a test that says exactly what to fix.

> Prefer a mechanism that makes the wrong thing a build error over a convention in `AGENTS.md`.
> Where that is impossible, write the convention down **and** add a check.

| Contract (hand-written) | Generated | Enforced by the compiler / checks |
| --- | --- | --- |
| `api/openapi/*.openapi.yaml` | models, `StrictServerInterface`, router, embedded spec (**oapi-codegen**) | Add an operation → build breaks until it is implemented. Return a status the contract doesn't document → doesn't compile (no such response type). |
| `api/graphql/*.graphql` | executable schema, models, resolver interfaces (**gqlgen**) | Add a field/operation → `ResolverRoot` is no longer satisfied. |
| `internal/db/migrations/*.sql` + `queries/*.sql` | typed query methods (**sqlc**) | A query that doesn't match the schema fails at generate time; changed signatures break callers. |
| `config.Config` struct tags | `docs/configuration.md` | A test fails if the doc is stale; startup reports *all* config problems at once. |
| Tag types (`id.Todo`, `id.User`) | — | `id.TodoID` and `id.UserID` are distinct types: mixing them is a compile error. |

Beyond the compiler, [`internal/archtest`](internal/archtest/arch_test.go) turns architectural rules
into tests (services never import transports, transports never touch the database, only
`internal/config` reads the environment, no `init()`, generated files are marked and never
hand-written), and every REST response in the test suite is validated against the OpenAPI contract.

## Architecture

```text
            api/openapi/*.yaml          api/graphql/*.graphql          internal/db/{migrations,queries}
                    │ oapi-codegen              │ gqlgen                          │ sqlc
                    ▼                           ▼                                  ▼
   internal/transport/rest/{todos,users}api   internal/transport/graphql   internal/db/store
        (generated + thin adapter)            (generated + thin adapter)        (generated)
                    └──────────────┬────────────┘                                  ▲
                                   ▼                                               │
                    internal/todos, internal/users  ── business rules ─────────────┘
                                   │ enqueue (same DB transaction)
                                   ▼
                              internal/jobs  (River workers: reminder, notification, overdue sweep)

   internal/app  = composition root: explicit wiring of all of the above, plus middleware:
   otelhttp → recover → auth (deny by default) → router → [contract validator] → strict handler → adapter
```

- **REST and GraphQL are thin adapters** over one service layer. Rules (title ≥ 3 non-whitespace
  characters, paging bounds, …) live once in `internal/todos|users`; transports map types and translate
  `domain.ValidationError` / `domain.ErrNotFound` into whatever the contract documents.
- **Strong IDs:** `id.ID[T]` is a UUIDv7 tagged with a phantom type; it works directly with JSON, pgx
  and sqlc (via column overrides), so the distinction survives all the way to the database layer.
- **Contract validation at runtime:** every REST request is also validated against the OpenAPI document
  (required fields, types, patterns, bounds) before reaching an operation, producing the documented `400`.
  It costs allocations (see the benchmarks) and can be switched off with `API_VALIDATE_REQUESTS=false`
  because the services enforce all business rules regardless.
- **Jobs** are [River](https://riverqueue.com) (Postgres-backed). They are enqueued *inside* the
  service's database transaction, so a job exists if and only if the change that caused it committed.
- **Auth:** deny by default. A pinned allow-list (`/login`, `/logout`, `/access-denied`,
  `/auth/callback/google`, `/healthz`, `/readyz`) is reachable anonymously; everything else needs a signed
  session cookie. Google sign-in is wired up (OIDC); Facebook/Apple are not ported.
- **Errors:** expected outcomes are documented responses; anything else is a generic `500` problem
  document (with `traceId`) — never an internal message.

## Running locally

`compose.yaml` runs only the dependencies (Postgres, OTel collector, Aspire dashboard); the app runs on the host:

```bash
docker compose up -d
make run          # migrates on start, docs UI at http://localhost:8080/docs, telemetry to the collector
```

Logging in needs the [Google OAuth setup](#one-time-setup-google-oauth-client). For scripts and load tests,
mint a session cookie without a browser:

```bash
export DATABASE_URL="postgres://golden-user:golden-pass@localhost:5432/golden-path?sslmode=disable"
export AUTH_SESSION_SECRET="local-development-session-secret-0123456789"
export AUTH_COOKIE="$(go run ./tools/cmd/mint-session you@example.com)"
curl -s -XPOST localhost:8080/todos/create -H "Cookie: $AUTH_COOKIE" -H 'content-type: application/json' -d '{"title":"hello"}'
```

### Common commands

```bash
make generate   # regenerate everything from the contracts (go generate ./... + configuration docs)
make test       # go test -race ./...   (needs Docker: Testcontainers starts PostgreSQL)
make check      # generate + vet + lint + test — what CI does
make bench      # micro + in-process HTTP benchmarks
make docker     # build the scratch image
```

Generators (`oapi-codegen`, `gqlgen`, `sqlc`, `goose`) are pinned as `tool` directives in `go.mod`
(`go tool sqlc …`); there is nothing to install. See [AGENTS.md](AGENTS.md) for contributor conventions and
[docs/configuration.md](docs/configuration.md) for every environment variable.

### One-time setup: Google OAuth client

1. Create an OAuth 2.0 Client ID (Web application) in the [Google Cloud Console](https://console.cloud.google.com/apis/credentials).
2. Authorized redirect URI: `http://localhost:8080/auth/callback/google` (locally).
3. Provide `AUTH_GOOGLE_CLIENT_ID` / `AUTH_GOOGLE_CLIENT_SECRET` (and `AUTH_PUBLIC_BASE_URL` when not on localhost).
   Never commit them; deployed environments get them as injected secrets.

## Image

The `Dockerfile` builds a fully static binary (`CGO_ENABLED=0`, `-trimpath -s -w`) and ships it in a
`scratch` image with only CA certificates, running as a non-root UID. The result is ~27 MB (see
[docs/benchmarks.md](docs/benchmarks.md) for the comparison). There is no shell in the image; the
`webapi healthcheck` subcommand backs the Docker `HEALTHCHECK`, and `webapi migrate` runs migrations as a
separate step.

## Decisions

These were open questions in the original design sketch.

| Question | Decision | Why |
| --- | --- | --- |
| Where does generated code live? | A transport package per contract (`internal/transport/rest/todosapi`, `…/graphql`), with `*.gen.go` files and the hand-written adapter side by side | Services cannot import transport types at all (enforced), and the adapter must satisfy the generated interface in the same package, which gives the best compiler errors. |
| oapi-codegen vs ogen | **oapi-codegen** (strict server, std-http) | Handles the OpenAPI 3.1 contracts as-is, generates models and the embedded spec used for runtime validation and docs, and its strict mode gives exactly the "undocumented response = compile error" property. |
| GraphQL | **gqlgen**, schema-first, resolvers hand-written against the generated `ResolverRoot` | No `resolver:` section in `gqlgen.yml`, so gqlgen never rewrites hand-written code. |
| Router | stdlib `http.ServeMux` (method + path patterns) | The generated server targets it; no extra dependency. |
| Strong IDs | generic `id.ID[T]` | No generator needed; two lines per new ID type. |
| Config | `caarlos0/env` + a tiny doc generator | Aggregated errors, typed struct, generated reference. |
| Jobs | **River** | Transactional enqueue; no extra infrastructure. |
| Dependency injection | **None — explicit wiring** (see below) | |
| Migrations | `goose` SQL files, embedded, behind a Postgres advisory lock; startup apply is opt-in via `DATABASE_MIGRATE_ON_START` | Mimir's `migrations: startup` mode needs the app to migrate itself; the lock removes the multi-replica race noted in the ASP.NET repo's improvement list. |
| `POST`-everything style | Kept (`POST /todos/create` etc.) | Contract parity with the ASP.NET version is what lets one k6 suite and one set of clients drive both. |

### Why not Uber `fx` (or any DI container)?

[`fx`](https://github.com/uber-go/fx) is a runtime, reflection-based dependency-injection container with
lifecycle hooks, and it earns its keep in large services made of many independently owned modules. It is
a poor fit *here*:

- **It moves wiring errors from compile time to startup.** A missing or ambiguous dependency is found by
  running the app (or an `fx.ValidateApp` test), not by `go build`. The whole point of this repository is
  that wrong things fail the build.
- **The graph is invisible.** With `fx`, "what does this need and who provides it?" is answered by reading
  provider registrations scattered across modules; an agent (or a new teammate) cannot just follow the
  call. The composition root here, [`internal/app/app.go`](internal/app/app.go), is ~60 lines you read top to bottom.
- **There is little to inject.** Ten-odd components, constructed once. Constructors with explicit
  parameters (`todos.NewService(pool, enqueuer, paging, metrics)`) already *are* the DI.
- **Lifecycle is simple enough to be explicit:** start jobs, serve, then shut down in reverse order
  (`App.Run`).

What to do instead as the app grows: keep constructors explicit, and if the composition root becomes
unwieldy (say, hundreds of lines or multiple binaries sharing large subgraphs), reach for
[`google/wire`](https://github.com/google/wire) — it is *compile-time* DI that generates plain Go, which fits
the contract-first/generated philosophy — before reaching for a reflection container. `fx` would be the
right call mainly when joining an existing fx-based codebase.

## Not implemented (yet)

Honest gaps relative to the ASP.NET version and to the original sketch:

- Facebook and Apple sign-in (only Google is ported).
- CORS and forwarded-headers configuration (put a proxy in front, or add middleware in `internal/app`).
- Rate limiting and API versioning (both are open items in the ASP.NET repo's improvement list too).
- A job dashboard (River UI is a separate component) and the typed span-attribute helper generator from
  the sketch: spans carry the standard otelhttp/otelpgx attributes only.
- `golangci-lint` and `govulncheck` were run locally and are clean; the `oasdiff` breaking-change check,
  CodeQL and Dependabot are configured but have not run yet — the first CI run is their real test, as it is
  for the image/pin workflow and the Mimir manifests (the `bootstrap` image tag is a placeholder).
- The `deploy/mimir` manifests assume the operator injects `DATABASE_URL` in a libpq/pgx-compatible
  format (not an Npgsql key/value string); confirm with the platform.
