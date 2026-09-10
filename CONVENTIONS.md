##### go42 conventions

This file outlines conventions for the go42 project.

**Core**

* Semver: https://semver.org/
* Conventional Commits: https://www.conventionalcommits.org/en/v1.0.0/
* Google Engineering Practices: https://google.github.io/eng-practices/
* Google Go Style Guide: https://google.github.io/styleguide/go/decisions.html
* Google SRE Book: https://sre.google/sre-book/table-of-contents/

## Review

## Project Management

* Declare Go's version in `go.mod` and development-tool versions in `etc/mise.toml`. Update tool pins and corresponding
  `etc/mise.lock` entries together, preserving supported platform coverage. Install tools through the Make setup targets
  and keep any duplicate CI pins aligned.
* Record vendored dependencies' versions and upstream sources in adjacent `.versions.yaml` files. Update these records
  together with the vendored files, retain upstream license notices, and regenerate affected outputs.

## VCS

* Use Conventional Commits with the types and rules configured in `etc/.commitlintrc.yaml`.
* Branch names must match `^[A-Za-z0-9/_.-]+$`, be descriptive, and include task code identifier if applicable.
* Always prefer merge commits to rebase (disable rebase).
* Use .gitkeep to preserve empty directories in git.

## CI/CD

* Use `gh` client to access github resources.
* Any destructive operation should require user confirmation in interactive mode, or not be allowed in non-interactive mode unless explicitly mentioned by the user.

## General

* Use tools provided by mise (.tools).
* Use `cmd/cfg2env` to regenerate .env.example
* Edit source definitions or generator configuration, then regenerate derived files with `make generate` using the pinned
  tools. Commit source changes and corresponding tracked generated outputs together. After setup, running `make generate`
  from a clean checkout must produce no changes.
* Use `v` for validation tag as configured in `validator` package.
* Prefer `len(string)` == 0 vs `string == ""`.
* Prefer `any` instead of `interface{}`.
* Name `context.Context` -> `ctx` but `echo.Context` -> `c`.
* For general error wrapping in handwritten Go, use `fmt.Errorf` with `%w`. Inspect errors using the standard `errors`
  package, matching identity or type rather than message text. Generated code follows its generator's conventions.
* Prefer the shared `tools.BufferSize*` constants for buffer capacities when their values fit the requirement.
* Never use anonymous interfaces unless in tests.
* Never use casting to anonymous interfaces unless in tests.
* Never define types or constants inside functions unless in tests.
* Never use anonymous structs unless in tests.

## Code Architecture

* `cmd/app` composes dependencies and owns process lifecycle. Feature services own business operations. Within each
  feature, `domain/` contains service inputs, domain errors, and shared concepts; `models/` contains persistence models;
  `repository/` owns persistence operations; versioned HTTP and gRPC adapters own transport conversion and registration.
* Define DI interfaces in the consuming package, containing only the methods it needs. Keep them beside their consumer
  or group them in `accessors.go`. Keep mock-generation directives with the interface definitions and generate mocks
  into the package's `mocks/` directory.
* DI dependencies should be interfaces wherever possible, name interfaces `xxxAccessor` or if possible simple name of sub-system: `cache`, `events`.
* Services and worker handlers own transaction boundaries. Call the shared `WithTransaction` helper there and propagate
  its `txCtx` to all participating database operations. Repository data-access methods use the supplied context and
  must not manage transactions themselves.
* Use `GetTx(ctx)` for writes and reads that require primary consistency. Use `GetReadDB(ctx)` only when replica lag is
  acceptable. Persist required outbox events using the same `txCtx` as the business change, so both commit or roll back
  together. Document operations where event recording is best effort.
* Give background goroutines and resources explicit owners and shutdown paths. Use request contexts for request work and
  component contexts for workers. Bound external calls and retry waits. Shutdown must wait for owned work and release
  resources, reporting incomplete cleanup if its deadline expires. Clean up acquired resources when initialization fails.
  Keep concurrency limits in force until underlying work finishes, even if the caller has timed out.

## Linting

* Prefer `make lint` to manually invoking linters.
* Use linters defined in `make lint` target with corresponding configurations from etc folder if present.

## Testing

* Test observable behavior and failure paths. Keep tests requiring external services in `tests/integration` or
  `tests/resilience`, with isolated resources, cleanup, and bounded waits. Prefer synchronization or `testing/synctest` over
  fixed sleeps for in-process concurrency. Run focused checks during development and applicable `make test-*` targets
  before review. Report the commands, suites, and backends tested, including relevant skips.
* Keep unit tests for `foo.go` together in `foo_test.go` in the same directory, regardless of suite size. Do not split them
  into separate files by behavior. Use descriptive names for package-wide, integration, and fuzz tests.

## Observability

* Pass logger as dependency injection with component field, but can be used globally where needed.
* Use `snake_case` for structured log field names and metric label names.
* Logger should be passed as option, if not passed, must default to noop logger.
* Use fatal logging only for initialization failures in command entrypoints. Report terminal HTTP/gRPC server failures
  through a buffered `Errors()` channel owned by each server. Close it when serving ends; normal shutdown must not emit
  an error. Fail both readiness and liveness so Kubernetes can restart the container. Keep dependency availability checks
  in readiness. Run graceful shutdown when a termination signal is received.
* Use `slog.Any("error", err)` for slog errors.
* Prefer `xContext()` version of slog methods where context is available.

## SQL

* Migration files should be in migrate/{engine} directory.
* Name migrations `YYYYMMDDHHMMSS_description.sql`. Generate the prefix once with `make generate-migration-id` and use the
  same filename for the corresponding migration across database engines.
* Repeated migration runs through Goose must preserve existing schema and application data. Require raw SQL replay safety
  only when a documented recovery procedure depends on it. Include `Up` and `Down` sections in the same file. Rollback must
  target only changes introduced by that migration; document any data loss or irreversible changes.
* Migrations should use lowercase sql keywords and snake_case for table and column names.
* Store and compare timestamps in UTC. Normalize incoming timestamps with `UTC()` at repository write boundaries, including future CLI writes.

## Miscellaneous

* Always use `yaml` extension, NOT `yml` where possible.
* Use tags `@see` `@todo` `@fixme` `@note` etc. in comments for better visibility.
* Tool configuration files should be in etc directory.
* Use `// ---` comments to separate sections in code files.
* Use UUIDs for entity references in public APIs and events intended for consumers outside this application. Keep numeric
  database IDs internal. Authorization must be enforced independently of identifier format.
* Always leave trailing newline for text files.
