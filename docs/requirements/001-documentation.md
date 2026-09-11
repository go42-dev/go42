---
id: REQ-001
title: "REQ-001: Application documentation"
status: draft
related:
  - ADR-001
  - documentation
---

# REQ-001: Application documentation

## Outcome

A new contributor or AI can locate the current application's context, required behavior, significant decisions, and
operating instructions from its checkout. The repository can publish the same knowledge as a browsable website.

This initial requirement is a draft for review and adoption by the application maintainer. It records the intended
documentation coverage; it does not claim that all application guides have been written.

## Scope

Cover this application's handbook, requirements, decisions, authoring templates, and maintenance process. The external
[go42 operational guide](https://github.com/go42-dev/go42-docs) is maintained solely by go42's author. Application
contributors maintain their documentation in this repository. API reference is generated from its source contracts.
Delivery tasks and discussions remain in issues, pull requests, and Git history.

## Acceptance criteria

### REQ-001.1: Find the application's knowledge

`docs/README.md` is the single entry point for application documentation and becomes the published homepage.
It provides a reading order and a complete index of handbook pages, requirements, and decisions, grouped by type with
links and short purposes. Templates are listed separately. Retired requirements and rejected or superseded decisions
remain discoverable.
Conventions are a handbook page under `docs/handbook/`. The index links the local documentation ownership rules and the
external guide.

### REQ-001.2: Distinguish intent, implementation, and history

The collections define their purpose and update rules. Requirements and decisions expose valid statuses. Acceptance and
delivery are distinguished. Superseded decisions link to a replacement, while their original reasoning remains available.

### REQ-001.3: Maintain documentation with changes

Contributors and AI agents update affected requirements, decisions, and handbook pages alongside implementation changes,
following the local documentation policy. Application contributions do not require changes to go42-docs.

### REQ-001.4: Validate and publish from the sources

The documented check rejects missing metadata, duplicate IDs, invalid record statuses, missing related records, broken
local file links, and broken published page links or anchors. Templates appear as authoring resources. Publishing uses
the source Markdown and shows the source revision.

### REQ-001.5: Operate the adopted application

The application handbook identifies its purpose and owners and documents the setup, configuration, architecture,
verification, release, deployment, recovery, and support details needed for its actual environment. It includes inherited
defaults and the effective local instructions. Links to external guidance provide additional explanation.

### REQ-001.6: Maintain documentation independently

Contributors can read, author, validate, and assemble application documentation from its checkout using its local policy,
templates, and sources. The documentation tooling and publishing workflow do not require a go42-docs checkout or fetch
its content. References to that guide remain external web links.

## Verification

| Criteria | Evidence or gap |
| --- | --- |
| REQ-001.1 | [Documentation index](../README.md); compare its entries with the authored files under `docs/` |
| REQ-001.2 | [Local policy](../handbook/documentation.md#status-and-implementation) and record metadata checks |
| REQ-001.3 | [Documentation workflow](../handbook/documentation.md#change-workflow-for-people-and-ai); review checks meaning |
| REQ-001.4 | `make docs-check`; documentation tooling tests, validation, and the site build |
| REQ-001.5 | Outstanding: the [application profile](../handbook/project.md#documentation-coverage) identifies the initial coverage gaps |
| REQ-001.6 | [Local source assembly](../../pages/assemble.mjs), [isolated fixtures](../../pages/assemble.test.mjs), and [publishing workflow](../../.github/workflows/210-github-pages.yaml) |

Re-run the relevant checks when changing these sources. Successful tooling checks establish structural validity and
publishing behavior; they do not establish complete operating documentation or acceptance of this requirement.
