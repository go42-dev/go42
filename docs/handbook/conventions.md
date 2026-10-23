---
id: conventions
title: Project conventions
collection: handbook
sidebar_position: 2
---

# Project conventions

These engineering rules and preferences guide changes to the application in this repository. Follow the
[development workflow](development.md) for setup, tooling, generation, verification, and contribution procedures, and
the [documentation policy](documentation.md) when updating application knowledge.
Paths are relative to the repository root. Preferences and exceptions are stated explicitly.

Shared Go conventions are maintained in [go42x's code-style guidance][go42x-codestyle].
The rules below cover application-specific choices and exceptions.

## Code conventions

* Use `v` for validation tags, as configured in the `validator` package.
* Name `echo.Context` variables `c`.
* Prefer the shared `tools.BufferSize*` constants for buffer capacities when their values fit the requirement.

## Architecture and data

* `cmd/app` composes dependencies and owns process lifecycle. Feature services own business operations. Within each
  feature, `domain/` contains service inputs, domain errors, and shared concepts; `models/` contains persistence models;
  `repository/` owns persistence operations; versioned HTTP and gRPC adapters own transport conversion and registration.
* Adapters validate pagination before invoking services. Use `tools.NormalizePagination` and the shared `tools.Pagination*`
  constants for defaults and bounds across features. Align API contracts with the
  [pagination rules](api.md#user-list-pagination).
* Services and worker handlers own transaction boundaries. Call the shared `WithTransaction` helper there and propagate
  its `txCtx` to all participating database operations. Repository data-access methods use the supplied context and
  must not manage transactions themselves.
* Use `GetTx(ctx)` for writes and reads that require primary consistency. Use `GetReadDB(ctx)` only when replica lag is
  acceptable. Persist required outbox events using the same `txCtx` as the business change, so both commit or roll back
  together. Document operations where event recording is best effort.
* Use UUIDs for entity references in public APIs and events intended for consumers outside this application. Keep numeric
  database IDs internal. Authorization must be enforced independently of identifier format.
* Keep migration files in `migrate/{engine}/`. Name them `YYYYMMDDHHMMSS_description.sql` and use the same filename for
  the corresponding migration across database engines.
* Repeated migration runs through Goose must preserve existing schema and application data. Require raw SQL replay safety
  only when a documented recovery procedure depends on it. Include `Up` and `Down` sections in the same file. Rollback must
  target only changes introduced by that migration; document any data loss or irreversible changes.
* Add a new migration for changes to an already-applied schema. Editing an old migration does not update databases that
  have recorded it as applied. Test both a fresh database and an upgrade containing existing data.
* Use lowercase SQL keywords and `snake_case` for table and column names in migrations.
* Store and compare timestamps in UTC. Normalize incoming timestamps with `UTC()` at repository write boundaries,
  including future CLI writes.

## Runtime and observability

* Global logging is allowed where needed.
* Use `snake_case` for structured log field names and metric label names.
* Use fatal logging only for initialization failures in command entrypoints.
* Report terminal HTTP/gRPC server failures through a buffered `Errors()` channel owned by each server. Close it when
  serving ends; normal shutdown must not emit an error. A terminal server failure must fail both readiness and liveness
  so Kubernetes can restart the container. Keep dependency availability checks in readiness. Run graceful shutdown when
  a termination signal is received.

[go42x-codestyle]: ../../.go42x/chunks/300-codestyle.tpl.md
