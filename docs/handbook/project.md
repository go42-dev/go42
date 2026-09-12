---
id: project
title: Application profile
sidebar_position: 1
---

# Application profile

## Purpose and ownership

This checkout currently contains the go42 Go service scaffold. An application-specific purpose, intended users, and
operational owner have not been recorded.

## Origin

This repository is the upstream [go42 blueprint](https://github.com/go42-dev/go42). An imported revision is not applicable.

For creating a project from the blueprint, see the
[Go42 adoption guide](https://github.com/go42-dev/go42-docs/blob/master/guides/adopting-go42.md).

## Documentation coverage

This initial handbook covers the documentation process, engineering conventions, and the current
[development workflow](development.md), and summarizes current capabilities. Application-specific setup, architecture,
configuration, release, deployment, recovery, and support guides remain to be written as the application's purpose and
operating environment are established.

## System overview

The [application entrypoint](../../cmd/app/main.go) runs HTTP and gRPC servers in one Go process. Both use a shared
authentication service and database-backed repositories.

- **User access:** [HTTP endpoints](../../api/openapi/v1/auth.yaml) provide signup, login, token refresh, logout,
  and user management. [gRPC endpoints](../../api/proto/auth/v1/auth.proto) expose user management.
- **Background processing:** [Outbox workers](../../internal/outbox/workers) publish messages and clean up old records.
  [Authentication workers](../../internal/auth/workers) clean up sessions and update token usage information.
- **Operational support:** Liveness and readiness checks, [metrics](../../internal/metrics), structured logging,
  and coordinated shutdown.
- **Configuration defaults:** The [configuration example](../../.env.example) selects in-memory SQLite, a local cache,
  and in-process events. [Runtime configuration](../../internal/config/config.go) can select different backends.

Application-specific business capabilities and deployment context have not yet been documented.
