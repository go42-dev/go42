---
id: development
title: Development workflow
collection: handbook
sidebar_position: 5
---

# Development workflow

Use this guide to take a change from a fresh checkout to a verified pull request. Start at the
[documentation index](../README.md) for the application's context and relevant requirements and decisions. Follow the
[project conventions](03-conventions.md) for engineering rules and the [documentation workflow](01-documentation.md) when
updating application knowledge alongside the implementation.

[Taskfile.yaml](../../Taskfile.yaml) defines the executable workflows. Task runs commands; mise manages the project tools
and their locked versions. This guide explains which commands to use, their scope, and their prerequisites.

## Environment setup

Install Go, mise, and Task and ensure their executables are on `PATH`. Use the Go version from [go.mod](../../go.mod) and
the minimum mise version from [etc/mise.toml](../../etc/mise.toml). Install Docker for container workflows.
If Task is missing, use the bootstrap instructions below.

From the repository root, install the project tools:

```sh
task setup
```

This installs all tools locked in [etc/mise.lock](../../etc/mise.lock), including Task and gopls. It also syncs Vale styles
and downloads Go modules.

The [MCP configuration](../../.go42x/go42x.yaml) runs the local servers through `task tool -- gopls mcp`,
`task tool -- go42x mcp`, and `task tool -- mise mcp`. Start these commands from the repository root to use the project
tool versions and environment configured by mise.

Mise MCP exposes tool versions, installation status, environment values, and active configuration files. Its server
configuration sets `MISE_EXPERIMENTAL=1`. Workflows remain in Taskfile; no mise tasks are defined, so the MCP `run_task`
tool cannot execute Taskfile commands directly.

GitHub MCP uses [GitHub's hosted HTTP server](https://github.com/github/github-mcp-server/blob/main/docs/remote-server.md)
at `https://api.githubcopilot.com/mcp/`. Export `GITHUB_PERSONAL_ACCESS_TOKEN` in the MCP client's environment; the
configuration uses it for bearer authentication and sets `X-MCP-Toolsets: all`. After changing the MCP configuration,
run `task x -- agentenv generate` and restart the client.

For code generation, `task setup:generators` installs the generators and their supporting tools from the same lockfile.
Run it before `task generate` to prepare only the tools needed for generation.

Create `.env` from [.env.example](../../.env.example) if it does not already exist, then adjust the local settings:

```sh
if [ ! -f .env ]; then
  cp .env.example .env
fi
```

Task supplies mise's project paths, so Task commands work without shell exports. Tasks invoke installed tools through
`.tools/shims` or `mise exec`. Existing exported mise settings override Taskfile defaults. Go commands use the installed
`go` executable; Task defaults to `GOTOOLCHAIN=local` and adds the project shims to `PATH` for generators.

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

Use `group:name` for related commands: `test:unit`, `fmt:yaml`, `lint:go`, `docs:check`, and `setup:generators`.
Keep standalone commands short, such as `run`, `build`, `generate`, and `setup`. Use hyphens within a name for multiple
words, such as `generate:migration-id` and `lint:openapi-breaking`. Give each task one name; update its callers and
documentation when renaming it.

Run commands from the repository root. Discover tasks and inspect their behavior with:

```sh
task help
task --list --sort none --json
task fmt:sql --summary
task --dry test:resilience
```

`task` and `task help` list commands in Taskfile order. Tasks run without caching. Pass file or local Go package arguments
after `--`; put Task variables before that separator:

```sh
task fmt:yaml -- .github/workflows/150-load-tests.yaml
task lint:yaml -- Taskfile.yaml .github/workflows/150-load-tests.yaml
task lint:go -- ./internal/metrics
task test:unit -- ./internal/metrics
```

Use `task tool -- COMMAND [ARGS...]` to run a command with mise's project versions and environment. Pass the executable
name and its arguments after `--`; the task preserves standard input and output:

```sh
task tool -- gopls version
task tool -- node --version
task tool -- go42x kwb build
```

Formatters require existing individual files. Linters that accept file arguments also reject directories, literal globs,
and tool flags. Quote paths containing spaces. Go package arguments use local paths such as `./internal/metrics` or
`./internal/auth/...`. A `lint:*` task without arguments checks its default project scope; `task lint` accepts no file or
package arguments.

### Go navigation and diagnostics

The generated agent instructions use the configured gopls tools for Go navigation and feedback during editing:

1. Before changing a function signature, shared type, or interface, inspect symbol references and read affected callers,
    implementations, and tests. Request file context or a package's public API when needed to understand unfamiliar code.
2. After a coherent batch of saved Go edits, request diagnostics for the changed files. Investigate relevant findings,
    fix errors introduced by the change, and check again after fixes.
3. Run the applicable [lint and test commands](06-testing.md#choosing-checks-before-review) before completing the change.
    Include affected callers when selecting packages; diagnostics supplement those checks.

The configured standalone gopls server reads saved files. Its results reflect the loaded workspace and build
configuration; review affected build tags and platforms separately. If gopls is unavailable, use local source inspection
and the project's lint and test commands, and report the limitation.

Edit the [authored search guidance](../../.go42x/chunks/200-search.tpl.md) to change this workflow, then run
`task x -- agentenv generate` to refresh the generated instructions.

### Refreshing project knowledge

The [AI preparation action](../../.github/actions/ai-prepare-env/action.yml) refreshes the knowledge base after generating
agent configuration. Use the same order from the repository root when preparing a local session:

```sh
task x -- agentenv generate
task x -- kwb build
```

The [project exclusions](../../.go42x/kwb.ignore) omit generated Swagger JavaScript bundles while retaining handwritten
JavaScript. Reading this file, indexing authored `.go42x` configuration and `.env.example`, and `kwb check` require the
go42x KB-01–06 source changes dated September 14, 2026; they are not included in this repository's pinned 0.28.0 release.
After adopting a release containing those changes, run `task x -- kwb check --json` to verify freshness without updating
the index. It exits 0 for a complete, fresh scan and 1 for stale, unavailable, or incomplete results. Build again to apply
source or exclusion changes. The scan uses recorded build settings and current ignore files.

That version also supports `--context-doc=project,conventions,documentation` in the go42x MCP server's arguments to load
these authored guidance IDs. Add it to the [MCP configuration](../../.go42x/go42x.yaml) after upgrading, then regenerate
agent output and restart the client. Context requests can retrieve multiple source ranges and documentation sections
from one file; supplied files are read directly and directory paths scope searches.

## Running and debugging

The launch tasks require `.env` through `task check:env`. They load defaults from `.env.example`, then local overrides
from `.env`. Start the services required by the selected database, cache, and event settings.

| Command           | Behavior                                                                            |
|-------------------|-------------------------------------------------------------------------------------|
| `task run`        | Run the application locally with race detection and compiler optimizations disabled |
| `task run:docker` | Run the application in a Linux Go container with the checkout mounted               |
| `task debug`      | Start the headless Delve debugger on port 2345                                      |
| `task build`      | Build `.build/app` with race detection and compiler optimizations disabled          |
| `task image`      | Build the development Docker image for Linux amd64 and arm64                        |

`task run` and `task run:docker` treat exit code 1 as success, as recorded in Taskfile. Check the application output when
verifying startup or shutdown behavior.

For optional inspection tools, `task grpcui` requires `grpcui` and targets plaintext `localhost:50051`. Enable
`SERVER_GRPC_REFLECTION_ENABLED=true` on the local test application and restart it first; reflection is disabled by default.
Business calls still require `x-api-key` metadata and permissions. The generated integration clients use compiled
descriptors and do not need reflection.
`task generate:dep-graph` writes the dependency graph and requires `goda` and Graphviz's `dot`. These dependencies are
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
`task run:docker`, whose port mapping expects `:port` listener values. Disable local profiling again when finished.
Its endpoint availability does not establish main-server readiness.

## Generated files and dependencies

Edit source definitions or generator configuration, then run `task generate` when the change affects generated content.
It tidies Go modules, regenerates APIs and mocks, generates `.env.example` through `cmd/cfg2env`, and combines the OpenAPI
definitions. Review the resulting diff and commit source changes and corresponding tracked outputs together. After setup,
running `task generate` from a clean checkout must produce no changes.

Generated outputs include `api/gen/`, package `mocks/` directories, `.env.example`, and the combined OpenAPI document.
Documentation assembly owns `pages/docs/`, and the website build owns `pages/build/`; regenerate those through the
[documentation commands](01-documentation.md#verification). Dependency tools own lockfiles such as `etc/mise.lock` and
`pages/package-lock.json`.

Update vendored files, including Protobuf definitions under `api/proto/third_party/`, through their dependency update
process. Record versions and upstream sources in adjacent `.versions.yaml` files, retain upstream license notices, and
regenerate affected outputs. Follow [tool maintenance](#maintaining-the-workflow) when changing tool pins or configuration.

For a new database migration, run `task generate:migration-id` once to obtain its filename prefix. Use the same filename
across database engines and follow the [migration conventions](03-conventions.md#architecture-and-data).

## Implementing a feature

Use the [source map](07-architecture.md#source-map) to trace an existing operation with similar behavior. Confirm the desired
outcome and failure cases in the relevant requirement; record a significant new choice in a decision when needed.

1. Define the transport contract in [OpenAPI](../../api/openapi/v1/auth.yaml) or
    [Protobuf](../../api/proto/auth/v1/auth.proto). Specify validation, authorization, response fields, and error behavior.
2. Implement service inputs and errors in the feature's `domain/` package, persistence models and migrations when needed,
    and repository operations. Keep transaction boundaries in the service and propagate `txCtx` to required outbox writes.
3. Update consumer interfaces and implement the service operation. Decide which failures roll back the operation and
    which side effects can be retried independently. Cover those outcomes in service and repository tests.
4. Implement each affected adapter. HTTP routes and request/response types are handwritten in
    [adapter.go](../../internal/auth/adapters/http/v1/adapter.go) and
    [models.go](../../internal/auth/adapters/http/v1/models.go).
    Generating an HTTP SDK does not add a running route. For gRPC, implement the generated service interface and update
    [adapterPermissionMapping](../../internal/auth/adapters/grpc/v1/adapter.go). With gRPC authorization enabled, methods
    without registered permissions are denied.
5. Register a new adapter or dependency in [cmd/app](../../cmd/app/main.go). `RegisterV1` supplies the HTTP `/api/v1`
    prefix; the gRPC adapter registers its generated service. For new background work, define its startup and shutdown
    owner.
6. Run the [generation workflow](#generated-files-and-dependencies). A new HTTP contract also needs generator directives
    in [api/generate.go](../../api/generate.go); the existing directives name the authentication contract explicitly.
7. Test the service and transport behavior, including validation, missing permissions, and dependency failure. When both
    transports expose an operation, verify both mappings. Run applicable API compatibility checks before review.
8. Update the owning handbook page, requirement evidence, and any affected operating instructions with the change.

For persisted-data or message-format changes, include the [migration handoff](10-deployment.md#migrations-and-change-handoff).

For the existing authentication feature, a focused unit check is:

```sh
task test:unit -- ./internal/auth/... ./internal/api/...
```

The [HTTP integration clients](../../tests/integration/http/v1/users_clients_test.go) exercise both generated HTTP SDKs;
[gRPC integration tests](../../tests/integration/grpc/v1/auth_test.go) exercise the generated gRPC client. Follow the
[integration prerequisites](06-testing.md#integration-test-environment) before running `task test:integration`.

## Formatting and linting

Format only edited source files and review the diff; linters check without applying fixes. In the commands below, `FILE`
is an existing file and `PACKAGE` is a local Go package or subtree. Use `task TASK --summary` for usage.

| Source type                     | Formatter                                    | Check                                                            |
|---------------------------------|----------------------------------------------|------------------------------------------------------------------|
| Go                              | `task fmt:go -- "$FILE"`                     | `task lint:go -- "$PACKAGE"`                                     |
| Plain YAML, including workflows | `task fmt:yaml -- "$FILE"`                   | `task lint:yaml -- "$FILE"`                                      |
| JSON                            | `task fmt:json -- "$FILE"`                   | `task lint:json -- "$FILE"`                                      |
| TOML                            | `task fmt:toml -- "$FILE"`                   | `task lint:toml -- "$FILE"`                                      |
| Markdown                        | `task fmt:markdown -- "$FILE"`               | `task lint:markdown -- "$FILE"` and `task lint:prose -- "$FILE"` |
| Protobuf                        | `task fmt:proto -- "$FILE"`                  | `task lint:proto` checks the entire `api` module                 |
| SQL migrations                  | `task fmt:sql DIALECT="$DIALECT" -- "$FILE"` | `task lint:sql DIALECT="$DIALECT" -- "$FILE"`                    |

For SQL, use `DIALECT=sqlite` for `migrate/sqlite/`, `mysql` for `migrate/mysql/`, and `postgres` for `migrate/pgsql/`.
Explicit files require a dialect. With no files, `task lint:sql` checks all three; adding `DIALECT` selects one.

YAML, JSON, and TOML checks default to tracked files and unignored untracked files, skipping deleted files.
YAML skips Helm templates and combined OpenAPI output; JSON skips `package-lock.json`; TOML selects `*.toml`.
Markdown and prose default to `docs/`. Let the owning tools update lockfiles.

[Go formatter settings](../../etc/golangci.yaml) control line length and import grouping; use `go mod edit -fmt` for
`go.mod`. TOML tasks load [etc/tombi.toml](../../etc/tombi.toml), use offline schemas, and treat lint warnings as errors.
Markdown formatting fixes supported issues; remaining diagnostics need manual edits.

JavaScript, TypeScript, CSS, shell, and Dockerfiles have no configured formatter. Follow the surrounding style and
[.editorconfig](../../.editorconfig); check edited files with `task lint:editorconfig -- "$FILE"`.

### Checks for specific purposes

| Change           | Additional checks                                                                                                         |
|------------------|---------------------------------------------------------------------------------------------------------------------------|
| Workflow YAML    | `task lint:actions -- "$FILE"` runs Actionlint and Zizmor; omit files for composite actions or shared workflow interfaces |
| OpenAPI          | `task lint:openapi` and `task lint:openapi-breaking`                                                                      |
| Protobuf         | `task lint:proto-breaking`, in addition to the full-module checks above                                                   |
| Dockerfile       | `task lint:docker`                                                                                                        |
| Helm             | `task lint:helm` checks tag and digest image references; format only plain chart YAML, preserving Go template syntax      |
| Standalone shell | `task tool -- shellcheck "$FILE"`                                                                                         |

Regenerate affected outputs before API compatibility checks and ensure `origin/master` is available.

### Editor watchers

Run Task-based watchers from the repository root on the edited file. Make Go and Task available on the editor's `PATH`
and run `task setup` first.

* **JetBrains:** For a Markdown watcher, use program `task`, arguments `fmt:markdown -- "$FilePath$"`, and working directory
  `$ProjectFileDir$`. The [shared definitions](../../etc/.ide/jetbrains/watchers.xml) use project shims.
* **VS Code:** Install [Run on Save](https://marketplace.visualstudio.com/items?itemName=pucelle.run-on-save) and merge
  [the preset](../../etc/.ide/vscode/settings.json) into `.vscode/settings.json`.

The VS Code preset runs Task commands in sequence, formats Go before package linting, selects SQL dialects, and skips
Helm templates for YAML formatting. Per-project `.idea/` and `.vscode/` settings are ignored.

Keep tool calls aligned with Taskfile and the [tool environment](#bootstrapping-task-and-invoking-tools-directly).
Markdown calls need `--no-globs` for file-specific fixes; Tombi needs `etc/` as its working directory and absolute
file paths.

## Testing and verification

Use the [testing guide](06-testing.md) to select suites, prepare dependencies, and check results.

### Integration test environment

Follow [integration setup](06-testing.md#integration-test-environment) for application settings, test credentials, and
backend requirements.

### Choosing checks before review

Follow the [review checks](06-testing.md#choosing-checks-before-review) and report commands, results, backends, and skips.

## Preparing a pull request

Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/) with the types and rules in
[etc/commitlint.yaml](../../etc/commitlint.yaml). Branch names must match `^[A-Za-z0-9/_.-]+$`, be descriptive, and
include a task identifier when applicable. Prefer merge commits over rebasing, and keep rebase disabled. Use `.gitkeep`
to preserve empty directories in Git and the `gh` client to access GitHub resources. Use
[Semantic Versioning](https://semver.org/) for releases; follow [Release](09-release.md) for publishing artifacts.

Destructive operations require user confirmation in interactive mode. In non-interactive mode, allow them only when
explicitly requested by the user.

Before opening or updating a pull request:

1. Review the final diff, including generated outputs. Update affected documentation through its
    [change workflow](01-documentation.md#change-workflow-for-contributors).
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
    [deployment instructions](10-deployment.md).

## Maintaining the workflow

| Setting                            | Source                                                                        |
| ---------------------------------- | ----------------------------------------------------------------------------- |
| Go version                         | [go.mod](../../go.mod)                                                        |
| Tool versions and platform locks   | [etc/mise.toml](../../etc/mise.toml) and [etc/mise.lock](../../etc/mise.lock) |
| Commands                           | [Taskfile.yaml](../../Taskfile.yaml)                                          |
| Formatter and linter configuration | `etc/`                                                                        |

To change a mise-managed tool, select its key from `etc/mise.toml` and the required version:

```sh
task bump TOOL=node VERSION=26.7.0
```

`TOOL` and `VERSION` are required. The task installs the version and updates its exact pin and lock entries for the
existing platforms. Review both files together and align any duplicate CI pins.

CI uses the same tool pins and lockfile. Review the [environment action](../../.github/actions/prepare-env/action.yaml)
and affected workflows when changing setup or dependencies. Update Task definitions, CI callers, and this guide together
when commands, prerequisites, or check scopes change. Fuzz and load steps need `--exit-code` to propagate failures.

Upstream reference: [Task guide](https://taskfile.dev/docs/guide).
