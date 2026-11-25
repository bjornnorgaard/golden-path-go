# k6 load tests

The scripts are byte-for-byte compatible with the ones in golden-path-aspnet because both services expose
the same HTTP and GraphQL contracts, so a run against either is directly comparable
(see [../docs/benchmarks.md](../docs/benchmarks.md)).

- `rest-api.js` — full REST todo lifecycle (create, get-by-id, get-list, update, toggle, delete) plus validation-error requests; ramps 1→16 VUs over 60 s.
- `graphql-api.js` — the same lifecycle through GraphQL.
- `all.js` — both concurrently.
- `rest-saturation.js` — constant VUs (`VUS`, `DURATION`) with no think time, to find a single instance's limit.
- `lib/helpers.js` — shared helpers; writes JSON + Markdown summaries to `results/` (git-ignored).

## Running

Every route is authenticated, so mint a session cookie first — no browser needed:

```bash
export DATABASE_URL="postgres://golden-user:golden-pass@localhost:5432/golden-path?sslmode=disable"
export AUTH_SESSION_SECRET="<the target's AUTH_SESSION_SECRET>"
export AUTH_COOKIE="$(go run ./tools/cmd/mint-session loadtest@example.com)"

k6 run k6/rest-api.js                                   # BASE_URL defaults to http://localhost:8080
BASE_URL=https://example.test k6 run k6/graphql-api.js
VUS=48 DURATION=30s k6 run k6/rest-saturation.js
```

Run from the repo root so results land in `k6/results/`. Compare two runs with `jq`, e.g.
`jq '.metrics.http_req_duration.values' k6/results/rest-api-latest.json`.
