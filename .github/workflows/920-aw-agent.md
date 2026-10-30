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
timeout-minutes: 45
imports:
  - 900-aw-mcp.md
pre-agent-steps:
  - name: Prepare agent environment
    uses: ./.github/actions/ai-prepare-env
    with:
      pull-mcp-image: true
network:
  allowed: [defaults, github, go, node, mcp.context7.com]
tools:
  github: false
  bash: ["*"]
  edit:
safe-outputs:
  threat-detection:
    continue-on-error: false
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
to identify its current head, then switch to that head using the already fetched
pull request ref. Verify the checked-out commit before reading implementation
files or requesting project context. If the branch changes tool or module
requirements, ensure its Go toolchain is available and run `task setup` again.
After switching, run `task x -- agentenv generate --providers=` and
`task x -- kwb build` before requesting project context. Apply the requested
change on that branch.
Publish through the
`push_to_pull_request_branch` safe output. For a fork pull request, explain the
proposed change in a comment; do not push into the contributor's fork.
The request authorizes the requested implementation and the corresponding safe
output. Never merge, release, or change repository settings.

Use safe-output tools for every GitHub write. Answer questions or report the
implementation result in one comment. Empty or unclear requests should receive
a short clarification comment without code changes.

For new pull requests, use safe-output pull request references and list file
paths. For existing pull requests, link to the pull request's Files changed
view. Publication can rename branches and recreate commits, so local branch
names and commit hashes do not identify the published change.
