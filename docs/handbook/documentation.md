---
id: documentation
title: Maintaining application documentation
sidebar_position: 2
---

# Maintaining application documentation

## Purpose and scope

Keep the context a contributor needs to understand, change, verify, and operate this application. Start at the
[documentation index](../README.md). Git history, issues, and pull requests preserve delivery history; documentation
preserves the knowledge future work depends on.

## Documentation ownership

Application contributors maintain the local handbook, requirements, decisions, policy, conventions, and templates.
Include first-run instructions and the effective settings needed to develop and operate this checkout, even when they
are inherited defaults. Review these sources when adopting or upgrading go42.

The external [go42 operational guide](https://go42.dev), maintained solely by go42's author, covers creating projects
from the blueprint and operating go42 as a whole. It is outside the application checkout and its contributors'
responsibilities. Link to it for additional explanation, identifying the applicable version when guidance depends on one.

## Building task context

For a change, read the [application profile](project.md), [conventions](conventions.md), and this policy, then select
relevant handbook sections and records from the [index](../README.md). Follow their source and test links into the affected
component. Load additional context when an interface, dependency, or failure crosses that component's boundary.

| Source                                         | What it establishes                                                           |
|------------------------------------------------|-------------------------------------------------------------------------------|
| Current task and recorded maintainer agreement | The requested scope and any approved change to intent                         |
| Accepted requirements and decisions            | Agreed outcomes, constraints, and significant choices                         |
| Handbook, source, configuration, and tests     | Described and implemented behavior, executable checks, and known gaps         |
| Draft requirements and proposed decisions      | Material for review; status alone does not authorize an implementation change |
| External blueprint documentation               | Background about go42; verify applicability to this checkout                  |

When sources disagree, identify the conflicting claim and check the relevant implementation and test. Record whether
the work fixes an implementation defect, corrects stale guidance, or changes agreed intent. Resolve missing product or
operational decisions with the maintainer; label the unknown instead of deriving a requirement from an observed defect.

For AI-assisted work, retain a short task handoff: objective, affected paths and records, decisions made, checks and
results, and unresolved questions. Put durable application facts in their owning documents; keep transient work notes
with the task. Never include credentials or tokens in either.

## Document types

| Type        | Question                | Contents                               | Update rule                |
|-------------|-------------------------|----------------------------------------|----------------------------|
| Handbook    | How does it work today? | Behavior, architecture, instructions   | Update with code           |
| Requirement | What must it do?        | Outcomes, scope, criteria, evidence    | Revise when intent changes |
| Decision    | Why this approach?      | Context, options, choice, consequences | Supersede when replaced    |

A current architecture diagram belongs in the handbook. A reliability target belongs in requirements. The reasoning
behind an architectural choice belongs in a decision. Link the documents when they concern the same capability.

Keep one living requirement document per capability or concern. Several pull requests may implement or revise it.
Create a decision record for choices affecting system boundaries, dependencies, persistence, security, delivery policy,
or another area where later changes need the reasoning. Routine changes can follow existing decisions.

## Maintaining the index

Maintain the index tables manually under the headings **Handbook**, **Requirements**, **Decisions**, and **Templates**.
Every page in those collections appears exactly once in its table. Each row has two cells: one link to the document and
a short, nonempty purpose. Keep statuses, ownership, and other metadata in the documents themselves.

Order handbook entries by ascending `sidebar_position`, matching the published sidebar. Each handbook page needs a
unique positive integer; gaps are allowed. Order requirements and decisions by ascending record number. Keep retired
requirements and rejected or superseded decisions in the index.

Update the index in the same change when a page is added, renamed, removed, reordered, or its purpose changes.
`task docs-check` validates coverage, grouping, row structure, and order against the authored files.

## Creating a document

Read the existing guidance and [conventions](conventions.md); update an existing page when it already owns the subject.
Otherwise, copy the matching template:

| Template                                   | Destination                           |
|--------------------------------------------|---------------------------------------|
| [Handbook](../templates/handbook.md)       | `docs/handbook/topic.md`              |
| [Requirement](../templates/requirement.md) | `docs/requirements/NNN-capability.md` |
| [Decision](../templates/decision.md)       | `docs/decisions/NNN-choice.md`        |

Replace the template's metadata, heading, and prompts. Remove irrelevant sections, link related documents and evidence,
add the page to the index, and run the [checks](#verification).

- **Handbook:** adapt headings to the subject. Procedures need steps and expected results; architecture needs component
  responsibilities and flows; references need effective settings and defaults.
- **Requirements:** write observable criteria, including relevant failures and constraints. Give criteria stable IDs such
  as `REQ-001.1` and record verification evidence or explicit gaps.
- **Decisions:** explain the alternatives, choice, tradeoffs, and consequences. For reconstructed history, distinguish
  evidence from inference and identify unknown reasoning.

State assumptions and unknowns explicitly. Do not invent stakeholders, targets, dates, approvals, or historical reasoning
to fill a template.

## Writing procedures and evidence

Write for a named reader task. Keep procedures beside their constraints: execution environment, prerequisites, required
inputs, expected result, and relevant failure or cleanup steps. State when a command writes data, needs a running service,
or applies only to one backend. Put a limitation before the step it affects instead of hiding it at the end of the page.
Use bounded examples and placeholders or disposable values; keep secrets out of recorded output.

Link the source that owns a claim. Distinguish a source inspection from a test run, a local observation, or a deployed
result. Evidence should identify the check, its relevant environment/backend and result, and what remains unverified.
A successful source/site check proves structure and links; it does not prove application behavior or production readiness.

Keep one authoritative explanation for each subject and link to it from other guides. Use the smallest example that
demonstrates the outcome. Adapt the [handbook template](../templates/handbook.md) to the subject; an architectural
explanation does not need a procedure or an empty recovery section. Store detailed run logs with the task, while retaining
stable test/source links and material limitations in the documentation.

## Metadata and filenames

Every page has YAML front matter. IDs are unique across published pages and remain stable when titles change.

| Applies to           | Metadata                                                                                      |
|----------------------|-----------------------------------------------------------------------------------------------|
| All pages            | `id` and `title`; optional `related` list of published document IDs                           |
| Handbook             | Lowercase descriptive `id`; unique positive integer `sidebar_position`                        |
| Requirements         | `id: REQ-NNN` and `status`                                                                    |
| Decisions            | `id: ADR-NNN`, `status`, and a real `date` in `YYYY-MM-DD` form                               |
| Superseded decisions | `superseded_by`: the replacement decision's ID; its status must be `accepted` or `superseded` |
| Templates            | `template-` IDs; change the metadata when copying; record status validation does not apply    |

Use descriptive handbook filenames and numbered record filenames, such as `handbook/deployment.md`,
`requirements/002-releases.md`, and `decisions/002-release-trigger.md`. Allocate the next unused record number, keep it
when renaming, and never reuse retired IDs. The site renders record metadata and checks references and replacement chains.

## Status and implementation

| Collection   | Status       | Meaning                                                     |
|--------------|--------------|-------------------------------------------------------------|
| Requirements | `draft`      | Proposed behavior or a requirement awaiting agreement       |
| Requirements | `accepted`   | Agreed desired behavior; implementation may still have gaps |
| Requirements | `retired`    | An outcome that is no longer required; retain its context   |
| Decisions    | `proposed`   | A choice under consideration                                |
| Decisions    | `accepted`   | A choice that has been made                                 |
| Decisions    | `rejected`   | A considered choice that was declined                       |
| Decisions    | `superseded` | A previous choice replaced by another accepted decision     |

Acceptance requires agreement by the maintainer or delegated authority; record that decision or review. Passing checks
does not imply acceptance. The initial [documentation requirement](../requirements/001-documentation.md) and
[documentation decision](../decisions/001-documentation-model.md) remain drafts for local adoption.

Preserve accepted decision reasoning. Record reversals in a new decision and update the old status and replacement link;
clarifications and typo fixes can update the existing record. Track delivery through requirement evidence and gaps.
The handbook describes current implementation and labels plans. An observed defect does not silently change agreed intent.

## Change workflow for contributors

Read the application profile, conventions, and records relevant to the change. Use this table to identify its impact:

| Change                                                | Documentation to review and update                               |
|-------------------------------------------------------|------------------------------------------------------------------|
| Desired behavior, scope, or acceptance criteria       | Requirement; affected handbook instructions and evidence         |
| Significant architectural or operational choice       | Decision; affected requirements and current handbook explanation |
| Current behavior, settings, commands, or procedures   | The handbook page that owns the subject                          |
| Mechanical change with no change to documented claims | No document change; explain why in the change report             |

Update affected sources alongside implementation. Keep each fact in one authoritative place and link to it; maintain
the index when pages change. Verify claims against code, configuration, tests, or observed results. Report the
documentation impact, verification results, and remaining gaps with the completed change.

## Verification

Follow the [environment setup instructions](development.md#environment-setup) and install the pinned tools through
`task setup`, then run:

```sh
task docs-check
```

Preview the built website with:

```sh
task docs-serve
```

The check validates Markdown, prose, metadata, index coverage and order, local links, and the built site's links and
anchors. It also runs documentation tooling tests and type checks. Review establishes accuracy, completeness, and whether
evidence supports the claims; run any application checks needed to substantiate changed instructions.

## Source files and publishing

`docs/README.md` is the entry point and published homepage. Author pages under `docs/handbook/`, `docs/requirements/`,
`docs/decisions/`, and `docs/templates/`.

Use relative Markdown links to actual source files, including the `.md` extension for documents. The assembler adapts
links for the site and links other repository files to the source revision. Keep page links out of code blocks when they
are intended to be navigable. The generated OpenAPI view is available from site navigation.

Keep documentation images and the website logo in `pages/static/img/`, published through Docusaurus's static directory.
Use source-relative image links, such as `../../pages/static/img/diagram.svg` from a handbook page.

Website tooling and configuration live in `pages/`. The [assembler](../../pages/assemble.mjs) generates `pages/docs/`.
The website builds to `pages/build/`. Generated content and build output are ignored by Git.

The [Pages workflow](../../.github/workflows/210-github-pages.yaml) builds and publishes on pushes to `master`. Pages
display the source revision used for the build; a local preview identifies uncommitted changes when present.

Validation, assembly, and publishing use local documentation sources and do not import or require a go42-docs checkout.

Source links use the CI repository or the local Git origin. Set `DOCS_REPOSITORY_URL` to the repository's web address when
building from an archive or a different hosting setup. `DOCS_REVISION` can select the source reference shown in links.
