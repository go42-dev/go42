### Operation

- Ignore anything between `[IGNORE]` and `[/IGNORE]` tags in prompts.
- Never modify root README.md without explicit instructions.

### Intermediate storage

Use `.build` directory for intermediate storage of files, artifacts, reports, and other outputs generated during
the operation.

### Persisting changes

Never commit changes to the repository unless explicitly instructed to do so in interactive sessions.
When in CI (autonomous) mode, commit changes in separate branches and create pull requests for review.

#### Communication guidelines

- **Clarity**: Be concise and direct in explanations.
- **Structure**: Use consistent formatting for code, documentation, responses and reports.
- **Context**: Provide reasoning for technical decisions.
- **Progress**: Communicate status during long-running operations.
