# golden-path-go

A Go reference application — a "golden path" showing how a production-shaped service fits
together, with the same goals as its sibling [golden-path-aspnet](../golden-path-aspnet): REST and
GraphQL over the same feature handlers, PostgreSQL, background jobs, OpenTelemetry, and OAuth
login in front of all of it, deployed to the operator's Mimir platform.

> **Status: ideas only.** This README is the design sketch. Nothing below is implemented yet; it
> exists to agree on direction before writing code. Everything here is a proposal and open to
> change (see [Open questions](#open-questions)).

## The thesis: contracts are the source of truth, generation is the guardrail

The ASP.NET version's central idea is that **the contract is written first, by hand, and
everything mechanical is derived from it**: `*.openapi.yaml` and `*.graphql` files are the HTTP
contract; Roslyn source generators emit DTOs, endpoint interfaces and registration; the developer
only writes the interface implementation (the business logic). If the contract and the code
disagree, the build fails — with a diagnostic that tells you what to do next.

That matters doubly for agents. A human can hold the conventions in their head; an agent can't, but
an agent *can* follow a compiler error, a failing `go generate` check, or a linter message that
says exactly what to fix. So the design rule for this repo is:

> **Prefer a mechanism that makes the wrong thing a build error over a convention written in
> AGENTS.md.** Where we can't, write the convention down *and* add a check.

The Go version should reach the same outcome, but **by Go's means, not by transliterating Roslyn**.
Go has no source generators in the compiler, no attribute-driven registration, no DI container,
and a strong culture of "explicit over magic". So the mapping is not 1:1, and that's fine:

| ASP.NET golden path | Go equivalent (proposed) |
| --- | --- |
| `*.openapi.yaml` → generated DTOs + endpoint interfaces | `oapi-codegen` in **strict-server** mode → generated `StrictServerInterface` + typed request/response objects |
| `*.graphql` → generated GraphQL transport | `gqlgen` (schema-first) → generated resolver interfaces + models |
| `[Endpoint]` / `[Service]` source-generated registration | **Explicit wiring in `main`/`app.New`** — compiler-checked, greppable. No container. |
| Missing implementation = diagnostic `GP2001` | Generated interface is not satisfied → compile error (free) |
| `GP2003` "handlers must not throw, return documented errors" | Strict-server response types only permit *documented* statuses; plus a custom linter for the rest |
| EF Core + migrations | SQL-first: `sqlc` for typed queries, `goose` (or `atlas`) for migrations |
| `[StrongId]` generator | Defined types (`type TodoID uuid.UUID`) + a tiny generator or generic `ID[T]` |
| `AppSettingsConfigurationGenerator` | One typed `Config` struct, loaded from env, with the env-var table generated into docs |
| Activity tag extension generator | Small generated typed `attribute.KeyValue` helpers from a tag list |
| TUnit + Testcontainers, black-box over HTTP | `testing` + `testcontainers-go`, `httptest`, black-box over the real router |
| `dotnet build` fails if generated code is stale | `go generate ./...` + CI `git diff --exit-code` |

## Principles

1. **Contract first, always.** Add or change an operation by editing the spec; never by writing a
   handler and hoping the docs catch up. The spec is hand-written, reviewed, and small.
2. **Generated code is committed, never edited.** Go builds from the module as checked in (no
   build-time codegen step like Roslyn), so generated files live in the repo, are marked
   `// Code generated ... DO NOT EDIT.`, and CI fails if regenerating changes anything. One
   command regenerates everything: `go generate ./...` (or `make generate`).
3. **Let the compiler be the guide.** Strict generated interfaces mean "what do I implement next?"
   is answered by `go build`. Agents get a precise to-do list from the error output.
4. **Explicit beats clever.** No reflection-based registration, no init-time side effects, no global
   DI container. Dependencies are constructor arguments, wired in one place you can read top to
   bottom. This is the single biggest deliberate departure from the ASP.NET version.
5. **Standard library first.** `net/http` (with 1.22+ method/path routing), `log/slog`,
   `database/sql`-compatible `pgx`, `context` everywhere. Add a dependency only when it earns its
   place, and prefer ones that generate code over ones that use reflection at runtime.
6. **Black-box tests are the main test unit.** Run the real app in-process against real Postgres.
7. **Every convention has a check.** If `AGENTS.md` says "do X", there is a test, linter rule, or
   CI step that fails when X isn't done.

## Proposed layout

Idiomatic Go leans flat and package-oriented, with `internal/` to keep the module's surface small.
The ASP.NET "vertical slice per feature" translates to **one package per feature area**, not one
per operation.

```text
golden-path-go/
├── api/                          # THE CONTRACTS — hand-written, the source of truth
│   ├── openapi/
│   │   ├── todos.openapi.yaml
│   │   └── users.openapi.yaml
│   └── graphql/
│       ├── todos.graphql
│       └── users.graphql
├── cmd/
│   └── webapi/main.go            # thin: load config, build app, run, graceful shutdown
├── internal/
│   ├── app/                      # composition root: explicit wiring of everything
│   ├── config/                   # typed Config, env loading, validation at startup
│   ├── todos/                    # feature package: handlers + domain logic
│   │   ├── api.gen.go            #   generated: strict server interface + types (oapi-codegen)
│   │   ├── graphql.gen.go        #   generated: gqlgen resolvers glue
│   │   ├── server.go             #   hand-written: implements the generated interface
│   │   ├── service.go            #   hand-written: business rules shared by REST + GraphQL
│   │   └── server_test.go        #   black-box tests via the real router
│   ├── users/
│   ├── db/
│   │   ├── migrations/           #   goose SQL migrations (hand-written)
│   │   ├── queries/              #   sqlc input: *.sql (hand-written)
│   │   └── sqlc/                 #   sqlc output (generated)
│   ├── jobs/                     # background jobs (see below)
│   ├── auth/                     # OAuth login, sessions, authz middleware
│   └── platform/                 # telemetry, health, error handling, OpenAPI/GraphQL docs UI
├── tools/                        # custom analyzers / codegen helpers (own package, own tests)
├── deploy/mimir/                 # services.test.yaml / services.prod.yaml
├── k6/                           # load tests, same shape as the ASP.NET repo
├── compose.yaml                  # dependencies only (Postgres, OTel collector, dashboard)
├── Makefile                      # thin task runner: generate, lint, test, run
├── AGENTS.md
└── go.mod                        # `tool` directives pin generators (see below)
```

Whether feature code splits the generated glue into its own sub-package (e.g.
`internal/todos/todosapi`) so hand-written code never shares a package with generated code is an
[open question](#open-questions).

## Contract-first, concretely

### REST: OpenAPI → strict server

Use [`oapi-codegen`](https://github.com/oapi-codegen/oapi-codegen) with **`strict-server`** and the
`std-http` (Go 1.22 `ServeMux`) target. That generates:

- request/response structs from `components/schemas`,
- a `StrictServerInterface` with **one method per `operationId`**, and
- a **response type per documented status code** per operation.

The developer implements the interface:

```go
func (s *Server) CreateTodo(ctx context.Context, req api.CreateTodoRequestObject) (api.CreateTodoResponseObject, error) {
    todo, err := s.svc.Create(ctx, req.Body.Title, req.Body.DueBy)
    if errors.As(err, &validation.Errors{}) {
        return api.CreateTodo400JSONResponse(validation.ToProblem(err)), nil // documented in the spec
    }
    if err != nil {
        return nil, err // undocumented → 500 via the platform error handler
    }
    return api.CreateTodo200JSONResponse{Id: todo.ID, Title: todo.Title /* ... */}, nil
}
```

What this buys us, for free and enforced by `go build`:

- Add an operation to the spec → build breaks until it's implemented. (≈ `GP2001`.)
- Return a status that the spec doesn't document → doesn't compile, because there's no such
  response type. (≈ the *intent* of `GP2003`, enforced more strongly.)
- A compile-time assertion `var _ api.StrictServerInterface = (*Server)(nil)` makes the error
  message point at the right place.

Spec hygiene we'd enforce in CI: lint with `vacuum` or `spectral` (every operation has an
`operationId`, `summary`, error responses use a shared problem schema), and a **breaking-change
check** against `main` with `oasdiff` so contract changes are deliberate.

### GraphQL: schema-first with gqlgen

[`gqlgen`](https://gqlgen.com) is schema-first and generates resolver interfaces, so the same
contract-first story applies. The ASP.NET repo keeps the GraphQL contract in lockstep with the
OpenAPI one ("the GraphQL contract mirrors the operations in …"); we want that *checked*, not just
stated.

The key idea carried over: **REST and GraphQL resolvers are thin adapters over one service
layer** (`internal/todos/service.go`). Transport code maps between generated transport types and
domain types; rules live once. Business logic never imports generated transport types.

Idea to explore: a small test (or generator in `tools/`) that asserts every OpenAPI `operationId`
has a GraphQL counterpart with the same name and input fields — turning the "mirrors" comment into a
failing test when the two drift.

### Database: SQL-first

Go's idiom is "write the SQL". [`sqlc`](https://sqlc.dev) turns `queries/*.sql` plus the migration
schema into typed Go, and checks the queries against the schema at generate time. That is the
direct analogue of "contract-first" for the data layer: the migration + query files are the
contract, the Go methods are derived.

- Migrations: [`goose`](https://github.com/pressly/goose) SQL files (or `atlas` if we want
  declarative schema diffing). Embedded with `go:embed`; applied by a **separate `migrate`
  subcommand/job**, not implicitly on every app start — the ASP.NET repo's
  [improvement notes](../golden-path-aspnet/docs/improvement-recommendations.md) call out
  start-up auto-migration as a multi-instance race, so we can avoid that from day one. (Mimir's
  `migrations: startup` mode needs checking against this.)
- Driver: `pgx/v5` with `pgxpool`; `sqlc` generates for it.

### Strong IDs

`type TodoID uuid.UUID` (or a generic `ID[T any]`) prevents passing a `UserID` where a `TodoID` is
expected, with `UnmarshalText`/`MarshalText` for JSON and `sql.Scanner`/`driver.Valuer` (or pgx
type registration) for the DB. Generic `ID[Todo]` needs no generator at all; if we want the
ASP.NET ergonomics of a one-line declaration, that's the right trade. Worth a small spike.

### Configuration

A single `config.Config` struct, loaded from environment variables once at startup, validated
(fail fast with *all* missing values listed, not just the first), and passed down explicitly. A
`go generate` step can emit the env-var reference table (name, type, default, secret?) into
`docs/configuration.md` and into the Mimir manifest comments, so docs and code can't drift. Candidates:
`caarlos0/env` or `kelseyhightower/envconfig`, or a ~100-line stdlib loader if we want zero deps.

### Telemetry

`log/slog` (JSON) + OpenTelemetry via `otelhttp`/`otelpgx`, exporting OTLP to the same collector
used in the ASP.NET repo, so the same Aspire dashboard and `aspire otel …` commands work for agents
debugging locally. Typed span-attribute helpers (`telemetry.TodoID(id)`) can be generated from a
single list of tag keys.

## Generation workflow

One pinned, reproducible toolchain — no "install these five CLIs first" step. Go 1.24+ supports
**`tool` directives in `go.mod`**, so generators are versioned with the module:

```text
// go.mod
tool (
    github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen
    github.com/99designs/gqlgen
    github.com/sqlc-dev/sqlc/cmd/sqlc
    github.com/pressly/goose/v3/cmd/goose
)
```

Each feature owns its generation via `//go:generate` directives next to the contract it
consumes:

```go
//go:generate go tool oapi-codegen -config oapi.cfg.yaml ../../api/openapi/todos.openapi.yaml
```

- `go generate ./...` regenerates everything, deterministically.
- **CI regenerates and runs `git diff --exit-code`.** Stale or hand-edited generated code fails
  the build with a message saying to run `make generate`.
- Generated files carry the standard `// Code generated … DO NOT EDIT.` header, which Go tooling
  (gopls, golangci-lint, GitHub's diff UI) already understands and de-emphasises.
- Only the generators' *inputs* are hand-edited: `api/**`, `internal/db/{migrations,queries}`.

## Enforcing conventions beyond the compiler

Where ASP.NET uses analyzer diagnostics, Go has `golangci-lint` plus the `go/analysis` framework
for project-specific rules in `tools/`. Candidate rules, each with a stable ID and an
error message that tells the reader (human or agent) what to do:

- Feature/service code must not import generated transport packages (keeps the layering honest).
- No `init()` registration and no package-level mutable state outside `main`.
- Every handler that touches the DB takes a `context.Context` and passes it on.
- No `panic` in request paths; no direct `os.Getenv` outside `internal/config`.
- Hand-written files must not contain `// Code generated` (and vice versa) — guards against
  agents "fixing" generated code by hand.
- Spec rules (via `spectral`/`vacuum`): every operation documents `400`/`401`/`500` the same way.

A convention that can't be turned into a check goes in `AGENTS.md` and gets revisited.

## Testing

- **Black-box first.** A `testapp` helper builds the real `app` (same wiring as `main`) in-process
  behind `httptest.NewServer`, backed by a real Postgres from `testcontainers-go`, with auth
  stubbed at one seam. Tests call the public HTTP and GraphQL interface using the *generated
  client* (`oapi-codegen` can emit one) — so tests are also checked against the contract.
- Standard `testing` with table-driven tests and `t.Parallel()`; a shared container per package via
  `TestMain`, with per-test isolation by schema or transaction rollback (to be decided).
- **Contract conformance test:** hit every documented operation and validate responses against the
  spec (`kin-openapi` or `libopenapi` validators) so spec and behaviour can't silently diverge.
- Benchmarks with the standard `testing.B`; `k6/` scripts ported as-is (they only speak HTTP).
- Generator/analyzer tests live next to `tools/` using `analysistest` and golden files.

## Background jobs

Hangfire has no direct Go counterpart. Options, in rough order of preference:

1. **[`river`](https://riverqueue.com)** — Postgres-backed, transactional enqueue (job is created
   in the same transaction as the todo), typed args, retries, scheduling, UI. Closest in spirit to
   Hangfire and needs no new infrastructure.
2. `pgxpool` + a small in-repo scheduler for the two example jobs (reminder, sweep) — least
   dependency, most code to own.

Either way, jobs depend on the same service layer as the transports.

## Auth

Same shape as ASP.NET: every route authenticated by default, an explicit allow-list of anonymous
routes (`/login`, `/logout`, `/access-denied`, health probes), session cookie, pluggable OAuth
providers enabled by configuration. In Go: `golang.org/x/oauth2` (+ `coreos/go-oidc` for
Google/Apple), a middleware that denies by default, and a test (like `RouteAuthorizationTests`)
that walks the router and fails if a new route is reachable anonymously without being listed.

## Developer & agent experience

- **`AGENTS.md`** from day one: layout, the generate-then-implement loop, "never edit `*.gen.go`",
  how to read telemetry locally, how to report what was run.
- **A single task runner** (`make` or `go tool` + a `justfile`): `generate`, `lint`, `test`, `run`,
  `check` (everything CI does). Agents run `make check` before handing off.
- **Scaffolding for the common change:** `make new-operation` (or documented steps) that edits the
  spec, runs generation, and leaves a failing build pointing at the stub to implement — the
  contract-first loop with the first step done.
- **Error messages as prompts:** lint and CI failures should state the fix, not just the fault.
- Devcontainer / `compose.yaml` for dependencies only; the app runs on the host with `go run`.
- Telemetry-first debugging via the same OTel collector + Aspire dashboard setup.

## Deployment & CI

- Multi-stage Dockerfile → static binary (`CGO_ENABLED=0`) in a distroless/scratch image;
  non-root, health probes at `/healthz` and `/readyz`.
- `deploy/mimir/services.{test,prod}.yaml` and the image-tagging scripts carried over from the
  ASP.NET repo, since the platform contract is language-agnostic.
- GitHub Actions: `go generate` + diff check, `golangci-lint`, `go vet`, `go test -race`,
  spec lint + `oasdiff` breaking-change check, `govulncheck`, Docker build smoke test. Dependabot
  and CodeQL from the start (both are open items in the ASP.NET repo's improvement list).

## Lessons carried over from the ASP.NET improvement list

Things to do right from the start rather than retrofit: auth on everything including the docs UI
and job dashboard; docs/playground UIs gated by config; migrations separate from app start; rate
limiting; API versioning decided up front; Dependabot/CodeQL/coverage in CI; Docker build in CI.

## Open questions

1. **Generated code location.** Same package as hand-written code (simple, but mixes them) or a
   dedicated `*api` sub-package per feature (cleaner boundary, an extra import)?
2. **oapi-codegen vs. alternatives** (`ogen`, which generates a faster, fully typed server and
   client with no reflection; or Connect/protobuf if we ever drop the OpenAPI requirement). OpenAPI
   is the stated contract here, which favours `oapi-codegen` or `ogen` — worth a short spike on
   both against the Todos spec before committing.
3. **gqlgen's code-heavy output** vs. keeping GraphQL as a thin optional transport. Is GraphQL a
   must-have for the Go version, or only a demonstration of "second transport over the same
   handlers"?
4. **Router:** stdlib `ServeMux` (preferred) vs. `chi` if middleware composition gets awkward.
5. **Strong IDs:** generic `ID[T]` vs. generated named types.
6. **Config library:** dependency vs. small stdlib loader.
7. **Jobs:** `river` vs. in-repo. Depends on how much we want the Hangfire-parity demo.
8. **Mimir:** does `migrations: startup` have a job/run-once alternative that fits a separate
   `migrate` step?
9. **POST-everything style.** The ASP.NET spec models all operations as `POST /todos/<verb>`.
   Keep it for parity, or use idiomatic REST verbs/resources in the Go version?
10. **Module path and Go version pin** (`go.mod`, `toolchain`) — to be fixed when the first
    commit of code lands.

## Proposed next steps

1. Spike: Todos OpenAPI spec → `oapi-codegen` strict server vs. `ogen`, one operation end to end,
   with a Testcontainers black-box test.
2. Decide the layout and generated-code question above; write `AGENTS.md`.
3. Skeleton: `cmd/webapi`, `internal/app`, `internal/config`, health, telemetry, CI with the
   regenerate-and-diff check.
4. Port the Todos feature (REST), then users, then GraphQL, auth, and jobs.
