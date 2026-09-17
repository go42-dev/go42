---
id: template-requirement
title: Requirement template
collection: templates
status: draft
related: []
---

# Requirement template

Copy this file to `docs/requirements/`. Set an unused `REQ-NNN` ID, write a clear title, and change the collection to
`requirements`. Start as `draft`. See [creating a document](../handbook/01-documentation.md#creating-a-document) and
[statuses](../handbook/01-documentation.md#status-and-implementation).

## Problem

Explain the problem, who it affects, and what needs to improve. Include any questions that still need an answer.

## Scope

Say what this requirement covers, what it leaves out, and any limits the solution must respect.

## Acceptance criteria

### REQ-NNN.1: Describe a result

Give a concrete example of an action or input and the result it must produce. Include failure cases or performance
needs when they matter. Add numbered criteria as needed, and keep their labels when the wording changes.

## Progress

For each criterion, say how you checked it and what is still missing. Link useful tests or examples. Mention the
environment when it affects the result, and keep detailed logs with the change.

| Criterion | How it was checked             | Still needed                   |
| --------- | ------------------------------ | ------------------------------ |
| REQ-NNN.1 | Test or example and its result | Missing work or checks, if any |
