---
id: template-requirement
title: Requirement template
status: draft
related: []
---

# Requirement template

Follow the [authoring workflow](../handbook/documentation.md#creating-a-document) and
[acceptance rules](../handbook/documentation.md#status-and-implementation).

## Outcome

Describe the problem, who experiences it, and the required outcome. Identify assumptions and open questions.

## Scope

Define the capability's boundaries, relevant exclusions, and constraints.

## Acceptance criteria

### REQ-NNN.1: Observable outcome

Describe a scenario and its required result, including relevant failure behavior and nonfunctional constraints.
Add stable numbered criteria as needed.

## Verification

For each criterion, link the check or source and record its relevant environment/backend, observed result, and remaining
implementation or verification gap. Distinguish inspected source, automated tests, manual observations, and untested plans.
Passing one backend's tests does not establish behavior on another. Keep detailed run logs with the task.

| Criterion | Evidence or gap                                                                                              |
|-----------|--------------------------------------------------------------------------------------------------------------|
| REQ-NNN.1 | Link a test/check or source; state its scope and result, or describe missing implementation or verification. |
