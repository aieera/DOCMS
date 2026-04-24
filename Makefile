# VaultDMS — root Makefile
# All targets are idempotent and safe to re-run.

SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

# ---- Config ----------------------------------------------------------------

GO              ?= go
GOLANGCI_LINT   ?= golangci-lint
BUF             ?= buf
MIGRATE         ?= migrate
DOCKER          ?= docker
COMPOSE         ?= docker compose

REGISTRY        ?= ghcr.io/vaultdms
VERSION         ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT          ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
LDFLAGS         := -w -s -X main.version=$(VERSION) -X main.commit=$(COMMIT)

DATABASE_URL    ?= postgres://vaultdms:devpassword@localhost:5432/vaultdms?sslmode=disable

SERVICES := document storage search auth policy workflow notification audit signature billing connector

# ---- Help ------------------------------------------------------------------

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage: make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

# ---- Build / Test / Lint ---------------------------------------------------

.PHONY: build
build: ## Build all service binaries into ./bin
	@mkdir -p bin
	@for svc in $(SERVICES); do \
		if [ -d "services/$$svc" ]; then \
			echo ">> building $$svc"; \
			(cd services/$$svc && $(GO) build -ldflags="$(LDFLAGS)" -o ../../bin/$$svc ./cmd/server) || exit 1; \
		fi; \
	done

.PHONY: test
test: ## Run all tests with race detector
	$(GO) test -race -timeout 5m ./...

.PHONY: test-audit test-billing test-connector test-notification test-signature test-storage test-workflow test-services
test-audit:        ; $(GO) test -race -cover ./services/audit/... ## Run audit tests
test-billing:      ; $(GO) test -race -cover ./services/billing/... ## Run billing tests
test-connector:    ; $(GO) test -race -cover ./services/connector/... ## Run connector tests
test-notification: ; $(GO) test -race -cover ./services/notification/... ## Run notification tests
test-signature:    ; $(GO) test -race -cover ./services/signature/... ## Run signature tests
test-storage:      ; $(GO) test -race -cover ./services/storage/... ## Run storage tests
test-workflow:     ; $(GO) test -race -cover ./services/workflow/... ## Run workflow tests
test-services: test-audit test-billing test-connector test-notification test-signature test-storage test-workflow ## Run every Go service's unit tests

.PHONY: test-integration test-integration-document test-integration-wave15 test-pades test-pades-strict
test-integration-document: ## Run document service integration tests (Postgres + NATS testcontainers)
	$(GO) test -tags integration -race -timeout 5m ./services/document/internal/service/...

# Wave 15 cross-tenant RLS hard-stops. Each sub-wave's repository
# integration test boots a Postgres testcontainer + runs the
# tenant-isolation assertion. Hard P0 per the Wave 15 brief.
test-integration-wave15: ## Run Wave 15 cross-tenant isolation tests
	$(GO) test -tags integration -race -timeout 10m \
		./services/auth/internal/repository/... \
		./services/policy/internal/repository/... \
		./services/acknowledgement/internal/repository/... \
		./services/signature/internal/repository/...

test-integration: test-integration-document test-integration-wave15 ## Run all service integration tests (build tag: integration)

# Wave 9 / Wave 15.4 — PAdES-B-LT structural validator against a
# fixture corpus. Point VAULTDMS_PADES_FIXTURES at a directory of
# real signed PDFs before running; see
# docs/runbooks/15-pades-harness.md.
test-pades: ## Run the PAdES-B-LT corpus validator against tests/fixtures/pades/*.pdf
	$(GO) test -tags pades_corpus -race -timeout 2m ./services/signature/internal/pades/...

# T-D-7 strict gate — no prod_accept build file may import the
# regex-based pades validator. Cheap + fast; safe to run on every PR.
test-pades-strict: ## Fail if any //go:build prod_accept file imports the (regex-based) pades validator
	@bash scripts/check-pades-prod-accept.sh

.PHONY: test-cover
test-cover: ## Run tests with coverage report
	$(GO) test -race -coverprofile=coverage.txt -covermode=atomic ./...
	$(GO) tool cover -html=coverage.txt -o coverage.html

.PHONY: lint
lint: ## Run golangci-lint across the workspace
	$(GOLANGCI_LINT) run ./...

.PHONY: fmt
fmt: ## Format Go code
	$(GO) fmt ./...
	@command -v goimports >/dev/null && goimports -w -local github.com/vaultdms/vaultdms . || true

.PHONY: tidy
tidy: ## Run go mod tidy in every service module
	@for svc in $(SERVICES); do \
		if [ -d "services/$$svc" ]; then \
			echo ">> tidy $$svc"; \
			(cd services/$$svc && $(GO) mod tidy) || exit 1; \
		fi; \
	done
	@if [ -d "pkg" ] && [ -f "pkg/go.mod" ]; then (cd pkg && $(GO) mod tidy); fi

# ---- Proto -----------------------------------------------------------------

.PHONY: proto-gen
proto-gen: ## Generate Go stubs, gateway, and OpenAPI from .proto files
	cd proto && $(BUF) dep update && $(BUF) generate
	@echo "stubs in proto/gen/go/ — run 'cd proto/gen/go && go mod tidy' if deps changed"

.PHONY: proto-lint
proto-lint: ## Lint .proto files
	cd proto && $(BUF) lint

.PHONY: proto-breaking
proto-breaking: ## Detect breaking proto changes against main
	cd proto && $(BUF) breaking --against '.git#branch=main,subdir=proto'

# ---- SDK codegen from docs/api/openapi.yaml -------------------------------
#
# The hand-maintained spec is the source for client SDKs. Generator
# runs on demand (not every build) because:
#   - Output diffs are noisy under version control.
#   - `docs/api/openapi.yaml` coverage is incomplete (see
#     `scripts/openapi/drift-allowlist.txt`); SDKs regenerated today
#     would miss ~40 grandfathered routes.
#
# Outputs live under `clients/<lang>/` and are committed so consumers
# can depend on a tagged commit without running the generator
# themselves. Empty directories today — `make sdk-gen-<lang>` writes
# into them.

OPENAPI_SPEC ?= docs/api/openapi.yaml

.PHONY: sdk-gen sdk-gen-go sdk-gen-ts sdk-gen-py
sdk-gen: sdk-gen-go sdk-gen-ts sdk-gen-py ## Regenerate all client SDKs from openapi.yaml

sdk-gen-go: ## Go client SDK (oapi-codegen; types + client)
	@command -v oapi-codegen >/dev/null || $(GO) install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
	@mkdir -p clients/go
	oapi-codegen -generate types,client -package vaultdmsclient \
		-o clients/go/vaultdmsclient.go $(OPENAPI_SPEC)

sdk-gen-ts: ## TypeScript client SDK (openapi-typescript-codegen)
	@command -v npx >/dev/null || { echo "npx required (install Node.js)"; exit 2; }
	@mkdir -p clients/ts
	npx -y openapi-typescript-codegen --input $(OPENAPI_SPEC) \
		--output clients/ts --client axios --useOptions

sdk-gen-py: ## Python client SDK (openapi-python-client)
	@command -v openapi-python-client >/dev/null || pip install --user openapi-python-client
	@mkdir -p clients/python
	openapi-python-client generate --path $(OPENAPI_SPEC) \
		--output-path clients/python/vaultdms --overwrite

# ---- Migrations ------------------------------------------------------------

MIGRATIONS_DIR ?= services/$(SERVICE)/migrations

.PHONY: migrate-up
migrate-up: ## Run all up migrations for SERVICE=<name>
	@if [ -z "$(SERVICE)" ]; then echo "SERVICE=<name> required"; exit 1; fi
	# §4.1 / A5: per-service bookkeeping table so versions don't collide
	# across services (see docs/architecture/migrations.md).
	$(MIGRATE) -database "$(DATABASE_URL)&x-migrations-table=$(SERVICE)_schema_migrations" \
	           -path $(MIGRATIONS_DIR) up

.PHONY: migrate-down
migrate-down: ## Roll back last migration for SERVICE=<name>
	@if [ -z "$(SERVICE)" ]; then echo "SERVICE=<name> required"; exit 1; fi
	$(MIGRATE) -database "$(DATABASE_URL)&x-migrations-table=$(SERVICE)_schema_migrations" \
	           -path $(MIGRATIONS_DIR) down 1

.PHONY: migrate-create
migrate-create: ## Create new migration: make migrate-create SERVICE=<svc> NAME=<name>
	@if [ -z "$(SERVICE)" ] || [ -z "$(NAME)" ]; then echo "SERVICE and NAME required"; exit 1; fi
	$(MIGRATE) create -ext sql -dir $(MIGRATIONS_DIR) -seq $(NAME)

.PHONY: migrate-force
migrate-force: ## Force migration version (recovery): SERVICE=<svc> VERSION=<n>
	@if [ -z "$(SERVICE)" ] || [ -z "$(VERSION)" ]; then echo "SERVICE and VERSION required"; exit 1; fi
	$(MIGRATE) -database "$(DATABASE_URL)" -path $(MIGRATIONS_DIR) force $(VERSION)

# ---- Docker ----------------------------------------------------------------

.PHONY: docker-up
docker-up: ## Start local dev infrastructure
	$(COMPOSE) up -d

.PHONY: docker-up-prebuilt
docker-up-prebuilt: ## Start the full stack using pre-built images from ghcr.io (set VAULTDMS_IMAGE_TAG to pin a version)
	$(COMPOSE) -f docker-compose.yml -f docker-compose.prebuilt.yml pull
	$(COMPOSE) -f docker-compose.yml -f docker-compose.prebuilt.yml up -d
	./scripts/wait-for-health.sh

.PHONY: docker-down
docker-down: ## Stop local dev infrastructure
	$(COMPOSE) down

.PHONY: docker-logs
docker-logs: ## Tail compose logs
	$(COMPOSE) logs -f

.PHONY: docker-build
docker-build: ## Build Docker images for all services
	@for svc in $(SERVICES); do \
		if [ -f "services/$$svc/Dockerfile" ]; then \
			echo ">> building image $$svc"; \
			$(DOCKER) build -f services/$$svc/Dockerfile -t $(REGISTRY)/$$svc:$(VERSION) . || exit 1; \
		fi; \
	done

.PHONY: docker-push
docker-push: ## Push Docker images
	@for svc in $(SERVICES); do \
		$(DOCKER) push $(REGISTRY)/$$svc:$(VERSION); \
	done

# ---- Local setup (one-command onboarding) ---------------------------------

.PHONY: setup
setup: gen-env up migrate seed ## Full local setup: env, docker, migrate, seed
	@echo ""
	@echo "Setup complete. Start the web UI with 'make run-web'."
	@echo "Then open http://localhost:5173 and log in with the credentials above."

.PHONY: gen-env
gen-env: ## Generate .env from .env.example with random secrets
	./scripts/gen-dev-env.sh

.PHONY: up
up: ## Start all infra services via docker compose + wait for health
	$(COMPOSE) up -d
	./scripts/wait-for-health.sh

.PHONY: migrate
migrate: ## Run all DB migrations
	$(MIGRATE) -database "$(DATABASE_URL)" -path services/document/migrations up
	@if [ -d services/search/migrations ]; then \
		$(MIGRATE) -database "$(DATABASE_URL)" -path services/search/migrations up || true; \
	fi

.PHONY: seed
seed: ## Insert default organization and admin user (idempotent)
	./scripts/seed.sh

.PHONY: run-all
run-all: ## Run all Go services locally in the background (requires 'up')
	./scripts/run-all-services.sh

.PHONY: stop-all
stop-all: ## Stop all locally-running Go services started by run-all
	@if [ -f .run/services.pids ]; then \
		cat .run/services.pids | cut -d: -f1 | xargs -r kill 2>/dev/null || true; \
		rm -f .run/services.pids; \
		echo "Stopped."; \
	else \
		echo "No services.pids file — nothing to stop."; \
	fi

.PHONY: run-web
run-web: ## Run the web dev server (Vite)
	cd web && npm run dev

.PHONY: reset
reset: ## DANGER: destroy all local data and start fresh
	$(COMPOSE) down -v
	rm -rf .run
	$(MAKE) setup

# ---- Security --------------------------------------------------------------

.PHONY: security-check
security-check: ## Run gosec + govulncheck
	@command -v gosec >/dev/null || $(GO) install github.com/securego/gosec/v2/cmd/gosec@latest
	@command -v govulncheck >/dev/null || $(GO) install golang.org/x/vuln/cmd/govulncheck@latest
	gosec -quiet ./...
	govulncheck ./...

# ---- Load Testing ----------------------------------------------------------

.PHONY: load-test load-doc-crud load-search load-upload load-mixed load-isolation load-chaos load-readiness

load-test: ## Run all standard load tests
	$(MAKE) -C tests/load all

load-doc-crud: ## Load test: document CRUD (1K users, 30 min)
	$(MAKE) -C tests/load doc-crud

load-search: ## Load test: search (200 req/s, 15 min)
	$(MAKE) -C tests/load search

load-upload: ## Load test: upload (100 concurrent, SHA-256 verify)
	$(MAKE) -C tests/load upload

load-mixed: ## Load test: mixed realistic (10K users, 1 hour)
	$(MAKE) -C tests/load mixed

load-isolation: ## Load test: cross-tenant isolation (10 tenants, zero violations)
	$(MAKE) -C tests/load isolation

load-chaos: ## Chaos tests (requires kubectl access)
	$(MAKE) -C tests/load chaos

load-readiness: ## Full production readiness check
	$(MAKE) -C tests/load readiness

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin dist coverage.txt coverage.html
