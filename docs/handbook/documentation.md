---
id: documentation
title: Maintaining application documentation
sidebar_position: 3
---

# Maintaining application documentation

## Purpose and scope

Keep enough durable context in this repository for a new person or AI to understand the application, change it, verify
the change, and operate it. Git history and pull requests preserve delivery history; these documents preserve the
knowledge that future work depends on.

This policy is the local documentation contract. It ships as a go42 default and can evolve with the application.
Start at the [documentation index](../README.md) for the application profile, conventions, and relevant documents.

## Documentation ownership

Maintain this application's purpose, requirements, decisions, development instructions, deployment details, and runbooks
in this repository. This responsibility belongs to the application's contributors and AI agents.

Go42's author solely maintains the external [go42 operational guide](https://github.com/go42-dev/go42-docs) for the go42
project. Users can consult it online; cloning go42 or an application does not include that repository. Application work
does not require cloning, accessing, or updating it. Documentation checks and the website build use this checkout's sources.

The handbook must describe the effective local setup, including inherited defaults. Include the instructions needed for
routine development and operation, and use links to the external guide for additional explanation. Identify the applicable
version when referring to version-specific guidance.

The local conventions, templates, and documentation policy are inherited defaults that this application owns after
adoption. Review them when adopting or upgrading go42; they must describe the rules used by the checked-out code.

## Document types

| Type | Question | Contents | Update rule |
| --- | --- | --- | --- |
| Handbook | How does it work today? | Behavior, architecture, instructions | Update with code |
| Requirement | What must it do? | Outcomes, scope, criteria, evidence | Revise when intent changes |
| Decision | Why this approach? | Context, options, choice, consequences | Supersede when replaced |

A current architecture diagram belongs in the handbook. A reliability target belongs in requirements. The reasoning
behind an architectural choice belongs in a decision. Link the documents when they concern the same capability.

Keep one living requirement document per capability or concern. Several pull requests may implement or revise it.
Create a decision record for choices affecting system boundaries, dependencies, persistence, security, delivery policy,
or another area where later changes need the reasoning. Routine changes can follow existing decisions.

## Maintaining the index

Keep [the documentation index](../README.md) complete. List every handbook page, requirement, and decision under its
collection, with a link and a one-line purpose. List templates separately. Keep statuses, ownership, and other record
metadata in the documents themselves.

Update the index in the same change when a document is added, renamed, removed, or its purpose changes. Retain entries
for retired requirements and rejected or superseded decisions so their history remains discoverable.

## Creating a document

1. Read the existing documents about the affected subject and the [conventions](conventions.md).
2. Choose the collection using the table above. Update an existing document when it already owns the subject.
3. Copy the appropriate template from the table below into that collection.
4. Replace its ID, title, heading, and prompts with supported information. Remove instructions and irrelevant sections.
5. Link related documents and verification evidence. Record assumptions, unknowns, and outstanding work explicitly.
6. Add the document to the [documentation index](../README.md).
7. Run the documentation checks described below.

Templates are reusable starting points for new documents:

| Template | Destination |
| --- | --- |
| [Handbook](../templates/handbook.md) | `docs/handbook/topic.md` |
| [Requirement](../templates/requirement.md) | `docs/requirements/NNN-capability.md` |
| [Decision](../templates/decision.md) | `docs/decisions/NNN-choice.md` |

For example, from the repository root:

```sh
cp docs/templates/requirement.md docs/requirements/002-releases.md
```

Set the copied ID to `REQ-002` and replace its title, heading, and prompts. Choose a different unused number if necessary.
Follow the [metadata rules](#metadata-and-filenames) and [acceptance workflow](#status-and-implementation).

Handbook pages can use the structure that fits their subject: steps and expected results for a procedure, responsibilities
and diagrams for architecture, or tables for reference material. Keep the required metadata and adapt the body.

Document the effective local setup and the instructions needed for routine development and operation, including inherited
defaults. External guidance can provide additional explanation. Identify its applicable version when instructions depend
on a particular go42 release, and keep the application's actual commands and settings in its handbook.

Requirements need observable acceptance criteria, including important failure behavior and relevant constraints. Use
stable criterion identifiers such as `REQ-001.1` so tests and reviews can refer to them. Record meaningful verification
evidence or an explicit gap. Include metrics, stakeholders, and timelines only when they are known and useful.

Decisions need the alternatives and tradeoffs that explain the choice. Link implementation and operating details to
their current sources. When reconstructing a past choice, distinguish evidence from inference and identify unknown
reasoning.

Record choices made for this application. Upstream explanations provide context for inherited defaults. The initial
[documentation requirement](../requirements/001-documentation.md) and
[local documentation proposal](../decisions/001-documentation-model.md) are filled drafts for application adoption.
Review their applicability before acceptance.

## Metadata and filenames

Every page has YAML front matter containing `id` and `title`. IDs are unique across published pages and remain stable
when titles change. Use lowercase descriptive IDs for handbook pages, `REQ-001` for requirements, and `ADR-001` for
decisions. Allocate the next unused number in each record collection and check for collisions before merging.

Use descriptive filenames such as `handbook/deployment.md`, `requirements/002-releases.md`, and
`decisions/002-release-trigger.md`. Keep record numbers when renaming files; do not reuse retired IDs.

Requirements and decisions also require `status`. Decisions require a recorded `date` in `YYYY-MM-DD` form. An optional
`related` list contains the IDs of related published documents. A superseded decision requires `superseded_by`, containing
the replacement decision's ID. The site renders these fields and checks their references.

Templates have `template-` IDs and are published separately as authoring resources. Change the ID before using a template
as an application record. Templates are excluded from requirement and decision status validation.

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

Acceptance records an actual decision by the maintainer or the authority delegated for the task. Writing a draft or
passing a test alone does not establish acceptance. Identify the decision or review in the record's context or related
links. Preserve material reasoning after acceptance; record later reversals in a new decision and update the old status
and replacement link. Typo fixes and clarifications can update an existing record.

Record delivery separately through a requirement's verification evidence and outstanding gaps. The handbook describes
the checked-out implementation and labels planned behavior explicitly. Resolve disagreements between requirements,
documentation, and code; an observed defect does not silently change the agreed behavior.

## Change workflow for people and AI

1. Read this policy, the application profile, and the relevant requirements, decisions, and conventions.
2. Identify changes to desired behavior, significant choices, and current instructions or explanations.
3. Update the affected sources in this repository alongside the implementation. Keep each fact in one authoritative
    place and link to it. Apply the [index maintenance rules](#maintaining-the-index) when documents change.
4. Verify changed claims against code, configuration, tests, or observed results. State relevant gaps and limitations.

Do not invent stakeholders, targets, dates, approvals, or historical reasoning to fill a template. Read relevant linked
material as needed rather than loading the entire decision history for every change.

## Verification

From the repository root, install the pinned tools through `make setup-common setup-linters`, then run:

```sh
make docs-check
```

This runs Markdown linting and Vale, installs the locked website dependencies, tests the documentation tooling, validates
metadata and local file links, checks TypeScript, and builds the site with strict page and anchor checks. CI runs the same
checks through its `docs-lint` job. Markdown linting and Vale scan only `docs/`.

For a quick metadata and source-link check after installing dependencies:

```sh
npm --prefix pages run validate-docs
```

Preview the built website with:

```sh
make docs-serve
```

Document any application-specific verification required by the change. CI checks document structure and links; reviewers
assess meaning, scope, and whether the recorded evidence supports the claims.

## Source files and publishing

The [documentation index](../README.md) is the single entry point for application documentation and the published homepage.
Author pages under `docs/handbook/`, `docs/requirements/`, and `docs/decisions/`; templates live under `docs/templates/`.
The [project conventions](conventions.md) are maintained and published as a handbook page.

Use relative Markdown links to actual source files, including the `.md` extension for documents. The assembler adapts
links for the site and links other repository files to the source revision. Keep page links out of code blocks when they
are intended to be navigable. The generated OpenAPI view is available from site navigation.

Keep documentation images and the website logo in `pages/static/img/`. Docusaurus publishes `pages/static/` with the site.
Link to images using paths relative to the source document. For example, a handbook page references a shared image with
`../../pages/static/img/diagram.svg`. The assembler adjusts image paths to reference these originals from generated pages.

Website tooling and configuration live in `pages/`. The [assembler](../../pages/assemble.mjs) generates `pages/docs/`.
The website builds to `pages/build/`. Generated content and build output are ignored by Git.

The [Pages workflow](../../.github/workflows/210-github-pages.yaml) builds and publishes on pushes to `master`. Pages
display the source revision used for the build; a local preview identifies uncommitted changes when present.

The assembler reads this repository's documentation sources under `docs/`. Links to go42-docs remain external web
links; validation, assembly, and publishing do not import its content or require its repository.

Source links use the CI repository or the local Git origin. Set `DOCS_REPOSITORY_URL` to the repository's web address when
building from an archive or a different hosting setup. `DOCS_REVISION` can select the source reference shown in links.

When adopting go42, review the policy, templates, conventions, and inherited records as local defaults. Record the imported
go42 revision in the application profile and document the effective local setup. The go42 operational guide remains an
external reference under its author's maintenance. Current application instructions and the policy enforced by this
repository stay here.
