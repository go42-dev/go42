### Operations

- Ignore anything between `[IGNORE]` and `[/IGNORE]` tags in prompts.
- Never modify root README.md without explicit instructions.

### Intermediate storage

Use `.build` directory for intermediate storage of files, artifacts, reports, and other outputs generated during
the operation.

### Side effect protection in interactive sessions

- **NEVER** commit changes to the repository unless explicitly instructed to do so.
- When working with external cli tools, any command which will produce side effects, for example:
  `git push`, `git commit`, `docker push`, `kubectl apply`, etc.
  You **MUST ALWAYS** ask for confirmation before executing the command.

#### Communication guidelines

- **Clarity**: Be concise and direct in explanations.
- **Structure**: Use consistent formatting for code, documentation, responses and reports.
- **Context**: Provide reasoning for technical decisions.
- **Progress**: Communicate status during long-running operations.
