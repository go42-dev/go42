---
id: development
title: Development workflow
sidebar_position: 5
---

# Development workflow

Use this guide to take a change from a fresh checkout to a verified pull request. Start at the
[documentation index](../README.md) for the application's context and relevant requirements and decisions. Follow the
[project conventions](conventions.md) for engineering rules and the [documentation workflow](documentation.md) when
updating application knowledge alongside the implementation.

[Taskfile.yaml](../../Taskfile.yaml) defines the executable workflows. Task runs commands; mise manages the project tools
and their locked versions. This guide explains which commands to use, their scope, and their prerequisites.

## Environment setup

Install Go, mise, and Task and ensure their executables are on `PATH`. Use the Go version from [go.mod](../../go.mod) and
the minimum mise version from [etc/mise.toml](../../etc/mise.toml). Install Docker for container workflows and optional
MCP setup. If Task is missing, use the bootstrap instructions below.

From the repository root, install the project tools:

```sh
task setup
```

This installs the tools locked in [etc/mise.lock](../../etc/mise.lock), including Task, syncs Vale styles, and downloads
Go modules. `task setup-mcp` additionally installs gopls and pulls the pinned GitHub MCP image.

For code generation, `task setup-generators` installs the generators and their supporting tools from the same lockfile.
Run it before `task generate` to prepare only the tools needed for generation.

Create `.env` from [.env.example](../../.env.example) if it does not already exist, then adjust the local settings:

```sh
if [ ! -f .env ]; then
  cp .env.example .env
fi
```

Task invokes installed tools through `.tools/shims` and supplies mise's project paths, so Task commands work without
shell exports. Existing exported mise settings override Taskfile defaults. Go commands use the installed `go` executable;
Task defaults to `GOTOOLCHAIN=local` and adds the project shims to `PATH` for generators.

### Bootstrapping Task and invoking tools directly

For direct tool calls from a shell or editor, configure the project paths from the repository root:

```sh
export MISE_DEFAULT_CONFIG_FILENAME=etc/mise.toml
export MISE_DATA_DIR="$PWD/.tools"
export MISE_CACHE_DIR="$MISE_DATA_DIR/cache"
export MISE_STATE_DIR="$MISE_DATA_DIR/state"
export PATH="$MISE_DATA_DIR/shims:$PATH"
```

If Task is missing, run `mise install --locked task` after these exports, then `task setup`. The tool shims load applicable
mise settings, including Redocly's update-notice and telemetry settings.

## Using Task

Run commands from the repository root. Discover tasks and inspect their behavior with:

```sh
task help
task --list --sort none --json
task fmt:sql --summary
task --dry test-resilience
```

`task` and `task help` list commands in Taskfile order. Tasks run without caching. Pass file or local Go package arguments
after `--`; put Task variables before that separator:

```sh
task fmt:yaml -- .github/workflows/150-load-tests.yaml
task lint:yaml -- Taskfile.yaml .github/workflows/150-load-tests.yaml
task lint:go -- ./internal/metrics
task test-unit -- ./internal/metrics
```

Formatters require existing individual files. Linters that accept file arguments also reject directories, literal globs,
and tool flags. Quote paths containing spaces. Go package arguments use local paths such as `./internal/metrics` or
`./internal/auth/...`. A `lint:*` task without arguments checks its default project scope; `task lint` accepts no file or
package arguments.

## Running and debugging

The launch tasks require `.env` through `task check-env`. They load defaults from `.env.example`, then local overrides
from `.env`. Start the services required by the selected database, cache, and event settings.

| Command           | Behavior                                                                            |
|-------------------|-------------------------------------------------------------------------------------|
| `task run`        | Run the application locally with race detection and compiler optimizations disabled |
| `task run-docker` | Run the application in a Linux Go container with the checkout mounted               |
| `task debug`      | Start the headless Delve debugger on port 2345                                      |
| `task build`      | Build `.build/app` with race detection and compiler optimizations disabled          |
| `task image`      | Build the development Docker image for Linux amd64 and arm64                        |

`task run` and `task run-docker` treat exit code 1 as success, as recorded in Taskfile. Check the application output when
verifying startup or shutdown behavior.

For optional inspection tools, `task grpcui` requires `grpcui` and targets plaintext `localhost:50051`. Enable
`SERVER_GRPC_REFLECTION_ENABLED=true` on the local test application and restart it first; reflection is disabled by default.
Business calls still require `x-api-key` metadata and permissions. The generated integration clients use compiled
descriptors and do not need reflection.
`task generate-dep-graph` writes the dependency graph and requires `goda` and Graphviz's `dot`. These dependencies are
described in Taskfile comments and are installed separately.

### Profiling a local process

Use a host process launched by `task run`, `task debug`, or a direct binary for this procedure. Set `PPROF_ENABLED=true`,
`PPROF_LISTEN=127.0.0.1:6060`, and `PPROF_PREFIX=/debug/pprof` in its configuration, then restart it.
The [profiling server](../../cmd/app/main.go) is separate from the main HTTP listener and has no application authentication.
Use loopback access for local collection and retain profiles as private diagnostic artifacts.

From another terminal, capture a heap sample and ten seconds of CPU activity:

```sh
mkdir -p .build
curl --fail --silent --show-error --max-time 15 \
  http://127.0.0.1:6060/debug/pprof/heap -o .build/heap.pprof
curl --fail --silent --show-error --max-time 25 \
  'http://127.0.0.1:6060/debug/pprof/profile?seconds=10' -o .build/cpu.pprof
go tool pprof -top .build/heap.pprof
go tool pprof -top .build/cpu.pprof
```

Expect nonempty profile files and a function/sample summary. Exercise the affected workload during CPU collection;
an idle sample may contain little evidence. Record the running revision and symptom with the profile, and use a matching
binary for source or disassembly analysis. Adjust the URL port if the listener differs, but keep the default prefix:
the current custom-prefix handler returns HTML for named profiles such as heap. This recipe does not apply unchanged to
`task run-docker`, whose port mapping expects `:port` listener values. Disable local profiling again when finished.
Its endpoint availability does not establish main-server readiness.

## Generated files and dependencies

Edit source definitions or generator configuration, then run `task generate` when the change affects generated content.
It tidies Go modules, regenerates APIs and mocks, generates `.env.example` through `cmd/cfg2env`, and combines the OpenAPI
definitions. Review the resulting diff and commit source changes and corresponding tracked outputs together. After setup,
running `task generate` from a clean checkout must produce no changes.

Generated outputs include `api/gen/`, package `mocks/` directories, `.env.example`, and the combined OpenAPI document.
Documentation assembly owns `pages/docs/`, and the website build owns `pages/build/`; regenerate those through the
[documentation commands](documentation.md#verification). Dependency tools own lockfiles such as `etc/mise.lock` and
`pages/package-lock.json`.

Update vendored files, including Protobuf definitions under `api/proto/third_party/`, through their dependency update
process. Record versions and upstream sources in adjacent `.versions.yaml` files, retain upstream license notices, and
regenerate affected outputs. Follow [tool maintenance](#maintaining-the-workflow) when changing tool pins or configuration.

For a new database migration, run `task generate-migration-id` once to obtain its filename prefix. Use the same filename
across database engines and follow the [migration conventions](conventions.md#architecture-and-data).

## Implementing a feature

Use the [source map](architecture.md#source-map) to trace an existing operation with similar behavior. Confirm the desired
outcome and failure cases in the relevant requirement; record a significant new choice in a decision when needed.

1. Define the transport contract in [OpenAPI](../../api/openapi/v1/auth.yaml) or
   [Protobuf](../../api/proto/auth/v1/auth.proto). Specify validation, authorization, response fields, and error behavior.
2. Implement service inputs and errors in the feature's `domain/` package, persistence models and migrations when needed,
   and repository operations. Keep transaction boundaries in the service and propagate `txCtx` to required outbox writes.
3. Update consumer interfaces and implement the service operation. Decide which failures roll back the operation and
   which side effects can be retried independently. Cover those outcomes in service and repository tests.
4. Implement each affected adapter. HTTP routes and request/response types are handwritten in
   [adapter.go](../../internal/auth/adapters/http/v1/adapter.go) and [models.go](../../internal/auth/adapters/http/v1/models.go).
   Generating an HTTP SDK does not add a running route. For gRPC, implement the generated service interface and update
   [adapterPermissionMapping](../../internal/auth/adapters/grpc/v1/adapter.go). With gRPC authorization enabled, methods
   without registered permissions are denied.
5. Register a new adapter or dependency in [cmd/app](../../cmd/app/main.go). `RegisterV1` supplies the HTTP `/api/v1`
   prefix; the gRPC adapter registers its generated service. For new background work, define its startup and shutdown owner.
6. Run the [generation workflow](#generated-files-and-dependencies). A new HTTP contract also needs generator directives
   in [api/generate.go](../../api/generate.go); the existing directives name the authentication contract explicitly.
7. Test the service and transport behavior, including validation, missing permissions, and dependency failure. When both
   transports expose an operation, verify both mappings. Run applicable API compatibility checks before review.
8. Update the owning handbook page, requirement evidence, and any affected operating instructions with the change.

For persisted-data or message-format changes, include the [migration handoff](deployment.md#migrations-and-change-handoff).

For the existing authentication feature, a focused unit check is:

```sh
task test-unit -- ./internal/auth/... ./internal/api/...
```

The [HTTP integration clients](../../tests/integration/http/v1/users_clients_test.go) exercise both generated HTTP SDKs;
[gRPC integration tests](../../tests/integration/grpc/v1/auth_test.go) exercise the generated gRPC client. Follow the
[integration prerequisites](#integration-test-environment) before running `task test-integration`.

## Formatting and linting

Format only the source files edited for the task, then review the diff. Linters check without applying fixes. Select
checks by purpose as well as extension: workflow YAML, for example, also needs GitHub Actions validation.

In the commands below, set `FILE` to an individual source file, `PACKAGE` to its Go package or subtree, and `DIALECT` to
the SQL dialect described below.

| Source type | Formatter | Check |
| --- | --- | --- |
| Go | `task fmt:go -- "$FILE"` | `task lint:go -- "$PACKAGE"` |
| Plain YAML, including workflows | `task fmt:yaml -- "$FILE"` | `task lint:yaml -- "$FILE"` |
| JSON | `task fmt:json -- "$FILE"` | `task lint:json -- "$FILE"` |
| TOML | `task fmt:toml -- "$FILE"` | `task lint:toml -- "$FILE"` |
| Markdown | `task fmt:markdown -- "$FILE"` | `task lint:markdown -- "$FILE"` and `task lint:prose -- "$FILE"` |
| Protobuf | `task fmt:proto -- "$FILE"` | `task lint:proto` checks the entire `api` module |
| SQL migrations | `task fmt:sql DIALECT="$DIALECT" -- "$FILE"` | `task lint:sql DIALECT="$DIALECT" -- "$FILE"` |

Go formatting runs the formatters enabled in [the Go configuration](../../etc/.golangci.yml), which owns line length and
import grouping. Use `go mod edit -fmt` for `go.mod` layout.

YAML, JSON, and TOML checks without file arguments select tracked and untracked source files that Git does not ignore,
skipping deleted files. YAML excludes Helm templates and combined OpenAPI output; JSON excludes `package-lock.json`;
TOML selects `*.toml` files, leaving `etc/mise.lock` to mise. Markdown and prose checks default to `docs/`.

TOML uses [Tombi's configuration](../../etc/tombi.toml). Task handles configuration discovery from `etc/` and resolves
file paths from the repository root. Formatting and linting use the pinned release's bundled schema catalog offline;
lint warnings fail the check.

For SQL, `migrate/sqlite/` uses `DIALECT=sqlite`, `migrate/mysql/` uses `DIALECT=mysql`, and `migrate/pgsql/` uses
`DIALECT=postgres`. Format one dialect at a time and review the changes. `task lint:sql` checks all three migration
directories; adding `DIALECT=sqlite`, for example, checks only that dialect. Explicit SQL files require a dialect.

Markdown formatting fixes supported lint issues. Remaining Markdown and prose diagnostics need manual edits.
Helm files under `infra/helm/app/templates/` contain Go templates: preserve their syntax and validate the chart with Helm.
Use the YAML formatter on plain chart metadata and values files.

JavaScript, TypeScript, CSS, shell, and Dockerfiles have no configured formatter. Match the surrounding style and
[.editorconfig](../../.editorconfig), then run applicable checks. Use `task lint:editorconfig -- "$FILE"` to check an
edited file or omit arguments to check the whole project.

### Checks for specific purposes

* For workflow YAML, add `task lint:actions -- "$FILE"`, which runs Actionlint and Zizmor. For composite actions or shared
  workflow interfaces, use `task lint:actions` without arguments so it checks all workflows and includes composite actions.
* For OpenAPI changes, use `task lint:openapi` and `task lint:openapi-breaking`. For Protobuf changes, add
  `task lint:proto-breaking` to the module checks above. Regenerate affected outputs before compatibility checks; those
  comparisons require `origin/master`.
* For Dockerfile changes, use `task lint:docker`. For Helm changes, use `task lint:helm`, which validates both tag and digest
  image references.
* For standalone shell files, use the pinned `shellcheck "$FILE"` after configuring the
  [direct tool environment](#bootstrapping-task-and-invoking-tools-directly).

### Editor watchers

Configure watchers to run the matching formatter task from the repository root on the edited source file. For example,
a GoLand Markdown watcher can use program `task`, arguments `fmt:markdown -- "$FilePath$"`, and working directory
`$ProjectFileDir$`.

When invoking a tool directly, use the pinned tool environment above and the same configuration as Taskfile. For Markdown:

```sh
markdownlint-cli2 --config etc/.markdownlint-cli2.yaml --no-globs --fix "$FILE"
```

`--no-globs` prevents the configured documentation glob from expanding a file-specific fix. Direct Tombi calls need `etc/`
as the working directory and an absolute file path. Shared JetBrains watcher definitions live in
[etc/.ide/jetbrains/watchers.xml](../../etc/.ide/jetbrains/watchers.xml); keep imported settings aligned with the workflow.

For VS Code, install [Run on Save](https://marketplace.visualstudio.com/items?itemName=pucelle.run-on-save)
and copy or merge [etc/.ide/vscode/settings.json](../../etc/.ide/vscode/settings.json)
into `.vscode/settings.json` at the repository root.
Make Go and Task available on VS Code's `PATH`, then run `task setup` to install the project tools. The preset invokes
Task formatters on save, with Go formatting followed by package linting. It preserves the SQL migration dialect scopes,
skips Helm templates for YAML formatting, and runs commands in sequence. Task supplies the mise environment and Tombi's
working directory. Per-project `.idea/` and `.vscode/` settings are ignored by Git.

## Testing and verification

While editing, run the matching focused checks and tests for the affected behavior. Check Go packages together, including
callers when interfaces change. Follow the [test design conventions](conventions.md#testing).

| Command                                | Purpose and scope                                                                                                                  |
|----------------------------------------|------------------------------------------------------------------------------------------------------------------------------------|
| `task test-unit`                       | Full unit suite with race detection and coverage; excludes generated, mock, and external test trees                                |
| `task test-unit -- ./internal/metrics` | Selected local packages with race detection, without coverage reports                                                              |
| `task test-fuzz`                       | All fuzz targets, with 30 seconds per target by default                                                                            |
| `task test-integration`                | Integration suite with coverage; loads `.env` when present and needs configured services                                           |
| `task test-resilience`                 | Dependency recovery tests with an instrumented application and combined coverage; needs Toxiproxy and the suite's backing services |
| `task test-load`                       | HTTP and gRPC k6 suites against a running application by default                                                                   |

Coverage reports are written under `.build/`. Resilience tests are build-tagged and excluded from other test tasks.
Use the [resilience suite](../../tests/resilience/application_test.go) and
[CI service configuration](../../.github/workflows/120-resilience-tests.yaml) to identify its required backends.

Fuzz tests accept `FUZZ_TIME` as a Go duration or iteration count. Load tests accept `K6_TEST_PATH` and `K6_SUMMARY_PATH`
together to select an existing script and its summary output. These values can be Task variables or environment variables:

```sh
task test-fuzz FUZZ_TIME=5m
task test-load K6_TEST_PATH=tests/load/http/v1/auth_test.js \
  K6_SUMMARY_PATH=.build/k6-summary-http-v1.json
```

Without load-test selectors, both protocols run, even if the first fails, and write `.build/k6-summary-{http,grpc}-v1.json`.
Each invocation removes previous summaries for its selected scope before running k6 and reports failures through its exit
status.

### Integration test environment

`task test-integration` runs the full suite; it does not start the application. Use a dedicated test application because
the API tests create, update, and delete user records. The repository tests separately create isolated databases through
the [test helper](../../tests/integration/helpers.go).

| Test process setting | Required setup |
| --- | --- |
| `HTTP_SERVER_ADDRESS` | Application base URL, default `http://localhost:8080`; omit the `/api/v1` suffix |
| `GRPC_SERVER_ADDRESS` | Application address, default `localhost:50051`; the current client uses plaintext gRPC |
| `HTTP_API_KEY` | Explicit key with `users:list`, `users:read_others`, `users:create`, `users:update`, and `users:delete` |
| `GRPC_API_KEY` | Key with the same permissions; set it explicitly instead of relying on the helper's inherited test key |
| `DATABASE_*` | Select the engine and test database service; MySQL/PostgreSQL credentials need create/drop database privileges |

Start the application with its own configuration and verify [readiness](operations.md#run-and-verify-locally). Supply the
test settings through `.env` or the test process environment; the Task command loads `.env` when present but does not load
`.env.example`. Values loaded from `.env` override inherited environment values, including `DATABASE_*` and test addresses
or keys. Check that file when an exported override appears ineffective. The app and test runner are separate processes,
so changing the runner's settings does not reconfigure the app. Keep `SERVER_GRPC_AUTHORIZATION_ENABLED=true` on the test
application: the suite tests denied requests too. SQLite repository tests use temporary files and need no database service.

The [integration CI workflow](../../.github/workflows/150-integration-tests.yaml) is a complete example of application,
backend, address, and test-credential setup. It disables the application's authentication rate limiter for its test load.
Keep such overrides scoped to the test application. If the suite reports a missing HTTP key, follow this section; its
current failure message refers to an obsolete `tests/integration/README.md` path.

### Choosing checks before review

* Run applicable `task test-*` commands for the changed behavior, using the suites and backends relevant to the change.
* Use `task lint` for full code and project validation, changes spanning several areas, or shared tooling and dependency
  changes. It includes Go analysis, security, licenses, capabilities, commit history, API compatibility, and source checks.
  Individual checks remain available through `task help`.
* Use `task docs-check` for documentation or website changes. It runs Markdown and prose checks, installs locked website
  dependencies, then tests the tooling, validates documents, builds the site, and checks TypeScript.
* For a focused change, select the relevant checks above. A workflow edit can use its YAML and GitHub Actions checks.
  Repeat checks after relevant edits or failures, and report material checks left unrun.

`task lint` and `task docs-check` run their checks in sequence and stop at the first failure, except that NilAway remains
advisory in `task lint`. A failed invocation does not mean every check ran. Neither command selects checks automatically
from changed files or applies formatting fixes. History and compatibility comparisons require `origin/master`.

For a documentation preview, use `task docs-serve`. For a focused metadata and source-link check after installing website
dependencies, use `npm --prefix pages run validate-docs` with the pinned tool environment. The full documentation build
also checks published links and anchors. Follow the [documentation policy](documentation.md#verification) for publishing
and application-specific evidence. CI runs its configured jobs independently of the local checks selected here.

## Preparing a pull request

Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/) with the types and rules in
[etc/.commitlintrc.yaml](../../etc/.commitlintrc.yaml). Branch names must match `^[A-Za-z0-9/_.-]+$`, be descriptive, and
include a task identifier when applicable. Prefer merge commits over rebasing, and keep rebase disabled. Use `.gitkeep`
to preserve empty directories in Git and the `gh` client to access GitHub resources. Use
[Semantic Versioning](https://semver.org/) for releases; follow [Release](release.md) for publishing artifacts.

Destructive operations require user confirmation in interactive mode. In non-interactive mode, allow them only when
explicitly requested by the user.

Before opening or updating a pull request:

1. Review the final diff, including generated outputs. Update affected documentation through its
    [change workflow](documentation.md#change-workflow-for-contributors).
2. Use the [pull request template](../../.github/pull_request_template.md) for PRs created through the web interface
    or tools.
    Describe the problem and resulting behavior, with a brief before/after example when useful. Keep the title and
    description proportional to and aligned with the final change.
3. Link relevant issues, requirements, decisions, and project documentation. Keep durable rationale and operating
    instructions in the project documentation.
4. In `Verification`, report checks actually performed and their results, including commands, suites, backends, and
    relevant manual checks. Distinguish completed checks from planned checks, identify anything left unverified, and
    explain material skips.
5. Add a `Deployment` section when adopting the change requires action. Describe relevant migrations, configuration or
    compatibility changes, rollout requirements, and rollback limitations. Link the relevant
    [deployment instructions](deployment.md).

## Maintaining the workflow

Keep Go's version in `go.mod`, tool versions in `etc/mise.toml`, and formatter and linter configurations in `etc/`.
Keep `Taskfile.yaml` at the repository root. Update tool pins and their `etc/mise.lock` entries together, preserving
supported platform coverage and keeping duplicate CI pins aligned. Update this guide when commands, prerequisites,
formatters, or check scopes change.

CI installs the tools each job needs through mise using the same pins and lockfile as local setup. Jobs configure
`MISE_DEFAULT_CONFIG_FILENAME=etc/mise.toml` and the project tool paths. Go test jobs install Task, load-test jobs add k6,
and documentation jobs install Task and the documentation tools.

The shared [environment action](../../.github/actions/prepare-env/action.yaml) installs Task, then runs the task selected
by its `setup-task` input, which defaults to `setup`. The `project-lint` job selects `setup-generators`. Each setup task
has a separate project-tool cache, saved at successful job completion so subsequent jobs can reuse the installed tools.

Every test workflow invokes its Task command. GitHub Actions manages matrices, service containers, caches, timeouts,
artifacts, and reporting. Fuzz and load steps use `--exit-code` to preserve the underlying command's failure code.
Update Taskfile and its relevant CI callers together when changing a workflow.

Upstream reference: [Task guide](https://taskfile.dev/docs/guide).
