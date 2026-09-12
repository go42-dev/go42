---
id: operations
title: Operations
sidebar_position: 10
---

# Operations

Use this guide to check a running application, inspect its signals, diagnose failures, and recover service.
[Deployment](deployment.md) covers container and Helm configuration, upgrades, and migrations. The
[application profile](project.md#documentation-coverage) identifies the ownership and environment details that still
need to be established.

## Run and verify locally

Complete [environment setup](development.md#environment-setup), review your [configuration](configuration.md), and run:

```sh
task run
```

With the default listener, use a second terminal to check the process:

```sh
curl --fail --silent --show-error --max-time 10 --output /dev/null \
  --write-out '%{http_code}\n' http://localhost:8080/health
curl --fail --silent --show-error --max-time 10 --output /dev/null \
  --write-out '%{http_code}\n' http://localhost:8080/ready
```

Both commands should print `200` once startup completes. The default local database is in memory, so a restart loses its
data. Persistent storage settings are covered in [configuration](configuration.md#storage-and-events).

| Interface | Expected behavior |
| --- | --- |
| HTTP `/health` | `200` while live; `503` after a terminal failure when the HTTP listener remains reachable |
| HTTP `/ready` | `200` while serving with available database and cache; otherwise `503` |
| HTTP `/metrics` | Application, request, worker, and database metrics for collection |
| HTTP `/api/v1/` | Swagger UI for the bundled HTTP API when its files are available |
| gRPC health service | Readiness-based status for the empty service name; refreshed at the configured interval |

Probe behavior is implemented in the [HTTP server](../../internal/api/http/server.go),
[gRPC server](../../internal/api/grpc/server.go), and [composition root](../../cmd/app/main.go).
If the HTTP listener itself fails, probes can fail to connect instead of returning an HTTP status.
Readiness does not check broker connectivity or establish that an event reached its consumer. For a deployment check,
also exercise an authenticated application request and inspect event processing where that deployment uses it.
The [API guide](api.md#check-an-authenticated-request-locally) provides a local session check and explains response statuses.

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
or worker. Follow [monitoring](monitoring.md) to collect metrics, select useful signals, and configure the dashboard.
The development workflow describes [debugger tasks](development.md#running-and-debugging) and
[local profiling](development.md#profiling-a-local-process).

Business HTTP requests and gRPC metadata accept or generate `x-request-id`. Context-aware logs can carry `request_id`,
and active tracing adds `trace_id` and `span_id`. Capture the request ID with the route, timestamp, response status, and
component. Authentication subscriber logs use `event_id` to correlate with database records. Not every path emits a log;
health, readiness, and metrics requests skip HTTP request-ID middleware.

These fields come from the [request middleware](../../internal/api/http/middleware/request_id.go),
[gRPC interceptors](../../internal/api/grpc/interceptors/request_id.go), and [context logger](../../internal/tools/logger.go).

## Inspect outbox delivery

For delayed history or events, collect the selected backend, incident time window, publisher/subscriber errors, and event
ID when available. Use a read-only connection to the application's primary database with a query timeout suited to the
environment. A separate SQLite CLI connection cannot inspect the application's default in-memory database.

Summarize persisted state; on large tables, add a time window relevant to the incident:

```sql
select status, count(*) as message_count, min(created_at) as oldest_created_at
from transactional_outbox
group by status;
```

Inspect a bounded sample without copying event payloads or metadata:

```sql
select id, topic, status, created_at, next_attempt_at, retry_count, last_error
from transactional_outbox
where status in ('pending', 'failed')
order by created_at, id
limit 50;
```

The [publisher query](../../internal/outbox/repository/outbox.go) selects pending rows whose `next_attempt_at` is null or
due in UTC. A future timestamp explains a scheduled retry. Due rows that remain pending across polls need investigation
of publisher progress, database access, or broker availability. Capacity rejection can leave a row pending without
incrementing the publication retry counter; an unchanged counter does not rule out a blocked publisher.

For a known authentication event, replace `EVENT_UUID` with its ID and check persisted history:

```sql
select id, occurred_at, created_at, event_type
from auth_users_history
where id = 'EVENT_UUID';
```

The [history model](../../internal/auth/models/models.go) uses the event UUID as its primary key. A row confirms that
history was stored; `occurred_at` is the event time and `created_at` is the history insertion time. A processed outbox row
alone does not prove this. If history is absent, inspect subscriber errors, transaction failures, and dead-letter messages.
Account for the selected backend and [best-effort event recording](architecture.md#requests-and-persistence).
Redact sensitive values from `last_error` and logs before sharing incident evidence.

The outbox cleaner removes only processed records after retention, which defaults to seven days. Pending and failed
records are retained. The application provides no command for replaying failed outbox records. Establish a reviewed
replay procedure before changing their state or retrying messages manually.

## Failure investigation and recovery

First record the affected operation, expected and observed result, UTC time window, running revision or image digest,
relevant non-secret settings, probe results, and request/event IDs. Reproduce with the smallest request that demonstrates
the failure. Follow the owning component in the [source map](architecture.md#source-map) and select checks from the
[development workflow](development.md#testing-and-verification).

| Symptom | Initial checks |
| --- | --- |
| Process exits during startup | [Configuration, dependency errors, and paths](configuration.md#diagnose-a-configuration-problem); [migration failures](deployment.md#migrations-and-change-handoff) |
| `/health` succeeds but `/ready` fails | [Database and cache reachability; shutdown state](#run-and-verify-locally) |
| Liveness fails after serving began | [Terminal serving errors and supervisor restart behavior](#startup-and-shutdown) |
| Requests succeed but history is missing | [Outbox state, event recording, consumers, and dead letters](#inspect-outbox-delivery) |
| Data disappears after restart | [In-memory SQLite, ephemeral storage, and database paths](configuration.md#storage-and-events) |
| Shutdown times out | [Component close errors](#startup-and-shutdown) and [configured deadlines](configuration.md#startup-and-shutdown) |

Capture the running image digest, configuration context, and error evidence before changing the deployment. Repair the
identified dependency or configuration and repeat readiness and application checks. Pending outbox messages retry
automatically; consumer handling must tolerate duplicates.

Use the [migration handoff](deployment.md#migrations-and-change-handoff) to check repair or rollback compatibility.
Backups, restore commands, retention, recovery objectives, and verification of restored data still need an
environment-specific procedure. Preserve pending and failed outbox records when investigating delivery failures.
Record the chosen recovery procedure and its tested outcome in this handbook when the operating environment is established.
