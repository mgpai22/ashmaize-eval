package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testConfigYAML = `default: codex
harnesses:
  codex:
    driver: codex
    images:
      agent:
        name: ashmaize-eval-agent
        target: agent
      grader:
        name: ashmaize-eval-grader
        target: grader
    prompt: harness/prompts/ashmaize_codex.txt
    auth_home: .codex-eval/codex-home
    model: gpt-5.5
    attempt_timeout: 4h
    grader_timeout: 120s
    workspace:
      files:
        - source: spec/TASK.md
          target: /workspace/spec/TASK.md
        - source: harness/prompts/workspace_agents.md
          target: /workspace/AGENTS.md
    docker:
      flags:
        - --cap-add=SYS_ADMIN
    cli:
      sandbox_mode: workspace-write
      approval_policy: never
      web_search: disabled
      reasoning_effort: high
      allow_login_shell: false
      workspace_write:
        network_access: false
      exec_flags:
        - --strict-config
        - --json
`

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return repoRoot
}

func TestLoadConfigSelectsDefaultHarness(t *testing.T) {
	repoRoot := writeTestConfig(t, testConfigYAML)

	gotRoot, cfg, err := loadConfig(commonOptions{repoPath: repoRoot, configPath: "config.yaml"})
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if gotRoot != repoRoot {
		t.Fatalf("repo root = %q, want %q", gotRoot, repoRoot)
	}
	if cfg.ID != "codex" {
		t.Fatalf("cfg.ID = %q, want codex", cfg.ID)
	}
	if cfg.Driver != "codex" {
		t.Fatalf("cfg.Driver = %q, want codex", cfg.Driver)
	}
	if cfg.AttemptDuration != 4*time.Hour {
		t.Fatalf("AttemptDuration = %s, want 4h", cfg.AttemptDuration)
	}
	if cfg.GraderDuration != 120*time.Second {
		t.Fatalf("GraderDuration = %s, want 120s", cfg.GraderDuration)
	}
}

func TestLoadConfigSelectsExplicitHarness(t *testing.T) {
	content := strings.Replace(testConfigYAML, "default: codex", "default: other", 1)
	repoRoot := writeTestConfig(t, content)

	_, cfg, err := loadConfig(commonOptions{repoPath: repoRoot, configPath: "config.yaml", harnessID: "codex"})
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.ID != "codex" {
		t.Fatalf("cfg.ID = %q, want codex", cfg.ID)
	}
}

func TestLoadConfigUnknownHarnessFails(t *testing.T) {
	repoRoot := writeTestConfig(t, testConfigYAML)

	_, _, err := loadConfig(commonOptions{repoPath: repoRoot, configPath: "config.yaml", harnessID: "missing"})
	if err == nil || !strings.Contains(err.Error(), `unknown harness "missing"`) {
		t.Fatalf("error = %v, want unknown harness", err)
	}
}

func TestLoadConfigInvalidAttemptTimeoutFails(t *testing.T) {
	content := strings.Replace(testConfigYAML, "attempt_timeout: 4h", "attempt_timeout: 0s", 1)
	repoRoot := writeTestConfig(t, content)

	_, _, err := loadConfig(commonOptions{repoPath: repoRoot, configPath: "config.yaml"})
	if err == nil || !strings.Contains(err.Error(), "attempt_timeout must be > 0") {
		t.Fatalf("error = %v, want invalid attempt_timeout", err)
	}
}

func TestLoadConfigForbiddenExecFlagFails(t *testing.T) {
	content := strings.Replace(testConfigYAML, "        - --json\n", "        - --json\n        - --dangerously-bypass-approvals-and-sandbox\n", 1)
	repoRoot := writeTestConfig(t, content)

	_, _, err := loadConfig(commonOptions{repoPath: repoRoot, configPath: "config.yaml"})
	if err == nil || !strings.Contains(err.Error(), "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("error = %v, want forbidden exec flag", err)
	}
}

func TestLoadConfigInvalidReasoningEffortFails(t *testing.T) {
	content := strings.Replace(testConfigYAML, "reasoning_effort: high", "reasoning_effort: maximum", 1)
	repoRoot := writeTestConfig(t, content)

	_, _, err := loadConfig(commonOptions{repoPath: repoRoot, configPath: "config.yaml"})
	if err == nil || !strings.Contains(err.Error(), "unsupported cli.reasoning_effort") {
		t.Fatalf("error = %v, want invalid reasoning effort", err)
	}
}

func TestRenderCodexConfig(t *testing.T) {
	out := string(renderCodexConfig(harnessConfig{
		Model: "gpt-5.5",
		CLI: cliConfig{
			SandboxMode:     "workspace-write",
			ApprovalPolicy:  "never",
			WebSearch:       "disabled",
			ReasoningEffort: "high",
			AllowLoginShell: false,
			WorkspaceWrite:  workspaceWriteConfig{NetworkAccess: false},
		},
	}, ""))
	for _, want := range []string{
		`model = "gpt-5.5"`,
		`model_reasoning_effort = "high"`,
		`sandbox_mode = "workspace-write"`,
		`approval_policy = "never"`,
		`web_search = "disabled"`,
		`allow_login_shell = false`,
		`[sandbox_workspace_write]`,
		`network_access = false`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered config missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "model_provider") {
		t.Fatalf("non-provider config should not emit model_provider:\n%s", out)
	}
}

func TestRenderCodexConfigOmitsEmptyReasoning(t *testing.T) {
	out := string(renderCodexConfig(harnessConfig{
		Model: "glm-4.6",
		CLI:   cliConfig{SandboxMode: "workspace-write", ApprovalPolicy: "never", WebSearch: "disabled"},
	}, ""))
	if strings.Contains(out, "model_reasoning_effort") {
		t.Fatalf("empty reasoning_effort should be omitted:\n%s", out)
	}
}

func TestRenderCodexConfigProvider(t *testing.T) {
	out := string(renderCodexConfig(harnessConfig{
		Model: "glm-4.6",
		CLI:   cliConfig{SandboxMode: "workspace-write", ApprovalPolicy: "never", WebSearch: "disabled"},
		Provider: &providerConfig{
			ID:                  "zai_proxy",
			Name:                "Z.AI GLM",
			WireAPI:             "responses",
			APIKeyEnv:           "OPENAI_API_KEY",
			StreamIdleTimeoutMs: 3000000,
		},
	}, "http://ashmaize-glm-bridge:31415"))
	for _, want := range []string{
		`model_provider = "zai_proxy"`,
		`[model_providers.zai_proxy]`,
		`name = "Z.AI GLM"`,
		`base_url = "http://ashmaize-glm-bridge:31415"`,
		`env_key = "OPENAI_API_KEY"`,
		`wire_api = "responses"`,
		`stream_idle_timeout_ms = 3000000`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("provider config missing %q in:\n%s", want, out)
		}
	}
}

func TestCopyWorkspaceFiles(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, "spec"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "spec", "TASK.md"), []byte("task"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "AGENTS.md"), []byte("agents"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()

	err := copyWorkspaceFiles(repoRoot, workspace, []workspaceFile{
		{Source: "spec/TASK.md", Target: "/workspace/spec/TASK.md"},
		{Source: "AGENTS.md", Target: "AGENTS.md"},
	})
	if err != nil {
		t.Fatalf("copyWorkspaceFiles returned error: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "spec", "TASK.md")); err != nil || string(got) != "task" {
		t.Fatalf("copied TASK.md = %q, %v; want task", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md")); err != nil || string(got) != "agents" {
		t.Fatalf("copied AGENTS.md = %q, %v; want agents", got, err)
	}
}

func TestCleanWorkspaceTargetRejectsEscape(t *testing.T) {
	for _, target := range []string{"../secret", "/tmp/secret", "/workspace/../secret", "/workspace"} {
		if _, err := cleanWorkspaceTarget(target); err == nil {
			t.Fatalf("cleanWorkspaceTarget(%q) succeeded, want error", target)
		}
	}
}

func TestRequireAgentRejectsHardcodedWorkspacePath(t *testing.T) {
	dir := t.TempDir()
	agent := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(agent, []byte("#!/bin/sh\nexec /usr/bin/env python3 /workspace/agent.py\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := requireAgent(agent)
	if err == nil || !strings.Contains(err.Error(), "hardcodes /workspace paths") {
		t.Fatalf("error = %v, want hardcoded workspace rejection", err)
	}
}

func TestRequireAgentAcceptsRelativeWrapper(t *testing.T) {
	dir := t.TempDir()
	agent := filepath.Join(dir, "agent.sh")
	wrapper := "#!/bin/sh\nDIR=$(CDPATH= cd -- \"$(dirname -- \"$0\")\" && pwd)\nexec /usr/bin/env python3 \"$DIR/agent.py\"\n"
	if err := os.WriteFile(agent, []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := requireAgent(agent); err != nil {
		t.Fatalf("requireAgent returned error: %v", err)
	}
}

func TestEnsurePersistentAuthHomeRemovesRuntimeJunk(t *testing.T) {
	repoRoot := t.TempDir()
	authHome := filepath.Join(repoRoot, ".codex-eval", "codex-home")
	for _, dir := range []string{filepath.Join(authHome, "tmp"), filepath.Join(authHome, "cache")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		filepath.Join(authHome, "auth.json"):      `{"OPENAI_API_KEY":"test"}`,
		filepath.Join(authHome, "config.toml"):    "model = \"old\"\n",
		filepath.Join(authHome, "state_5.sqlite"): "sqlite junk",
		filepath.Join(authHome, "tmp", "junk"):    "tmp junk",
		filepath.Join(authHome, "cache", "junk"):  "cache junk",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	gotHome, err := ensurePersistentAuthHome(repoRoot, harnessConfig{AuthHome: ".codex-eval/codex-home"})
	if err != nil {
		t.Fatalf("ensurePersistentAuthHome returned error: %v", err)
	}
	if gotHome != authHome {
		t.Fatalf("auth home = %q, want %q", gotHome, authHome)
	}
	entries, err := os.ReadDir(authHome)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "auth.json" {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("entries = %v, want [auth.json]", names)
	}
	st, err := os.Stat(filepath.Join(authHome, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Fatalf("auth.json mode = %o, want 600", got)
	}
}

const providerConfigYAML = `default: glm
harnesses:
  glm:
    driver: codex
    images:
      agent: {name: ashmaize-eval-agent, target: agent}
      grader: {name: ashmaize-eval-grader, target: grader}
    prompt: harness/prompts/ashmaize_codex.txt
    auth_home: .codex-eval/codex-home
    model: glm-4.6
    attempt_timeout: 4h
    grader_timeout: 120s
    workspace:
      files:
        - source: spec/TASK.md
          target: /workspace/spec/TASK.md
    docker:
      flags: [--cap-add=SYS_ADMIN]
    cli:
      sandbox_mode: workspace-write
      approval_policy: never
      web_search: disabled
      workspace_write: {network_access: false}
      exec_flags: [--json]
    provider:
      id: zai_proxy
      name: Z.AI GLM
      wire_api: responses
      api_key_env: OPENAI_API_KEY
      key_env: ZAI_API_KEY
      bridge:
        image: ashmaize-zai-bridge
        dockerfile: container/zai-bridge.Dockerfile
        port: 31415
        upstream_base_url: https://api.z.ai/api/coding/paas/v4
`

func TestLoadConfigProviderValid(t *testing.T) {
	repoRoot := writeTestConfig(t, providerConfigYAML)

	_, cfg, err := loadConfig(commonOptions{repoPath: repoRoot, configPath: "config.yaml"})
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.Provider == nil {
		t.Fatal("cfg.Provider = nil, want non-nil")
	}
	if cfg.Provider.ID != "zai_proxy" || cfg.Provider.Bridge.Port != 31415 {
		t.Fatalf("provider = %+v, want zai_proxy on port 31415", cfg.Provider)
	}
}

func TestLoadConfigProviderWireAPIRejected(t *testing.T) {
	content := strings.Replace(providerConfigYAML, "wire_api: responses", "wire_api: chat", 1)
	repoRoot := writeTestConfig(t, content)

	_, _, err := loadConfig(commonOptions{repoPath: repoRoot, configPath: "config.yaml"})
	if err == nil || !strings.Contains(err.Error(), "wire_api") {
		t.Fatalf("error = %v, want wire_api rejection", err)
	}
}

func TestParseCodexMetricsUsesLastTurn(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "codex-events.jsonl")
	lines := []string{
		`{"type":"thread.started","thread_id":"t"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"i1"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":20,"reasoning_output_tokens":5}}`,
		`{"type":"item.completed","item":{"id":"i2"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":250,"cached_input_tokens":30,"output_tokens":60,"reasoning_output_tokens":15}}`,
	}
	if err := os.WriteFile(events, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := parseCodexMetrics(events)
	if m == nil {
		t.Fatal("parseCodexMetrics returned nil")
	}
	checks := map[string]int{
		"input_tokens":        250,
		"cached_input_tokens": 30,
		"output_tokens":       60,
		"reasoning_tokens":    15,
		"total_tokens":        310,
		"turns":               2,
		"item_count":          2,
	}
	for k, want := range checks {
		if got, ok := m[k].(int); !ok || got != want {
			t.Fatalf("metric %s = %v, want %d", k, m[k], want)
		}
	}
}

func TestParseCodexMetricsMissingFile(t *testing.T) {
	if m := parseCodexMetrics(filepath.Join(t.TempDir(), "nope.jsonl")); m != nil {
		t.Fatalf("expected nil for missing events file, got %v", m)
	}
}
