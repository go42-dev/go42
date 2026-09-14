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
requirements and decisions. Record the complete procedures needed by this checkout, including inherited defaults and
local changes.

Generic quickstart, blueprint adoption, and reusable workflow explanations belong in the external
[go42 documentation](https://go42.dev), maintained by go42's author. Check the applicable upstream version and review local
differences when adopting or upgrading the blueprint.
The [global documentation model](https://go42.dev/docs/documentation/) explains the reusable framework.

The agreed go42x role covers orchestration of setup, shared defaults, validation, and publishing. Use the commands
actually available in the selected go42x version and label planned capabilities. The application's
[Taskfile](../../Taskfile.yaml), source, and contracts define its executable workflows and interfaces; application
contributors own the resulting local configuration, authored documentation, and operating instructions.

## Shared writing rules

The [public documentation policy](https://go42.dev/docs/documentation/) is the editorial home of these rules. This local
copy keeps them usable in the go42x checkout. Apply each rule to the page's purpose; choose useful headings and remove
empty or irrelevant template sections.

| Rule                                | Required practice                                                                                                                                                                                                     |
|-------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| State the scope                     | Identify the reader's task, promised result, prerequisites, and applicable software versions or environments.                                                                                                         |
| Make procedures executable          | Specify the working directory and required inputs. Separate copyable commands from output, preserve exact identifiers, and show expected results, effects on state, recovery, and cleanup where relevant.             |
| Support claims with evidence        | Link the relevant source or check. Distinguish inspected code, executed checks, proposals, and delivered behavior. Label assumptions and gaps; use disposable examples and keep credentials out of recorded evidence. |
| Make content accessible             | Use meaningful headings and links, readable examples, and text alternatives for informative images. Check navigation and the rendered meaning of changed content.                                                     |
| Maintain documentation with changes | Update the owning document with behavior changes, preserve important URLs and anchors, and explain documentation impact. Update authored inputs and regenerate derived output.                                        |
| Write consistently                  | Use direct language, stable terminology, and exact technical identifiers. Assume technical competence while explaining knowledge specific to go42.                                                                    |

Use **go42** for the upstream blueprint, **go42x** for its orchestration tool, **application** for a project adopting the
blueprint, and **local handbook** for that application's effective instructions. Preserve exact command names, flags,
configuration keys, output fields, and file paths.

## Change workflow for contributors

Update documentation alongside implementation. Use this table to identify the owning document:

| Change                                          | Review and update                                         |
|-------------------------------------------------|-----------------------------------------------------------|
| Desired behavior, scope, or acceptance criteria | Requirement, affected handbook, and verification evidence |
| Significant architectural or operational choice | Decision, affected requirements, and current handbook     |
| Behavior, settings, commands, or procedures     | The handbook page that owns the subject                   |
| Mechanical change with no documented effect     | Explain why no documentation update is needed             |

Report the documentation impact, checks, results, and remaining gaps with the completed change.

### Reviewing related guidance

Use the changed source to identify the procedures that need review:

| Changed source                                | Documentation to inspect                                                                 |
|-----------------------------------------------|------------------------------------------------------------------------------------------|
| Taskfile, tool versions, or environment setup | Local development and testing instructions; public setup and workflow examples.          |
| Configuration or backend defaults             | Local configuration and operating instructions; public trial prerequisites and cleanup.  |
| Contracts, handlers, or authentication        | Local API guidance and owned reference inputs; public request and first-change examples. |
| Documentation model or publishing             | This policy, templates, index, source links, and the public documentation-model guide.   |

When reusable public guidance is affected, link the related go42-docs change and identify the software revision it will
describe. Review go42x guidance when an orchestration command or its effective inputs change. Update the application's
complete local instructions alongside implementation, including after it diverges from the upstream blueprint.

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

| Template                                   | Purpose                                             | Destination                           |
|--------------------------------------------|-----------------------------------------------------|---------------------------------------|
| [Handbook](../templates/handbook.md)       | Current behavior and procedures                     | `docs/handbook/topic.md`              |
| [Requirement](../templates/requirement.md) | Desired outcomes and acceptance criteria            | `docs/requirements/NNN-capability.md` |
| [Decision](../templates/decision.md)       | Significant choices, alternatives, and consequences | `docs/decisions/NNN-choice.md`        |

Keep one living requirement per capability or concern, with stable criterion IDs such as `REQ-NNN.1`. Several changes
may implement it. Routine choices can follow existing decisions.

### Metadata and filenames

Every page needs YAML `id`, `title`, and `collection`; optional `related` lists existing document IDs. IDs are unique and
remain stable across title changes and moves. Allocate the next unused record number; never reuse retired IDs.

The publication rules require `collection` to match its source: `overview` for `docs/README.md`, and `handbook`,
`requirements`, `decisions`, or `templates` for the corresponding directory under `docs/`.

| Page                | Additional metadata                                                                                  |
|---------------------|------------------------------------------------------------------------------------------------------|
| Handbook            | Descriptive lowercase `id`; unique positive integer `sidebar_position`                               |
| Requirement         | `id: REQ-NNN`; `status`                                                                              |
| Decision            | `id: ADR-NNN`; `status`; real `date` in `YYYY-MM-DD` form                                            |
| Superseded decision | `superseded_by`: ID of an `accepted` or `superseded` replacement                                     |
| Template            | `template-` ID; change metadata and collection when copying; record status validation does not apply |

### Status and implementation

| Record      | Status       | Meaning                               |
|-------------|--------------|---------------------------------------|
| Requirement | `draft`      | Awaiting agreement                    |
| Requirement | `accepted`   | Agreed intent; delivery may have gaps |
| Requirement | `retired`    | No longer required                    |
| Decision    | `proposed`   | Under consideration                   |
| Decision    | `accepted`   | Agreed choice                         |
| Decision    | `rejected`   | Declined choice                       |
| Decision    | `superseded` | Replaced by another accepted decision |

Acceptance needs recorded maintainer or delegated agreement; passing checks does not grant it.

Preserve accepted decision reasoning. Record reversals in a new decision and update the old status and replacement link;
clarifications and typo fixes may update the existing record. Track delivery through requirement evidence and gaps.
Describe current implementation in the handbook and label planned behavior.

### Writing procedures and evidence

| Rule                                | Required practice                                                                                                                                                                                                     |
|-------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| State the scope                     | Identify the reader's task, promised result, prerequisites, and applicable software versions or environments.                                                                                                         |
| Make procedures executable          | Specify the working directory and required inputs. Separate copyable commands from output, preserve exact identifiers, and show expected results, effects on state, recovery, and cleanup where relevant.             |
| Support claims with evidence        | Link the relevant source or check. Distinguish inspected code, executed checks, proposals, and delivered behavior. Label assumptions and gaps; use disposable examples and keep credentials out of recorded evidence. |
| Make content accessible             | Use meaningful headings and links, readable examples, and text alternatives for informative images. Check navigation and the rendered meaning of changed content.                                                     |
| Maintain documentation with changes | Update the owning document with behavior changes, preserve important URLs and anchors, and explain documentation impact. Update authored inputs and regenerate derived output.                                        |
| Write consistently                  | Use direct language, stable terminology, and exact technical identifiers. Assume technical competence while explaining knowledge specific to go42.                                                                    |

Use **go42** for the upstream blueprint, **go42x** for its orchestration tool, and **application** for the adopted project.
Use **local handbook** for its effective instructions. Name the imported commit or release when referring to the upstream
revision. Keep exact identifiers in commands, configuration, and API examples. Use language-tagged code fences, explain
placeholders, and keep prompt characters outside copyable commands.

Keep the authoritative local explanation with its subject and link to it. Maintain complete instructions for the
application's actual environment and backends. Prefer a common workflow across operating systems and record the platforms
actually verified. Put input requirements, state changes, failure checks, and cleanup beside the affected steps.

Record each check's scope, command or method, environment, relevant software revision, observed result, and unverified
gaps. Review headings, links, keyboard navigation, and the meaning of rendered examples where relevant. Preserve the
metadata, agreement, and delivery distinctions defined above; label historical inference and unresolved contradictions.

When adopting a changed shared rule, update this local policy and its edition together. Record the scope and reason for
local exceptions. Changes that affect reusable guidance also need review in go42-docs and, where relevant, go42x.

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
task docs:check
```

Preview with `task docs:serve`. Checks cover Markdown, prose, metadata, index coverage and order, links, rendered anchors,
documentation tooling tests, and type checks. Review accuracy and completeness separately; run the application checks
needed to substantiate changed instructions. A passing documentation build does not prove runtime behavior.

Run these commands from the repository root. Stop the preview server with Ctrl+C. After editing, inspect the rendered
page's headings, examples, links, and keyboard navigation. The [documentation lint job](../../.github/workflows/110-lint.yaml)
runs `task docs:check` in CI. Update Task definitions, CI callers, and this policy together when their commands or coverage
change.

### Procedure evidence

Rerun affected procedures when commands, prerequisites, defaults, or expected outcomes change. Start from the documented
state and retain the date, documentation and application revisions, relevant go42x version, local modifications,
OS/architecture, tools/backends, commands, expected and observed results, recovery, cleanup, and remaining gaps.
Record only the fields relevant to the check; distinguish source review, execution, and reader observation.

Keep detailed results with the task or implementation review. Link durable evidence from the owning handbook or
requirement and update the procedure's applicability after a successful rehearsal. Earlier results remain evidence for
their original revision and environment. A wording-only edit requires documentation checks and relevant rendered review.

For a documentation failure, report the page, failing step, relevant revisions and environment, and expected and actual
results. Remove credentials from retained output. Verify the correction with the affected procedure before reporting it
as resolved.

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
