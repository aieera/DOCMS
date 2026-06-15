# ERP↔SeDoc integration product. Run from integration/.
.DEFAULT_GOAL := help

.PHONY: help
help: ## List targets
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build all binaries → ./bin
	go build -o ./bin/worker  ./cmd/worker
	go build -o ./bin/bff     ./cmd/bff
	go build -o ./bin/mockerp ./cmd/mockerp

.PHONY: test
test: ## Unit tests
	go test ./...

.PHONY: test-integration
test-integration: ## Integration tests (testcontainers Postgres + mock SeDoc)
	go test -tags integration ./...

.PHONY: fmt
fmt: ## gofmt
	gofmt -w .

.PHONY: vet
vet: ## go vet (incl. integration-tagged files)
	go vet ./...
	go vet -tags integration ./...

.PHONY: run-worker
run-worker: ## Run the integration worker (needs SEDOC_* + DB env)
	go run ./cmd/worker

.PHONY: run-bff
run-bff: ## Run the Files BFF
	go run ./cmd/bff

.PHONY: run-mockerp
run-mockerp: ## Run the mock ERP (render + authz)
	go run ./cmd/mockerp serve

.PHONY: web-dev
web-dev: ## Web dev server (Vite, :5180, proxies /files → BFF)
	cd web && npm install && npm run dev

.PHONY: web-build
web-build: ## Typecheck + build the web app
	cd web && npm install && npm run build

.PHONY: docker-up
docker-up: ## One-command demo: postgres + worker + bff + mockerp + web
	docker compose up --build

.PHONY: docker-down
docker-down: ## Tear down the demo
	docker compose down -v
