---
id: operations
title: Operations
sidebar_position: 7
---

# Operations

This guide describes the operating behavior available in this checkout. It covers local checks and the supplied
container and Helm configuration. The [application profile](project.md#documentation-coverage) identifies the ownership
and environment details that still need to be established.

## Run and verify locally

Complete [environment setup](development.md#environment-setup), review your [configuration](configuration.md), and run:

```sh
task run
```

With the default listener, use a second terminal to check the process:

```sh
curl --fail --silent --show-error --output /dev/null --write-out '%{http_code}\n' http://localhost:8080/health
curl --fail --silent --show-error --output /dev/null --write-out '%{http_code}\n' http://localhost:8080/ready
```

Both commands should print `200` once startup completes. The default local database is in memory, so a restart loses its
data. Persistent storage settings are covered in [configuration](configuration.md#storage-and-events).

| Interface | Expected behavior |
| --- | --- |
| HTTP `/health` | `200` while live; `503` after a terminal serving or event router failure |
| HTTP `/ready` | `200` while serving with available database and cache; otherwise `503` |
| HTTP `/metrics` | Application, request, worker, and database metrics for collection |
| HTTP `/api/v1/` | Swagger UI for the bundled HTTP API when its files are available |
| gRPC health service | Readiness-based status for the empty service name; refreshed at the configured interval |

Probe behavior is implemented in the [HTTP server](../../internal/api/http/server.go),
[gRPC server](../../internal/api/grpc/server.go), and [composition root](../../cmd/app/main.go).
Readiness does not check broker connectivity or establish that an event reached its consumer. For a deployment check,
also exercise an authenticated application request and inspect event processing where that deployment uses it.

## Startup and shutdown

Startup connects dependencies and applies database migrations before opening the serving interfaces. During that period,
a connection failure at the probe address can mean startup is still in progress; use startup logs to distinguish progress
from an initialization failure. Connection retries and timeouts are bounded by the
[startup settings](configuration.md#startup-and-shutdown).

`SIGINT`, `SIGTERM`, or `SIGHUP` begins shutdown: cancel the application context, become unready, wait for probe propagation,
and close components within the configured deadlines. Inspect logs for completion or a shutdown timeout. Give the process
enough time to finish before its supervisor forces termination.

A terminal HTTP, gRPC, or event router failure marks the process unhealthy. It does not initiate shutdown by itself;
the supervisor must restart it, delivering a termination signal for graceful cleanup. Dependency unavailability affects
readiness while liveness can remain healthy.

## Logs, metrics, and event processing

JSON logs go to standard output by default. Use the `component` field and error context to locate the failing dependency
or worker. Metrics are exposed through the [metrics adapter](../../internal/metrics/adapters/http/adapter.go).
The [development workflow](development.md) describes local profiling and debugging tools.

For delayed user history or events, inspect the outbox publisher and subscriber logs together. A pending outbox record
can be waiting for its next retry; a failed record needs investigation; a processed record establishes publication but
does not establish consumer completion. Check broker connectivity and topology, consumer errors, and dead-letter messages.
The [delivery model](architecture.md#event-delivery) explains duplicates and the limits of the default in-process backend.

The outbox cleaner removes only processed records after retention, which defaults to seven days. Pending and failed
records are retained. The application provides no command for replaying failed outbox records. Establish a reviewed
replay procedure before changing their state or retrying messages manually.

## Container and Helm deployment

The [runtime image](../../Dockerfile) contains the binary, migrations, and static API files and runs as user ID `1000`.
It receives configuration through the container environment. The [Helm chart](../../infra/helm/app) supplies the
Deployment, Service, probes, and optional SQLite storage.

| Deployment input | Current behavior or required choice |
| --- | --- |
| Image | Set `image.digest` or `image.tag`; the chart requires one and prefers the digest when both are set |
| Environment | Review chart `env` values; the supplied values select a development environment and debug logging |
| SQLite | Defaults to a persistent volume at `/data`, one replica, and a `Recreate` deployment strategy |
| SQLite storage | Set a suitable storage class or existing claim; disabling persistence uses an ephemeral `emptyDir` |
| Other databases | Configure external MySQL or PostgreSQL; the chart does not provision those services |
| Network | Keep Service ports aligned with `SERVER_HTTP_LISTEN` and `SERVER_GRPC_LISTEN` |
| Startup | Probe budget defaults to 15 minutes; accommodate migrations and align the rollout wait timeout |

The chart rejects multiple replicas with SQLite. Before changing the database or replica count, review cache and event
sharing as well as persistence. Validate chart rendering with `task lint:helm`; a successful render does not verify a
deployment against a cluster.

The [release workflow](../../.github/workflows/300-release.yaml) produces versioned images and records the immutable image
digest in release information. It does not deploy the application. Record the application's target environment, image
promotion procedure, secret delivery, network exposure, and operator ownership before relying on this chart for that
environment.

## Failure investigation and recovery

| Symptom | Initial checks |
| --- | --- |
| Process exits during startup | Configuration validation, dependency connection errors, migration errors, writable paths |
| `/health` succeeds but `/ready` fails | Database and cache reachability; shutdown state |
| Liveness fails after serving began | Terminal HTTP, gRPC, or event router errors; supervisor restart behavior |
| Requests succeed but history is missing | Best-effort event recording, outbox state, selected backend, consumers, dead letters |
| Data disappears after restart | In-memory SQLite, ephemeral volumes, or the wrong database path |
| Shutdown times out | Component close errors and the configured application and supervisor deadlines |

Capture the running image digest, configuration context, and error evidence before changing the deployment. Repair the
identified dependency or configuration and repeat readiness and application checks. Pending outbox messages retry
automatically; consumer handling must tolerate duplicates.

Startup applies forward migrations. Rolling back an image does not undo schema changes or restore data. Backups, restore
commands, compatibility checks, retention, recovery objectives, and verification of restored data still need an
environment-specific procedure. Preserve pending and failed outbox records when investigating delivery failures.
Record the chosen recovery procedure and its tested outcome in this handbook when the operating environment is established.
