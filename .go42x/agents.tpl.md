# Project: {{ .project.name }}

{{ .project.description }}

Primary language: {{ .project.language }}
{{ if .project.tags }}
Tags: {{ join .project.tags ", " }}
{{ end }}
{{ if .project.metadata }}
{{ range $key, $value := .project.metadata -}}
- {{ $key }}: {{ $value }}
{{ end }}
{{ end }}

{{ with .git -}}
## Repository

Root: {{ .root }}
{{ with .remote }}
Remote: {{ . }}
{{ end }}
Read the current branch, commit, and working-tree status with Git when needed.

{{ end -}}
{{ with .environment -}}
## Environment

Platform: {{ .os }}/{{ .arch }}
{{ with .working_dir }}
Working directory: {{ . }}
{{ end }}
{{ with .go_required_version }}
Required Go version (go.mod): {{ . }}
{{ end }}
{{ with .variables }}
Configured environment variables:

{{ range $key, $value := . -}}
- {{ $key }} = {{ printf "%q" $value }}
{{ end }}
{{ end }}

{{ end -}}
{{ with .github_actions -}}
## GitHub Actions

{{ with .repository }}{{ with .full_name }}Repository: {{ . }}
{{ end }}{{ end -}}
{{ with .event }}{{ with .name }}Event: {{ . }}
{{ end }}{{ with .action }}Action: {{ . }}
{{ end }}{{ end -}}
{{ with .ref }}Checkout ref: {{ . }}
{{ end -}}
{{ with .build_url }}Run: {{ . }}
{{ end }}
{{ with .pull_request -}}
### Pull request{{ with .number }} #{{ . }}{{ end }}

{{ with .title }}{{ . }}
{{ end }}
{{ with .url }}URL: {{ . }}
{{ end -}}
{{ with .head }}Source branch: {{ . }}
{{ end -}}
{{ with .base }}Target branch: {{ . }}
{{ end }}
{{ with .body }}{{ . }}
{{ end }}
{{ end -}}
{{ with .issue -}}
### Issue{{ with .number }} #{{ . }}{{ end }}

{{ with .title }}{{ . }}
{{ end }}
{{ with .url }}URL: {{ . }}
{{ end }}
{{ with .body }}{{ . }}
{{ end }}
{{ end -}}
{{ with .user_request -}}
### Requested task

{{ . }}

{{ end -}}
{{ end -}}
{{ with .mcp -}}
## MCP servers

{{ range $name, $server := . -}}
- {{ $name }} ({{ $server.Transport }})
{{ end }}
All tools exposed by these enabled servers are available through MCP.

{{ end -}}
## Instructions

{{ .chunks }}
