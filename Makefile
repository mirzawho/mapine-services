GO ?= go
DOCKER ?= docker
KUBECTL ?= kubectl

IMAGE ?= ghcr.io/mirzawho/mapine-services
TAG ?= local
BIN_DIR ?= bin
COMPOSE_FILE ?= docker-compose.yaml

.PHONY: help fmt test race vet build run openapi validate-openapi docker-build docker-run docker-push \
	compose-up compose-down compose-logs k8s-apply migrate backup

help:
	@printf '%s\n' \
		'Targets:' \
		'  fmt          Format Go sources' \
		'  test         Run tests' \
		'  race         Run race-enabled tests' \
		'  vet          Run go vet' \
		'  build        Build API, OpenAPI, migration, and backup binaries into bin/' \
		'  run          Run the API locally' \
		'  openapi      Write the generated OpenAPI JSON to bin/openapi.json' \
		'  validate-openapi Validate OpenAPI and run documentation contract tests' \
		'  docker-build Build IMAGE:TAG' \
		'  docker-run   Run the built image on port 3000' \
		'  docker-push  Push IMAGE:TAG' \
		'  compose-up   Build and start Docker Compose services' \
		'  compose-down Stop Docker Compose services' \
		'  compose-logs Follow Docker Compose logs' \
		'  k8s-apply    Apply k8s.yaml' \
		'  migrate      Run the Mongo export importer (pass ARGS="...")' \
		'  backup       Create a SQLite snapshot (pass ARGS="...")'

fmt:
	gofmt -w cmd internal

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

build:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/mapine ./cmd/api
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/mapine-openapi ./cmd/openapi
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/mapine-migrate ./cmd/migrate
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/mapine-backup ./cmd/backup

run:
	$(GO) run ./cmd/api

openapi:
	mkdir -p $(BIN_DIR)
	$(GO) run ./cmd/openapi > $(BIN_DIR)/openapi.json

validate-openapi: openapi
	$(GO) test ./internal/docs ./cmd/api
	npx --yes @redocly/cli@1.34.2 lint $(BIN_DIR)/openapi.json

docker-build:
	$(DOCKER) build -t $(IMAGE):$(TAG) .

docker-run:
	$(DOCKER) run --rm --name mapine -p 3000:3000 \
		-e SQLITE_PATH=/data/mapine.db \
		-v mapine-data:/data \
		$(IMAGE):$(TAG)

docker-push:
	$(DOCKER) push $(IMAGE):$(TAG)

compose-up:
	$(DOCKER) compose -f $(COMPOSE_FILE) up --build -d

compose-down:
	$(DOCKER) compose -f $(COMPOSE_FILE) down

compose-logs:
	$(DOCKER) compose -f $(COMPOSE_FILE) logs -f

k8s-apply:
	$(KUBECTL) apply -f k8s.yaml

migrate:
	$(GO) run ./cmd/migrate $(ARGS)

backup:
	$(GO) run ./cmd/backup $(ARGS)
