---
id: docs-home
title: Application documentation
sidebar_label: Overview
sidebar_position: 1
slug: /
---

# Application documentation

This index is the entry point for the application in this checkout: its purpose, working rules, required behavior,
significant decisions, and operating instructions. The handbook table follows the published sidebar's reading order.

## Documentation index

### Handbook

| Document                                               | Purpose                                                                      |
| ------------------------------------------------------ | ---------------------------------------------------------------------------- |
| [Application profile](handbook/project.md)             | Purpose, ownership, current capabilities, and documentation gaps             |
| [Maintaining documentation](handbook/documentation.md) | Document types, authoring, maintenance, checks, and publishing               |
| [Project conventions](handbook/conventions.md)         | Engineering rules for code, architecture, data, runtime, and test design     |
| [Development workflow](handbook/development.md)        | Setup, local development, formatting, tests, verification, and contributions |
| [Architecture](handbook/architecture.md)               | Components, request and event flows, persistence, and process lifecycle      |
| [Configuration](handbook/configuration.md)             | Configuration sources, backend choices, defaults, and deployment settings    |
| [Operations](handbook/operations.md)                   | Startup checks, health, observability, deployment, and recovery boundaries   |

### Requirements

| Document                                                                | Purpose                                                    |
|-------------------------------------------------------------------------|------------------------------------------------------------|
| [REQ-001: Application documentation](requirements/001-documentation.md) | Outcomes and acceptance criteria for project documentation |

### Decisions

| Document                                                                                      | Purpose                                                         |
|-----------------------------------------------------------------------------------------------|-----------------------------------------------------------------|
| [ADR-001: Keep application documentation with its code](decisions/001-documentation-model.md) | Rationale for maintaining application knowledge beside the code |

## Templates

Start new documents from a template and follow the
[authoring workflow](handbook/documentation.md#creating-a-document).

| Template                                | Purpose                                                        |
|-----------------------------------------|----------------------------------------------------------------|
| [Handbook](templates/handbook.md)       | Current behavior, architecture, and working instructions       |
| [Requirement](templates/requirement.md) | Desired outcomes, scope, acceptance criteria, and verification |
| [Decision](templates/decision.md)       | Significant choices, alternatives, reasoning, and consequences |

## References

API contracts live with their [source definitions](../api/openapi/v1). The published site also provides an OpenAPI view.

For the blueprint and its default workflows, consult the external [go42 documentation](https://go42.dev).

The [documentation ownership rules](handbook/documentation.md#documentation-ownership) explain the maintenance boundary.
