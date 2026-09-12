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
[Go42 adoption guide](https://go42.dev/docs/adopting-go42/).

## Documentation coverage

The [documentation index](../README.md) lists the application guides. They describe the checked-out implementation,
including inherited defaults.

The application's purpose, users, owners, target environments, and support contacts still need to be recorded.
Deployment promotion, secret delivery, backup and restore procedures, and recovery objectives depend on that operating
context and remain outstanding. No application-specific business requirements have been recorded.

## System overview

The application provides signup and session endpoints over HTTP, and user management over HTTP and gRPC. It includes
database persistence, background event processing, health checks, metrics, and structured logs. The
[architecture](architecture.md#components-and-boundaries) describes component responsibilities and flows.
The local defaults use in-memory SQLite, a local cache, and in-process events; see
[storage and event configuration](configuration.md#storage-and-events) for persistence and backend choices.
