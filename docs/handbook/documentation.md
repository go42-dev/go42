---
id: documentation
title: Maintaining application documentation
collection: handbook
sidebar_position: 2
---

# Maintaining application documentation

## Purpose and scope

Maintain the knowledge needed to change and operate this application. Start at the [documentation index](../README.md).

### Documentation ownership

Application contributors own current behavior, effective settings, adopted conventions, operating procedures, and local
requirements and decisions. Record application-specific setup and deviations here.

Generic Quick start, blueprint adoption, and reusable workflow explanations belong in the external
[go42 documentation](https://go42.dev), maintained by go42's author. Check the applicable upstream version and review local
differences when adopting or upgrading the blueprint.
The [global documentation model](https://go42.dev/docs/documentation/) explains the reusable framework.

## Change workflow for contributors

Update documentation alongside implementation. Use this table to identify the owning document:

| Change | Review and update |
| --- | --- |
| Desired behavior, scope, or acceptance criteria | Requirement, affected handbook, and verification evidence |
| Significant architectural or operational choice | Decision, affected requirements, and current handbook |
| Behavior, settings, commands, or procedures | The handbook page that owns the subject |
| Mechanical change with no documented effect | Explain why no documentation update is needed |

Report the documentation impact, checks, results, and remaining gaps with the completed change.

### Building task context

Read the [application profile](project.md), [conventions](conventions.md), and relevant handbook sections and records.
Follow source and test links; expand context when the change crosses component boundaries.

The task and recorded maintainer agreement establish scope. Accepted records establish intent; source, configuration,
and tests establish implementation. Drafts and proposals require agreement before implementation. If sources conflict,
check the claim and distinguish a defect, stale guidance, or a change to agreed intent. Resolve missing decisions with
the maintainer; an observed defect does not redefine a requirement.

Keep task handoffs and detailed logs with the task: objective, affected paths and records, decisions, checks and results,
and open questions. Put durable facts in their owning documents. Keep credentials out of both.

## Creating a document

Update an existing page when it owns the subject. Otherwise, copy a template, replace its metadata and prompts, remove
irrelevant sections, link related documents and evidence, update the index, and run the [checks](#verification).
Create the destination directory if this is its first record.

### Document types

| Template | Purpose | Destination |
| --- | --- | --- |
| [Handbook](../templates/handbook.md) | Current behavior and procedures | `docs/handbook/topic.md` |
| [Requirement](../templates/requirement.md) | Desired outcomes and acceptance criteria | `docs/requirements/NNN-capability.md` |
| [Decision](../templates/decision.md) | Significant choices, alternatives, and consequences | `docs/decisions/NNN-choice.md` |

Keep one living requirement per capability or concern, with stable criterion IDs such as `REQ-NNN.1`. Several changes
may implement it. Routine choices can follow existing decisions.

### Metadata and filenames

Every page needs YAML `id`, `title`, and `collection`; optional `related` lists existing document IDs. IDs are unique and
remain stable across title changes and moves. Allocate the next unused record number; never reuse retired IDs.

The publication rules require `collection` to match its source: `overview` for `docs/README.md`, and `handbook`,
`requirements`, `decisions`, or `templates` for the corresponding directory under `docs/`.

| Page | Additional metadata |
| --- | --- |
| Handbook | Descriptive lowercase `id`; unique positive integer `sidebar_position` |
| Requirement | `id: REQ-NNN`; `status` |
| Decision | `id: ADR-NNN`; `status`; real `date` in `YYYY-MM-DD` form |
| Superseded decision | `superseded_by`: ID of an `accepted` or `superseded` replacement |
| Template | `template-` ID; change metadata and collection when copying; record status validation does not apply |

### Status and implementation

| Record | Status | Meaning |
| --- | --- | --- |
| Requirement | `draft` | Awaiting agreement |
| Requirement | `accepted` | Agreed intent; delivery may have gaps |
| Requirement | `retired` | No longer required |
| Decision | `proposed` | Under consideration |
| Decision | `accepted` | Agreed choice |
| Decision | `rejected` | Declined choice |
| Decision | `superseded` | Replaced by another accepted decision |

Acceptance needs recorded maintainer or delegated agreement; passing checks does not grant it.

Preserve accepted decision reasoning. Record reversals in a new decision and update the old status and replacement link;
clarifications and typo fixes may update the existing record. Track delivery through requirement evidence and gaps.
Describe current implementation in the handbook and label planned behavior.

### Writing procedures and evidence

- Keep one authoritative explanation per subject and link to it. Adapt headings to the reader's task; remove empty or
  irrelevant template sections and use bounded examples.
- Put environment, prerequisites, inputs, expected results, state changes, backend limits, failure checks, and cleanup
  beside the steps they affect.
- Link the source or test behind a claim. State the check, environment/backend, result, and unverified gaps; distinguish
  inspected source from executed tests and observed behavior.
- Label assumptions, unknowns, and historical inference. Do not invent stakeholders, targets, dates, approvals, or reasoning.
  Use placeholders or disposable values in examples and keep secrets out of recorded output.

### Maintaining the index

List each handbook page, requirement, decision, and template once under the matching index heading:
**Handbook**, **Requirements**, **Decisions**, or **Templates**. Every row has two cells: one document link and a short,
nonempty purpose. Keep metadata in the document. When a collection has no records, keep its heading and an empty-state
message linking the appropriate template. Replace that message with a table when adding the first record.

Order handbook entries by ascending `sidebar_position`; gaps are allowed. Order requirements and decisions by ascending
record number, retaining retired, rejected, and superseded records. Update the index when adding, renaming, moving,
removing, reordering, or changing a page's purpose.

## Verification

Install the pinned tools through [environment setup](development.md#environment-setup), then run:

```sh
task docs-check
```

Preview with `task docs-serve`. Checks cover Markdown, prose, metadata, index coverage and order, links, rendered anchors,
documentation tooling tests, and type checks. Review accuracy and completeness separately; run the application checks
needed to substantiate changed instructions. A passing documentation build does not prove runtime behavior.

## Source files and publishing

Author in `docs/`; `docs/README.md` is the published homepage. Use relative links to source files and include `.md` for
documents. Keep navigable links outside code blocks. Store images and the logo in `pages/static/img/`, using relative
paths such as `../../pages/static/img/diagram.svg` from a handbook page.

The [assembler](../../pages/assemble.mjs) adapts links for publication and generates `pages/docs/`. Website configuration
lives in `pages/`; builds go to `pages/build/`. Generated pages and build output are ignored by Git.

The [Pages workflow](../../.github/workflows/210-github-pages.yaml) publishes on pushes to `master`. Builds use local
sources independently of a go42-docs checkout. Pages display the source revision and identify uncommitted local changes.
Source links use the CI repository or local Git origin; override with `DOCS_REPOSITORY_URL` for an archive or another host,
and use `DOCS_REVISION` to select the source reference.
