---
id: documentation
title: Writing application documentation
collection: handbook
sidebar_position: 1
---

# Writing application documentation

Write docs that help someone run, change, or troubleshoot this application. Start at the
[documentation index](../README.md) and update the page that covers your subject.

## Documentation ownership

Application contributors maintain the docs for this checkout, including its settings, changes, and operating procedures.

## Shared writing rules

Use these rules when writing or reviewing docs, including work done with AI assistance.

- Start with the reader's task. Say what they will learn or do and what they need first.
- Use plain language. Keep sentences short and explain unfamiliar terms.
- Make examples easy to use. Say where to run commands, explain placeholders, and show the expected result.
  Keep commands and output in separate code blocks. Use exact command names, flags, and paths, with sample values
  instead of secrets.
- Check your instructions. Try the steps you describe. Say what you have not tested and label planned features.
- Make pages easy to scan. Use clear headings, useful link text, and descriptions for images.
- Update docs with the code. Fix affected instructions and links in the same change.

## Change workflow for contributors

Update docs in the same change as the behavior they describe:

- Change the handbook when commands, settings, or procedures change.
- Update a requirement when the needed behavior changes.
- Write a decision when an important choice needs an explanation.

Read the [application profile](02-project.md), [conventions](03-conventions.md), and relevant pages before starting. Compare
instructions with the code and tests. If they disagree, find out which needs fixing. Confirm changes to agreed
requirements or decisions with the maintainers.

Check related guidance in go42x and the go42 guide when the same change affects their readers. In the change description,
say which docs changed, what you checked, and anything still untested. Keep detailed logs with the change.

## Creating a document

Use an existing page when it covers the topic. For a new page:

1. Copy the matching template into the destination folder. Create the folder if it does not exist yet.
2. Replace the sample header fields and prompts. Use one H1 heading that matches the page title.
3. Write the explanation or steps. Choose useful headings and remove sections you do not need.
4. Add the page to the [documentation index](../README.md).
5. Run the [checks](#verification).

| Template                                   | Use it for                                | Filename                              |
| ------------------------------------------ | ----------------------------------------- | ------------------------------------- |
| [Handbook](../templates/handbook.md)       | How things work and how to do a task      | `docs/handbook/NN-topic.md`           |
| [Requirement](../templates/requirement.md) | A result the application needs to deliver | `docs/requirements/NNN-capability.md` |
| [Decision](../templates/decision.md)       | An important choice and why it was made   | `docs/decisions/NNN-choice.md`        |

### Metadata and filenames

The YAML header at the top of a page is called front matter. Every page needs `id`, `title`, and `collection`.
Use a unique ID that starts with a letter and contains only letters, numbers, and hyphens. Keep it when a page is renamed
or moved. Choose the next unused number for a requirement or decision, and never reuse an old ID.

In this checkout, prefix handbook filenames with their `sidebar_position`, padded to two digits: `01-documentation.md`,
`02-project.md`, and so on. Keep the prefix in sync when changing the position, and update links to the renamed page.

The `collection` value matches the folder under `docs/`: `handbook`, `requirements`, `decisions`, or `templates`.
The index uses `collection: overview`. An optional `related` list contains IDs of other documents that exist.

| Page        | Other header fields                                                                       |
| ----------- | ----------------------------------------------------------------------------------------- |
| Handbook    | A descriptive lowercase `id` and an unused positive integer for `sidebar_position`        |
| Requirement | An ID such as `REQ-001` and a `status`                                                    |
| Decision    | An ID such as `ADR-001`, a `status`, and a real `date` written as `YYYY-MM-DD`            |
| Template    | An ID starting with `template-`; replace the sample fields and collection when copying it |

### Status and implementation

Use these statuses to show what the team has agreed:

| Document    | Status       | Meaning                                  |
| ----------- | ------------ | ---------------------------------------- |
| Requirement | `draft`      | Still being discussed                    |
| Requirement | `accepted`   | Agreed, but may not be fully implemented |
| Requirement | `retired`    | No longer needed                         |
| Decision    | `proposed`   | Still being discussed                    |
| Decision    | `accepted`   | Agreed                                   |
| Decision    | `rejected`   | Considered and declined                  |
| Decision    | `superseded` | Replaced by another decision             |

Mark a requirement or decision `accepted` after the maintainers or someone they authorize agree to it. Passing tests
alone does not mean a proposal has been accepted.

Keep one requirement for each feature or problem. Give its criteria stable labels such as `REQ-001.1`, and record what
has been built or tested and what is still missing. Several changes can contribute to the same requirement.

When a decision changes, create a new one and mark the old one `superseded`. Set the old page's `superseded_by` to the
replacement ID. The replacement must be `accepted` or itself `superseded`, and the links must not form a loop. Use
`superseded_by` only on superseded decisions. Keep the original reasoning; fix typos and clarify wording in place.

### Maintaining the index

List every page once under its matching heading: **Handbook**, **Requirements**, **Decisions**, or **Templates**.
Each table row has two cells: one link to the document and a short description.

Order handbook pages by `sidebar_position`, from lowest to highest; gaps are fine. Order requirements and decisions by
number. Keep retired, rejected, and superseded pages in the index. For an empty group, keep the heading and a message
linking to its template. Replace the message with a table when adding the first page.

Update the index when a page is added, moved, renamed, removed, reordered, or given a different purpose.

## Verification

Set up the tools through [environment setup](05-development.md#environment-setup). From the repository root, run:

```sh
task docs:check
```

This checks Markdown, prose, page headers, the index, and links. It also runs the documentation tooling tests, builds
the website, and checks types and published links. Fix any failures and rerun the check.

Tables must use leading and trailing pipes, aligned columns, the same number of cells in each row, and blank lines
around the table. Check them with `task lint:markdown`. The Markdown formatter cannot fix column alignment; use your
editor's table formatter or align the pipes manually.

Use `task docs:serve` to read the built pages locally. Check headings, examples, links, and keyboard navigation. Stop the
server with Ctrl+C.

When you change instructions, try the affected steps from the starting point you describe. A website build cannot tell
whether an application command works. In the review, say what you ran, what happened, and what you could not test.
Include environment details when they affect the result. Keep secrets out of examples and logs.

## Source files and publishing

Edit files in `docs/`. Use relative links to other source files, including `.md` for Markdown pages. When moving a page,
fix links to it and links inside it. Store images in `pages/static/img/` and link to them from the source page.

The [assembler](../../pages/assemble.mjs) generates `pages/docs/`; the website build writes `pages/build/`. Edit the
original files rather than those generated copies. Website configuration lives in `pages/`.

The [publishing workflow](../../.github/workflows/210-github-pages.yaml) publishes changes from `master` using this
repository's files. It adds source links to the generated pages.

When the shared writing rules change, update their copies in the go42 guide and go42x too. Explain any rule this
application needs to apply differently.
