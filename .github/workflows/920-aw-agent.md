---
name: Agent request
description: Answer /agent requests from trusted contributors on issues and pull requests.
on:
  slash_command:
    name: agent
    events: [issue_comment, pull_request_comment]
  roles: [admin, maintainer, write]
  github-app:
    client-id: ${{ vars.AI_APP_CLIENT_ID }}
    private-key: ${{ secrets.AI_APP_PRIVATE_KEY }}
checkout:
  pull-request: false
  repository: ${{ github.repository }}
  ref: ${{ github.event.repository.default_branch }}
  fetch-depth: 0
  fetch: ["refs/pulls/open/*"]
permissions:
  actions: read
  contents: read
  packages: read
  issues: read
  pull-requests: read
engine:
  id: codex
  config: |
    project_doc_max_bytes = 65536
timeout-minutes: 45
imports:
  - 900-aw-mcp.md
pre-agent-steps:
  - name: Prepare agent environment
    uses: ./.github/actions/ai-prepare-env
    with:
      pull-mcp-image: true
network:
  allowed: [defaults, github, go, mcp.context7.com]
tools:
  github:
    toolsets: [repos, issues, pull_requests, actions]
  bash: ["*"]
  edit:
safe-outputs:
  github-app:
    client-id: ${{ vars.AI_APP_CLIENT_ID }}
    private-key: ${{ secrets.AI_APP_PRIVATE_KEY }}
  add-comment:
    target: triggering
    max: 1
  create-pull-request:
    draft: true
    max: 1
    fallback-as-issue: false
  push-to-pull-request-branch:
    target: triggering
    max: 1
    check-branch-protection: false
    fallback-as-pull-request: false
---

The authorized request is the text following `/agent` in this triggering comment:

${{ steps.sanitized.outputs.text }}

Read the issue or pull request and relevant discussion with GitHub tools.

For an implementation request on an issue, propose the change through one draft
pull request. For a request on a same-repository pull request, use GitHub tools
to identify its current head, use the already fetched pull request ref, and apply
the requested change on that branch. Publish through the
`push_to_pull_request_branch` safe output. For a fork pull request, explain the
proposed change in a comment; do not push into the contributor's fork.
The request authorizes the requested implementation and the corresponding safe
output. Never merge, release, or change repository settings.

Use safe-output tools for every GitHub write. Answer questions or report the
implementation result in one comment. Empty or unclear requests should receive
a short clarification comment without code changes.
