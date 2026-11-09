---
name: go42x-skills
description: Create, change, or share agent skills in a project managed by go42x, where skill sources live in .go42x/skills and generation installs them for each agent. Use when asked to add or edit a skill, to use project details in a skill, to reuse skills from another repository, or when a skill does not appear in an agent.
---

# Agent skills with go42x

A skill is a directory with a `SKILL.md` file and optional supporting files. Agents see only each skill's name
and description until a task matches; then they load the rest.

In this project, skill sources live in `.go42x/{{ .skills.dir }}/`, set by `context.skills-dir` in
`.go42x/go42x.yaml`. `go42x agentenv generate` installs every skill in the directories that enabled agents read:

| Directory | Providers |
| --------- | --------- |{{ range .skills.targets }}
| `{{ .dir }}/` | {{ join .providers ", " }} |{{ end }}

Claude Code reads `.claude/skills/`; Codex, Antigravity, and Copilot read `.agents/skills/`; gh-aw workflows get
both, for whichever engine a run uses. Copilot also reads `.claude/skills/`, so with Claude and Copilot both enabled
it sees each skill twice.

Edit the sources, not the copies. Generation replaces the copies it wrote, removes copies whose source is gone,
and never touches skills added to those directories by hand. Each directory keeps a `.go42x-skills.json` list of
the files that generation wrote and a `.gitignore` block that hides the copies from Git, so only skills added by
hand show up as changes.

Skills that go42x ships, such as this one, are replaced by `go42x agentenv update`; the backup keeps local edits.
To customize one, copy it under a new name.

## Write a skill

1. Create `.go42x/{{ .skills.dir }}/<name>/SKILL.md`, or `SKILL.tpl.md` for a template skill. The directory
   name must equal the skill name: lowercase letters, digits, and single hyphens, at most 64 characters.
2. Start the file with front matter:

   ```markdown
   ---
   name: release-notes
   description: Draft release notes from merged changes. Use when asked for release notes or a changelog entry.
   ---
   ```

   The description is all an agent reads before choosing the skill. Say what the skill does and when to use it,
   in words people will type, within 1024 characters.
3. Write the instructions below the front matter. Keep `SKILL.md` short and move long references or scripts into
   subdirectories that `SKILL.md` links to. Copies are not executable, so call scripts through their interpreter,
   for example `sh scripts/check.sh`.
4. Run `go42x agentenv generate`. Hidden files and directories are not copied.

## Use project details

Name the main file `SKILL.tpl.md` to make the skill a template. Generation renders it, and every other `*.tpl.md`
file in the skill, as a Go template and writes the result without `.tpl`: `SKILL.tpl.md` becomes `SKILL.md` and
`references/setup.tpl.md` becomes `references/setup.md`. Other files are copied unchanged, as is every file of a
skill with a plain `SKILL.md`.

Templates see the same data as the shared `AGENTS.md` instructions; `.go42x/agents.tpl.md` shows the fields. For
example:

```markdown
{{ `Run the checks for {{ .project.name }} from {{ .git.root }}.
Write scratch files to {{ .context.TempDir }}.
{{ if .mcp.gopls }}Use the gopls MCP tools to navigate Go code.{{ end }}
{{ range .skills.targets }}{{ .dir }} is read by {{ join .providers ", " }}.{{ end }}` }}
```

Front matter is checked after rendering. Each copy is rendered for the environment that generates it and stays out
of Git. Write {{ "`{{\"{{\"}}`" }} where a template skill needs literal braces.

## Share skills between projects

Keep shared skills in their own repository and vendor them into `.go42x/{{ .skills.dir }}/`. Add the
dependency to `.go42x/.vendor.yaml`, next to the files that go42x vendors, and map each skill directory:

```yaml
dependencies:
  team-skills:
    source:
      type: git
      url: https://example.com/team/agent-skills.git
      ref: main
    directories:
      - from: release-notes
        to: {{ .skills.dir }}/release-notes
```

Destinations are relative to `.go42x/`. Run `go42x vendor update --manifest .go42x/.vendor.yaml`, then
`go42x agentenv generate`. Use `files` instead of `directories` to pick single files.

Vendored files are overwritten on vendor update, so change them in the shared repository. A vendored skill with a
plain `SKILL.md` is installed exactly as fetched. `go42x agentenv update` keeps your dependencies and rebuilds the
lock; `go42x agentenv init --force` replaces `.go42x/.vendor.yaml`, and only the backup keeps them.
