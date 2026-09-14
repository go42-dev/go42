### Searching documentation and code

Use the tools available in the current session. Prefer `rg --files` to locate
files and `rg` for literal or regular-expression searches within them. Narrow
searches by path, filename, language, or symbol before reading large files.
Read relevant source before editing and preserve unrelated work.

For code relationships, use an available language server to find definitions,
references, implementations, type information, and call relationships. For
documentation and broad code discovery, use an available knowledge-base search.
Confirm indexed results against current source before making changes, especially
for recently edited files.

MCP tool names in these instructions are raw tool names. Select the matching
tool exposed by the named server in your client; clients may add their own prefix.
Only call tools exposed in the current session. When a service is unavailable,
continue with local file search and source inspection.

{{ if or (hasMCPTool .mcp "gopls" "go_symbol_references") (hasMCPTool .mcp "gopls" "go_file_context") (hasMCPTool .mcp "gopls" "go_package_api") (hasMCPTool .mcp "gopls" "go_diagnostics") -}}

#### Go navigation and diagnostics

Use the available `gopls` tools to understand the impact of Go changes and check
saved edits. Results reflect the loaded workspace and build configuration;
review other affected build tags or platforms through the project's checks.

{{ if hasMCPTool .mcp "gopls" "go_symbol_references" -}}
Before changing a function signature, shared type, or interface, use
`go_symbol_references` with the declaration's `file` and `symbol`. Read affected
callers, implementations, and tests before editing.
{{ end }}
{{ if hasMCPTool .mcp "gopls" "go_file_context" -}}
Use `go_file_context` with `file` when you need to understand declarations used
from other files in the same package.
{{ end }}
{{ if hasMCPTool .mcp "gopls" "go_package_api" -}}
Use `go_package_api` with `packagePaths` when you need the public API of an
unfamiliar package. Supply Go import paths.
{{ end }}
{{ if hasMCPTool .mcp "gopls" "go_diagnostics" -}}
After a coherent batch of saved Go edits, run `go_diagnostics` with `files`
containing the changed files' absolute paths. Investigate relevant diagnostics,
fix errors introduced by the change, and rerun diagnostics after fixes.
Standalone `gopls mcp` sees saved files on disk, so save edits before checking.
{{ end }}

Before completing the change, run the project's prescribed lint and test
commands for the changed packages and affected callers. Diagnostics supplement
those checks; report their scope and any unresolved errors or unavailable tools.
{{ end }}

{{if hasMCPTool .mcp "go42x" "project_context"}}
Use `project_context` with the task and affected project-relative paths to load
project guidance and relevant evidence. Follow source continuation pointers when
needed. Check coverage diagnostics before relying on retrieval results.
{{end}}
{{if hasMCPTool .mcp "go42x" "docs_get"}}
Use `docs_get` to read a requirement, decision, or handbook page by its authored ID.
Documents can live anywhere in the project. YAML front matter supplies `id`,
`title`, and optional `collection`; directory names do not determine document type.
The documentation entrypoint defaults to `docs/README.md`; configure another path
with `go42x mcp --docs-entrypoint`. Discovery respects `.gitignore` files.
Draft and proposed records require agreement before implementation. Acceptance
alone does not establish delivery; retain the status and replacement chain when
using historical records.
{{end}}
{{if hasMCPTool .mcp "go42x" "docs_impact"}}
Use `docs_impact` with changed paths to find linked documentation to review.
Unmapped paths still need judgment: missing links do not prove that documentation
is unaffected. Maintain the application's local docs according to its policy.
{{end}}

{{ if or (hasMCPTool .mcp "go42x" "kwb_search") (hasMCPTool .mcp "go42x" "kwb_get_file") (hasMCPTool .mcp "go42x" "kwb_list_files") (hasMCPTool .mcp "go42x" "kwb_stats") -}}

#### Knowledge-base search

Use the knowledge-base tools from the `go42x` MCP server to find documentation
sections, code declarations, configuration keys, examples, and related files.

The knowledge base indexes Markdown by heading and Go by declaration. Other text
formats use bounded line chunks. Exact symbols, headings, and filenames receive
more weight than body matches. Code identifiers can also match component words:
`NewHTTPServer` is searchable as `http server`.

| Tool                         | Arguments                                                              | Result                                                                               |
|------------------------------|------------------------------------------------------------------------|--------------------------------------------------------------------------------------|
{{ if hasMCPTool .mcp "go42x" "kwb_search" -}}
| `kwb_search`     | `query`; optional `kind`, `language`, `path_prefix`, `limit`, `offset` | Ranked snippets with path, title, kind, language, score, and source line ranges      |
{{ end -}}
{{ if hasMCPTool .mcp "go42x" "kwb_get_file" -}}
| `kwb_get_file`   | `path`; optional `start_line`, `end_line`                              | Current source lines and the next line to read                                       |
{{ end -}}
{{ if hasMCPTool .mcp "go42x" "kwb_list_files" -}}
| `kwb_list_files` | Optional `type`, `language`, `path_prefix`, `limit`, `offset`          | Unique files in path order, total, and the next offset                               |
{{ end -}}
{{ if hasMCPTool .mcp "go42x" "kwb_stats" -}}
| `kwb_stats`      | None                                                                   | File count (`document_count`), chunk count, project root, index path, and generation |
{{ end }}

These tools return structured JSON with a matching JSON text fallback.

Search queries are plain text. Use `kind="documentation"` for prose,
`kind="code"` for source, or `kind="config"` for configuration. The file listing
uses the argument `type` for the same categories. `language` narrows further,
for example `go`, `md`, `ts`, or `json`. `path_prefix` is a project-relative prefix.

{{ if and (hasMCPTool .mcp "go42x" "kwb_search") (hasMCPTool .mcp "go42x" "kwb_get_file") -}}
Example workflow:

1. Search with `query="http server", kind="code", language="go"`.
2. Read a result using its `path`, `start_line`, and `end_line`.
3. Use an available Go language server to resolve references or implementations.
4. Follow `next_offset` to paginate or `next_start_line` to continue reading.
{{ end }}

Search defaults to 10 results and allows at most 100 per page. Search offsets
are limited to 10,000; `window_limited=true` means filters are needed to reach
additional matches. File listing defaults
to 100 files and allows up to 500 per page, without the former 1,000-file cutoff.
If the returned `generation` changes between pages, restart pagination to avoid
mixing snapshots. Search totals count chunks; file-list totals count unique files.

File reads default to 200 lines and are limited to 500 lines and 64 KiB per
response. Line numbers are one-based and inclusive. Paths resolve relative to
the indexed project root, including when the server starts elsewhere. File reads
reflect current source; search results reflect the last completed index update.

Run `go42x kwb build` after changing files. It hashes eligible source files, indexes
only changed files, and removes deleted or newly ignored files. An unchanged
project does not publish another index. Use `go42x kwb build --rebuild` for a full rebuild.
The old index remains available until the new generation is published, and a
running MCP server picks it up on its next request.
Run `go42x kwb` or `go42x kwb --help` to list the available subcommands.

Indexing respects `.gitignore` files inside the selected root, including nested
rules and negation. It excludes its own index directory, build/tool directories,
binary files, symlinks, generated Go files, and common lockfiles. Use
`--include-ext=.xyz` to add an extension, or a filename such as `Makefile.custom`.
The index is local and requires no embedding service.

Set `go42x mcp --search-timeout=5s` or `GO42X_SEARCH_TIMEOUT=5s` to control the
maximum duration of knowledge-base reads. Flags override `GO42X_*` environment
variables. Build settings include `--root`, `--index`, `--exclude-dir`,
`--include-ext`, and `--rebuild` (`GO42X_REBUILD=true`).
{{ end }}
