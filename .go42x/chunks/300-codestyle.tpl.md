### Go code style

- Follow explicit user instructions and project conventions before bundled defaults or
  upstream recommendations.

- Before editing or reviewing handwritten Go, read the relevant sections of
  `.go42x/resources/go-codestyle.md` for the project's defaults and examples.
  Generated code follows its generator's conventions.

#### Read detailed conventions on demand

- Use the table below to select guidance for the task. Paths are relative to the project
  root. Documents in `resources/` are maintained by go42x; documents in `upstream/` are
  vendored from external sources. Both are installed locally; no network access is needed.

- When available, use the knowledge-base tools from the `go42x` MCP server. Search with
  `kwb_search`, setting `kind="documentation"`, `language="md"`, and `path_prefix` to
  the selected reference. Read matching sections with `kwb_get_file` using their source
  line ranges. If the tools are unavailable or the reference is not indexed, use `rg`
  (ripgrep) to find headings or symbols and read the relevant source lines with a
  file-reading tool. Follow cross-references when more detail is needed. Read only the
  necessary sections; do not load every reference into the initial context.

| Task or question                                 | Local reference                               | Sections to find                                                                                                    |
| ------------------------------------------------ | --------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| Project Go defaults and patterns                 | `.go42x/resources/go-codestyle.md`            | Comments, Formatting, Readability, Naming and declarations, Errors, Resource ownership, Testing, Functional options |
| Choosing package names and package boundaries    | `.go42x/upstream/go/package-names.md`         | Package names, Naming package contents, Package paths, Bad package names                                            |
| Naming exported APIs, getters, and interfaces    | `.go42x/upstream/go/effective-go.md`          | Package names, Getters, Interface names, MixedCaps                                                                  |
| Common review issues: names, imports, APIs       | `.go42x/upstream/go/code-review-comments.md`  | Imports, Initialisms, Interfaces, Receiver Type, Contexts, Handle Errors                                            |
| Choosing a Google style document                 | `.go42x/upstream/google-go/index.md`          | About, Documents                                                                                                    |
| Readability, simplicity, formatting, consistency | `.go42x/upstream/google-go/guide.md`          | Style principles, Core guidelines, Local consistency                                                                |
| Detailed rules for code, comments, errors, tests | `.go42x/upstream/google-go/decisions.md`      | Naming, Commentary, Imports, Errors, Language, Contexts, Test structure                                             |
| Practical examples and design tradeoffs          | `.go42x/upstream/google-go/best-practices.md` | Error handling, Documentation, Function argument lists, Tests, Interfaces                                           |
