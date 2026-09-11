---
id: ADR-001
title: "ADR-001: Keep application documentation with its code"
status: proposed
date: 2026-09-11
related:
  - REQ-001
  - documentation
---

# ADR-001: Keep application documentation with its code

## Context

The application needs documentation that describes its own purpose, requirements, choices, and operating environment.
Contributors and AI need instructions that apply to the checked-out code, including after it diverges from go42 defaults.

The external [go42 operational guide](https://github.com/go42-dev/go42-docs) is maintained solely by go42's authors and
describes go42 as a whole. Application users consult it online; it is outside the application checkout and its contributors'
documentation responsibilities.

This record ships as a proposal for local adoption. The application maintainer reviews its applicability before
acceptance. The local policy and tooling can be evaluated while the record remains proposed.

## Alternatives

- Rely on the external operational guide for application instructions. This reduces local writing, but the guide describes
  blueprint defaults and cannot establish this application's requirements, settings, or operating responsibilities.
- Copy the operational guide into the application. This makes the explanations local but creates another copy to maintain
  and still requires application-specific instructions.
- Maintain application documents beside the code and link to external explanations where useful. This makes contributors
  responsible for the application's knowledge and keeps the guide under its author's separate maintenance.

## Decision

Keep this application's handbook, requirements, decisions, conventions, and authoring templates beside its code.
Contributors and AI maintain those sources in this repository. Review inherited defaults during adoption and upgrades.
Treat go42-docs as an external reference; application changes do not require cloning or updating it.

Maintain one living requirement per capability or concern. Keep the handbook aligned with the current implementation.
Include the effective instructions needed to develop and operate this application, including inherited defaults. Preserve
reasoning for significant local choices and link superseded records to their replacements. Publish authoring templates
separately and assemble the website from local sources.

## Consequences

Readers can distinguish desired behavior, current implementation, and decision history. Authors have explicit update
rules, and AI tools follow the application's local policy. Documentation checks and publishing work independently of
go42-docs. The guide's author can maintain the blueprint explanations while application contributors maintain local facts.

Maintainers must review inherited defaults for their application, keep links and verification evidence current, and
record material changes to requirements or decisions. CI checks structure and publishing behavior; review remains
responsible for accuracy and completeness. Existing application coverage gaps remain visible until the guides are written.
