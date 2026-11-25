# Thin task runner. Every target is also runnable by hand; CI runs the same commands.
.PHONY: generate lint test check run migrate build docker bench

generate: ## Regenerate all generated code from the hand-written contracts
	go generate ./...
	go run ./tools/cmd/gen-config-docs > docs/configuration.md

lint:
	go vet ./...
	golangci-lint run

test: ## Unit + architecture + black-box tests (needs Docker for Testcontainers)
	go test -race ./...

# Everything CI does. Run before handing off.
check: generate
	@git diff --exit-code -- . ':!go.sum' >/dev/null || (echo "Generated files changed: commit them." && git status --short)
	$(MAKE) lint test

run: ## Run against the compose dependencies (docker compose up -d first)
	DATABASE_URL="postgres://golden-user:golden-pass@localhost:5432/golden-path?sslmode=disable" \
	DATABASE_MIGRATE_ON_START=true DOCS_ENABLED=true \
	AUTH_SESSION_SECRET="local-development-session-secret-0123456789" \
	TELEMETRY_COLLECTOR_ENDPOINT=localhost:4317 \
	go run ./cmd/webapi

migrate:
	go run ./cmd/webapi migrate

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/webapi ./cmd/webapi

docker:
	docker build -t golden-path-go:local .

bench: ## Go micro- and in-process HTTP benchmarks
	go test -run '^$$' -bench . -benchmem ./internal/paging ./bench
