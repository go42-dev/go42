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
[application profile](handbook/02-project.md) for purpose and ownership. Contributors follow the
[project conventions](handbook/03-conventions.md) and [documentation rules](handbook/01-documentation.md).

## Start here

| Task                       | Guide                                                                                                                                                      |
|----------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Run locally                | [Set up the environment](handbook/05-development.md#environment-setup), then [start and check readiness](handbook/11-operations.md#run-and-verify-locally) |
| Implement a feature        | [Follow the implementation workflow](handbook/05-development.md#implementing-a-feature)                                                                    |
| Test a change              | [Choose checks and prepare dependencies](handbook/06-testing.md)                                                                                           |
| Diagnose a failure         | [Follow symptoms and recovery checks](handbook/11-operations.md#failure-investigation-and-recovery)                                                        |
| Investigate missing events | [Inspect outbox delivery](handbook/11-operations.md#inspect-outbox-delivery)                                                                               |

## Documentation index

The complete index follows the published sidebar's reading order.

### Handbook

| Document                                              | Purpose                                                                        |
| ----------------------------------------------------- | ------------------------------------------------------------------------------ |
| [Writing documentation](handbook/01-documentation.md) | Writing rules, document templates, checks, and publishing                      |
| [Application profile](handbook/02-project.md)         | Purpose, ownership, current capabilities, and documentation gaps               |
| [Project conventions](handbook/03-conventions.md)     | Engineering rules for code, architecture, data, runtime, and test design       |
| [Configuration](handbook/04-configuration.md)         | Configuration sources, backend choices, defaults, and deployment settings      |
| [Development workflow](handbook/05-development.md)    | Setup, implementation, generation, tooling, and contributions                  |
| [Testing and verification](handbook/06-testing.md)    | Suite selection, test environments, coverage, and checks before review         |
| [Architecture](handbook/07-architecture.md)           | Source map, component boundaries, request and event flows, and lifecycle       |
| [Using the API](handbook/08-api.md)                   | Credentials, session flow, local verification, and request failure diagnosis   |
| [Release](handbook/09-release.md)                     | Version selection, release validation, published artifacts, and image identity |
| [Deployment](handbook/10-deployment.md)               | Container and Helm configuration, migration handoff, and rollout verification  |
| [Operations](handbook/11-operations.md)               | Probe checks, process lifecycle, outbox diagnosis, and failure recovery        |
| [Monitoring](handbook/12-monitoring.md)               | Collect metrics, interpret signals, and configure the supplied dashboard       |
| [Agentic workflow](handbook/20-agentic-workflow.md)   | AI workflow inputs, execution, and outputs                                     |

### Requirements

No application requirements are recorded yet. Start with the [requirement template](templates/requirement.md).

### Decisions

No application decisions are recorded yet. Start with the [decision template](templates/decision.md).

## Templates

Start new documents from a template and follow the
[steps for creating a document](handbook/01-documentation.md#creating-a-document).

| Template                                | Purpose                                                        |
|-----------------------------------------|----------------------------------------------------------------|
| [Handbook](templates/handbook.md)       | Current behavior, architecture, and working instructions       |
| [Requirement](templates/requirement.md) | Desired outcomes, scope, acceptance criteria, and verification |
| [Decision](templates/decision.md)       | Significant choices, alternatives, reasoning, and consequences |

## References

API contracts live with their [source definitions](../api/openapi/v1). The published site also provides an OpenAPI view.

For the blueprint and its default workflows, see the [go42 guide](https://go42.dev).

See [where docs belong](handbook/01-documentation.md#documentation-ownership) when deciding which project to update.
