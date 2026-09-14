---
id: docs-home
title: Application documentation
collection: overview
sidebar_label: Overview
sidebar_position: 1
slug: /
---

# Application documentation

Use this handbook to run, change, and operate the application in this checkout. Read the
[application profile](handbook/project.md) for purpose and ownership. Contributors follow the
[project conventions](handbook/conventions.md) and [documentation rules](handbook/documentation.md).

## Start here

| Task | Guide |
| --- | --- |
| Run locally | [Set up the environment](handbook/development.md#environment-setup), then [start and check readiness](handbook/operations.md#run-and-verify-locally) |
| Implement a feature | [Follow the implementation workflow](handbook/development.md#implementing-a-feature) |
| Test a change | [Choose checks and prepare dependencies](handbook/testing.md) |
| Diagnose a failure | [Follow symptoms and recovery checks](handbook/operations.md#failure-investigation-and-recovery) |
| Investigate missing events | [Inspect outbox delivery](handbook/operations.md#inspect-outbox-delivery) |

## Documentation index

The complete index follows the published sidebar's reading order.

### Handbook

| Document | Purpose |
| ------------------------------------------------------ | ---------------------------------------------------------------------------- |
| [Application profile](handbook/project.md) | Purpose, ownership, current capabilities, and documentation gaps |
| [Maintaining documentation](handbook/documentation.md) | Task context, document types, authoring, checks, and publishing |
| [Project conventions](handbook/conventions.md) | Engineering rules for code, architecture, data, runtime, and test design |
| [Configuration](handbook/configuration.md) | Configuration sources, backend choices, defaults, and deployment settings |
| [Development workflow](handbook/development.md) | Setup, implementation, generation, tooling, and contributions |
| [Testing and verification](handbook/testing.md) | Suite selection, test environments, coverage, and checks before review |
| [Architecture](handbook/architecture.md) | Source map, component boundaries, request and event flows, and lifecycle |
| [Using the API](handbook/api.md) | Credentials, session flow, local verification, and request failure diagnosis |
| [Release](handbook/release.md) | Version selection, release validation, published artifacts, and image identity |
| [Deployment](handbook/deployment.md) | Container and Helm configuration, migration handoff, and rollout verification |
| [Operations](handbook/operations.md) | Probe checks, process lifecycle, outbox diagnosis, and failure recovery |
| [Monitoring](handbook/monitoring.md) | Collect metrics, interpret signals, and configure the supplied dashboard |

### Requirements

No application requirements are recorded yet. Start with the [requirement template](templates/requirement.md).

### Decisions

No application decisions are recorded yet. Start with the [decision template](templates/decision.md).

## Templates

Start new documents from a template and follow the
[authoring workflow](handbook/documentation.md#creating-a-document).

| Template | Purpose |
| ----------------------------------------- | ---------------------------------------------------------------- |
| [Handbook](templates/handbook.md) | Current behavior, architecture, and working instructions |
| [Requirement](templates/requirement.md) | Desired outcomes, scope, acceptance criteria, and verification |
| [Decision](templates/decision.md) | Significant choices, alternatives, reasoning, and consequences |

## References

API contracts live with their [source definitions](../api/openapi/v1). The published site also provides an OpenAPI view.

For the blueprint and its default workflows, consult the external [go42 documentation](https://go42.dev).

The [documentation ownership rules](handbook/documentation.md#documentation-ownership) explain the maintenance boundary.
