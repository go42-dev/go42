---
id: testing
title: Testing and verification
collection: handbook
sidebar_position: 6
related:
  - development
  - conventions
---

# Testing and verification

Complete [environment setup](development.md#environment-setup) before running checks.
[Taskfile.yaml](../../Taskfile.yaml) defines the commands and their outputs.

## Choose a test suite

While editing, run the matching focused checks and tests for the affected behavior. Check Go packages together, including
callers when interfaces change. Follow the [test design conventions](conventions.md#testing).

| Command                                | Coverage and prerequisites                                                                                |
|----------------------------------------|-----------------------------------------------------------------------------------------------------------|
| `task test-unit`                       | Full unit suite with race detection and coverage; excludes generated code, mocks, and external test trees |
| `task test-unit -- ./internal/metrics` | Selected packages with race detection; no coverage report                                                 |
| `task test-fuzz`                       | All fuzz targets; 30 seconds per target by default                                                        |
| `task test-integration`                | Full integration suite with coverage; loads `.env` and needs configured services                          |
| `task test-resilience`                 | Recovery tests with an instrumented app and combined coverage; needs Toxiproxy and backing services       |
| `task test-load`                       | HTTP and gRPC k6 suites against a running application by default                                          |

Coverage reports are written under `.build/`. Resilience tests are build-tagged and excluded from other test tasks.
Use the [resilience suite](../../tests/resilience/application_test.go) and
[CI service configuration](../../.github/workflows/120-resilience-tests.yaml) to identify its required backends.

## Adjust fuzz and load runs

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

## Integration test environment

`task test-integration` runs the full suite; it does not start the application. Use a dedicated test application because
the [HTTP tests](../../tests/integration/http/v1/users_test.go) create, update, and delete user records.
Repository tests create isolated databases through the [test helper](../../tests/integration/helpers.go).

| Test process setting  | Required setup                                                                                                 |
|-----------------------|----------------------------------------------------------------------------------------------------------------|
| `HTTP_SERVER_ADDRESS` | Application base URL, default `http://localhost:8080`; omit the `/api/v1` suffix                               |
| `GRPC_SERVER_ADDRESS` | Application address, default `localhost:50051`; the current client uses plaintext gRPC                         |
| `HTTP_API_KEY`        | Explicit key with `users:list`, `users:read_others`, `users:create`, `users:update`, and `users:delete`        |
| `GRPC_API_KEY`        | Key with the same permissions; set it explicitly instead of relying on the helper's inherited test key         |
| `DATABASE_*`          | Select the engine and test database service; MySQL/PostgreSQL credentials need create/drop database privileges |

Start the application with its own configuration and verify [readiness](operations.md#run-and-verify-locally).
Keep `SERVER_GRPC_AUTHORIZATION_ENABLED=true` on the test application: the suite tests denied requests too.
The app and test runner are separate processes, so changing the runner's settings does not reconfigure the app.

Supply test settings through `.env` or the test process environment. The Task command loads `.env` when present but does
not load `.env.example`. Values loaded from `.env` override inherited environment values, including `DATABASE_*` and test
addresses or keys. Check that file when an exported override appears ineffective.

SQLite repository tests use temporary files and need no database service.

The [integration CI workflow](../../.github/workflows/150-integration-tests.yaml) is a complete example of application,
backend, address, and test-credential setup. It disables the application's authentication rate limiter for its test load.
Keep such overrides scoped to the test application.

## Choosing checks before review

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
dependencies, use `npm --prefix pages run validate-docs` with the
[pinned tool environment](development.md#bootstrapping-task-and-invoking-tools-directly). The full documentation build
also checks published links and anchors. Follow the [documentation policy](documentation.md#verification) for publishing
and application-specific evidence. CI runs its configured jobs independently of the local checks selected here.
