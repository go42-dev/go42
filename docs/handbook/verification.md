---
id: testing
title: Verification
collection: handbook
sidebar_position: 6
related:
  - development
  - conventions
---

# Verification

Follow the formatting, linting, and testing rules in this guide.
[Taskfile.yaml](../../Taskfile.yaml) defines the commands, arguments, defaults,
and outputs; follow its declarations when running checks.

## Formatting

Format changed source files before linting and review the diff. Pass explicit
existing file paths after `--`; formatters do not accept directories or globs.
Replace `FILE` in the commands below with the affected file paths.

| Command                                         | When to run                   | Purpose                                  |
| ----------------------------------------------- | ----------------------------- | ---------------------------------------- |
| `task fmt:go -- FILE.go ...`                    | Go files changed              | Apply the configured Go formatters       |
| `task fmt:yaml -- FILE.yaml ...`                | Plain YAML files changed      | Apply YAML formatting                    |
| `task fmt:json -- FILE.json ...`                | JSON files changed            | Apply JSON formatting                    |
| `task fmt:toml -- FILE.toml ...`                | TOML files changed            | Apply TOML formatting                    |
| `task fmt:markdown -- FILE.md ...`              | Authored Markdown changed     | Fix supported Markdown formatting issues |
| `task fmt:proto -- FILE.proto ...`              | Protobuf source files changed | Apply Protobuf formatting                |
| `task fmt:sql DIALECT=sqlite -- FILE.sql ...`   | SQLite migrations changed     | Apply SQLite formatting                  |
| `task fmt:sql DIALECT=mysql -- FILE.sql ...`    | MySQL migrations changed      | Apply MySQL formatting                   |
| `task fmt:sql DIALECT=postgres -- FILE.sql ...` | PostgreSQL migrations changed | Apply PostgreSQL formatting              |

Pass only plain YAML to the YAML formatter; preserve Helm template syntax.

Run `task fmt:markdown` on changed Markdown files and correct any remaining diagnostics.

### Generated outputs

Regenerate affected outputs through their owning tasks. Review the generated
diff before running checks; do not edit generated files to satisfy formatting.
Use `task generate` when both generation tasks below apply.

| Command                 | When to run                                                                                             | Purpose                                                                          |
| ----------------------- | ------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| `task generate:code`    | API definitions, generator directives, mocked interfaces, configuration schema, or dependencies changed | Refresh generated code, configuration examples, and combined OpenAPI definitions |
| `task generate:agentic` | Agent templates, configuration, or workflow sources changed                                             | Refresh shared instructions, MCP definitions, and workflow locks                 |

## Linting

Use focused checks while editing. Before handing back repository changes, run
`task lint`, which includes all configured linters and `task docs:check`.
The individual tasks below provide focused feedback; their selection does not
replace the full lint run before review.

| Command                              | When to run                                                                               | Purpose                                                         |
| ------------------------------------ | ----------------------------------------------------------------------------------------- | --------------------------------------------------------------- |
| `task lint`                          | Before handing back any repository changes                                                | Run the complete project lint and documentation checks          |
| `task lint:go`                       | While editing Go source                                                                   | Run configured Go static analysis                               |
| `task lint:nilaway`                  | While editing Go source                                                                   | Check for potential nil panics                                  |
| `task lint:gosec`                    | While editing Go source                                                                   | Check Go security patterns                                      |
| `task lint:vulnerabilities`          | While changing Go dependencies or release targets                                         | Check reachable vulnerabilities on configured release platforms |
| `task lint:secrets`                  | While changing committed configuration or credential handling                             | Scan Git history for exposed secrets                            |
| `task lint:secrets -- FILE ...`      | Generated or untracked files may contain credentials; always after regenerating AGENTS.md | Scan explicit files that the Git-history scan cannot see        |
| `task lint:json -- FILE.json ...`    | While editing JSON source                                                                 | Check JSON formatting                                           |
| `task lint:yaml -- FILE.yaml ...`    | While editing plain YAML source                                                           | Check YAML formatting                                           |
| `task lint:toml -- FILE.toml ...`    | While editing TOML source                                                                 | Check TOML formatting and rules                                 |
| `task lint:docker`                   | While editing the Dockerfile                                                              | Check Dockerfile instructions with Hadolint                     |
| `task lint:actions`                  | While editing workflows or composite actions                                              | Check GitHub Actions syntax and security                        |
| `task lint:agentic`                  | After recompiling agentic workflows                                                       | Check compiled workflow locks with native gh-aw rules           |
| `task lint:editorconfig -- FILE ...` | While editing source files                                                                | Check the project EditorConfig rules                            |
| `task lint:markdown -- FILE.md ...`  | While editing authored Markdown                                                           | Check Markdown formatting                                       |
| `task lint:prose -- FILE.md ...`     | While editing authored Markdown                                                           | Check prose rules                                               |
| `task docs:check`                    | While editing documentation; also included in full lint                                   | Run the full project documentation check                        |
| `task lint:commits`                  | While preparing commits for review                                                        | Check commit messages against origin/master                     |
| `task lint:licenses`                 | While changing Go dependencies                                                            | Check dependency licenses, including tests                      |
| `task lint:capabilities`             | While changing Go dependencies or package behavior                                        | Compare dependency capabilities against origin/master           |
| `task lint:sql`                      | While editing SQL migrations                                                              | Check all configured SQL dialects                               |
| `task lint:helm`                     | While editing the Helm chart                                                              | Check tag and digest image references                           |
| `task lint:openapi`                  | While editing OpenAPI definitions                                                         | Validate source API contracts                                   |
| `task lint:openapi-breaking`         | After regenerating changed OpenAPI definitions                                            | Compare API compatibility with the selected base                |
| `task lint:proto`                    | While editing Protobuf definitions                                                        | Build, lint, and check formatting of the entire API module      |
| `task lint:proto-breaking`           | While editing Protobuf definitions                                                        | Check compatibility against origin/master                       |

### Results and review

An aggregate stops at the first failed command. A failed invocation does not
confirm that later checks ran. Fix failures introduced by the changes and rerun
affected checks. Review warnings and advisory findings as well as exit codes;
a skipped test or analyzer crash leaves a gap in verification.

Review the final diff and run `git diff --check` before handing back changes.
Report passed linters, applied formatters, executed tests, their results, and
required checks left unrun, with the reasons.

## Testing

### Test suites

Run every suite whose trigger matches the change. Include affected callers
when interfaces change, and use the full unit suite for changes shared across
packages. Comment and formatting changes do not require behavior tests.
Replace `PACKAGE` with local package paths after `--`.

| Command                         | When to run                                                                             | Coverage                                                     |
| ------------------------------- | --------------------------------------------------------------------------------------- | ------------------------------------------------------------ |
| `task test:unit -- PACKAGE ...` | Go behavior or interfaces changed; include affected callers                             | Selected packages with race detection                        |
| `task test:unit`                | Shared wiring, dependencies, or the toolchain changed                                   | Full unit suite with race detection and coverage             |
| `task test:integration`         | API behavior, authentication, repositories, migrations, or backend interactions changed | Application, transport, and database boundaries              |
| `task test:fuzz`                | Fuzzed parsing, validation, or boundary handling changed                                | Inputs beyond fixed test cases                               |
| `task test:resilience`          | Dependency recovery, retries, reconnection, or shutdown behavior changed                | Recovery with the configured backing services and Toxiproxy  |
| `task test:load`                | Request throughput, latency, concurrency, or resource usage may change                  | HTTP and gRPC k6 suites against an isolated test application |

### Suite options

Fuzz tests accept `FUZZ_TIME` as a Go duration or iteration count. Load tests accept
`K6_TEST_PATH` and `K6_SUMMARY_PATH` together to select an existing script and its
summary output. These values can be Task variables or environment variables:

```sh
task test:fuzz FUZZ_TIME=5m
task test:load K6_TEST_PATH=tests/load/http/v1/auth_test.js \
  K6_SUMMARY_PATH=.build/k6-summary-http-v1.json
```

Without load-test selectors, both protocols run, even if the first fails, and
write `.build/k6-summary-{http,grpc}-v1.json`. Each invocation removes previous
summaries for its selected scope before running k6 and reports failures through
its exit status.

Coverage reports are written under `.build/`. Explicit unit packages skip
coverage reports. Resilience tests are build-tagged and excluded from the other
test tasks. Test application and backend configuration belongs in the
[development guide](development.md#integration-test-environment).

### Build and artifact checks

Run the matching build when build settings or packaged tools change. Build
artifacts before running tests that consume them.

| Command      | When to run                                                    | Purpose                              |
| ------------ | -------------------------------------------------------------- | ------------------------------------ |
| `task build` | Application entry points or Go build settings changed          | Verify the development binary builds |
| `task image` | The Dockerfile, runtime tools, or image build settings changed | Verify the container image builds    |
