# Project context

{{/*
The context block owns field selection, names, nesting, and order.
Use yamlValue for dynamic values and keys; it handles quoting and escaping.
The omit-empty option removes nulls, empty strings, and empty collections recursively.
False, zero, and whitespace-only strings are preserved. Omit the option to keep all values.
Instruction chunks below remain Markdown.
*/ -}}
{{ define "context" -}}
project:
  name: {{ yamlValue .project.name }}
  description: {{ yamlValue .project.description }}
  language: {{ yamlValue .project.language }}
  tags: {{ yamlValue .project.tags }}
  metadata: {{ yamlValue .project.metadata }}
repository:
  root: {{ yamlValue .git.root }}
  remote: {{ yamlValue .git.remote }}
environment:
  os: {{ yamlValue .environment.os }}
  arch: {{ yamlValue .environment.arch }}
  is_ci: {{ yamlValue .environment.is_ci }}
  ci_mode: {{ yamlValue .environment.ci_mode }}
  working_dir: {{ yamlValue .environment.working_dir }}
  variables: {{ yamlValue .environment.variables }}
golang:
  go_version: {{ yamlValue .golang.go_version }}
  env: {{ yamlValue .golang.env }}
github_actions:
  repository:
    full_name: {{ yamlValue .github_actions.repository.full_name }}
  actor:
    login: {{ yamlValue .github_actions.actor.login }}
  event:
    name: {{ yamlValue .github_actions.event.name }}
    action: {{ yamlValue .github_actions.event.action }}
  ref: {{ yamlValue (or .github_actions.ref .github_actions.ref_name) }}
  sha: {{ yamlValue .github_actions.sha }}
  build_url: {{ yamlValue .github_actions.build_url }}
  pull_request:
    number: {{ yamlValue .github_actions.pull_request.number }}
    title: {{ yamlValue .github_actions.pull_request.title }}
    url: {{ yamlValue .github_actions.pull_request.url }}
    head: {{ yamlValue .github_actions.pull_request.head }}
    base: {{ yamlValue .github_actions.pull_request.base }}
    body: {{ yamlValue .github_actions.pull_request.body }}
  issue:
    number: {{ yamlValue .github_actions.issue.number }}
    title: {{ yamlValue .github_actions.issue.title }}
    url: {{ yamlValue .github_actions.issue.url }}
    body: {{ yamlValue .github_actions.issue.body }}
  user_request: {{ yamlValue .github_actions.user_request }}
{{ $hasMCP := false }}
{{ range .mcp }}{{ if .Enabled }}{{ $hasMCP = true }}{{ end }}{{ end }}
{{ if $hasMCP }}
mcp:
  tools: all_exposed
  servers:
{{ range $name, $server := .mcp }}{{ if $server.Enabled }}
    {{ yamlValue $name }}:
      transport: {{ yamlValue $server.Transport }}
{{ end }}{{ end }}
{{ end }}
{{ end -}}

{{ yamlBlock "context" . "omit-empty" }}

## Instructions

{{ .chunks }}
