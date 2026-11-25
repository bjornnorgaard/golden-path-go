# Benchmarks: golden-path-go vs golden-path-aspnet

Measured on 2026-11-25. Both implementations expose the **same HTTP and GraphQL contracts**, so the same
unmodified k6 scripts (`k6/rest-api.js`, `k6/graphql-api.js`, plus `k6/rest-saturation.js`) drove both.

## Setup (and what it does *not* tell you)

- One Apple M2 laptop (Docker Desktop VM, 8 CPUs). Each app ran in its own container with
  `--cpus 2 --memory 512m`, against its own `postgres:18-alpine`, exporting OTLP telemetry to the same
  collector/Aspire dashboard. k6 (`grafana/k6`) ran in a container on the same machine, sequentially, never
  concurrently with another app under test.
- **Go:** `docker build` of this repo's `Dockerfile` (scratch). Real signed-cookie authentication
  (`tools/cmd/mint-session`), background jobs running (River).
- **ASP.NET:** the repo's own `src/WebApi/Dockerfile` (chiseled runtime, JIT, not AOT), with two
  benchmark-only patches applied to a *copy*: `linux-x64` → `linux-arm64` so it runs natively on this
  machine, and an always-authenticated scheme in place of Google login (so the ASP.NET side skips cookie
  validation that the Go side performs; this slightly favours ASP.NET). Hangfire was running.
- Apple Silicon, not production hardware; a single machine hosts the load generator, the apps and the
  databases. Treat ratios as indicative, not absolute. Two rounds were run for the ramp scenarios; the
  numbers below are the range.

## Results

### Footprint

| | golden-path-go | golden-path-aspnet |
| --- | --- | --- |
| Image size (arm64) | **27.2 MB** (`scratch` + static binary + CA certs) | 170 MB (chiseled `aspnet:10.0`, framework-dependent) |
| Time from `docker start` to `/readyz` 200 | **0.17–0.25 s** | 1.3–1.85 s |
| Memory, idle | **≈ 16 MiB** | ≈ 86 MiB |
| Memory, after a load run | **27–40 MiB** | 145–272 MiB |

Image sizes are for linux/arm64 on this machine; linux/amd64 images differ by a few MB but not in ratio.

### k6 ramp scenarios (REST and GraphQL lifecycle, 1→16 VUs over 60 s, 2-CPU container)

| Scenario | | requests/s | avg | median | p95 |
| --- | --- | --- | --- | --- | --- |
| REST | Go | **2 863–3 034** | 1.6 ms | 0.7 ms | **4.1 ms** |
| REST | ASP.NET | 816–850 | 5.6–5.9 ms | 1.3–1.5 ms | 13.6–14.4 ms |
| GraphQL | Go | **2 590–2 827** | 1.7–1.8 ms | 0.8–1.0 ms | **3.5–4.3 ms** |
| GraphQL | ASP.NET | 588–590 | 8.1 ms | 2.5–2.8 ms | 18.1–23.6 ms |

All checks passed and the failure rate was 0 on both sides. The scenario is a closed loop with no think
time, so requests/s here is also an inverse-latency figure. ASP.NET's first seconds include JIT warm-up
(max ≈ 2.3 s in both rounds), which inflates its averages and p95; its medians are the fairer comparison
(≈ 2× slower than Go).

### Saturation (`k6/rest-saturation.js`, constant VUs, 30 s)

| VUs | Go req/s (p95) | ASP.NET req/s (p95) |
| --- | --- | --- |
| 32 | **3 975** (27 ms) | ≈ 10 (31 s) |
| 48 | **3 980** (19 ms) | ≈ 10 (32 s) |
| 64 | **3 037–3 345** (23–41 ms) | 9–15 (27–32 s) |

Go uses ≈ 100–160 % of its 2-CPU allowance at these loads and degrades smoothly. **The ASP.NET build
stalled at ≥ 32 concurrent users** in this setup (it was healthy at 16). While stalled, its CPU was ~0 %,
Postgres showed ≈ 30 connections *idle in transaction* whose last statement was Hangfire's
`INSERT INTO hangfire.job`, and requests hit the 30 s Npgsql timeout. Raising the thread-pool minimum
(`DOTNET_ThreadPool_MinThreads=64`) did not change it, and Postgres' 100-connection limit was not reached
(≈ 65 connections). I did not root-cause or tune it; it may well be a configuration issue (Hangfire storage
and EF sharing pools, `CreateTodo`'s `jobs.Schedule` inside a request) rather than anything inherent to
ASP.NET. Do not read the saturation row as "ASP.NET can only do 10 req/s" — read it as "this exact build,
untuned, falls over between 16 and 32 concurrent users while the Go version does not." The Go
version avoids the problem structurally: jobs are inserted in the *same* transaction and connection as the
write, rather than through a second client on a second connection.

### In-process Go benchmarks (`make bench`, handler called directly, real Postgres)

`go test -bench . -benchmem ./bench`, 8 parallel workers. This isolates the cost of the application itself
and quantifies the runtime contract validator (`API_VALIDATE_REQUESTS`):

| Benchmark | Validation on (default) | Validation off |
| --- | --- | --- |
| REST create todo | 206 µs, 67.5 KB, 898 allocs | 139 µs, 13.4 KB, 151 allocs |
| REST get by id | 87 µs, 80.7 KB, 876 allocs | 49 µs, 12.7 KB, 121 allocs |
| REST list (20) | 115 µs, 85.1 KB, 1 058 allocs | 60 µs, 30.4 KB, 301 allocs |
| REST validation error (400) | 30 µs, 75.7 KB, 989 allocs | **2.8 µs**, 11.2 KB, 82 allocs |
| GraphQL get by id (no validator) | 62–72 µs, 34.7 KB, 448 allocs | same |
| REST lifecycle (6 requests) | 1.02 ms, 574 KB, 6 489 allocs | 1.01 ms, 116 KB, 1 273 allocs |

The generic contract validator (kin-openapi) is the single most expensive thing in the request path: ~25–40 µs
and ~65 KB of allocations per request, 4–6× the allocations of everything else combined. It is on by default
because the contract is the point of this repository, and it is cheap relative to a database round trip; it
can be disabled for hot paths because the services enforce every business rule independently. A generated
validator (instead of a reflective one) is the obvious next optimisation.

### Micro-benchmark parity: `PagingClampBenchmarks`

The ASP.NET repo's only BenchmarkDotNet suite compares three ways to clamp paging values. Ported to Go
(`internal/paging/paging_bench_test.go`, same three formulations and inputs):

| | .NET 10 (BenchmarkDotNet) | Go 1.27 (`testing.B`) |
| --- | --- | --- |
| MathMinMax / ConditionalOperators / IfStatements | 0.152 / 0.152 / 0.159 ns | 1.97 / 1.98 / 1.98 ns |

These numbers are **not comparable**: BenchmarkDotNet itself warns that all three methods are "indistinguishable
from the empty method" (the JIT constant-folded the `readonly` inputs), while Go's figure includes the
`b.Loop` iteration overhead. What the benchmark *does* show, in both languages, is that the three formulations
are equally fast — i.e. the choice is purely about readability. `BenchmarkResolve` takes ≈ 2.4 ns and allocates nothing.

## Reproducing

```bash
# Go image and tools
docker build -t golden-path-go:local .
export DATABASE_URL=… AUTH_SESSION_SECRET=…                      # pointing at the Go app's Postgres
export AUTH_COOKIE="$(go run ./tools/cmd/mint-session bench@example.com)"
docker run --rm --network <net> -v "$PWD":/work -w /work -e BASE_URL=http://<app>:8080 \
  -e AUTH_COOKIE grafana/k6 run k6/rest-api.js
VUS=48 DURATION=30s … k6 run k6/rest-saturation.js
make bench                      # in-process Go benchmarks (BENCH_NO_VALIDATE=1 for the validator-off column)
```

For the ASP.NET side, copy `golden-path-aspnet`, apply the two patches described above, build its
Dockerfile, set `AUTH_NOT_REQUIRED=true` for k6, and seed the fixed bench user row.
