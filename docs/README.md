---
id: docs-home
title: Application documentation
collection: overview
sidebar_label: Overview
sidebar_position: 1
slug: /
---

# Application documentation

Use this handbook for application configuration and contributor guidance. Read the
[application profile](handbook/project.md) for purpose and ownership. Contributors follow the
[project conventions](handbook/conventions.md) and [documentation rules](handbook/documentation.md).

## Start here

| Task                         | Guide                                                                                                                                                        |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Configure the application    | [Review configuration sources and settings](handbook/configuration.md)                                                                                       |
| Change the application       | [Follow project conventions](handbook/conventions.md)                                                                                                        |
| Test a change                | [Choose checks and test suites](handbook/verification.md)                                                                                                    |

## Documentation index

The complete index follows the published sidebar's reading order.

### Handbook

| Document                                                    | Purpose                                                                        |
| ----------------------------------------------------------- | ------------------------------------------------------------------------------ |
| [Writing documentation](handbook/documentation.md)          | Writing rules, document templates, checks, and publishing                      |
| [Project conventions](handbook/conventions.md)              | Application-specific rules and a reference to shared Go conventions            |
| [Application profile](handbook/project.md)                  | Purpose, ownership, current capabilities, and documentation gaps               |
| [Configuration](handbook/configuration.md)                  | Configuration sources, backend choices, defaults, and deployment settings      |
| [Verification](handbook/verification.md)                    | Formatting, linting, test suites, and checks required before review            |
| [GitHub configuration](handbook/github-configuration.md)    | Required and optional workflow variables and secrets                           |

### Requirements

No application requirements are recorded yet. Start with the [requirement template](templates/requirement.md).

### Decisions

No application decisions are recorded yet. Start with the [decision template](templates/decision.md).

## Templates

Start new documents from a template and follow the
[steps for creating a document](handbook/documentation.md#creating-a-document).

| Template                                  | Purpose                                                          |
| ----------------------------------------- | ---------------------------------------------------------------- |
| [Handbook](templates/handbook.md)         | Current behavior, architecture, and working instructions         |
| [Requirement](templates/requirement.md)   | Desired outcomes, scope, acceptance criteria, and verification   |
| [Decision](templates/decision.md)         | Significant choices, alternatives, reasoning, and consequences   |

## References

API contracts live with their [source definitions](../api/openapi/v1). The published site also provides an OpenAPI view.

For the blueprint and its default workflows, see the [go42 guide](https://go42.dev).

See [where docs belong](handbook/documentation.md#documentation-ownership) when deciding which project to update.
