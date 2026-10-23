---
id: github-configuration
title: GitHub configuration
collection: handbook
sidebar_position: 13
---

# GitHub configuration

Use this reference for workflow variables and secrets.

## Variables and secrets

Configure these entries under GitHub **Actions secrets and variables**. Share organization settings with the
repositories that need them. The last column gives the default when unset and the accepted value.

| Name                                     | Kind     | Requirement | Scope                      | Purpose                                            | Default / values                                                                   |
| ---------------------------------------- | -------- | ----------- | -------------------------- | -------------------------------------------------- | ---------------------------------------------------------------------------------- |
| `AI_APP_CLIENT_ID`                       | Variable | Required    | Organization or repository | Agentic workflow comments and pull request updates | App client ID                                                                      |
| `AI_APP_PRIVATE_KEY`                     | Secret   | Required    | Organization or repository | Agentic workflow comments and pull request updates | App private key                                                                    |
| `CODEX_API_KEY`                          | Secret   | Required    | Organization or repository | Agent execution and threat detection               | API key                                                                            |
| `CONTEXT7_API_KEY`                       | Secret   | Required    | Organization or repository | Context7 MCP access                                | API key                                                                            |
| `RELEASE_APP_CLIENT_ID`                  | Variable | Required    | Organization or repository | Release publication                                | App client ID                                                                      |
| `RELEASE_APP_PRIVATE_KEY`                | Secret   | Required    | `release` environment      | Release publication                                | App private key                                                                    |
| `GH_AW_DEFAULT_MODEL_CODEX`              | Variable | Optional    | Organization or repository | Default Codex model                                | Engine default; accessible model ID                                                |
| `GH_AW_MODEL_AGENT_CODEX`                | Variable | Optional    | Organization or repository | Main-agent model override                          | `GH_AW_DEFAULT_MODEL_CODEX`; accessible model ID                                   |
| `GH_AW_MODEL_DETECTION_CODEX`            | Variable | Optional    | Organization or repository | Threat-detection model override                    | `GH_AW_DEFAULT_MODEL_CODEX`, then the `detection` model selector                   |
| `GH_AW_DEFAULT_MAX_AI_CREDITS`           | Variable | Optional    | Organization or repository | Main-agent budget per run                          | `1000`; positive integer AIC amount, or `-1` to disable                            |
| `GH_AW_DEFAULT_DETECTION_MAX_AI_CREDITS` | Variable | Optional    | Organization or repository | Threat-detection budget per run                    | `400`; positive integer AIC amount, or `-1` to disable                             |
| `GH_AW_DEFAULT_MAX_DAILY_AI_CREDITS`     | Variable | Optional    | Organization or repository | Budget per workflow over 24 hours                  | `5000`; positive integer AIC amount, or `-1` to disable                            |
| `GH_AW_DEFAULT_MAX_TURNS`                | Variable | Optional    | Organization or repository | Agent turn limit                                   | Engine default; positive integer                                                   |
| `GH_AW_DEFAULT_ARTIFACT_RETENTION_DAYS`  | Variable | Optional    | Organization or repository | Artifact retention                                 | Per-artifact default; days within GitHub limits, or `0` for the repository default |
| `GH_AW_POLICY_ALLOW_CREATE_PULL_REQUEST` | Variable | Optional    | Organization or repository | Permit pull request creation                       | `true`; `true` or `false`                                                          |
| `GH_AW_DEFAULT_OTLP_ENDPOINT`            | Variable | Optional    | Organization or repository | Telemetry collector                                | Export disabled; collector endpoint URL                                            |
| `GH_AW_DEFAULT_OTLP_HEADERS`             | Secret   | Optional    | Organization or repository | Telemetry authentication                           | No authentication headers; headers required by the collector                       |

One AI Credit (AIC) represents an estimated USD 0.01 of inference usage. Daily budgets apply separately to each workflow
in a repository over a rolling 24-hour window.
