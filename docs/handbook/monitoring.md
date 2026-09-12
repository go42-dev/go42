---
id: monitoring
title: Monitoring
sidebar_position: 11
---

# Monitoring

Use this guide to collect and interpret the current application's signals. See [operations](operations.md) for process
checks and failure investigation. Alert thresholds and response ownership depend on the application's operating context
and have not yet been established.

## Collect metrics locally

Start the application using the [development workflow](development.md#running-and-debugging), then check its metrics:

```sh
curl --fail --silent --show-error --max-time 10 http://localhost:8080/metrics
```

Expect Prometheus text containing runtime and application series. Request and worker series appear when their code paths
execute; an absent series does not prove that a component is healthy or that an event count is zero.

The optional [Compose configuration](../../docker-compose.yml) includes Prometheus and Grafana. Before starting them,
edit `configs.prometheus_cfg.content` in that file: under the `service` scrape job, keep the single target address reachable
from the Prometheus container for the application's port. The supplied `host.docker.internal:8080` and `172.17.0.1:8080`
are host-access alternatives. Keep one target per process: unreachable alternatives create failed targets, and duplicate
targets double-count traffic.

With Docker available, start the monitoring services from the repository root:

```sh
docker compose up -d prometheus grafana
```

Prometheus is exposed at `http://localhost:9090`; Grafana at `http://localhost:3333`. The application runs separately.
Inspect `up{job="service"}` in Prometheus. A value of `1` means a successful scrape; it does not establish application
readiness. If you change the inline configuration while Prometheus is running, recreate that container to load it,
then check the target again:

```sh
docker compose up -d --force-recreate prometheus
```

Configure a Grafana Prometheus data source using `http://prometheus:9090` on the Compose network and import the
[dashboard JSON](../../infra/grafana/dashboard.json). Compose does not provision the data source or dashboard.
Update the dashboard's data-source references to your chosen source: the supplied JSON contains both `prometheus` and
`celxe9zgzr400e` UIDs. Use the local login configured in Compose; establish different access settings for a shared environment.

## Interpret the signals

Custom application metrics have `service`, `environment`, and `hostname` labels from
[metric initialization](../../cmd/app/main.go). Prometheus adds scrape labels such as `job` and `instance`.
Runtime collectors do not all carry the custom application labels. Scope queries to the intended targets before aggregating.

| Signal | What it measures and where to investigate |
| --- | --- |
| `application_http_responses_count` | Responses by `method`, route-pattern `path`, `status`, and `is_error`; inspect failing routes |
| `application_http_latency_sec` | Request duration in seconds; compare like routes and status codes |
| `application_grpc_responses_count`, `application_grpc_latency_sec` | Calls by full `method`, `grpc_type`, status, and numeric code |
| `errors`, `application_errors` | Centrally handled HTTP 5xx errors/panics and application/worker errors respectively; inspect `type` |
| `go_sql_in_use_connections`, `go_sql_wait_count_total` | Pool occupancy and cumulative waits; inspect dependency latency and concurrency |
| `application_startup_connection_attempts_total` | Startup connection attempts by `backend` and `result`: `success` or `failure` |
| `application_outbox_messages_total` | Publication attempts by `result`: `processed`, `retry`, or `permanently_failed` |
| `application_auth_event_subscriber_processed` | Successful history-write calls before transaction commit, including duplicate deliveries |
| `application_event_consumer_dead_letters_total` | Dead-letter attempts by topic and result; check broker and consumer logs |
| `application_outbox_cleanup_lag_seconds` | Seconds beyond retention for eligible processed rows, sampled after cleanup |

The [HTTP collector](../../internal/api/http/middleware/metrics.go) excludes health, readiness, and metrics requests.
The [gRPC collector](../../internal/api/grpc/interceptors/metrics.go) includes health and reflection traffic.
The [database observer](../../internal/metrics/observers/db.go) samples every five seconds; with SQLite, `gorm-master` and
`gorm-slave` describe the same pool and should not be summed as separate capacity.

`application_startup_connection_attempts_total` covers database, migration, cache, and event backend initialization.
It replaces `application_event_backend_connection_attempts_total`; update queries that use the previous name.
Database and migration attempts share the `mysql` or `pgsql` backend label.

Publication counters and the subscriber's processed counter are updated before their database transactions commit.
The subscriber's `event saved` debug log also precedes commit. They cannot establish persisted completion after a commit
failure. Use [outbox and history inspection](operations.md#inspect-outbox-delivery) for database evidence. Cleanup lag
concerns retention of processed rows, not delivery delay of pending rows.

## Example queries

These PromQL examples assume the local `service` job contains one target per process. Adjust selectors for a deployed
environment. Observe a window containing relevant traffic and successful scrapes before interpreting rates.

```promql
sum by (method, path, status) (
  rate(application_http_responses_count{job="service",is_error="yes"}[5m])
)
```

This counts HTTP error responses per second; it includes client errors as well as server failures. To include all business
HTTP 5xx responses observed by middleware, including directly rendered authentication failures:

```promql
sum by (method, path, status) (
  rate(application_http_responses_count{job="service",status=~"5.."}[5m])
)
```

For failures that reach the central HTTP error handler, inspect their error types:

```promql
sum by (type) (rate(errors{job="service"}[5m]))
```

For publication progress and retries:

```promql
sum by (result) (rate(application_outbox_messages_total{job="service"}[5m]))
```

Compare results with request volume, dependency availability, and the incident's time window. A quiet graph can mean
no traffic, missing series, or failed collection; inspect scrape health and raw metrics before concluding recovery.

## Dashboard limitations

The supplied dashboard's Request Latency panel shows mean duration, not percentiles. Its Errors panel queries
`application_errors`, so it omits the central HTTP handler's `errors` series. Neither error counter covers every HTTP 5xx
response: handled authentication failures can return `503` without reaching that handler. The Build Date panel expects
a `build_date` label that metric initialization does not emit; use the available build commit and tag labels instead.

No application alert rules, service objectives, or on-call routing are supplied. Define required outcomes with the
maintainer and record thresholds and response procedures together with evidence from the actual workload.
