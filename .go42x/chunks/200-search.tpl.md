### Searching documentation and code

Use `rg --files` to locate files and `rg` for text searches. Narrow searches
before reading large files. Read relevant source before editing and preserve
unrelated work. Verify indexed results against current source.

Use only tools exposed in the current session. Tool names below are raw names;
clients may add a server prefix. When a service is unavailable, continue with
local file search and source inspection.
{{- if .mcp.go42x }}

#### Project knowledge

| When                                           | Tool              |
| ---------------------------------------------- | ----------------- |
| Load project guidance for affected paths       | `project_context` |
| Read a project document by its authored ID     | `docs_get`        |
| Find documentation to review for changed paths | `docs_impact`     |
| Discover documentation, code, or configuration | `kwb_search`      |
| Read current source lines for a search result  | `kwb_get_file`    |
| Browse indexed files                           | `kwb_list_files`  |
| Inspect index availability and scope           | `kwb_stats`       |

Start with `project_context` for affected project-relative paths. It reads supplied
files directly; directory paths select documentation impact. `project_context`,
`docs_get`, and `docs_impact` work without a search index. Use search and
language-server tools to discover declarations beyond supplied files.

Respect document status and replacement chains. Draft and proposed records
require agreement before implementation. Review documentation affected by a
change; missing impact links do not prove that no documentation needs updating.

Use focused plain-text queries and narrow by kind, language, or project-relative
path when useful. Search results reflect the last index build; file reads reflect
current source. Read matching source before following definitions or references.

Follow continuation pointers and inspect coverage diagnostics. Narrow truncated
searches; if the index generation changes during pagination, restart the search.

#### Index maintenance

Run `task x -- kwb build` from the project root after changing indexed files or
exclusions, or when the index is missing or stale. After changing agent
configuration or templates, run `task x -- agentenv generate` before building the
index. Builds update changed files and remove deleted or newly excluded files.
A running MCP server picks up the updated index on its next request. Use
`task x -- kwb build --rebuild` when a full rebuild is needed.

Use `task x -- kwb check --json` to check freshness without updating the index.
Exit status 0 confirms a complete, fresh scan. Status 1 means the index is stale,
missing, unavailable, or the scan could not confirm freshness. Inspect reported
completeness, truncation, and diagnostics before relying on the index; address
diagnostics for incomplete or unavailable scans.

Indexing respects project `.gitignore` files and built-in filters for generated
state, binary files, and tool or build directories. Authored `.go42x`
configuration, instruction templates, vendored Markdown, and `.env.example` can
be indexed. Generated agent files and local overrides are excluded.

Keep custom root-relative Git ignore patterns in `.go42x/kwb.ignore`, after the
managed block. `!` exceptions can override bundled rules, but cannot include
files excluded by `.gitignore` or built-in filters.

#### Knowledge-base configuration

Project documents live under the configured documentation entrypoint's directory.
The default entrypoint is `docs/README.md`; set `project.documentation-entrypoint`
in `.go42x/go42x.yaml` to change it. Document IDs and status come from YAML front
matter. Markdown references outside that directory remain searchable.

The MCP server's `--context-doc` argument selects authored document IDs to include
as project guidance; `--search-timeout` controls read deadlines. Set these
arguments in the project's MCP definition. Regenerate agent configuration and
restart the client after changing them. Use `task x` for both generation and the
local MCP server.

Consult `task x -- kwb --help` and `task x -- mcp --help` for supported
commands and options.
{{- end }}
{{- if .mcp.gopls }}

#### Go navigation and diagnostics

Before changing a function signature, shared type, or interface, use
`go_symbol_references` and read affected callers, implementations, and tests.

Use `go_file_context` to understand a file's dependencies on its package.

Use `go_package_api` to inspect an unfamiliar package's public API.

After a coherent batch of saved Go edits, run `go_diagnostics` for the changed
files. Fix errors introduced by the change and rerun diagnostics after fixes.

Language-server results reflect the loaded workspace and build configuration.
Run the project's applicable lint and tests, including affected callers, build
tags, and platforms. Report check results and unresolved verification gaps.
{{- end }}
{{- if .mcp.context7 }}

#### External documentation

Check dependency manifests or lockfiles for the version in use before consulting
Context7 for library APIs, setup, or examples.

Use `resolve-library-id` to find the matching library unless the user supplied
a valid Context7 library ID.

Use `query-docs` with the resolved or supplied ID and a focused question.
Prefer a matching version when available; do not guess library IDs.

When Context7 is unavailable or lacks the needed information, use official
documentation and installed source. Verify examples against the project's
dependency version and existing usage.
{{- end }}
