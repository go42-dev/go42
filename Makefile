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
SHELL := /usr/bin/env bash

export MISE_DEFAULT_CONFIG_FILENAME := etc/mise.toml
export MISE_DATA_DIR := $(CURDIR)/.tools
export MISE_CACHE_DIR := $(MISE_DATA_DIR)/cache
export MISE_STATE_DIR := $(MISE_DATA_DIR)/state
export PATH := $(MISE_DATA_DIR)/shims:$(PATH)

.PHONY: help setup setup-common setup-linters setup-generators setup-mcp
.PHONY: test-unit test-fuzz test-integration test-resilience test-load
.PHONY: run run-docker debug build image lint lint-make lint-nilaway generate serve-docs
.PHONY: check-env generate-migration-id generate-dep-graph grpcui show-asm

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
		checkmake \
		editorconfig-checker \
		gitleaks \
		golangci-lint \
		golines \
		hadolint \
		markdownlint-cli2 \
		vale \
		zizmor \
		npm:@commitlint/cli \
		npm:@commitlint/config-conventional \
		pipx:sqlfluff \
		github:oasdiff/oasdiff \
		go:github.com/daixiang0/gci \
		go:github.com/caarlos0/jsonfmt \
		go:github.com/google/yamlfmt/cmd/yamlfmt \
		go:github.com/securego/gosec/v2/cmd/gosec \
		go:go.uber.org/nilaway/cmd/nilaway \
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

COVERAGE_EXCLUDE_DIRS := \
	api/gen \
	mocks \
	tests

## test-unit | run unit tests with coverage
# -count=1 is needed to prevent caching of test results.
test-unit:
	@mkdir -p .build
	@set -eo pipefail; \
	unit_packages=$$(go list ./...); \
	unit_exclude_pattern=$$(printf '%s\n' $(COVERAGE_EXCLUDE_DIRS) | paste -sd '|' -); \
	unit_packages=$$(printf '%s\n' "$$unit_packages" | grep -vE "/($$unit_exclude_pattern)(/|$$)"); \
	go test -count=1 -v -race -skip '^Fuzz' -covermode=atomic \
		-coverpkg="$$(printf '%s\n' "$$unit_packages" | paste -sd, -)" \
		-coverprofile=.build/coverage.out $$unit_packages \
		| sed -E 's/(coverage: [0-9.]+% of statements) in .*/\1/'
	@go tool cover -html=.build/coverage.out -o=.build/coverage.html
	@go tool cover -func=.build/coverage.out | grep '^total:'

## test-fuzz | run all fuzz targets (30 seconds per target)
test-fuzz:
	@go run ./cmd/fuzz -fuzztime 30s

## test-integration | run integration tests with coverage
# -count=1 is needed to prevent caching of test results.
# Uses application settings from .env when present.
test-integration:
	@mkdir -p .build
	@set -eo pipefail; \
	if [ -f .env ]; then set -a; . ./.env; set +a; fi; \
	coverage_packages=$$(go list -deps -test -f '{{if .Module}}{{if .Module.Main}}{{.ImportPath}}{{end}}{{end}}' ./tests/integration/...); \
	coverage_exclude_pattern=$$(printf '%s\n' $(COVERAGE_EXCLUDE_DIRS) | paste -sd '|' -); \
	coverage_packages=$$(printf '%s\n' "$$coverage_packages" | grep -vE "/($$coverage_exclude_pattern)(/|$$)" | paste -sd, -); \
	go test -count=1 -v -race -covermode=atomic -coverpkg="$$coverage_packages" \
		-coverprofile=.build/coverage-integration.out ./tests/integration/... \
		| sed -E 's/(coverage: [0-9.]+% of statements) in .*/\1/'
	@go tool cover -html=.build/coverage-integration.out -o=.build/coverage-integration.html
	@go tool cover -func=.build/coverage-integration.out | grep '^total:'

## test-resilience | verify dependency recovery with coverage
# Resilience tests are build-tagged and excluded from all other test targets.
# @note Requires `toxiproxy pgsql mysql redis memcached nats kafka rabbitmq`
test-resilience:
	@rm -rf .build/coverage-resilience{.out,.out.tmp,.html,-tests,-app}
	@mkdir -p .build/coverage-resilience-{tests,app}
	@set -eo pipefail; \
	coverage_exclude_pattern=$$(printf '%s\n' $(COVERAGE_EXCLUDE_DIRS) | paste -sd '|' -); \
	app_packages=$$(go list -deps -f '{{if .Module}}{{if .Module.Main}}{{.ImportPath}}{{end}}{{end}}' ./cmd/app); \
	app_packages=$$(printf '%s\n' "$$app_packages" | grep -vE "/($$coverage_exclude_pattern)(/|$$)" | paste -sd, -); \
	resilience_packages=$$(go list -deps -test -tags=resilience -f '{{if .Module}}{{if .Module.Main}}{{.ImportPath}}{{end}}{{end}}' ./tests/resilience/...); \
	resilience_packages=$$(printf '%s\n' "$$resilience_packages" | grep -vE "/($$coverage_exclude_pattern)(/|$$)" | paste -sd, -); \
	go build -race -cover -covermode=atomic -coverpkg="$$app_packages" -o .build/resilience-app ./cmd/app; \
	export RESILIENCE_APP_BINARY="$(CURDIR)/.build/resilience-app"; \
	export RESILIENCE_APP_COVERDIR="$(CURDIR)/.build/coverage-resilience-app"; \
	go test -count=1 -v -tags=resilience -cover -covermode=atomic \
		-coverpkg="$$resilience_packages" ./tests/resilience/... \
		-args -test.gocoverdir="$(CURDIR)/.build/coverage-resilience-tests" \
		| sed -E 's/(coverage: [0-9.]+% of statements) in .*/\1/'
	@ls .build/coverage-resilience-{tests,app}/cov{meta,counters}.* > /dev/null
	@go tool covdata textfmt \
		-i=.build/coverage-resilience-tests,.build/coverage-resilience-app \
		-o=.build/coverage-resilience.out.tmp
	@mv .build/coverage-resilience.out.tmp .build/coverage-resilience.out
	@go tool cover -html=.build/coverage-resilience.out -o=.build/coverage-resilience.html
	@go tool cover -func=.build/coverage-resilience.out | grep '^total:'

## test-load | run load tests (http and grpc)
test-load:
	@rm -f .build/k6-summary-{http,grpc}-v1.json
	@mkdir -p .build
	@k6 version
	@load_status=0; \
	for protocol in http grpc; do \
		k6 run --summary-export=.build/k6-summary-$$protocol-v1.json \
			tests/load/$$protocol/v1/auth_test.js || load_status=$$?; \
	done; \
	exit $$load_status

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
	@commitlint --config etc/.commitlintrc.yaml \
		--extends "$$(mise where npm:@commitlint/config-conventional)/node_modules/@commitlint/config-conventional/lib/index.js" \
		--from origin/master --to HEAD --verbose
	@golangci-lint run --config etc/.golangci.yml
	@sqlfluff lint --config etc/sqlfluff.toml --disable-progress-bar migrate/sqlite/*.sql --dialect sqlite
	@sqlfluff lint --config etc/sqlfluff.toml --disable-progress-bar migrate/mysql/*.sql --dialect mysql
	@sqlfluff lint --config etc/sqlfluff.toml --disable-progress-bar migrate/pgsql/*.sql --dialect postgres
	@gosec -quiet -exclude-generated ./...
	@checkmake --config etc/checkmake.ini Makefile
	@hadolint Dockerfile
	@helm lint --strict infra/helm/app --set-string image.tag=ci-validation
	@helm lint --strict infra/helm/app --set-string image.digest=sha256:0000000000000000000000000000000000000000000000000000000000000000
	@REDOCLY_SUPPRESS_UPDATE_NOTICE=true REDOCLY_TELEMETRY=false redocly lint --config etc/redocly.yaml --format stylish api/openapi/**/*.yaml
	@oasdiff breaking --fail-on ERR origin/master:api/openapi/v1/.combined.yaml api/openapi/v1/.combined.yaml
	@buf lint api
	@gitleaks git --config etc/gitleaks.toml --no-banner --redact -v
	@markdownlint-cli2 --config etc/.markdownlint.yaml README.md docs/**/*.md
	@vale --no-exit --config etc/vale.ini README.md docs/**/*.md internal/ cmd/ pkg/ tests/
	@actionlint -oneline --config-file etc/actionlint.yaml
	@zizmor -q --persona regular --min-severity high --min-confidence high --offline --format plain --color never --no-progress .
	@ec
	@nilaway \
		-include-pkgs=github.com/go42-dev/go42 \
		-exclude-file-docstrings='Code generated' \
		-pretty-print=false \
		-print-full-file-path=true \
		./... || true

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
