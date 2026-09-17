---
id: project
title: Application profile
collection: handbook
sidebar_position: 2
---

# Application profile

This page records the application's purpose, ownership, origin, and current capabilities.

## Purpose and ownership

| Field               | Value                            |
| ------------------- | -------------------------------- |
| Application name    | `{{APPLICATION_NAME}}`           |
| Repository          | `{{APPLICATION_REPOSITORY_URL}}` |
| Purpose             | `{{APPLICATION_PURPOSE}}`        |
| Intended users      | `{{APPLICATION_USERS}}`          |
| Development owner   | `{{DEVELOPMENT_OWNER}}`          |
| Operational owner   | `{{OPERATIONAL_OWNER}}`          |
| Target environments | `{{TARGET_ENVIRONMENTS}}`        |
| Support contacts    | `{{SUPPORT_CONTACTS}}`           |

## Origin

The source blueprint is [go42](https://github.com/go42-dev/go42).

- Starting revision: `{{GO42_SOURCE_COMMIT}}`.

## Documentation coverage

The [documentation index](../README.md) lists the application guides. They describe the checked-out implementation,
including inherited defaults.

- Deployment: `{{DEPLOYMENT_DOC_LINK}}`.
- Recovery: `{{RECOVERY_DOC_LINK}}`.
- Requirements: `{{REQUIREMENTS_INDEX_LINK}}`.

## System overview

The application provides signup and session endpoints over HTTP, and user management over HTTP and gRPC. It includes
database persistence, background event processing, health checks, metrics, and structured logs. The
[architecture](07-architecture.md#components-and-boundaries) describes component responsibilities and flows.
The local defaults use in-memory SQLite, a local cache, and in-process events; see
[storage and event configuration](04-configuration.md#storage-and-events) for persistence and backend choices.
