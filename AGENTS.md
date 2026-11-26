# AGENTS.md

## Project overview

`golden-path-go` is a Go reference application: a REST + GraphQL + gRPC API over one service layer, PostgreSQL,
background jobs (River), OpenTelemetry, and OAuth login. It is the Go sibling of `golden-path-aspnet` and
follows the same principle: **contracts are written by hand first, everything mechanical is generated from
them, and a violation should be a compile error.**

Read [README.md](README.md) for the reasoning. This file is the operating manual.

## The loop for every change

1. **Edit the contract first** (the hand-written source of truth):
   - HTTP: `api/openapi/*.openapi.yaml`
   - GraphQL: `api/graphql/*.graphql`
   - gRPC: `api/grpc/**/*.proto` (linted and breaking-change checked by `buf`)
   - Database: `internal/db/migrations/*.sql` (schema) and `internal/db/queries/*.sql` (queries)
   - Configuration: the struct tags on `internal/config.Config`
2. **Regenerate:** `make generate` (= `go generate ./...` + the configuration reference).
3. **Follow the compiler.** `go build ./...` now fails exactly where something must be implemented:
   a generated interface gained a method, a response type changed, a query signature moved.
4. **Implement** in the hand-written files (see layout), then write/extend black-box tests in `tests/`.
5. **`make check`** before handing off (generate, vet, lint, all tests).

## Layout

| Path | Hand-written? | Purpose |
| --- | --- | --- |
| `api/` | yes | Contracts: OpenAPI, GraphQL. Embedded into the binary. |
| `internal/db/migrations`, `internal/db/queries` | yes | SQL contract. |
| `internal/db/store` | **generated (sqlc)** | Typed queries. |
| `internal/transport/rest/{todos,users}api/api.gen.go` | **generated (oapi-codegen)** | Models, strict server interface, router, embedded spec. |
| `internal/transport/rest/*/server.go` | yes | Adapter implementing the strict server interface. |
| `internal/transport/graphql/{generated,models}.gen.go` | **generated (gqlgen)** | Executable schema, models, resolver interfaces. |
| `internal/transport/graphql/resolver.go` | yes | Adapter implementing the resolver interfaces. |
| `internal/transport/grpcapi/gen/**.pb.go` | **generated (buf, protoc-gen-go, protoc-gen-go-grpc)** | Messages and service interfaces. |
| `internal/transport/grpcapi/*.go` | yes | Adapter implementing the generated service interfaces, plus the auth/recover/error-translation interceptors. |
| `internal/todos`, `internal/users` | yes | **Business rules.** Transport-agnostic services. |
| `internal/jobs` | yes | River job args and workers. |
| `internal/id` | yes | Strongly typed IDs (`id.TodoID` ≠ `id.UserID`). |
| `internal/auth` | yes | Sessions, login, deny-by-default middleware. |
| `internal/config` | yes | The only reader of the environment. |
| `internal/app` | yes | Composition root: explicit wiring, no DI container. |
| `internal/archtest` | yes | Architecture rules as tests. |
| `tests/` | yes | Black-box test suite. |
| `bench/` | yes | In-process HTTP benchmarks. |
| `tools/` | yes | Small code generators and developer tools. |

## Rules (each one is enforced by a compiler error, a test, or CI)

- **Never edit generated files** (`*.gen.go`, `*.pb.go`, `internal/db/store/*`). Change the contract and regenerate.
  `internal/archtest` fails if a hand-written file carries a generated header or vice versa; CI fails if
  `go generate ./...` produces a diff.
- **Business rules live in `internal/todos|users` only.** Transports map types and translate errors
  (`domain.ValidationError` → documented 400 / gRPC `INVALID_ARGUMENT` with a `BadRequest` detail,
  `domain.ErrNotFound` → documented 404 / gRPC `NOT_FOUND`). They must not
  import `internal/db` or pgx, and services must not import transports or `net/http`
  (`archtest`).
- **Services return `domain.ValidationError` / `domain.ErrNotFound`** for expected outcomes. Anything else
  is an unexpected failure and becomes a 500 (REST), `INTERNAL` (GraphQL) or `INTERNAL` (gRPC) without leaking detail.
  gRPC adapters just return the service's error; the `translateErrors` interceptor maps it.
  For REST, the strict server types make returning an *undocumented* status a compile error: to add a new error
  scenario, add the response to the OpenAPI contract first.
- **Use `id.TodoID` / `id.UserID`**, never raw `uuid.UUID`/`string`, below the transport layer.
- **No `init()`, no package-level mutable state, no `os.Getenv` outside `internal/config`**, no `os.Exit`
  outside `cmd`/`tools`. Wire dependencies explicitly in `internal/app`.
- **Add configuration** by adding a tagged field to `config.Config`, then `make generate` (updates
  `docs/configuration.md`; a test fails if it is stale). Never print or log fields tagged `secret:"true"`.
- **Anonymous routes** are an allow-list in `internal/auth` (`anonymousPaths`), pinned by a test. Everything
  else is authenticated by default. Do not widen it without being asked. gRPC has its own pinned allow-list
  (`anonymousMethods` in `internal/transport/grpcapi`; only the standard health check) and denies streaming
  RPCs (e.g. reflection) by default as well.
- **Jobs** are enqueued inside the service's database transaction (`jobs.Enqueuer.InsertTx`) so a job exists
  iff the change committed.
- Keep contract changes backwards compatible; CI runs `oasdiff breaking` (OpenAPI) and `buf breaking` (gRPC)
  against `main`. Never reuse or renumber a proto field; `reserved` removed ones.

## Common commands

```bash
make generate                       # regenerate everything from the contracts
make test                           # go test -race ./...  (needs Docker: Testcontainers starts Postgres)
go tool buf lint                    # lint the gRPC contract
make check                          # generate + vet + lint + test; run before handing off
docker compose up -d && make run    # run locally against compose dependencies (docs UI at /docs)
make bench                          # micro + in-process HTTP benchmarks
go test ./tests -run TestCreateTodo # a single black-box test
```

Tools (`oapi-codegen`, `gqlgen`, `sqlc`, `goose`, `buf`, `protoc-gen-go`, `protoc-gen-go-grpc`) are pinned as `tool` directives in `go.mod` and run with
`go tool <name>`; do not `go install` or `brew install` them.

## Testing approach

- The **running application is the unit**: `tests/` boots the real app (same wiring as `main`) in-process
  against real PostgreSQL via Testcontainers and calls it only over HTTP. Authentication is replaced at one
  seam (`app.Options.Authenticator`) so the real deny-by-default middleware is still exercised.
- Every REST response in `tests/` is **validated against the OpenAPI contract** (`post[...]` in
  `harness_test.go`); an undocumented status or a body that violates the schema fails the test.
- Cover invalid input and `404`s for every operation. Cover every transport when a rule changes (REST, GraphQL, and gRPC in `tests/grpc_test.go` over an in-memory `bufconn` listener; they share
  the service, so one rule test through each is enough).
- Use focused unit tests only for isolated logic that is simpler to test directly (e.g. config parsing,
  panic recovery). Do not mock many collaborators; use the black-box suite instead.
- Verify the affected package at minimum; run `go test ./...` for changes to shared code, generators, SQL,
  or contracts.

## Runtime diagnostics

- Logs are JSON on stdout (`log/slog`) and include `trace_id` when a span is active. Problem responses
  carry `traceId`.
- Locally, `docker compose up -d` runs the OTel collector and Aspire dashboard (http://localhost:18888);
  `make run` exports to it. From a terminal: `aspire otel logs|traces|spans --dashboard-url
  http://localhost:18888 --non-interactive` (add `--has-error` or `--trace-id <id>`).

## Deployment (Mimir)

Manifests live in `deploy/mimir/services.{test,prod}.yaml`; CI builds the image from the repo-root
`Dockerfile` (scratch, static binary) and pins the tag in `services.test.yaml`. Migrations run at startup
(`DATABASE_MIGRATE_ON_START=true`), guarded by a Postgres advisory lock; `webapi migrate` runs them as a
separate step.

## Change hygiene

- Keep changes scoped to the requested behaviour; no unrelated formatting churn (`gofmt` is the only formatter).
- Do not commit secrets or local connection strings.
- Before handing off, report the commands you ran and any checks that could not run (e.g. Docker unavailable).
