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

## Golang

### Upgrading Go version

* Is done by changing the go version in go.mod and running `go mod tidy` to update dependencies.

### General

* Use cmd/cfg2env to regenerate .env.example
* Use `make generate` to regenerate code, e.g., mocks, protobufs, etc.
* When using //go:generate mockgen, always use local binary
* Use v for validation tag
* Use db for db column name tag
* WithTransaction should NOT be used in repository level
* Prefer len(string) == 0 vs string == ""
* Prefer `any` instead of `interface{}`
* Name `context.Context` -> ctx but `echo.Context` -> c
* Put technical phrases in backticks in comments to avoid linting issues
* `fmt.Errorf` vs `errors.Wrap` (collides vs std errors)
* Use power of 2 for buffer sizing, implemented using bitwise shift operator. Use package `internal/tools/buffer` for buffer sizing if possible.
* Never use anonymous interfaces
* Never use casting to anonymous interfaces
* Never define types or constants inside functions
* Never use anonymous structs
* Put DI interfaces in accessors.go with mock generation into mocks/ directory
* DI dependencies should be interfaces wherever possible, name interfaces `xxxAccessor` or if possible simple name of sub-system: `cache`, `events`.
* Panic recovery should be handled by upstream framework, e.g., echo, gin, fiber, etc. and not in the business logic layer.

### Linting

* Prefer `make lint` to manually invoking linters.
* Use linters defined in `make lint` target with corresponding configurations from etc folder if present.

### Testing

* Prefer `make test-*` to manually invoking tests.
* When implementing tests, always name test file after the file being tested, e.g., `foo_test.go` for `foo.go`.

### Observability

* Pass logger as dependency injection with component field, but can be used globally where needed
* Log fields with dash, metric labels with underscore
* Logger should be passed as option, if not passed, must default to noop logger
* log.fatal can be used only during init phase in main functions
* Use `slog.Any("error", err)` for slog errors
* Prefer xContext() version of slog methods where context is available


## SQL

* Migration files should be in migrate/{engine} directory
* Migration files should be named with a timestamp prefix and a descriptive name, e.g., `20240101_create_users_table.sql`
* Migrations should be idempotent
* Migrations should use lowercase sql keywords and snake_case for table and column names.
* Store and compare timestamps in UTC. Normalize incoming timestamps with `UTC()` at repository write boundaries, including future CLI writes. GORM clocks and database connection timezones must also use UTC.

## Miscellaneous

* Always use yaml extension, NOT yml
* Use tags @see @todo @fixme @note etc. in comments for better visibility
* Tool configuration files should be in etc directory
* Use `// ---`` comments to separate sections in code files
* Never expose IDs -> expose UUIDs
* Always leave trailing newline for text files
