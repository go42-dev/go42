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
* `fmt.Errorf` vs `errors.Wrap` (collides vs std errors)
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
* When implementing tests, always name test file after the file being tested, e.g., `foo_test.go` for `foo.go`.

## Observability

* Pass logger as dependency injection with component field, but can be used globally where needed.
* Use `snake_case` for structured log field names and metric label names.
* Logger should be passed as option, if not passed, must default to noop logger.
* `log.fatal` can be used only during init phase in main functions.
* Use `slog.Any("error", err)` for slog errors.
* Prefer `xContext()` version of slog methods where context is available.

## SQL

* Migration files should be in migrate/{engine} directory.
* Migration files should be named with a timestamp prefix and a descriptive name, e.g., `20240101_create_users_table.sql`.
* Migrations should be idempotent and reversible, with both up and down scripts included in the same file.
* Migrations should use lowercase sql keywords and snake_case for table and column names.
* Store and compare timestamps in UTC. Normalize incoming timestamps with `UTC()` at repository write boundaries, including future CLI writes.

## Miscellaneous

* Always use `yaml` extension, NOT `yml` where possible.
* Use tags `@see` `@todo` `@fixme` `@note` etc. in comments for better visibility.
* Tool configuration files should be in etc directory.
* Use `// ---` comments to separate sections in code files.
* Never expose IDs -> expose UUIDs.
* Always leave trailing newline for text files.
