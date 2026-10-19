package workflows_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestDockerCleanupProtectsOtherTags(t *testing.T) {
	script := dockerWorkflowScript(t, "160-cleanup.yaml", "cleanup", "Delete temporary Docker image")
	version := dockerRegistryVersion(1, "2026-01-01T00:00:00Z", "ci-123-1")
	for _, tt := range []struct {
		name        string
		tag         string
		versions    []map[string]any
		current     map[string]any
		deleteError string
		valid       bool
		deleted     bool
		warning     bool
	}{
		{
			name:     "exact CI tag",
			tag:      "ci-123-1",
			versions: []map[string]any{version},
			current:  version,
			valid:    true,
			deleted:  true,
		},
		{
			name:        "last tagged version",
			tag:         "ci-123-1",
			versions:    []map[string]any{version},
			current:     version,
			deleteError: "gh: You cannot delete the last tagged version of a package. You must delete the package instead. (HTTP 400)",
			valid:       true,
			warning:     true,
		},
		{
			name:        "other bad request",
			tag:         "ci-123-1",
			versions:    []map[string]any{version},
			current:     version,
			deleteError: "gh: Invalid package version (HTTP 400)",
		},
		{
			name:        "permission denied",
			tag:         "ci-123-1",
			versions:    []map[string]any{version},
			current:     version,
			deleteError: "gh: Resource not accessible by integration (HTTP 403)",
		},
		{
			name:     "already absent",
			tag:      "ci-123-1",
			versions: []map[string]any{},
			valid:    true,
		},
		{
			name:     "release tag",
			tag:      "v1.2.3",
			versions: []map[string]any{version},
		},
		{
			name:     "latest tag",
			tag:      "latest",
			versions: []map[string]any{version},
		},
		{
			name: "shared release",
			tag:  "ci-123-1",
			versions: []map[string]any{
				dockerRegistryVersion(1, "2026-01-01T00:00:00Z", "ci-123-1", "v1.2.3"),
			},
		},
		{
			name:     "duplicate matches",
			tag:      "ci-123-1",
			versions: []map[string]any{version, version},
		},
		{
			name:     "concurrent retag",
			tag:      "ci-123-1",
			versions: []map[string]any{version},
			current:  dockerRegistryVersion(1, "2026-01-01T00:00:00Z", "ci-123-1", "latest"),
		},
		{
			name:     "changed digest",
			tag:      "ci-123-1",
			versions: []map[string]any{version},
			current:  dockerRegistryVersion(2, "2026-01-01T00:00:00Z", "ci-123-1"),
		},
		{
			name:     "malformed response",
			tag:      "ci-123-1",
			versions: []map[string]any{{"id": 1}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := dockerRegistryFixture(t, tt.versions, tt.current)
			output, err := runDockerCleanup(t, dir, script, map[string]string{
				"TARGET_TAG":   tt.tag,
				"DELETE_ERROR": tt.deleteError,
			})
			if (err == nil) != tt.valid {
				t.Fatalf("cleanup: %v\n%s", err, output)
			}
			deleted, err := os.ReadFile(filepath.Join(dir, "deleted"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if (len(deleted) > 0) != tt.deleted {
				t.Fatalf("unexpected deletions: %s\n%s", deleted, output)
			}
			if strings.Contains(string(output), "::warning::Retaining CI tag") != tt.warning {
				t.Fatalf("unexpected cleanup warning: %s", output)
			}
			if len(tt.deleteError) > 0 && !tt.warning && !strings.Contains(string(output), tt.deleteError) {
				t.Fatalf("cleanup hid the API error: %s", output)
			}
		})
	}
}

func TestDockerRegistryRetention(t *testing.T) {
	script := dockerWorkflowScript(t, "400-cleanup-registry.yaml", "cleanup-registry", "Cleanup old CI images")
	old := dockerRegistryVersion(1, "2025-01-01T00:00:00Z", "ci-123-1")
	versions := []map[string]any{
		old,
		dockerRegistryVersion(2, "2026-02-01T00:00:00Z", "ci-124-1"),
		dockerRegistryVersion(3, "2026-03-01T00:00:00Z", "ci-125-1"),
		dockerRegistryVersion(4, "2025-01-01T00:00:00Z", "v1.2.3"),
		dockerRegistryVersion(5, "2025-01-01T00:00:00Z", "ci-120-1", "latest"),
		dockerRegistryVersion(6, "2025-01-01T00:00:00Z"),
	}
	for _, tt := range []struct {
		name    string
		dryRun  string
		keep    string
		current map[string]any
		valid   bool
		deleted bool
	}{
		{
			name:    "old CI only",
			dryRun:  "false",
			keep:    "1",
			current: old,
			valid:   true,
			deleted: true,
		},
		{
			name:   "dry run",
			dryRun: "true",
			keep:   "1",
			valid:  true,
		},
		{
			name:   "keep count",
			dryRun: "false",
			keep:   "3",
			valid:  true,
		},
		{
			name:    "concurrent retag",
			dryRun:  "false",
			keep:    "1",
			current: dockerRegistryVersion(1, "2025-01-01T00:00:00Z", "ci-123-1", "v1.2.3"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := dockerRegistryFixture(t, versions, tt.current)
			output, err := runDockerCleanup(t, dir, script, map[string]string{
				"DRY_RUN":        tt.dryRun,
				"KEEP_VERSIONS":  tt.keep,
				"RETENTION_DAYS": "30",
				"DEBUG_MODE":     "false",
			})
			if (err == nil) != tt.valid {
				t.Fatalf("retention cleanup: %v\n%s", err, output)
			}
			deleted, err := os.ReadFile(filepath.Join(dir, "deleted"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			want := ""
			if tt.deleted {
				want = "orgs/go42-dev/packages/container/go42/versions/1\n"
			}
			if string(deleted) != want {
				t.Fatalf("deleted %q, want %q\n%s", deleted, want, output)
			}
		})
	}
}

func TestDockerScanInputs(t *testing.T) {
	script := dockerWorkflowScript(
		t,
		"140-security-extra.yaml",
		"trivy",
		"Validate image reference",
	)
	reference := "ghcr.io/go42-dev/go42@sha256:" + strings.Repeat("a", 64)
	for _, tt := range []struct {
		name      string
		reference string
		valid     bool
	}{
		{
			name:      "immutable digest",
			reference: reference,
			valid:     true,
		},
		{
			name:      "mutable tag",
			reference: "ghcr.io/go42-dev/go42:latest",
		},
		{
			name:      "different registry",
			reference: "docker.io/go42-dev/go42@sha256:" + strings.Repeat("a", 64),
		},
		{
			name:      "short digest",
			reference: strings.TrimSuffix(reference, "a"),
		},
		{
			name:      "invalid digest",
			reference: "ghcr.io/go42-dev/go42@sha256:" + strings.Repeat("g", 64),
		},
		{
			name: "empty reference",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), "bash", "-c", script)
			command.Env = []string{
				"PATH=" + os.Getenv("PATH"),
				"IMAGE_REFERENCE=" + tt.reference,
			}
			output, err := command.CombinedOutput()
			if (err == nil) != tt.valid {
				t.Fatalf("scan input validation: %v\n%s", err, output)
			}
		})
	}
}

func TestDockerScanArchitectures(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("../..", ".github/workflows/140-security-extra.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Strategy struct {
				Matrix struct {
					Arch []string `yaml:"arch"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
			Env map[string]string `yaml:"env"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}

	scan := workflow.Jobs["trivy"]
	if !slices.Equal(scan.Strategy.Matrix.Arch, []string{"amd64", "arm64"}) {
		t.Fatalf("scan architectures = %v, want amd64 and arm64", scan.Strategy.Matrix.Arch)
	}
	if scan.Env["TRIVY_PLATFORM"] != "linux/${{ matrix.arch }}" {
		t.Fatalf("scan platform = %q, want the matrix architecture", scan.Env["TRIVY_PLATFORM"])
	}
}

func TestDockerLatestPromotion(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("../..", ".github/workflows/300-release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			If    string   `yaml:"if"`
			Needs []string `yaml:"needs"`
			Uses  string   `yaml:"uses"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	promotion := workflow.Jobs["promote-docker"]
	const promotionCondition = "needs.validate-release.outputs.is_prerelease == 'false'"
	if promotion.If != promotionCondition {
		t.Fatalf("latest promotion must require a successful stable release: %s", promotion.If)
	}
	if len(promotion.Uses) > 0 {
		t.Fatal("latest promotion must be defined in the release workflow")
	}
	for _, dependency := range []string{"validate-release", "docker-build", "publish-release"} {
		if !slices.Contains(promotion.Needs, dependency) {
			t.Fatalf("latest promotion must wait for %s", dependency)
		}
	}

	if !slices.Equal(workflow.Jobs["docker-build"].Needs, []string{"validate-release"}) {
		t.Fatal("Docker publication must follow validation and precede release publication")
	}
	if !slices.Contains(workflow.Jobs["publish-release"].Needs, "docker-build") {
		t.Fatal("release publication must wait for Docker publication and attestations")
	}

	data, err = os.ReadFile(filepath.Join("../..", ".github/workflows/140-docker-build.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var build compiledWorkflow
	if err := yaml.Unmarshal(data, &build); err != nil {
		t.Fatal(err)
	}
	for _, step := range build.Jobs["docker-build"].Steps {
		if step.Name == "Publish stable image as latest" {
			t.Fatal("the reusable Docker build workflow must not promote latest")
		}
	}

	script := dockerWorkflowScript(t, "300-release.yaml", "promote-docker", "Publish stable image as latest")
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tt := range []struct {
		name    string
		latest  string
		failure string
		valid   bool
	}{
		{
			name:   "same manifest",
			latest: digest,
			valid:  true,
		},
		{
			name:   "different manifest",
			latest: "sha256:" + strings.Repeat("b", 64),
		},
		{
			name:    "registry promotion failure",
			latest:  digest,
			failure: "create",
		},
		{
			name:    "registry verification failure",
			latest:  digest,
			failure: "inspect",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			mock := `#!/bin/bash
set -euo pipefail
case "$*" in
  'buildx imagetools create --tag ghcr.io/go42-dev/go42:latest ghcr.io/go42-dev/go42@'*)
    if [ "$MOCK_FAILURE" = create ]; then exit 1; fi
    ;;
  'buildx imagetools inspect --format {{.Manifest.Digest}} ghcr.io/go42-dev/go42:latest')
    if [ "$MOCK_FAILURE" = inspect ]; then exit 1; fi
    echo "$MOCK_LATEST_DIGEST"
    ;;
  *) echo "Unexpected Docker command: $*" >&2; exit 1 ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(mock), 0o700); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(t.Context(), "bash", "-c", script)
			command.Env = []string{
				"PATH=" + dir,
				"IMAGE_NAME=ghcr.io/go42-dev/go42",
				"IMAGE_REFERENCE=ghcr.io/go42-dev/go42@" + digest,
				"IMAGE_DIGEST=" + digest,
				"MOCK_LATEST_DIGEST=" + tt.latest,
				"MOCK_FAILURE=" + tt.failure,
				"GITHUB_STEP_SUMMARY=" + filepath.Join(dir, "summary"),
			}
			output, err := command.CombinedOutput()
			if (err == nil) != tt.valid {
				t.Fatalf("latest promotion: %v\n%s", err, output)
			}

			summary, err := os.ReadFile(filepath.Join(dir, "summary"))
			if tt.valid {
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(summary), "## Docker image promotion") ||
					!strings.Contains(string(summary), "ghcr.io/go42-dev/go42@"+digest) {
					t.Fatalf("promotion summary must identify the promoted image: %s", summary)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("failed promotion must not publish a success summary: %v\n%s", err, summary)
			}
		})
	}
}

func dockerWorkflowScript(t *testing.T, file, job, name string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("../..", ".github/workflows", file))
	if err != nil {
		t.Fatal(err)
	}
	var workflow compiledWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, step := range workflow.Jobs[job].Steps {
		if step.Name == name && len(step.Run) > 0 {
			return step.Run
		}
	}
	t.Fatalf("missing script %s in %s/%s", name, file, job)
	return ""
}

func dockerRegistryVersion(id int, created string, tags ...string) map[string]any {
	if tags == nil {
		tags = []string{}
	}
	return map[string]any{
		"id":         id,
		"name":       "sha256:" + strings.Repeat(string(rune('a'+id)), 64),
		"created_at": created,
		"metadata": map[string]any{
			"container": map[string]any{"tags": tags},
		},
	}
}

func dockerRegistryFixture(t *testing.T, versions []map[string]any, current map[string]any) string {
	t.Helper()

	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatal("Docker cleanup tests require jq")
	}
	dir := t.TempDir()
	for name, value := range map[string]any{"versions.json": versions, "current.json": current} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Every API call stays inside the fixture, including DELETE. The real gh
	// executable is never invoked and no registry credentials are supplied.
	for name, script := range map[string]string{
		"gh": `#!/bin/bash
set -euo pipefail
case "$*" in
  'api users/go42-dev --jq .type') echo Organization ;;
  'api --paginate orgs/go42-dev/packages/container/go42/versions?per_page=100') cat "$FIXTURE/versions.json" ;;
  'api orgs/go42-dev/packages/container/go42/versions/'*) cat "$FIXTURE/current.json" ;;
  'api --method DELETE orgs/go42-dev/packages/container/go42/versions/'*)
    if [ -n "${DELETE_ERROR:-}" ]; then
      printf '%s\n' "$DELETE_ERROR" >&2
      exit 1
    fi
    echo "$4" >> "$FIXTURE/deleted"
    ;;
  *) echo "Unexpected API request: $*" >&2; exit 1 ;;
esac
`,
		"date": "#!/bin/bash\necho '2026-01-01T00:00:00Z'\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runDockerCleanup(t *testing.T, dir, script string, environment map[string]string) ([]byte, error) {
	t.Helper()

	command := exec.CommandContext(t.Context(), "bash", "-c", script)
	command.Env = []string{
		"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FIXTURE=" + dir,
		"OWNER=go42-dev",
		"PACKAGE_NAME=go42",
		"GITHUB_OUTPUT=" + filepath.Join(dir, "output"),
	}
	for key, value := range environment {
		command.Env = append(command.Env, key+"="+value)
	}
	return command.CombinedOutput()
}
