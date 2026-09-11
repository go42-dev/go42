---
id: conventions
title: Project conventions
sidebar_position: 2
---

# Project conventions

These rules and preferences guide contributors and AI agents when changing the application in this repository.
Paths and commands are relative to the repository root. Preferences and exceptions are stated explicitly.

## Contribution workflow

* Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/) with the types and rules configured in
  `etc/.commitlintrc.yaml`.
* Branch names must match `^[A-Za-z0-9/_.-]+$`, be descriptive, and include a task identifier when applicable.
* Prefer merge commits over rebasing, and keep rebase disabled.
* Use `.gitkeep` to preserve empty directories in Git.
* Use the `gh` client to access GitHub resources.
* Destructive operations require user confirmation in interactive mode. In non-interactive mode, allow them only when
  explicitly requested by the user.
* Follow the [documentation policy](documentation.md) and update affected documentation alongside implementation changes.
* Use the [pull request template](../../.github/pull_request_template.md) for PRs created through the web interface or tools.
  Keep the description proportional to the change. Report checks and results as described under
  [Testing and verification](#testing-and-verification).
* Describe the problem and resulting behavior, with a brief before/after example when useful. Keep the title and
  description aligned with the final change.
* Link relevant issues, requirements, decisions, and project documentation. Keep durable rationale and operating
  instructions in the project documentation.
* Add a `Deployment` section when adopting the change requires action. Describe relevant migrations, configuration or
  compatibility changes, rollout requirements, and rollback limitations. Link detailed operating instructions.
* Use [Semantic Versioning](https://semver.org/) for releases.

## Tools and generated files

* Use the tools managed by mise in `.tools/`, installed through the Make setup targets.
* Declare Go's version in `go.mod` and development-tool versions in `etc/mise.toml`. Update tool pins and corresponding
  `etc/mise.lock` entries together, preserving supported platform coverage. Keep any duplicate CI pins aligned.
* Keep tool configuration files in `etc/`.
* Record vendored dependencies' versions and upstream sources in adjacent `.versions.yaml` files. Update these records
  together with the vendored files, retain upstream license notices, and regenerate affected outputs.
* Edit source definitions or generator configuration, then regenerate derived files with `make generate`. This includes
  regenerating `.env.example` through `cmd/cfg2env`. Commit source changes and corresponding tracked generated outputs
  together. After setup, running `make generate` from a clean checkout must produce no changes.

## Code conventions

* Use `v` for validation tags, as configured in the `validator` package.
* Prefer `len(s) == 0` to `s == ""` for empty strings.
* Prefer `any` instead of `interface{}`.
* Name `context.Context` variables `ctx` and `echo.Context` variables `c`.
* For general error wrapping in handwritten Go, use `fmt.Errorf` with `%w`. Inspect errors using the standard `errors`
  package, matching identity or type rather than message text. Generated code follows its generator's conventions.
* Prefer the shared `tools.BufferSize*` constants for buffer capacities when their values fit the requirement.
* Use named structs and interfaces. Anonymous structs and interfaces, including type assertions to anonymous interfaces,
  are allowed only in tests.
* Define types and constants at package scope. Definitions inside functions are allowed only in tests.
* Prefer the `.yaml` extension over `.yml` wherever possible.
* Use comment tags such as `@see`, `@todo`, `@fixme`, and `@note` for visibility. Use `// ---` comments to separate sections
  in code files.
* End text files with a newline.

## Architecture and data

* `cmd/app` composes dependencies and owns process lifecycle. Feature services own business operations. Within each
  feature, `domain/` contains service inputs, domain errors, and shared concepts; `models/` contains persistence models;
  `repository/` owns persistence operations; versioned HTTP and gRPC adapters own transport conversion and registration.
* Define dependency interfaces in the consuming package, containing only the methods it needs. Keep them beside their
  consumer or group them in `accessors.go`. Keep mock-generation directives with the interface definitions and generate
  mocks into the package's `mocks/` directory.
* Prefer interfaces for injected dependencies. Name them `xxxAccessor`, or use a simple subsystem name such as `cache`
  or `events` when possible.
* Services and worker handlers own transaction boundaries. Call the shared `WithTransaction` helper there and propagate
  its `txCtx` to all participating database operations. Repository data-access methods use the supplied context and
  must not manage transactions themselves.
* Use `GetTx(ctx)` for writes and reads that require primary consistency. Use `GetReadDB(ctx)` only when replica lag is
  acceptable. Persist required outbox events using the same `txCtx` as the business change, so both commit or roll back
  together. Document operations where event recording is best effort.
* Use UUIDs for entity references in public APIs and events intended for consumers outside this application. Keep numeric
  database IDs internal. Authorization must be enforced independently of identifier format.
* Keep migration files in `migrate/{engine}/`. Name them `YYYYMMDDHHMMSS_description.sql`. Generate the prefix once with
  `make generate-migration-id` and use the same filename for the corresponding migration across database engines.
* Repeated migration runs through Goose must preserve existing schema and application data. Require raw SQL replay safety
  only when a documented recovery procedure depends on it. Include `Up` and `Down` sections in the same file. Rollback must
  target only changes introduced by that migration; document any data loss or irreversible changes.
* Use lowercase SQL keywords and `snake_case` for table and column names in migrations.
* Store and compare timestamps in UTC. Normalize incoming timestamps with `UTC()` at repository write boundaries,
  including future CLI writes.

## Runtime and observability

* Give background goroutines and resources explicit owners and shutdown paths. Use request contexts for request work and
  component contexts for workers. Bound external calls and retry waits. Shutdown must wait for owned work and release
  resources, reporting incomplete cleanup if its deadline expires. Clean up acquired resources when initialization fails.
  Keep concurrency limits in force until underlying work finishes, even if the caller has timed out.
* Pass loggers as options, using dependency injection and a `component` field. Default to a no-op logger when none is
  supplied. Global logging is allowed where needed.
* Use `snake_case` for structured log field names and metric label names.
* Use fatal logging only for initialization failures in command entrypoints.
* Report terminal HTTP/gRPC server failures through a buffered `Errors()` channel owned by each server. Close it when
  serving ends; normal shutdown must not emit an error. A terminal server failure must fail both readiness and liveness
  so Kubernetes can restart the container. Keep dependency availability checks in readiness. Run graceful shutdown when
  a termination signal is received.
* Use `slog.Any("error", err)` for logged errors. Prefer context-aware slog methods, such as `InfoContext`, when a context
  is available.

## Testing and verification

* Prefer `make lint` for code and project files and `make docs-check` for documentation. Use the linters and configurations
  defined by these targets.
* Test observable behavior and failure paths.
* Keep unit tests for `foo.go` together in `foo_test.go` in the same directory, regardless of suite size. Do not split them
  into separate files by behavior. Use descriptive names for package-wide, integration, and fuzz tests.
* Keep tests requiring external services in `tests/integration` or `tests/resilience`, with isolated resources, cleanup,
  and bounded waits. Prefer synchronization or `testing/synctest` over fixed sleeps for in-process concurrency.
* Run focused checks during development and applicable `make test-*` targets before review.
* In the pull request's `Verification` section, report checks actually performed and their results, including commands,
  suites, backends, and relevant manual checks. Distinguish completed checks from planned checks, identify anything left
  unverified, and explain material skips.
