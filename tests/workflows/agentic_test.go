package workflows_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type compiledWorkflow struct {
	Jobs map[string]compiledJob `yaml:"jobs"`
}

type compiledJob struct {
	Permissions map[string]string `yaml:"permissions"`
	Steps       []compiledStep    `yaml:"steps"`
}

type compiledStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	With map[string]any    `yaml:"with"`
	Env  map[string]string `yaml:"env"`
	Run  string            `yaml:"run"`
}

func TestCompiledWorkflowBoundaries(t *testing.T) {
	for _, name := range []string{"920-aw-agent", "910-aw-description"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("../..", ".github/workflows", name+".lock.yml"))
			if err != nil {
				t.Fatal(err)
			}
			var workflow compiledWorkflow
			if err := yaml.Unmarshal(data, &workflow); err != nil {
				t.Fatal(err)
			}
			agent, exists := workflow.Jobs["agent"]
			if !exists {
				t.Fatal("missing agent job")
			}
			for permission, level := range agent.Permissions {
				if level == "write" {
					t.Errorf("agent has write permission %s", permission)
				}
			}
			var order []string
			for _, step := range agent.Steps {
				if step.Name == "Checkout PR branch" {
					t.Fatal("untrusted PR checkout precedes runtime preparation")
				}
				if strings.Contains(step.Run, "AI_APP_PRIVATE_KEY") ||
					strings.Contains(step.Run, "AI_APP_CLIENT_ID") ||
					strings.Contains(fmt.Sprint(step.With, step.Env), "AI_APP_PRIVATE_KEY") {
					t.Fatal("App credentials exposed to the agent job")
				}
				if step.Uses == "./.github/actions/ai-prepare-env" {
					if fmt.Sprint(step.With["pull-mcp-image"]) != "true" {
						t.Fatalf("agent preparation does not enable MCP image pulling: %v", step.With)
					}
					if setup, exists := step.With["setup-task"]; exists && setup != "setup" {
						t.Fatalf("agent preparation overrides the full development setup: %v", setup)
					}
				}
				switch step.Name {
				case "Prepare agent environment", "Start MCP Gateway":
					order = append(order, step.Name)
				}
			}
			if !reflect.DeepEqual(
				order,
				[]string{"Prepare agent environment", "Start MCP Gateway"},
			) {
				t.Fatalf("runtime order = %v", order)
			}
			foundApp := false
			for _, job := range workflow.Jobs {
				for _, step := range job.Steps {
					if !strings.HasPrefix(step.Uses, "actions/create-github-app-token@") {
						continue
					}
					foundApp = true
					if step.With["client-id"] != "${{ vars.AI_APP_CLIENT_ID }}" ||
						step.With["private-key"] != "${{ secrets.AI_APP_PRIVATE_KEY }}" ||
						step.With["repositories"] != "${{ github.event.repository.name }}" {
						t.Fatalf("App token is not configured and scoped to this repository: %v", step.With)
					}
					if _, exists := step.With["permission-administration"]; exists {
						t.Fatal("App token requests an unconfigured administration permission")
					}
				}
			}
			if !foundApp {
				t.Fatal("safe outputs have no App token")
			}
			for _, command := range []string{"go42x", "gopls", "mise"} {
				if !strings.Contains(string(data), `"entrypoint": "`+command+`"`) {
					t.Errorf("missing native container entrypoint %s", command)
				}
			}
			if strings.Contains(string(data), "8765/mcp") || strings.Contains(string(data), "supergateway") {
				t.Fatal("compiled workflow still uses a stdio bridge")
			}

		})
	}
}

func TestMCPImageMatchesMiseVersion(t *testing.T) {
	data, err := os.ReadFile("../../.github/actions/ai-prepare-env/action.yml")
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Runs struct {
			Steps []compiledStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatal(err)
	}

	var script string
	for _, step := range action.Runs.Steps {
		if step.Name == "Pull MCP tools image" {
			script = step.Run
		}
	}
	if len(script) == 0 {
		t.Fatal("missing MCP image preparation script")
	}

	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tt := range []struct {
		name    string
		version string
		wantTag string
		changes map[string]string
	}{
		{
			name:    "release",
			version: "1.2.3",
			wantTag: "v1.2.3",
		},
		{
			name:    "prefixed release",
			version: "v1.2.3",
			wantTag: "v1.2.3",
		},
		{
			name:    "prerelease",
			version: "1.2.3-rc.1",
			wantTag: "v1.2.3-rc.1",
		},
		{name: "missing version"},
		{
			name:    "development version",
			version: "dev",
		},
		{
			name:    "multiple versions",
			version: "1.2.3 1.2.4",
		},
		{
			name:    "mise failure",
			version: "1.2.3",
			changes: map[string]string{"MOCK_MISE_STATUS": "1"},
		},
		{
			name:    "image unavailable",
			version: "1.2.3",
			changes: map[string]string{"MOCK_PULL_STATUS": "1"},
		},
		{
			name:    "missing digest",
			version: "1.2.3",
			changes: map[string]string{"MOCK_IMAGE_DIGEST": "ghcr.io/go42-dev/go42x@"},
		},
		{
			name:    "different repository",
			version: "1.2.3",
			changes: map[string]string{"MOCK_IMAGE_DIGEST": "ghcr.io/other/image@" + digest},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, stub := range map[string]string{
				"mise": `#!/bin/bash
set -euo pipefail
[[ "$*" == 'current go:github.com/go42-dev/go42x' ]] || exit 2
printf '%s\n' "$MOCK_MISE_VERSION"
exit "$MOCK_MISE_STATUS"
`,
				"docker": `#!/bin/bash
set -euo pipefail
printf '%s\n' "$*" >> "$MOCK_DOCKER_LOG"
case "$*" in
  'pull '*) exit "$MOCK_PULL_STATUS" ;;
  'image inspect --format {{index .RepoDigests 0}} '*) printf '%s\n' "$MOCK_IMAGE_DIGEST" ;;
  *) exit 2 ;;
esac
`,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(stub), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			environment := map[string]string{
				"PATH":              dir,
				"MOCK_MISE_VERSION": tt.version,
				"MOCK_MISE_STATUS":  "0",
				"MOCK_PULL_STATUS":  "0",
				"MOCK_IMAGE_DIGEST": "ghcr.io/go42-dev/go42x@" + digest,
				"MOCK_DOCKER_LOG":   filepath.Join(dir, "docker.log"),
				"GITHUB_ENV":        filepath.Join(dir, "github.env"),
			}
			for key, value := range tt.changes {
				environment[key] = value
			}
			command := exec.CommandContext(t.Context(), "bash", "-c", script)
			for key, value := range environment {
				command.Env = append(command.Env, key+"="+value)
			}

			output, err := command.CombinedOutput()

			if len(tt.wantTag) == 0 {
				if err == nil {
					t.Fatalf("image preparation unexpectedly succeeded: %s", output)
				}
				if _, err := os.ReadFile(environment["GITHUB_ENV"]); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed image preparation published an environment value: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("image preparation: %v\n%s", err, output)
			}
			published, err := os.ReadFile(environment["GITHUB_ENV"])
			if err != nil {
				t.Fatal(err)
			}
			want := "GO42X_MCP_IMAGE_VERSION=" + tt.wantTag + "@" + digest + "\n"
			if string(published) != want {
				t.Fatalf("published environment = %q, want %q", published, want)
			}
			calls, err := os.ReadFile(environment["MOCK_DOCKER_LOG"])
			if err != nil {
				t.Fatal(err)
			}
			wantCalls := "pull ghcr.io/go42-dev/go42x:" + tt.wantTag + "\n" +
				"image inspect --format {{index .RepoDigests 0}} ghcr.io/go42-dev/go42x:" + tt.wantTag + "\n"
			if string(calls) != wantCalls {
				t.Fatalf("Docker calls = %q, want %q", calls, wantCalls)
			}
		})
	}
}
