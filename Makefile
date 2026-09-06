# ╭────────────────────----------------──────────╮
# │                     go42                     │
# ╰─────────────────────----------------─────────╯
#
# Before running any commands, ensure you have the following tools are installed:
# - go @see https://go.dev/
# - mise @see https://mise.jdx.dev/
# - docker @see https://www.docker.com/
#
# Also, ensure you have logged in to GitHub Container Registry:
#   docker login ghcr.io -u YOUR_GITHUB_USERNAME --password YOUR_GITHUB_TOKEN
# @see https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry

# Fixes macOS GNU Make 3.81 PATH issue
SHELL := /usr/bin/env sh

export MISE_DEFAULT_CONFIG_FILENAME := etc/mise.toml
export MISE_DATA_DIR := $(CURDIR)/.tools
export MISE_CACHE_DIR := $(MISE_DATA_DIR)/cache
export MISE_STATE_DIR := $(MISE_DATA_DIR)/state
export PATH := $(MISE_DATA_DIR)/shims:$(PATH)

.PHONY: help setup setup-common setup-linters setup-generators setup-mcp \
	test-unit test-fuzz test-integration test-resilience test-load \
	run run-docker debug build image lint generate serve-docs \
	check-env generate-migration-id generate-dep-graph grpcui show-asm

help: Makefile
	@sed -n 's/^##//p' $< | awk 'BEGIN {FS = "|"}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

## setup | install dependencies
setup: setup-common setup-linters setup-generators
	@go mod download
	@mise install --locked \
		npm:@redocly/cli k6 \
		go:github.com/go-delve/delve/cmd/dlv \
		go:github.com/go42-dev/go42x

## setup-common | install shared tools
setup-common:
	@mise install --locked \
		node jq yq python uv

## setup-linters | install code linters
setup-linters:
	@mise install --locked \
		actionlint \
		editorconfig-checker \
		gitleaks \
		golangci-lint \
		golines \
		hadolint \
		markdownlint-cli2 \
		vale \
		zizmor \
		pipx:sqlfluff \
		github:oasdiff/oasdiff \
		go:github.com/daixiang0/gci \
		go:github.com/caarlos0/jsonfmt \
		go:github.com/google/yamlfmt/cmd/yamlfmt \
		go:github.com/securego/gosec/v2/cmd/gosec \
		go:golang.org/x/vuln/cmd/govulncheck
	@vale --config etc/vale.ini sync

## setup-generators | install code generators
setup-generators:
	@mise install --locked \
		helm buf \
		oapi-codegen \
		go:go.uber.org/mock/mockgen \
		go:github.com/ogen-go/ogen/cmd/ogen \
		go:google.golang.org/protobuf/cmd/protoc-gen-go \
		go:google.golang.org/grpc/cmd/protoc-gen-go-grpc

## setup-mcp | setup mcp servers
setup-mcp:
	@mise install --locked \
		go:golang.org/x/tools/gopls
	@docker pull ghcr.io/github/github-mcp-server:v1.12.0@sha256:46cdbbd810faf6f7aed1745ea04057443f5cb9fcadc15c7308add18cf9a83e33

# ╭────────────────────----------------──────────╮
# │               General workflow               │
# ╰─────────────────────----------------─────────╯

## test-unit | run unit tests
# -count=1 is needed to prevent caching of test results.
test-unit:
	@go test -count=1 -v -race $(shell go list ./... | grep -v './tests')

## test-fuzz | run all fuzz targets (30 seconds per target)
test-fuzz:
	@set -eu; \
	tests=$$(go test -json -list '^Fuzz' ./...) || { printf '%s\n' "$$tests"; exit 1; }; \
	targets=$$(printf '%s\n' "$$tests" | jq -r 'select(.Action == "output") | select(.Output | test("^Fuzz[[:alnum:]_]*\n$$")) | [.Package, (.Output | rtrimstr("\n"))] | @tsv'); \
	printf '%s\n' "$$targets" | while read -r package target; do \
		[ -n "$$target" ] || continue; \
		go test -run '^$$' -fuzz "^$${target}$$" -fuzztime 30s -parallel 2 "$$package"; \
	done

## test-integration | run integration tests (http and grpc)
# -count=1 is needed to prevent caching of test results.
# @note Start the application with `make run-integration` in another terminal.
test-integration:
	@go test -count=1 -v -race ./tests/integration/...

## test-resilience | verify dependency recovery after network interruptions
# Resilience tests are build-tagged and excluded from all other test targets.
# @note Requires `toxiproxy pgsql mysql redis memcached nats kafka rabbitmq`
# @note Because of the bug RabbitMQ tests are executed separately without race.
# @see https://github.com/ThreeDotsLabs/watermill/issues/693
test-resilience:
	@go build -race -o ./.build/resilience-app ./cmd/app
	@RESILIENCE_APP_BINARY="$(CURDIR)/.build/resilience-app" go test -count=1 -v -race -tags=resilience ./tests/resilience/...
	@RESILIENCE_APP_BINARY="$(CURDIR)/.build/resilience-app" go test -count=1 -v -tags=resilience ./tests/resilience/... -run '^TestRabbitMQ'

## test-load | run load tests (http and grpc)
test-load:
	@k6 version
	@k6 run tests/load/http/v1/auth_test.js || true
	@k6 run tests/load/grpc/v1/auth_test.js || true

## run | run application
# `-N -l` disables compiler optimizations and inlining, which makes debugging easier.
# `[ $$? -eq 1 ]` treats exit code 1 as success. Exit after signal will always be != 0.
run: check-env
	@export $(shell grep -v '^#' .env.example | xargs) && \
	export $(shell grep -v '^#' .env | xargs) && \
	export DATABASE_MIGRATE_PATH=$(shell pwd)/migrate && \
	export SERVER_HTTP_STATIC_ROOT=$(shell pwd)/static && \
	export SERVER_HTTP_SWAGGER_ROOT=$(shell pwd)/api/openapi && \
	go run -gcflags="all=-N -l" -race ./cmd/app/main.go || [ $$? -eq 1 ]

## run-docker | run application in docker container (linux environment)
# `-N -l` disables compiler optimizations and inlining, which makes debugging easier.
# Using golang image version from go.mod file.
# `[ $$? -eq 1 ]` treats exit code 1 as success. Exit after signal will always be != 0.
run-docker: check-env
	@export $(shell grep -v '^#' .env.example | xargs) && \
	export $(shell grep -v '^#' .env | xargs) && \
	docker run --rm -it --init \
	--env-file .env.example \
	--env-file .env \
	--env DATABASE_MIGRATE_PATH=/app/migrate \
	--env SERVER_HTTP_STATIC_ROOT=/app/static \
	--env SERVER_HTTP_SWAGGER_ROOT=/app/api/openapi \
	-p "$${PPROF_LISTEN#:}:$${PPROF_LISTEN#:}" \
	-p "$${SERVER_HTTP_LISTEN#:}:$${SERVER_HTTP_LISTEN#:}" \
	-p "$${SERVER_GRPC_LISTEN#:}:$${SERVER_GRPC_LISTEN#:}" \
	-v go-cache:/root/.cache/go-build \
	-v go-mod-cache:/go/pkg/mod \
	-v $(shell pwd):/app \
	-w /app \
	golang:$(shell grep '^go ' go.mod | awk '{print $$2}') \
	go run -gcflags="all=-N -l" -race ./cmd/app/main.go || [ $$? -eq 1 ]

## debug | run application with delve debugger
debug: check-env
	@export $(shell grep -v '^#' .env.example | xargs) && \
	export $(shell grep -v '^#' .env | xargs) && \
	export DATABASE_MIGRATE_PATH=$(shell pwd)/migrate && \
	export SERVER_HTTP_STATIC_ROOT=$(shell pwd)/static && \
	export SERVER_HTTP_SWAGGER_ROOT=$(shell pwd)/api/openapi && \
	dlv debug ./cmd/app --headless --listen=:2345 --accept-multiclient --api-version=2

## build | build development version of binary
build:
	@go build -gcflags="all=-N -l" -race -v -o ./.build/app ./cmd/app/main.go
	@file -h ./.build/app && du -h ./.build/app && sha256sum ./.build/app && go tool buildid ./.build/app

## image | build docker image
# @see https://reproducible-builds.org/docs/source-date-epoch/
image:
	@export SOURCE_DATE_EPOCH=0 && \
	docker buildx build --no-cache --platform linux/amd64,linux/arm64 \
	--build-arg "GO_VERSION=$(shell grep '^go ' go.mod | awk '{print $$2}')" \
	--build-arg "COMMIT_HASH=$(shell git rev-parse HEAD 2>/dev/null || echo '')" \
	--build-arg "RELEASE_TAG=$(shell git describe --tags --abbrev=0 2>/dev/null || echo '')" \
	-t ghcr.io/go42-dev/go42:dev \
	.

## lint | run all validation tools
lint:
	@golangci-lint run --config etc/.golangci.yml || true
	@hadolint Dockerfile || true
	@helm lint --strict infra/helm/app --set-string image.tag=ci-validation || true
	@helm lint --strict infra/helm/app --set-string image.digest=sha256:0000000000000000000000000000000000000000000000000000000000000000 || true
	@sqlfluff lint --config etc/sqlfluff.toml --disable-progress-bar migrate/sqlite/*.sql --dialect sqlite || true
	@sqlfluff lint --config etc/sqlfluff.toml --disable-progress-bar migrate/mysql/*.sql --dialect mysql || true
	@sqlfluff lint --config etc/sqlfluff.toml --disable-progress-bar migrate/pgsql/*.sql --dialect postgres || true
	@REDOCLY_SUPPRESS_UPDATE_NOTICE=true REDOCLY_TELEMETRY=false redocly lint --config etc/redocly.yaml --format stylish api/openapi/**/*.yaml || true
	@oasdiff breaking --fail-on ERR origin/master:api/openapi/v1/.combined.yaml api/openapi/v1/.combined.yaml || true
	@buf lint api || true
	@gosec -quiet -exclude-generated ./... || true
	@gitleaks git --config etc/gitleaks.toml --no-banner --redact -v || true
	@markdownlint-cli2 --config etc/.markdownlint.yaml README.md docs/**/*.md || true
	@vale --no-exit --config etc/vale.ini README.md docs/**/*.md internal/ cmd/ pkg/ tests/ || true
	@actionlint -oneline --config-file etc/actionlint.yaml
	@zizmor -q --persona regular --min-severity high --min-confidence high --offline --format plain --color never --no-progress .
	@ec

## generate | generate code for all modules
# Side effects of this command should to be commited.
generate:
	@go mod tidy -e
	@rm -rf api/gen
	@buf generate api --template api/buf.gen.yaml
	@go generate ./...
	@go run cmd/cfg2env/main.go
	@REDOCLY_SUPPRESS_UPDATE_NOTICE=true REDOCLY_TELEMETRY=false redocly join api/openapi/v1/*.yaml -o api/openapi/v1/.combined.yaml
	@yq eval '.info.title = "v1 combined specification"' -i api/openapi/v1/.combined.yaml

## docs | serve documentation
serve-docs:
	@npm --prefix docs/pages install
	@npm --prefix docs/pages run build
	@npm --prefix docs/pages run serve

# ╭────────────────────----------------──────────╮
# │                Miscellaneous                 │
# ╰─────────────────────----------------─────────╯

## check-env | check if .env file exists
check-env:
	@if [ ! -f .env ]; then \
		echo "Error: .env file is missing. Please create it from .env.example"; \
		exit 1; \
	fi

## generate-migration-id | generate migration file prefix
generate-migration-id:
	@echo "$(shell date +%Y%m%d%H%M%S)"

## generate-dep-graph | generate dependency graph
# Dependencies:
#   * brew install graphviz
#   * go install github.com/loov/goda@latest
generate-dep-graph:
	@goda graph "github.com/go42-dev/go42/..." | dot -Tsvg -o dep-graph.svg

## grpcui | run grpcui for debugging gRPC services
# Dependencies:
#   * brew install grpcui
grpcui:
	@grpcui -plaintext localhost:9090

## show-asm | visualise assembly
# Dependencies:
#   * go install loov.dev/lensm@main
# Usage: FILTER={regex} make show-asm
show-asm: build
	@lensm -watch -text-size 22 -filter $(FILTER) .build/app
