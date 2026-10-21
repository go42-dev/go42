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

Complete [environment setup](05-development.md#environment-setup) before running checks.
[Taskfile.yaml](../../Taskfile.yaml) defines the commands and their outputs.

## Choose a test suite

While editing, run the matching focused checks and tests for the affected behavior. Check Go packages together, including
callers when interfaces change. Follow the shared Go conventions linked from [Project conventions](02-conventions.md).

| Command                                | Coverage and prerequisites                                                                                |
| -------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| `task test:unit`                       | Full unit suite with race detection and coverage; excludes generated code, mocks, and external test trees |
| `task test:unit -- ./internal/metrics` | Selected packages with race detection; no coverage report                                                 |
| `task test:fuzz`                       | All fuzz targets; 30 seconds per target by default                                                        |
| `task test:integration`                | Full integration suite with coverage; loads `.env` and needs configured services                          |
| `task test:resilience`                 | Recovery tests with an instrumented app and combined coverage; needs Toxiproxy and backing services       |
| `task test:load`                       | HTTP and gRPC k6 suites against a running application by default                                          |

Coverage reports are written under `.build/`. Resilience tests are build-tagged and excluded from other test tasks.
Use the [resilience suite](../../tests/resilience/application_test.go) and
[CI service configuration](../../.github/workflows/120-resilience-tests.yaml) to identify its required backends.

## Adjust fuzz and load runs

Fuzz tests accept `FUZZ_TIME` as a Go duration or iteration count. Load tests accept `K6_TEST_PATH` and `K6_SUMMARY_PATH`
together to select an existing script and its summary output. These values can be Task variables or environment variables:

```sh
task test:fuzz FUZZ_TIME=5m
task test:load K6_TEST_PATH=tests/load/http/v1/auth_test.js \
  K6_SUMMARY_PATH=.build/k6-summary-http-v1.json
```

Without load-test selectors, both protocols run, even if the first fails, and write `.build/k6-summary-{http,grpc}-v1.json`.
Each invocation removes previous summaries for its selected scope before running k6 and reports failures through its exit
status.

## Integration test environment

`task test:integration` runs the full suite; it does not start the application. Use a dedicated test application because
the [HTTP tests](../../tests/integration/http/v1/users_test.go) create, update, and delete user records.
Repository tests create isolated databases through the [test helper](../../tests/integration/helpers.go).

| Test process setting  | Required setup                                                                                                             |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| `HTTP_SERVER_ADDRESS` | Application base URL, default `http://localhost:8080`; omit the `/api/v1` suffix                                           |
| `GRPC_SERVER_ADDRESS` | Application address, default `localhost:50051`; the current client uses plaintext gRPC                                     |
| `HTTP_API_KEY`        | HTTP integration requires a key with `users:list`, `users:read_others`, `users:create`, `users:update`, and `users:delete` |
| `GRPC_API_KEY`        | Defaults to the seeded development key; override through the test process environment when needed                          |
| `DATABASE_*`          | Select the engine and test database service; MySQL/PostgreSQL credentials need create/drop database privileges             |

Start the application with its own configuration and verify [readiness](11-operations.md#run-and-verify-locally).
Keep `SERVER_GRPC_AUTHORIZATION_ENABLED=true` on the test application: the suite tests denied requests too.
The app and test runner are separate processes, so changing the runner's settings does not reconfigure the app.

Keep `.env` for application configuration. The integration task loads it for settings such as `DATABASE_*`; file values
override inherited application settings. It does not load `.env.example`. Pass test-client addresses and keys through the
process environment or CI job settings.

The gRPC integration helper defaults to the public development key from the
[seed migration](../../migrate/sqlite/20250717175120_auth_data.sql). HTTP integration requires an explicit `HTTP_API_KEY`.
The load suites use `GRPC_API_KEY` for both protocols and default to the same development key. CI supplies the keys
explicitly. Override them through the environment when the application's test credentials differ.

SQLite repository tests use temporary files and need no database service.

The [integration CI workflow](../../.github/workflows/150-integration-tests.yaml) is a complete example of application,
backend, address, and test-credential setup. It disables the application's authentication rate limiter for its test load.
Keep such overrides scoped to the test application.

## HTTP schemas and boundary checks

The Go integration suites cover authentication, authorization, credential changes, token boundaries, UUID handling, and
pagination. The [authentication harness](../../tests/integration/auth/auth_test.go) uses the real service and repository
with an isolated database. It checks that rejected and no-op updates preserve credentials and existing sessions, and that
normalized duplicate emails return `409`. These tests run in the existing SQLite, MySQL, and PostgreSQL integration matrix.

The harness validates HTTP responses against the generated OpenAPI document through its
[shared HTTP helper](../../tests/integration/auth/helpers_test.go). Validation checks response bodies, required fields,
content types, and declared status codes. Behavioral assertions still check the expected status, user identity, and
session transitions. The [HTTP client suite](../../tests/integration/http/v1/users_test.go) exercises the running application
with both generated SDKs, and the [probe checks](../../tests/integration/http/v1/auth_test.go) verify health, readiness,
and application metrics.

The [schema tests](../../api/openapi/v1/auth_test.go) reject incomplete or invalid user and token responses. They also check
conditional current-password requirements and the distinction between missing and nonempty invalid refresh tokens.
Run them without a running application:

```sh
task test:unit -- ./api/openapi/v1
```

`User` responses require a UUID, email, creation time, roles, and permissions; empty role and permission arrays are valid.
Creation time retains the existing `YYYY-MM-DD HH:MM:SS` format. Token responses require nonempty access and refresh tokens
and a nonnegative integer lifetime in whole seconds. Subsecond lifetimes round down to zero.

These checks complement `oasdiff`, which compares API compatibility between revisions. Schema validation applies to the
responses exercised by the tests; it does not generate additional requests or establish business correctness by itself.

## Choosing checks before review

* Run applicable `task test:*` commands for the changed behavior, using the suites and backends relevant to the change.
* Use `task lint` for full code and project validation, changes spanning several areas, or shared tooling and dependency
  changes. It includes Go analysis, security, licenses, capabilities, commit history, API compatibility, and source checks.
  Individual checks remain available through `task help`.
* Use `task docs:check` for documentation or website changes. It runs Markdown and prose checks, installs locked website
  dependencies, then tests the tooling, validates documents, builds the site, and checks TypeScript.
* For a focused change, select the relevant checks above. A workflow edit can use its YAML and GitHub Actions checks.
  Repeat checks after relevant edits or failures, and report material checks left unrun.

`task lint` and `task docs:check` run their checks in sequence and stop at the first failed command. Findings from NilAway,
Capslock, and OpenAPI compatibility checks are advisory in `task lint`. A failed invocation does not mean every check ran.
Neither command selects checks automatically from changed files or applies formatting fixes. History and compatibility
comparisons require `origin/master` by default.
`task lint:openapi-breaking OPENAPI_BASE_REF=REVISION` checks another base. CI supplies the pull request base or the previous
push commit and runs the same Task command. OpenAPI compatibility findings are reported for review without failing the
command or CI; no ignore file is applied. Tool errors and invalid comparison inputs still fail the command. OpenAPI schema
lint and integration tests remain blocking.

`task lint:capabilities` reports capability additions and removals against `origin/master` for review. CI uses the same
advisory task on non-master branches. Capability differences do not fail the task; setup and analysis errors still do.

For a documentation preview, use `task docs:serve`. For a focused metadata and source-link check after installing website
dependencies, use `npm --prefix pages run validate-docs` with the
[pinned tool environment](05-development.md#invoking-tools-directly). The full documentation build
also checks published links and anchors. Follow the [documentation policy](01-documentation.md#verification) for publishing
and application-specific evidence. CI runs its configured jobs independently of the local checks selected here.
