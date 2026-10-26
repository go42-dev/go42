---
id: go42-blueprint
title: go42 blueprint
---

# go42 blueprint

## Purpose

This is set of instructions needed to setup newly created application repository from the go42 blueprint.

## GitHub and agentic workflow setup

Keep preparation actions and workflow sources in the new repository. Update the application identity and repository
paths in `.go42x/go42x.yaml`, Taskfile, and the release workflows while preserving application-specific configuration.
The agentic MCP containers use the published go42x tools image; they do not use the application's Docker image.

Configure the [required workflow variables and secrets](../docs/handbook/github-configuration.md) for the new repository.
Organization variables and secrets can be shared with selected repositories. Keep the release private key in the
`release` environment, restricted to `master`, and avoid a repository or organization copy of that key.

Install the agentic App on the new repository with Contents, Issues, and Pull requests write access for its safe outputs.
The runtime agent keeps read permissions; write operations use separate App-backed jobs. Install the release App with
Contents and Workflows write access. Grant the repository access to the published go42x GHCR package if required.

Review the engine and triggers in `.github/workflows/910-aw-description.md` and `920-aw-agent.md`. They use Codex;
model variables do not select another engine. Manual dispatch is not required: pull request events refresh descriptions,
and `/agent` comments start agent requests for trusted contributors. Native gh-aw budget and model overrides remain
available through the variables referenced in the compiled workflows; no project-specific reasoning-effort mapping is
introduced.

Install the pinned tools with `task setup`, then run `task generate:agentic`, `task lint:agentic`, and
`task lint:actions`. Review and commit workflow Markdown, `900-aw-mcp.md`, compiled `.lock.yml` files, and
`.github/aw/actions-lock.json` together. Ensure the go42x version selected by mise has a published matching Docker image
before activating these workflows. Use the application's development guide for ongoing workflow and vendor updates.

## Operations

Types of operations needed to complete the adoption of the go42 blueprint.

### Replace a value

Replace all field placeholder occurrences `{{.GO42_*}}` with the valid value.

- Placeholder names always start with `.GO42_` and are in uppercase.
- Placeholders can appear in any file, including code, configuration, and documentation.

### Replace a section

Put matching comments around content that needs application-specific prose:

```markdown
<!-- go42:replace:start id-go42-app-purpose -->

Describe the application's problem, intended users, and main responsibilities.

<!-- go42:replace:end id-go42-app-purpose -->
```

### Remove a section

```markdown
<!-- go42:remove:start id-go42-blueprint-setup -->

Fill in the project details and complete the adoption checklist.

<!-- go42:remove:end id-go42-blueprint-setup -->
```
