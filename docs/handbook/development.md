---
id: development
title: Development workflow
sidebar_position: 4
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

For optional inspection tools, `task grpcui` starts a UI for the local gRPC server and requires `grpcui`.
`task generate-dep-graph` writes the dependency graph and requires `goda` and Graphviz's `dot`. These dependencies are
described in Taskfile comments and are installed separately.

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
[Semantic Versioning](https://semver.org/) for releases.

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
    compatibility changes, rollout requirements, and rollback limitations. Link detailed operating instructions.

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
