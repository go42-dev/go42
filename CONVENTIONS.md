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

* tooling versions
* .versions.yaml
* release process

## SVC

* branch naming
* commit message
* pull request names and description
* tag naming
* sub-module tags
* always prefer merge commits to rebase (disable rebase)
* .gitignore -> current dir / .gitkeep

## CI/CD

* Use `gh` client to access github resources.
* Any destructive operation should require user confirmation in interactive mode, or not be allowed in non-interactive mode unless explicitly mentioned by the user.

## General

* Use tools provided by mise (.tools).
* Use `cmd/cfg2env` to regenerate .env.example
* Use `make generate` to regenerate code, e.g., mocks, protobufs, etc.
* Use `v` for validation tag as configured in `validator` package.
* Prefer `len(string)` == 0 vs `string == ""`.
* Prefer `any` instead of `interface{}`.
* Name `context.Context` -> `ctx` but `echo.Context` -> `c`.
* Put technical phrases in backticks in comments to avoid linting issues
* For general error wrapping in handwritten Go, use `fmt.Errorf` with `%w`. Inspect errors using the standard `errors`
  package, matching identity or type rather than message text. Generated code follows its generator's conventions.
* Prefer the shared `tools.BufferSize*` constants for buffer capacities when their values fit the requirement.
* Never use anonymous interfaces unless in tests.
* Never use casting to anonymous interfaces unless in tests.
* Never define types or constants inside functions unless in tests.
* Never use anonymous structs unless in tests.

## Code Architecture

* Define DI interfaces in the consuming package, containing only the methods it needs. Keep them beside their consumer
  or group them in `accessors.go`. Keep mock-generation directives with the interface definitions and generate mocks
  into the package's `mocks/` directory.
* DI dependencies should be interfaces wherever possible, name interfaces `xxxAccessor` or if possible simple name of sub-system: `cache`, `events`.
* Services and worker handlers own transaction boundaries. Call the shared `WithTransaction` helper there and propagate
  its `txCtx` to all participating database operations. Repository data-access methods use the supplied context and
  must not manage transactions themselves.

## Linting

* Prefer `make lint` to manually invoking linters.
* Use linters defined in `make lint` target with corresponding configurations from etc folder if present.

## Testing

* Prefer `make test-*` to manually invoking tests.
* Prefer `foo_test.go` for tests focused on `foo.go`. Split larger suites into `foo_<behavior>_test.go` when useful.
  Use descriptive names for package-wide, integration, and fuzz tests.

## Observability

* Pass logger as dependency injection with component field, but can be used globally where needed.
* Use `snake_case` for structured log field names and metric label names.
* Logger should be passed as option, if not passed, must default to noop logger.
* `log.fatal` can be used only during init phase in main functions.
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
* Never expose IDs -> expose UUIDs.
* Always leave trailing newline for text files.
