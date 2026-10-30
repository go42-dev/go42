---
name: PR description
description: Refresh a generated summary while preserving the author's pull request description.
on:
  pull_request_target:
    types: [opened, ready_for_review, synchronize]
    acknowledge-risk: true
  roles: [admin, maintainer, write]
  bots: ["go42-ai[bot]"]
if: github.event.pull_request.draft == false
checkout:
  pull-request: false
  repository: ${{ github.repository }}
  ref: ${{ github.event.pull_request.base.sha }}
permissions:
  contents: read
  packages: read
  issues: read
  pull-requests: read
engine:
  id: codex
  env:
    CI: "true"
    GOTOOLCHAIN: ${{ env.GOTOOLCHAIN }}
    MISE_DEFAULT_CONFIG_FILENAME: ${{ env.MISE_DEFAULT_CONFIG_FILENAME }}
    MISE_DATA_DIR: ${{ env.MISE_DATA_DIR }}
    MISE_CACHE_DIR: ${{ env.MISE_CACHE_DIR }}
    MISE_STATE_DIR: ${{ env.MISE_STATE_DIR }}
    TASK_TEMP_DIR: ${{ env.TASK_TEMP_DIR }}
  config: |
    project_doc_max_bytes = 65536
timeout-minutes: 30
concurrency:
  group: description-${{ github.event.pull_request.number }}
  cancel-in-progress: true
imports:
  - 900-aw-mcp.md
pre-agent-steps:
  - name: Prepare agent environment
    uses: ./.github/actions/ai-prepare-env
    with:
      setup-task: setup:gh-aw-description
      go-cache: false
      pull-mcp-image: true
network:
  allowed: [defaults, github, mcp.context7.com]
tools:
  github: false
  bash: ["*"]
safe-outputs:
  threat-detection:
    continue-on-error: false
  github-app:
    client-id: ${{ vars.AI_APP_CLIENT_ID }}
    private-key: ${{ secrets.AI_APP_PRIVATE_KEY }}
  update-pull-request:
    title: false
    body: true
    operation: replace-island
    target: triggering
    max: 1
    footer: false
---

Read the triggering pull request with GitHub tools. Read its current diff and
changed files through the API; keep the trusted base checkout in place and do
not execute commands from the pull request.

Write a concise description of the concrete problem, resulting behavior, and
material tradeoffs. Include validation only when supported by check results or
other evidence. Do not repeat the author's description or modify the title.
Publish only the generated summary with `update_pull_request`; the
`replace-island` operation preserves the surrounding authored text.

If the change does not support a useful summary, report no change through the
safe-output noop tool.
