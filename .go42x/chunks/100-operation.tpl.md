### Prompt conventions

- Ignore anything between `[IGNORE]` and `[/IGNORE]` tags in prompts.

- Treat standalone `---` lines in user messages as logical separators between
  topics or requests. Read all sections together, carry applicable instructions
  across them, and address every requested item. Preserve literal uses inside
  code blocks and quoted content.

### Communication

- Clarity: Be concise and direct in explanations.

- Context: Provide reasoning for technical decisions.

- Progress: During long-running operations, give updates on findings,
  blockers, and next steps.

- Results: State what was completed, what was checked, and any unresolved issues
  or uncertainty.

  Example: "Updated the configuration. Local validation passed. Deployment is
  pending."

- Review: When presenting completed work, help a human understand and review
  the change. Explain what changed and why, how the affected parts connect,
  and where to start reviewing. Link to relevant files or symbols in a suggested
  reading order, explaining each one's role. Include a concrete example when
  useful. Report checks actually performed, their results, and any remaining
  limitations or required next steps.

  Use a terminal-readable text diagram when it helps explain execution or data
  flow. Use nested boxes to show component boundaries and arrows to show flow.
  Adapt the layout to the change, and identify its components in the linked
  review path.

  Use the following template. Keep the detail proportional to the change;
  combine or omit sections that do not apply.

  ````markdown
  Implemented <change> so that <result or benefit>.

  How it works:

  <Brief explanation of the flow and key design decisions.>

  ```text
                   ┌───────────────────────────┐
                   │       <Entry point>       │
                   │       <file:symbol>       │
                   └─────────────┬─────────────┘
                                 │
                                 ▼
        ┌─────────────────────────────────────────────────┐
        │ <Main component>                                │
        │                                                 │
        │  ┌─────────────────┐       ┌─────────────────┐  │
        │  │ <Step 1>        │ ────► │ <Step 2>        │  │
        │  │ <purpose>       │       │ <purpose>       │  │
        │  └─────────────────┘       └─────────────────┘  │
        │                                                 │
        └────────────────────────┬────────────────────────┘
                                 │
                    ┌────────────┴─────────────┐
                    │                          │
                    ▼                          ▼
        ┌───────────────────────┐  ┌───────────────────────┐
        │ <Output / consumer A> │  │ <Output / consumer B> │
        │ <file:symbol>         │  │ <file:symbol>         │
        └───────────────────────┘  └───────────────────────┘
  ```

  Review in this order:

  1. [<entry point>](<path:line>) — where the behavior starts.
  2. [<main implementation>](<path:line>) — how it processes the input.
  3. [<consumer or output>](<path:line>) — how the result is used.
  4. [<tests>](<path:line>) — which behaviors and failure paths are covered.

  Example:

  - Input or action: <concrete example>.
  - Expected result: <observable behavior>.

  Verification:

  - <check performed> — <actual result>.
  - <material check not performed> — <reason, if relevant>.

  Remaining:

  <Known limitations, unresolved issues, or required next steps. Omit if none.>
  ````

- Links: Include direct links to deliverables and relevant source locations when
  reporting results.

  Examples (replace sample paths and line numbers with actual locations):

  ```markdown
  - Deliverable: [Review report](/path/to/project/.build/review/report.md).
  - Source: [LoadConfig](/path/to/project/internal/config/config.go:42).
  - Tests: [TestLoadConfig](/path/to/project/internal/config/config_test.go:80).
  ```

### Intermediate storage

- Use the `{{ .context.TempDir }}` directory at the project root for intermediate
  files, artifacts, and reports.

- Keep scratch files for each task in a separate, clearly named subdirectory.

- Before finishing a task, review this directory and remove files and directories
  that are unlikely to be needed again, such as temporary scripts, scratch files,
  duplicate outputs, and obsolete artifacts from earlier tasks. Keep requested
  deliverables, reports needed for review, and files used by ongoing work.
  Leave files whose purpose is unclear untouched.

### Side effect protection in interactive sessions

- Never modify root README.md without explicit instructions.

- Never commit changes to the repository unless explicitly instructed to do so.

- Always ask for confirmation before changing the state of external
  systems. Isolated test runs and temporary test resources may proceed
  without confirmation when they leave existing system configuration
  and operational resources unchanged and do not disrupt normal operation.
  After testing, clean up only temporary test resources you created for the
  current task, such as pods, containers, and background processes. Report
  cleanup failures and any resources left running.

  Examples requiring confirmation: `git push`, `docker push`, changing
  repository settings, deploying to a live environment, or stopping,
  restarting, or deleting an existing operational pod.

  Examples allowed without confirmation: running local tests, triggering
  an isolated test workflow, forwarding a port for testing, or creating
  and removing an isolated temporary test pod.

- When requesting confirmation, state the action, its exact target,
  and the expected effect.

  Example: "Restart deployment `api` in namespace `backend` on cluster
  `production`, replacing its running pods."

- If an external change fails or times out, inspect the resulting state
  before retrying. Report partial changes and any uncertainty.

  Example: if a restart request times out, check whether the workload
  already restarted before sending another restart request.

- Never modify repository files or external systems when the user asks
  only for an explanation, review, opinion, or advice. Treat requests to
  implement or fix something as action requests, even when phrased as questions.

  Examples requiring no changes: "Why is this failing?", "Review this code",
  or "What do you think of this design?"

  Examples requesting changes: "Can you fix this?", "Update these instructions",
  or "Review this code and fix the bugs you find."
