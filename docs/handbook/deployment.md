---
id: deployment
title: Deployment
sidebar_position: 9
---

# Deployment

Use this guide to configure the runtime container and Helm chart, prepare application upgrades, and verify a rollout.
Select a published image through the [release workflow](release.md). The application's target environments, promotion
procedure, secret delivery, and deployment ownership remain [documented gaps](project.md#documentation-coverage).

## Runtime container

The [runtime image](../../Dockerfile) contains the application binary, migrations, and static API files. It runs as user
ID `1000`, with `tini` forwarding signals through the [entrypoint](../../entrypoint.sh). Give that user access to any
mounted database files and writable directories.

Supply configuration through the container environment. The image's `.env.example` is a reference and is not loaded
automatically. Resource paths and backend settings are documented in [configuration](configuration.md#configuration-sources).
Use persistent storage when application data must survive container replacement.

## Helm configuration

The [Helm chart](../../infra/helm/app) supplies the Deployment, HTTP and gRPC Services and Ingresses, probes, service account,
and optional SQLite storage. Review its [values](../../infra/helm/app/values.yaml) for the intended environment:

| Deployment input | Current behavior or required choice |
| --- | --- |
| Image | Set `image.repository` and `image.digest` or `image.tag`; the chart requires an identity and prefers the digest |
| Environment | Review chart `env` values; the supplied values select a development environment and debug logging |
| SQLite | Defaults to a persistent volume at `/data`, one replica, and a `Recreate` deployment strategy |
| SQLite storage | Set a suitable storage class or existing claim; disabling persistence uses an ephemeral `emptyDir` |
| Other databases | Configure external MySQL or PostgreSQL; the chart does not provision those services |
| Network | Keep Service ports aligned with `SERVER_HTTP_LISTEN` and `SERVER_GRPC_LISTEN` |
| Resources | Review CPU/memory requests, limits, and scheduling settings against the workload and cluster |
| Startup | Probe budget defaults to 15 minutes and the progress deadline to 20 minutes; align the rollout wait timeout |

The chart rejects multiple replicas with SQLite. Before changing the database or replica count, review cache and event
sharing as well as persistence. SQLite's `Recreate` strategy replaces the old pod before starting the new one; account for
the resulting interruption in the deployment plan.

The [Ingress templates](../../infra/helm/app/templates/ingress.yaml) use fixed `nginx-http` and `nginx-grpc` classes,
empty host rules, and no TLS configuration. Adapt them to the environment's routing and TLS boundary. The
[Deployment template](../../infra/helm/app/templates/deployment.yaml) renders `env` entries as literal values; it does not
provide secret references through those values. Establish the application's secret delivery mechanism before deployment.

After [tool setup](development.md#environment-setup), validate the chart from the repository root:

```sh
task lint:helm
```

This checks the supplied chart with both tag and digest image references. Also review manifests rendered with the actual
environment's values. A successful chart check does not verify image access, storage, networking, or a running cluster.

## Migrations and change handoff

The application applies forward Goose migrations during startup, using the selected engine's `migrate/` directory.
MySQL and PostgreSQL use separate migration connections with engine-specific locks: a table-based lock for MySQL and a
session lock for PostgreSQL. SQLite uses the application pool. Connection retry budgets do not bound the full migration
or lock wait. Inspect migration logs before classifying slow startup as a probe failure, and account for this work in
deployment startup deadlines.

Before handing off a change to schema or message format, record:

| Handoff detail | Evidence to provide |
| --- | --- |
| Starting and target state | Application revisions, migration files, database engine, and affected message formats |
| Compatibility | Whether old/new application versions can share the schema or consume existing messages during rollout |
| Data effects | Changed constraints, transformed/deleted data, required backfills, and preserved event IDs |
| Verification | Fresh-database and existing-data upgrade results for each supported engine; failure/restart behavior |
| Recovery | A tested rollback or forward-repair path, required backup/restore steps, and any known irreversible effects |
| Operator checks | Expected startup/probe results, a representative application request, and persisted event progress |

Keep migration naming and authoring rules in [conventions](conventions.md#architecture-and-data). Existing
[migration integration tests](../../tests/integration/database/migrate_test.go) exercise repeated runs, failures, and
concurrent runners for MySQL/PostgreSQL; [SQLite tests](../../tests/integration/database/sqlite_test.go) cover that engine.
Run the relevant engine cases with the [test environment](development.md#integration-test-environment) configured.
A passing SQLite run does not establish behavior on the other engines; MySQL DDL can commit implicitly.

Review the last attempted migration and the actual schema/data before retrying a failed upgrade. A failed process does
not establish that every database change rolled back. Use the authored migration and engine behavior to determine the
next step; retain the error and version-history evidence. For NATS format changes, also follow the
[message upgrade guidance](configuration.md#nats-message-format).

No application command currently orchestrates database downgrade or data restoration. A chart rollback or image change
does not undo schema changes or restore data. Establish the environment-specific recovery procedure before an upgrade
depends on it.

## Verify the rollout

Record the target environment, source revision, deployed image digest, configuration changes, and rollout result.
Confirm startup and migration completion in logs, then verify [liveness and readiness](operations.md#run-and-verify-locally)
at the deployed listener. Allow for the configured initialization and probe deadlines.

Exercise a representative authenticated application request with credentials appropriate to that environment. Where the
application uses events, verify [persisted delivery progress](operations.md#inspect-outbox-delivery); readiness alone does
not establish broker or consumer health. Check [monitoring](monitoring.md) for traffic, errors, and dependency signals.

If rollout fails, capture the running image, migration state, and error evidence before another change. Follow
[failure investigation and recovery](operations.md#failure-investigation-and-recovery) and the change's migration handoff
to choose a compatible repair or rollback. Record its verified outcome with the deployment.
