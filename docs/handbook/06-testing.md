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
callers when interfaces change. Follow the [test design conventions](03-conventions.md#testing).

| Command                                | Coverage and prerequisites                                                                                |
|----------------------------------------|-----------------------------------------------------------------------------------------------------------|
| `task test:unit`                       | Full unit suite with race detection and coverage; excludes generated code, mocks, and external test trees |
| `task test:unit -- ./internal/metrics` | Selected packages with race detection; no coverage report                                                 |
| `task test:fuzz`                       | All fuzz targets; 30 seconds per target by default                                                        |
| `task test:integration`                | Full integration suite with coverage; loads `.env` and needs configured services                          |
| `task test:contract`                   | HTTP contract checks and probes against a dedicated test application                                      |
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

## HTTP contract testing

Run `task test:contract` against a dedicated test application. Sources live in [tests/contract](../../tests/contract).
The suite runs separately from `task test:integration` and does not load `.env`. It defaults to `http://localhost:8080`
and the seeded development API key. Override `HTTP_SERVER_ADDRESS` and `HTTP_API_KEY` through the process environment;
CI supplies both explicitly. An unset or empty key uses the development default, matching the gRPC integration and load
helpers. Disable authentication rate limiting on this test application. The command creates, changes, and deletes
disposable users and sessions. It does not start or stop the application.

The fixture API key needs all five administrative user permissions and must lack `users:read_self` and
`users:update_self`, matching the seeded development key. Preflight verifies its administrative access and both
self-service denials with requests that cannot change credentials. Ordinary user JWTs exercise administrative denials.

The pinned Schemathesis tool reads the generated [OpenAPI contract](../../api/openapi/v1/.combined.yaml).
The [configuration](../../etc/schemathesis.toml) enables successful examples and generated positive and negative requests
for all 11 business operations: signup, login, refresh, logout, current-user read/update, and administrative user
list/create/read/update/delete. The unimplemented `/dummy` tooling placeholder is excluded explicitly.

The [fixture hooks](../../tests/contract/hooks.py) supply live users, credentials, and owned mutation targets.
Positive credential fields use valid fixture values because email validation, password strength, and session state have
rules beyond the schema. Generated negative bodies and malformed UUIDs retain their invalid input. Every operation must
also complete a successful example; authentication errors alone cannot satisfy coverage. Adding an operation requires
adding its successful fixture scenario. Existing Go integration tests cover session behavior and all five gRPC user methods.

Deterministic examples cover authenticated `403` responses on all seven protected operations, duplicate-email `409`
responses on signup and all three user mutations, conditional current-password validation, successful self-update
no-ops, and refresh/logout token boundaries. Duplicate tests include uppercase and surrounding-whitespace variants;
rejected updates verify that original credentials and existing access and refresh tokens remain usable. Explicit invalid
bodies retain their input through the fixture hooks. The runner requires every planned example to complete, recording
names in `planned.txt` and `scenarios.txt` alongside the reports.

Schema tests check the conditional requirement and nullable no-op fields using Schemathesis's schema conversion.
The [Go HTTP integration harness](../../tests/integration/auth/auth_test.go) verifies indistinguishable inactive-account,
unknown-email, and incorrect-password login responses without creating sessions. It can set an inactive account directly
in its isolated database; the public HTTP API has no account-deactivation operation.

Checks validate response status, content type, headers, and schema; reject server errors; and check acceptance of valid
inputs and rejection of invalid inputs. Fixed checks verify `/health`, `/ready`, and the application's `/metrics` output
before and after the generated tests. These runtime checks complement `oasdiff`, which compares contract compatibility.

The default generated-test budget is 180 seconds, with seed 42, one worker, a ten-request-per-second CLI limit, and
three-second generated-request timeouts. Fixture requests allow ten seconds; cleanup has a sixty-second budget.
Setup and cleanup add time. A fixed seed helps reproduce generated inputs, but fixture IDs,
tokens, and the number of cases completed within the time budget vary. Change the shared configuration to adjust local
and CI runs together.

Internal Hypothesis caches live in `.build/hypothesis/` and can be deleted.

Inspect `.build/contract/schemathesis.log`, native `report.json`, `junit.xml`, and `requests.har` after a run. Reports retain
operation details, failures, and reproducing requests, with credentials redacted; replace fixture credentials when replaying.
The runner clears previous reports before a new run. It attempts cleanup after failures and timeouts. Cleanup failures
fail the task and leave remaining owned UUIDs in `.build/contract/fixtures.txt`; remove those test users before clearing
the journal and retrying. A forcibly terminated runner may need the same recovery.
Completed mutation cases delete their disposable targets immediately and remove verified deletions from the journal.
Read fixtures and any targets left by interrupted cases remain for final cleanup. This bounds the cleanup backlog as
Schemathesis repeats fuzzing within its time budget.
Cleanup uses API deletion, which soft-deletes users. Recreate the disposable database to remove retained rows and history.

The [contract CI workflow](../../.github/workflows/150-contract-tests.yaml) executes this exact Task command in a separate
SQLite, MySQL, and PostgreSQL matrix. Each job has its own application and database, using the same built image as the
integration suite. The matrices run independently after the image build; contract jobs do not install Go or collect Go
coverage. Contract failures block the final CI gate, and image cleanup waits for the contract jobs. Reports are uploaded
separately for each database even when checks fail. Contract jobs allow fifteen minutes; integration jobs allow ten.
When the project selects one database, retain the commands and reduce both matrices to the selected backend.

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
lint and runtime contract tests remain blocking.

`task lint:capabilities` reports capability additions and removals against `origin/master` for review. CI uses the same
advisory task on non-master branches. Capability differences do not fail the task; setup and analysis errors still do.

For a documentation preview, use `task docs:serve`. For a focused metadata and source-link check after installing website
dependencies, use `npm --prefix pages run validate-docs` with the
[pinned tool environment](05-development.md#bootstrapping-task-and-invoking-tools-directly). The full documentation build
also checks published links and anchors. Follow the [documentation policy](01-documentation.md#verification) for publishing
and application-specific evidence. CI runs its configured jobs independently of the local checks selected here.
