---
id: configuration
title: Configuration
sidebar_position: 4
---

# Configuration

The [configuration model](../../internal/config/config.go) defines environment variable names, types, defaults, and
validation tags. The generated [`.env.example`](../../.env.example) is the full variable reference. This guide explains
how those values reach the process and which choices affect its behavior.

## Configuration sources

`config.New()` reads the process environment, applies declared defaults, and validates the result. The application binary
does not load `.env` files itself.

| Execution method | Configuration supplied to the process |
| --- | --- |
| `task run` or `task debug` | Loads `.env.example`, then `.env`, then sets checkout paths for migrations, static files, and Swagger |
| Development Docker tasks | Loads the same files and sets paths for the checkout mounted at `/app` |
| Direct binary or runtime container | Uses its supplied environment and the configuration model's defaults |
| Helm deployment | Adds the chart's `env` values to the container environment |

Follow [environment setup](development.md#environment-setup) to create your local `.env`. Change that file for local
overrides. Supply deployed values through the deployment environment; the runtime image's example file is only a reference.

Local tasks set `DATABASE_MIGRATE_PATH`, `SERVER_HTTP_STATIC_ROOT`, and `SERVER_HTTP_SWAGGER_ROOT` after loading `.env`.
The [runtime image](../../Dockerfile) instead installs these resources at the model's default paths: `/migrate`,
`/usr/share/www`, and `/usr/share/www/api`. A directly launched binary needs paths valid for its own filesystem.

## Diagnose a configuration problem

Separate environment loading from typed parsing, validation, and dependency initialization:

| Symptom | Check |
| --- | --- |
| Parsing error before dependency startup | Match the field's type; duration `10s` has a unit, while `10` is invalid |
| `validation errors` at startup | A parsed value violates a `v` constraint; `STARTUP_CONNECT_TIMEOUT=0s` fails its positive-duration rule |
| Exported setting appears ignored | Launch tasks load `.env.example`, then `.env`, overriding inherited values for those keys |
| Edited `.env` has no effect | Running processes retain their startup configuration; restart the intended local process |
| Migration or static files are missing | Check paths inside that execution environment; local tasks override the three resource paths |
| Valid configuration but startup/readiness fails | Inspect the selected dependency, credentials, topology, and connection errors |

The local launch tasks use `grep`, `xargs`, and shell `export` to load files. Keep their `.env` values simple and on one
line; this is not a full dotenv parser and does not reliably preserve whitespace or complex quoting. For such values,
supply the environment directly to the binary or container and account for its filesystem paths.

`config.New()` validates declared fields; it does not report every unknown environment variable, so a misspelled key can
leave a default in effect. Compare the exact name with the configuration model and generated reference. Capture only
relevant non-secret settings in a diagnosis: `.env` and `Config.String()` can expose credentials.

Use [configuration tests](../../internal/config/config_test.go) to find accepted and rejected cases. The
[integration test environment](development.md#integration-test-environment) has its own process settings and precedence.

## Storage and events

| Setting | Default behavior | Available choices |
| --- | --- | --- |
| `DATABASE_ENGINE` | `sqlite`, with an in-memory database | `sqlite`, `mysql`, `pgsql` |
| `CACHE_ENGINE` | `local`, scoped to this process | `local`, `memcached`, `redis` |
| `EVENTS_ENGINE` | `gochan`, an in-process event backend | `none`, `gochan`, `nats`, `rabbitmq`, `kafka` |

With the model defaults, restarting the process loses SQLite data. For persistent SQLite, set
`DATABASE_SQLITE_MODE=rwc` and `DATABASE_SQLITE_PATH` to a file in an existing writable directory. The
[Helm defaults](../../infra/helm/app/values.yaml) already select this mode and use `/data/go42.db` on a persistent volume.
This differs from a default local run.

For MySQL or PostgreSQL, configure the selected engine's `DATABASE_MYSQL_MASTER_*` or `DATABASE_PGSQL_MASTER_*` variables.
Configure `SLAVE_*` settings when a separate read connection is needed. Review credentials, TLS settings, and database
access for the actual environment. The application applies the matching engine's [migrations](../../migrate) at startup.

External cache and broker choices also require their connection settings, such as `CACHE_REDIS_HOST`,
`CACHE_MEMCACHED_HOSTS`, `NATS_DSN`, `RABBITMQ_DSN`, or `KAFKA_BROKERS`. Provision the broker topology required by the selected
adapter, including dead-letter destinations. Automatic provisioning or topic creation is disabled by default through
`NATS_JETSTREAM_AUTO_PROVISION`, `RABBITMQ_AUTO_PROVISION`, and `KAFKA_TOPIC_AUTO_CREATE`.

RabbitMQ always uses publisher confirms, mandatory routing, and requeue after failed processing. Remove
`RABBITMQ_PUBLISH_MANDATORY` and `RABBITMQ_CONSUME_NO_REQUEUE` from deployment configuration. The application no longer
reads these settings.

`NATS_CONSUMER_BINDINGS` accepts a JSON object mapping topics to objects with `stream` and `consumer` fields. The typed
configuration field decodes it through `UnmarshalText` during `config.New()`; malformed JSON fails configuration loading
even when another event backend is selected. Application setup passes the map directly through `WithConsumerBindings`.
An empty setting uses the adapter's default binding rules.

`gochan` and the local cache are process-local. The `none` event backend discards outgoing messages while reporting
publication success, so outbox rows can become processed without creating user history. Choose a backend whose durability
and sharing behavior fits the application; see [event delivery](architecture.md#event-delivery).

### NATS message format

The NATS adapter uses Watermill's `NATSMarshaler`: the message body contains the original payload bytes, and metadata uses
native NATS headers. Outbox events therefore arrive as JSON. `_watermill_message_uuid` carries the event ID, while
`Nats-Msg-Id` combines the destination and event ID so retries deduplicate without suppressing delivery to a dead-letter
subject. Request IDs, trace context, and dead-letter details remain in the metadata headers.

This format replaces the previous Gob envelope. Coordinate upgrades of all publishers and consumers: new consumers do
not decode Gob messages. Before switching, drain or migrate any Gob messages that can still be delivered or replayed,
including dead-letter messages, preserving their event IDs and metadata. Existing SQL outbox rows need no conversion;
the publisher encodes them when sending. Automatic Gob fallback is not supported.

## Interfaces and authentication

| Setting | Default or effect |
| --- | --- |
| `SERVER_HTTP_LISTEN` | HTTP listens on `:8080` |
| `SERVER_GRPC_LISTEN` | gRPC listens on `:50051` |
| `SERVER_GRPC_REFLECTION_ENABLED` | `false`; reflection clients require an explicit change |
| `SERVER_GRPC_AUTHORIZATION_ENABLED` | `true`; business methods require authorization |
| `SERVER_GRPC_TLS_ENABLED` | `false`; enabling TLS also requires certificate and key configuration |
| `AUTH_JWT_SECRETS` | Inherited example signing secrets; set application-owned secrets for a deployed service |
| `AUTH_JWT_ACCESS_TOKEN_TTL` and `AUTH_JWT_REFRESH_TOKEN_TTL` | Access tokens last `15m`; refresh tokens last `168h` |

Set token identity and lifetime settings to match the application's clients. Review the inherited authentication seed
data in the engine-specific migrations when establishing application users and credentials. Secret delivery, rotation,
and the deployment's TLS boundary still need an application-specific operating procedure.

## Startup and shutdown

| Setting | Default | Meaning |
| --- | --- | --- |
| `STARTUP_CONNECT_TIMEOUT` | `1m` | Connection initialization budget |
| `STARTUP_RETRY_INITIAL_BACKOFF` / `STARTUP_RETRY_MAX_BACKOFF` | `500ms` / `5s` | Connection retry delays |
| `READINESS_CHECK_TIMEOUT` | `2s` | Dependency check deadline |
| `READINESS_CHECK_INTERVAL` | `5s` | gRPC health status refresh interval |
| `SHUTDOWN_GRACE_PERIOD` | `10s` | Overall shutdown budget |
| `SHUTDOWN_WAIT_FOR_PROBE` | `2s` | Wait after becoming unready |
| `SHUTDOWN_COMPONENT_TIMEOUT` | `3s` | Per-component close deadline within the overall budget |

Allow deployment probes and termination deadlines to accommodate migration, initialization, and shutdown behavior.
PostgreSQL, MySQL, their migration runners, Redis, Memcached, NATS, Kafka, and RabbitMQ share the
[startup retry policy](../../internal/tools/retry.go) for defaults, validation, backoff, connection-attempt metrics,
and retry logging. Timeout and backoff must be positive, with maximum backoff at least as large as initial backoff.
Each connection initialization has its own retry budget, bounded by the caller's context. Migration execution uses
the caller's context after connecting; the connection retry budget does not limit migration execution.
See [operations](operations.md#startup-and-shutdown) for the sequence and expected checks.

## Optional integrations

Logging defaults to JSON on standard output at `info` level. The configuration also includes optional tracing, Sentry,
profiling, Vault, and etcd integrations, all disabled by default.

The [composition root](../../cmd/app/main.go) has Vault and etcd initialization hooks, but the current configuration model
has no `vault` or `etcd` field mapping tags. Treat remote configuration as an integration to complete: define and test
the mappings, validation, and any reload behavior for the components that consume those fields.

## Changing the configuration contract

Change field names, types, defaults, and validation in the configuration model. Run `task generate` to regenerate the
example through the [configuration generator](../../cmd/cfg2env/main.go); follow the
[generation workflow](development.md#generated-files-and-dependencies) for prerequisites and other generated output.
Update this guide and deployment values when their behavior changes. Verify invalid values and the affected startup path
with the relevant application tests, then run `task docs-check` for documentation changes.
