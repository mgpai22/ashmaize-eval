package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type rootConfig struct {
	Default   string                   `yaml:"default"`
	Harnesses map[string]harnessConfig `yaml:"harnesses"`
}

type harnessConfig struct {
	Driver         string          `yaml:"driver"`
	Images         imageConfig     `yaml:"images"`
	Prompt         string          `yaml:"prompt"`
	AuthHome       string          `yaml:"auth_home"`
	Model          string          `yaml:"model"`
	AttemptTimeout string          `yaml:"attempt_timeout"`
	GraderTimeout  string          `yaml:"grader_timeout"`
	Workspace      workspaceConfig `yaml:"workspace"`
	Docker         dockerConfig    `yaml:"docker"`
	CLI            cliConfig       `yaml:"cli"`
	Provider       *providerConfig `yaml:"provider"`

	ID              string        `yaml:"-"`
	AttemptDuration time.Duration `yaml:"-"`
	GraderDuration  time.Duration `yaml:"-"`
}

type imageConfig struct {
	Agent  imageTarget `yaml:"agent"`
	Grader imageTarget `yaml:"grader"`
}

type imageTarget struct {
	Name   string `yaml:"name"`
	Target string `yaml:"target"`
}

type workspaceConfig struct {
	Files []workspaceFile `yaml:"files"`
}

type workspaceFile struct {
	Source string `yaml:"source"`
	Target string `yaml:"target"`
}

type dockerConfig struct {
	Flags []string `yaml:"flags"`
}

type cliConfig struct {
	SandboxMode     string               `yaml:"sandbox_mode"`
	ApprovalPolicy  string               `yaml:"approval_policy"`
	WebSearch       string               `yaml:"web_search"`
	ReasoningEffort string               `yaml:"reasoning_effort"`
	AllowLoginShell bool                 `yaml:"allow_login_shell"`
	WorkspaceWrite  workspaceWriteConfig `yaml:"workspace_write"`
	ExecFlags       []string             `yaml:"exec_flags"`
}

type workspaceWriteConfig struct {
	NetworkAccess bool `yaml:"network_access"`
}

// providerConfig, when set, routes Codex at a custom OpenAI-compatible model
// provider through a translation bridge sidecar (see zai-bridge.Dockerfile).
// Used to benchmark non-OpenAI models (e.g. Z.AI GLM) whose API only speaks
// Chat Completions while modern Codex only speaks the Responses API.
type providerConfig struct {
	ID                  string       `yaml:"id"`
	Name                string       `yaml:"name"`
	WireAPI             string       `yaml:"wire_api"`
	APIKeyEnv           string       `yaml:"api_key_env"`
	KeyEnv              string       `yaml:"key_env"`
	StreamIdleTimeoutMs int          `yaml:"stream_idle_timeout_ms"`
	Bridge              bridgeConfig `yaml:"bridge"`
}

type bridgeConfig struct {
	Image           string `yaml:"image"`
	Dockerfile      string `yaml:"dockerfile"`
	Port            int    `yaml:"port"`
	UpstreamBaseURL string `yaml:"upstream_base_url"`
}

type commonOptions struct {
	configPath string
	repoPath   string
	harnessID  string
}

var slugRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func main() {
	if err := runMain(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error: "+err.Error())
		os.Exit(1)
	}
}

func runMain(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("missing subcommand")
	}

	switch args[0] {
	case "run":
		return runSubcommand(args[1:])
	case "auth":
		return authSubcommand(args[1:])
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage:
  run-codex run -slug <slug> [-overwrite] [-fixture-agent <path>] [-config <path>] [-repo <path>] [-harness <id>]
  run-codex auth status [-config <path>] [-repo <path>] [-harness <id>]
  run-codex auth login-device [-config <path>] [-repo <path>] [-harness <id>]
  run-codex auth login-api-key [-config <path>] [-repo <path>] [-harness <id>]
  run-codex auth login-access-token [-config <path>] [-repo <path>] [-harness <id>]
  run-codex auth import-host [-config <path>] [-repo <path>] [-harness <id>]
`)
}

func addCommonFlags(fs *flag.FlagSet, opts *commonOptions) {
	fs.StringVar(&opts.configPath, "config", "config.yaml", "root harness config path")
	fs.StringVar(&opts.repoPath, "repo", ".", "repository root")
	fs.StringVar(&opts.harnessID, "harness", "", "harness id from config.yaml")
}

func runSubcommand(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var opts commonOptions
	var slug, fixtureAgent string
	var overwrite bool
	addCommonFlags(fs, &opts)
	fs.StringVar(&slug, "slug", "", "run slug")
	fs.BoolVar(&overwrite, "overwrite", false, "replace existing artifacts for this slug")
	fs.StringVar(&fixtureAgent, "fixture-agent", "", "local ABI program used instead of Codex")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateSlug(slug); err != nil {
		return err
	}

	repoRoot, cfg, err := loadConfig(opts)
	if err != nil {
		return err
	}
	return runAttempt(repoRoot, cfg, slug, overwrite, fixtureAgent)
}

func authSubcommand(args []string) error {
	if len(args) == 0 {
		return errors.New("missing auth subcommand")
	}
	action := args[0]
	fs := flag.NewFlagSet("auth "+action, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var opts commonOptions
	addCommonFlags(fs, &opts)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	repoRoot, cfg, err := loadConfig(opts)
	if err != nil {
		return err
	}
	if cfg.Provider != nil {
		return fmt.Errorf("auth subcommands are not applicable to provider-backed harness %q; set the %s environment variable and run directly", cfg.ID, cfg.Provider.KeyEnv)
	}

	switch action {
	case "status":
		return authStatus(repoRoot, cfg)
	case "login-device":
		return authLogin(repoRoot, cfg, []string{"codex", "login", "--device-auth"}, true)
	case "login-api-key":
		return authLogin(repoRoot, cfg, []string{"codex", "login", "--with-api-key"}, false)
	case "login-access-token":
		return authLogin(repoRoot, cfg, []string{"codex", "login", "--with-access-token"}, false)
	case "import-host":
		return authImportHost(repoRoot, cfg)
	default:
		return fmt.Errorf("unknown auth subcommand %q", action)
	}
}

func validateSlug(slug string) error {
	if slug == "" {
		return errors.New("-slug is required")
	}
	if !slugRE.MatchString(slug) || strings.ContainsAny(slug, `/\\`) {
		return fmt.Errorf("invalid slug %q: use [A-Za-z0-9][A-Za-z0-9._-]* with no path separators", slug)
	}
	return nil
}

func loadConfig(opts commonOptions) (string, harnessConfig, error) {
	repoRoot, err := filepath.Abs(opts.repoPath)
	if err != nil {
		return "", harnessConfig{}, err
	}
	cfgPath := resolvePath(repoRoot, opts.configPath)
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return "", harnessConfig{}, err
	}
	var root rootConfig
	if err := yaml.Unmarshal(b, &root); err != nil {
		return "", harnessConfig{}, fmt.Errorf("parse %s: %w", cfgPath, err)
	}
	harnessID := opts.harnessID
	if harnessID == "" {
		if root.Default == "" {
			return "", harnessConfig{}, errors.New("default is required when -harness is empty")
		}
		harnessID = root.Default
	}
	cfg, ok := root.Harnesses[harnessID]
	if !ok {
		return "", harnessConfig{}, fmt.Errorf("unknown harness %q", harnessID)
	}
	cfg.ID = harnessID
	if err := validateConfig(cfg); err != nil {
		return "", harnessConfig{}, err
	}
	attemptDuration, err := time.ParseDuration(cfg.AttemptTimeout)
	if err != nil {
		return "", harnessConfig{}, fmt.Errorf("invalid attempt_timeout %q: %w", cfg.AttemptTimeout, err)
	}
	cfg.AttemptDuration = attemptDuration
	graderDuration, err := time.ParseDuration(cfg.GraderTimeout)
	if err != nil {
		return "", harnessConfig{}, fmt.Errorf("invalid grader_timeout %q: %w", cfg.GraderTimeout, err)
	}
	cfg.GraderDuration = graderDuration
	return repoRoot, cfg, nil
}

func validateConfig(cfg harnessConfig) error {
	if cfg.Driver != "codex" {
		return fmt.Errorf("unsupported harness driver %q for harness %q", cfg.Driver, cfg.ID)
	}
	missing := []string{}
	if cfg.Images.Agent.Name == "" {
		missing = append(missing, "images.agent.name")
	}
	if cfg.Images.Agent.Target == "" {
		missing = append(missing, "images.agent.target")
	}
	if cfg.Images.Grader.Name == "" {
		missing = append(missing, "images.grader.name")
	}
	if cfg.Images.Grader.Target == "" {
		missing = append(missing, "images.grader.target")
	}
	if cfg.Prompt == "" {
		missing = append(missing, "prompt")
	}
	if cfg.AuthHome == "" {
		missing = append(missing, "auth_home")
	}
	if cfg.Model == "" {
		missing = append(missing, "model")
	}
	if cfg.AttemptTimeout == "" {
		missing = append(missing, "attempt_timeout")
	}
	if cfg.GraderTimeout == "" {
		missing = append(missing, "grader_timeout")
	}
	if cfg.CLI.SandboxMode == "" {
		missing = append(missing, "cli.sandbox_mode")
	}
	if cfg.CLI.ApprovalPolicy == "" {
		missing = append(missing, "cli.approval_policy")
	}
	if cfg.CLI.WebSearch == "" {
		missing = append(missing, "cli.web_search")
	}
	if len(cfg.Workspace.Files) == 0 {
		missing = append(missing, "workspace.files")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required config fields: %s", strings.Join(missing, ", "))
	}
	for i, file := range cfg.Workspace.Files {
		if file.Source == "" {
			return fmt.Errorf("workspace.files[%d].source is required", i)
		}
		if file.Target == "" {
			return fmt.Errorf("workspace.files[%d].target is required", i)
		}
		if _, err := cleanWorkspaceTarget(file.Target); err != nil {
			return fmt.Errorf("invalid workspace.files[%d].target %q: %w", i, file.Target, err)
		}
	}
	if d, err := time.ParseDuration(cfg.AttemptTimeout); err != nil {
		return fmt.Errorf("invalid attempt_timeout %q: %w", cfg.AttemptTimeout, err)
	} else if d <= 0 {
		return errors.New("attempt_timeout must be > 0")
	}
	if _, err := time.ParseDuration(cfg.GraderTimeout); err != nil {
		return fmt.Errorf("invalid grader_timeout %q: %w", cfg.GraderTimeout, err)
	}
	if err := validateReasoningEffort(cfg.CLI.ReasoningEffort); err != nil {
		return err
	}
	if len(cfg.CLI.ExecFlags) == 0 {
		return errors.New("cli.exec_flags must not be empty")
	}
	if len(cfg.Docker.Flags) == 0 {
		return errors.New("docker.flags must not be empty")
	}
	for _, flag := range cfg.CLI.ExecFlags {
		switch flag {
		case "--cd", "--output-last-message", "--dangerously-bypass-approvals-and-sandbox", "--full-auto":
			return fmt.Errorf("forbidden cli.exec_flags value %q", flag)
		}
		for _, forbidden := range []string{"/workspace", "/artifacts", "CODEX_API_KEY", "OPENAI_API_KEY", "auth", "token"} {
			if strings.Contains(flag, forbidden) {
				return fmt.Errorf("forbidden cli.exec_flags value %q contains %q", flag, forbidden)
			}
		}
	}
	if cfg.Provider != nil {
		p := cfg.Provider
		pmissing := []string{}
		if p.ID == "" {
			pmissing = append(pmissing, "provider.id")
		}
		if p.Name == "" {
			pmissing = append(pmissing, "provider.name")
		}
		if p.APIKeyEnv == "" {
			pmissing = append(pmissing, "provider.api_key_env")
		}
		if p.KeyEnv == "" {
			pmissing = append(pmissing, "provider.key_env")
		}
		if p.Bridge.Image == "" {
			pmissing = append(pmissing, "provider.bridge.image")
		}
		if p.Bridge.Dockerfile == "" {
			pmissing = append(pmissing, "provider.bridge.dockerfile")
		}
		if p.Bridge.UpstreamBaseURL == "" {
			pmissing = append(pmissing, "provider.bridge.upstream_base_url")
		}
		if len(pmissing) > 0 {
			return fmt.Errorf("missing required config fields: %s", strings.Join(pmissing, ", "))
		}
		if p.WireAPI != "responses" {
			return fmt.Errorf("provider.wire_api must be \"responses\" (Codex speaks the Responses API); got %q", p.WireAPI)
		}
		if p.Bridge.Port <= 0 {
			return errors.New("provider.bridge.port must be > 0")
		}
	}
	return nil
}

func resolvePath(repoRoot, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(repoRoot, p)
}

func runAttempt(repoRoot string, cfg harnessConfig, slug string, overwrite bool, fixtureAgent string) error {
	paths := artifactPaths(repoRoot, slug)
	if err := prepareSlugArtifacts(paths, overwrite); err != nil {
		return err
	}
	if err := ensureDirs(repoRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.transcriptDir, 0o755); err != nil {
		return err
	}
	if err := ensureGrader(repoRoot); err != nil {
		return err
	}
	if err := ensureImage(repoRoot, cfg.Images.Agent); err != nil {
		return err
	}
	if err := ensureImage(repoRoot, cfg.Images.Grader); err != nil {
		return err
	}
	if err := runOfflineCheck(repoRoot, cfg.Images.Grader.Name); err != nil {
		return err
	}
	if err := runAgentBoundaryCheck(repoRoot, cfg.Images.Agent.Name); err != nil {
		return err
	}

	workspace, err := os.MkdirTemp("", "ashmaize-codex-"+slug+"-")
	if err != nil {
		return err
	}
	if err := os.Chmod(workspace, 0o777); err != nil {
		return err
	}
	defer os.RemoveAll(workspace)

	if err := copyWorkspaceFiles(repoRoot, workspace, cfg.Workspace.Files); err != nil {
		return err
	}

	if cfg.Provider == nil {
		if _, err := ensurePersistentAuthHome(repoRoot, cfg); err != nil {
			return err
		}
	}

	genStart := time.Now()
	if fixtureAgent != "" {
		if err := setupFixture(workspace, repoRoot, fixtureAgent, paths.transcriptDir); err != nil {
			return err
		}
	} else {
		if err := runCodex(repoRoot, cfg, slug, workspace, paths.transcriptDir); err != nil {
			return err
		}
	}
	timings := runTimings{generationSeconds: time.Since(genStart).Seconds()}

	agentPath := filepath.Join(workspace, "agent.sh")
	if err := requireAgent(agentPath); err != nil {
		return err
	}
	if err := copyDir(workspace, paths.runDir, skipTransientCodexDirs); err != nil {
		return fmt.Errorf("freeze workspace: %w", err)
	}
	gradeStart := time.Now()
	if err := gradeFrozen(repoRoot, cfg, slug, paths); err != nil {
		return err
	}
	timings.gradingSeconds = time.Since(gradeStart).Seconds()
	if err := annotateScorecard(repoRoot, cfg, slug, paths, timings); err != nil {
		return err
	}
	fmt.Printf("harness scorecard: %s\n", paths.scorecard)
	return nil
}

type runPaths struct {
	runDir        string
	transcriptDir string
	scorecard     string
}

func artifactPaths(repoRoot, slug string) runPaths {
	return runPaths{
		runDir:        filepath.Join(repoRoot, "runs", slug),
		transcriptDir: filepath.Join(repoRoot, "transcripts", slug),
		scorecard:     filepath.Join(repoRoot, "scorecards", slug+".json"),
	}
}

func prepareSlugArtifacts(paths runPaths, overwrite bool) error {
	for _, p := range []string{paths.runDir, paths.transcriptDir, paths.scorecard} {
		_, err := os.Stat(p)
		if err == nil && !overwrite {
			return fmt.Errorf("artifact already exists: %s (use -overwrite to replace this slug only)", p)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if overwrite {
		for _, p := range []string{paths.runDir, paths.transcriptDir, paths.scorecard} {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureDirs(repoRoot string) error {
	for _, d := range []string{"bin", "runs", "transcripts", "scorecards"} {
		if err := os.MkdirAll(filepath.Join(repoRoot, d), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func ensureGrader(repoRoot string) error {
	cmd := exec.Command("go", "build", "-o", "../bin/grader", ".")
	cmd.Dir = filepath.Join(repoRoot, "grader")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return annotateCmdErr("build grader", cmd.Run())
}

func ensureImage(repoRoot string, image imageTarget) error {
	inspect := exec.Command("docker", "image", "inspect", image.Name)
	if inspect.Run() == nil {
		return nil
	}
	args := []string{"build", "-t", image.Name, "-f", "container/Dockerfile"}
	if image.Target != "" {
		args = append(args, "--target", image.Target)
	}
	args = append(args, ".")
	cmd := exec.Command("docker", args...)
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return annotateCmdErr("build Docker image "+image.Name, cmd.Run())
}

func runOfflineCheck(repoRoot, image string) error {
	cmd := exec.Command(filepath.Join(repoRoot, "container", "check_offline.sh"), image)
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return annotateCmdErr("offline affordance check", cmd.Run())
}

func runAgentBoundaryCheck(repoRoot, image string) error {
	cmd := exec.Command("docker", "run", "--rm", "--network", "none", image, "sh", "-c", "test ! -e /usr/local/bin/oracle && test -r /task/spec/TASK.md")
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return annotateCmdErr("agent image boundary check", cmd.Run())
}

func validateReasoningEffort(effort string) error {
	switch effort {
	case "", "low", "medium", "high", "xhigh", "max":
		return nil
	default:
		return fmt.Errorf("unsupported cli.reasoning_effort %q: use low, medium, high, xhigh, or max", effort)
	}
}

func renderCodexConfig(cfg harnessConfig, bridgeAddr string) []byte {
	c := cfg.CLI
	var b strings.Builder
	fmt.Fprintf(&b, "model = %s\n", strconv.Quote(cfg.Model))
	if c.ReasoningEffort != "" {
		fmt.Fprintf(&b, "model_reasoning_effort = %s\n", strconv.Quote(c.ReasoningEffort))
	}
	fmt.Fprintf(&b, "sandbox_mode = %s\n", strconv.Quote(c.SandboxMode))
	fmt.Fprintf(&b, "approval_policy = %s\n", strconv.Quote(c.ApprovalPolicy))
	fmt.Fprintf(&b, "web_search = %s\n", strconv.Quote(c.WebSearch))
	fmt.Fprintf(&b, "allow_login_shell = %t\n", c.AllowLoginShell)
	if cfg.Provider != nil {
		fmt.Fprintf(&b, "model_provider = %s\n", strconv.Quote(cfg.Provider.ID))
	}
	fmt.Fprintf(&b, "\n[sandbox_workspace_write]\n")
	fmt.Fprintf(&b, "network_access = %t\n", c.WorkspaceWrite.NetworkAccess)
	if cfg.Provider != nil {
		p := cfg.Provider
		fmt.Fprintf(&b, "\n[model_providers.%s]\n", p.ID)
		fmt.Fprintf(&b, "name = %s\n", strconv.Quote(p.Name))
		fmt.Fprintf(&b, "base_url = %s\n", strconv.Quote(bridgeAddr))
		fmt.Fprintf(&b, "env_key = %s\n", strconv.Quote(p.APIKeyEnv))
		fmt.Fprintf(&b, "wire_api = %s\n", strconv.Quote(p.WireAPI))
		if p.StreamIdleTimeoutMs > 0 {
			fmt.Fprintf(&b, "stream_idle_timeout_ms = %d\n", p.StreamIdleTimeoutMs)
		}
	}
	return []byte(b.String())
}

func writeCodexConfig(home string, cfg harnessConfig, bridgeAddr string) error {
	return os.WriteFile(filepath.Join(home, "config.toml"), renderCodexConfig(cfg, bridgeAddr), 0o600)
}

func ensurePersistentAuthHome(repoRoot string, cfg harnessConfig) (string, error) {
	authHome := resolvePath(repoRoot, cfg.AuthHome)
	if err := os.MkdirAll(authHome, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(authHome, 0o700); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(authHome)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		entryPath := filepath.Join(authHome, entry.Name())
		if entry.Name() == "auth.json" {
			if err := os.Chmod(entryPath, 0o600); err != nil {
				return "", err
			}
			continue
		}
		if err := removeLegacyCodexHomeEntry(repoRoot, entryPath, entry.Name()); err != nil {
			return "", err
		}
	}
	return authHome, nil
}

func removeLegacyCodexHomeEntry(repoRoot, entryPath, entryName string) error {
	if err := os.RemoveAll(entryPath); err == nil {
		return nil
	} else {
		removeErr := err
		cleanupDir := filepath.Join(repoRoot, ".codex-eval", "cleanup-stale")
		if err := os.MkdirAll(cleanupDir, 0o700); err != nil {
			return fmt.Errorf("remove legacy Codex home entry %s: remove failed (%v), create cleanup directory failed (%w); remove it manually with elevated permissions", entryPath, removeErr, err)
		}
		dst := filepath.Join(cleanupDir, fmt.Sprintf("%s-%d-%d", entryName, time.Now().UnixNano(), os.Getpid()))
		if err := os.Rename(entryPath, dst); err != nil {
			return fmt.Errorf("remove legacy Codex home entry %s: remove failed (%v), rename to %s failed (%v); remove it manually with elevated permissions", entryPath, removeErr, dst, err)
		}
	}
	return nil
}

func prepareTempCodexHome(repoRoot string, cfg harnessConfig, copyAuth bool, bridgeAddr string) (string, bool, error) {
	runHome, err := os.MkdirTemp("", "ashmaize-codex-home-")
	if err != nil {
		return "", false, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(runHome)
		}
	}()
	if err := os.Chmod(runHome, 0o700); err != nil {
		return "", false, err
	}
	if err := writeCodexConfig(runHome, cfg, bridgeAddr); err != nil {
		return "", false, err
	}
	hasAuth := false
	if copyAuth {
		authSrc := filepath.Join(resolvePath(repoRoot, cfg.AuthHome), "auth.json")
		if _, err := os.Stat(authSrc); err == nil {
			if err := copyFile(authSrc, filepath.Join(runHome, "auth.json"), 0o600); err != nil {
				return "", false, err
			}
			hasAuth = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", false, err
		}
	}
	cleanup = false
	return runHome, hasAuth, nil
}

func prepareRunCodexHome(repoRoot string, cfg harnessConfig, bridgeAddr string) (string, bool, error) {
	return prepareTempCodexHome(repoRoot, cfg, cfg.Provider == nil, bridgeAddr)
}

func setupFixture(workspace, repoRoot, fixtureAgent, transcriptDir string) error {
	fixturePath := resolvePath(repoRoot, fixtureAgent)
	if err := copyFile(fixturePath, filepath.Join(workspace, "fixture_agent.py"), 0o644); err != nil {
		return err
	}
	agent := `#!/bin/sh
set -eu
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec python3 "$DIR/fixture_agent.py"
`
	if err := os.WriteFile(filepath.Join(workspace, "agent.sh"), []byte(agent), 0o755); err != nil {
		return err
	}
	msg := "Codex was skipped because -fixture-agent was set. The fixture was wrapped as /workspace/agent.sh.\n"
	return os.WriteFile(filepath.Join(transcriptDir, "fixture.txt"), []byte(msg), 0o644)
}

func bridgeNetName(slug string) string       { return "ashmaize-" + slug + "-net" }
func bridgeContainerName(slug string) string { return "ashmaize-" + slug + "-bridge" }

func ensureBridgeImage(repoRoot string, b bridgeConfig) error {
	if exec.Command("docker", "image", "inspect", b.Image).Run() == nil {
		return nil
	}
	cmd := exec.Command("docker", "build", "-t", b.Image, "-f", b.Dockerfile, ".")
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return annotateCmdErr("build bridge image "+b.Image, cmd.Run())
}

func waitBridgeHealth(net, agentImage, name string, port int) error {
	url := fmt.Sprintf("http://%s:%d/health", name, port)
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		c := exec.Command("docker", "run", "--rm", "--network", net, agentImage, "sh", "-c", "curl -sf "+url)
		if err := c.Run(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("bridge %s did not become healthy: %v", name, lastErr)
}

// startBridge launches the translation proxy sidecar on a per-run Docker network
// and returns the in-network base URL Codex should use plus a cleanup function.
func startBridge(repoRoot string, cfg harnessConfig, slug, transcriptDir string) (string, func(), error) {
	p := cfg.Provider
	key := os.Getenv(p.KeyEnv)
	if key == "" {
		return "", nil, fmt.Errorf("provider requires the %s environment variable to hold the API key", p.KeyEnv)
	}
	if err := ensureBridgeImage(repoRoot, p.Bridge); err != nil {
		return "", nil, err
	}
	net := bridgeNetName(slug)
	name := bridgeContainerName(slug)
	_ = exec.Command("docker", "rm", "-f", name).Run()
	_ = exec.Command("docker", "network", "rm", net).Run()
	if out, err := exec.Command("docker", "network", "create", net).CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("create bridge network: %v: %s", err, out)
	}
	cleanup := func() {
		if logs, err := exec.Command("docker", "logs", name).CombinedOutput(); err == nil {
			_ = os.WriteFile(filepath.Join(transcriptDir, "bridge.log"), logs, 0o644)
		}
		_ = exec.Command("docker", "rm", "-f", name).Run()
		_ = exec.Command("docker", "network", "rm", net).Run()
	}
	runArgs := []string{
		"run", "-d", "--name", name, "--network", net,
		"-e", "ZAI_API_KEY", "-e", "HOST=0.0.0.0",
		"-e", fmt.Sprintf("PORT=%d", p.Bridge.Port),
		"-e", "ZAI_BASE_URL=" + p.Bridge.UpstreamBaseURL,
		"-e", "ALLOW_TOOLS=1",
		p.Bridge.Image,
	}
	start := exec.Command("docker", runArgs...)
	start.Env = append(os.Environ(), "ZAI_API_KEY="+key)
	if out, err := start.CombinedOutput(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("start bridge: %v: %s", err, out)
	}
	if err := waitBridgeHealth(net, cfg.Images.Agent.Name, name, p.Bridge.Port); err != nil {
		cleanup()
		return "", nil, err
	}
	return fmt.Sprintf("http://%s:%d", name, p.Bridge.Port), cleanup, nil
}

func runCodex(repoRoot string, cfg harnessConfig, slug, workspace, transcriptDir string) error {
	bridgeAddr := ""
	providerKey := ""
	if cfg.Provider != nil {
		addr, cleanup, err := startBridge(repoRoot, cfg, slug, transcriptDir)
		if err != nil {
			return err
		}
		defer cleanup()
		bridgeAddr = addr
		providerKey = os.Getenv(cfg.Provider.KeyEnv)
	}
	runCodexHome, hasAuth, err := prepareRunCodexHome(repoRoot, cfg, bridgeAddr)
	if err != nil {
		return err
	}
	defer os.RemoveAll(runCodexHome)
	if cfg.Provider == nil && !hasAuth && os.Getenv("CODEX_API_KEY") == "" {
		return errors.New("no eval Codex auth.json found and CODEX_API_KEY is not set; run auth login-device, auth login-api-key, or auth import-host")
	}
	prompt, err := os.ReadFile(resolvePath(repoRoot, cfg.Prompt))
	if err != nil {
		return err
	}
	events, err := os.Create(filepath.Join(transcriptDir, "codex-events.jsonl"))
	if err != nil {
		return err
	}
	defer events.Close()
	stderr, err := os.Create(filepath.Join(transcriptDir, "codex-stderr.log"))
	if err != nil {
		return err
	}
	defer stderr.Close()

	args := []string{
		"run", "--rm", "-i",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
	}
	args = append(args, cfg.Docker.Flags...)
	args = append(args, "-e", "CODEX_HOME=/codex-home")
	var extraEnv []string
	if cfg.Provider != nil {
		args = append(args, "--network", bridgeNetName(slug))
		args = append(args, "-e", cfg.Provider.APIKeyEnv)
		extraEnv = append(extraEnv, cfg.Provider.APIKeyEnv+"="+providerKey)
	} else if !hasAuth {
		args = append(args, "-e", "CODEX_API_KEY")
	}
	args = append(args,
		"-v", workspace+":/workspace",
		"-v", runCodexHome+":/codex-home",
		"-v", transcriptDir+":/artifacts",
		"-w", "/workspace",
		cfg.Images.Agent.Name,
	)
	codexArgs := []string{"codex", "exec"}
	codexArgs = append(codexArgs, cfg.CLI.ExecFlags...)
	codexArgs = append(codexArgs, "--cd", "/workspace", "--output-last-message", "/artifacts/codex-final.md", "-")
	args = append(args, codexArgs...)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.AttemptDuration)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	cmd.Stdin = strings.NewReader(string(prompt))
	cmd.Stdout = io.MultiWriter(os.Stdout, events)
	cmd.Stderr = io.MultiWriter(os.Stderr, stderr)
	err = cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("codex exec timed out after %s", cfg.AttemptDuration)
	}
	return annotateCmdErr("codex exec", err)
}

func requireAgent(agentPath string) error {
	st, err := os.Stat(agentPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s was not created", agentPath)
		}
		return err
	}
	if st.IsDir() {
		return fmt.Errorf("%s is a directory", agentPath)
	}
	if err := os.Chmod(agentPath, 0o755); err != nil {
		return err
	}
	st, err = os.Stat(agentPath)
	if err != nil {
		return err
	}
	if st.Mode()&0o111 == 0 {
		return fmt.Errorf("%s is not executable after chmod", agentPath)
	}
	b, err := os.ReadFile(agentPath)
	if err != nil {
		return err
	}
	if bytes.Contains(b, []byte("/workspace/")) {
		return fmt.Errorf("%s hardcodes /workspace paths; agent.sh must resolve helper files relative to its own directory", agentPath)
	}
	return nil
}

func gradeFrozen(repoRoot string, cfg harnessConfig, slug string, paths runPaths) error {
	logPath := filepath.Join(paths.transcriptDir, "grader.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer logFile.Close()
	args := []string{
		"run", "--rm", "--network", "none", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-v", filepath.Join(repoRoot, "bin", "grader") + ":/usr/local/bin/grader:ro",
		"-v", filepath.Join(repoRoot, "scenarios") + ":/scenarios:ro",
		"-v", paths.runDir + ":/candidate:ro",
		"-v", filepath.Join(repoRoot, "scorecards") + ":/scorecards",
		cfg.Images.Grader.Name,
		"grader", "-oracle-bin", "oracle", "-agent-bin", "/candidate/agent.sh", "-scenarios", "/scenarios", "-out", "/scorecards/" + slug + ".json", "-timeout", cfg.GraderTimeout,
	}
	cmd := exec.Command("docker", args...)
	cmd.Stdout = io.MultiWriter(os.Stdout, logFile)
	cmd.Stderr = io.MultiWriter(os.Stderr, logFile)
	return annotateCmdErr("grade frozen candidate", cmd.Run())
}

type runTimings struct {
	generationSeconds float64
	gradingSeconds    float64
}

type codexUsage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
}

type codexEvent struct {
	Type  string      `json:"type"`
	Usage *codexUsage `json:"usage"`
}

// parseCodexMetrics reads a codex exec --json event log and returns aggregate
// usage. turn.completed carries cumulative session usage, so the LAST one holds
// the run totals; turns/items are counted across the stream.
func parseCodexMetrics(eventsPath string) map[string]interface{} {
	f, err := os.Open(eventsPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 32*1024*1024)
	var last *codexUsage
	turns, items := 0, 0
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev codexEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "turn.completed":
			turns++
			if ev.Usage != nil {
				last = ev.Usage
			}
		case "item.completed":
			items++
		}
	}
	m := map[string]interface{}{"turns": turns, "item_count": items}
	if last != nil {
		m["input_tokens"] = last.InputTokens
		m["cached_input_tokens"] = last.CachedInputTokens
		m["output_tokens"] = last.OutputTokens
		m["reasoning_tokens"] = last.ReasoningOutputTokens
		m["total_tokens"] = last.InputTokens + last.OutputTokens
	}
	return m
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func annotateScorecard(repoRoot string, cfg harnessConfig, slug string, paths runPaths, timings runTimings) error {
	b, err := os.ReadFile(paths.scorecard)
	if err != nil {
		return err
	}
	var card map[string]interface{}
	if err := json.Unmarshal(b, &card); err != nil {
		return err
	}
	harness := map[string]interface{}{
		"slug":                               slug,
		"harness_id":                         cfg.ID,
		"harness_type":                       cfg.Driver,
		"model_label":                        cfg.Model,
		"codex_model":                        cfg.Model,
		"reasoning_effort":                   cfg.CLI.ReasoningEffort,
		"codex_surface":                      "codex exec",
		"codex_inside_container":             true,
		"oracle_available_during_generation": false,
		"attempt_policy":                     "single",
		"image":                              cfg.Images.Agent.Name,
		"generation_image":                   cfg.Images.Agent.Name,
		"grading_image":                      cfg.Images.Grader.Name,
		"run_dir":                            filepath.ToSlash(filepath.Join("runs", slug)),
		"transcript_dir":                     filepath.ToSlash(filepath.Join("transcripts", slug)),
		"codex_events":                       filepath.ToSlash(filepath.Join("transcripts", slug, "codex-events.jsonl")),
		"codex_final_message":                filepath.ToSlash(filepath.Join("transcripts", slug, "codex-final.md")),
		"grader_log":                         filepath.ToSlash(filepath.Join("transcripts", slug, "grader.log")),
	}
	if cfg.Provider != nil {
		harness["model_provider"] = cfg.Provider.ID
		harness["provider_wire_api"] = cfg.Provider.WireAPI
		harness["provider_upstream_base_url"] = cfg.Provider.Bridge.UpstreamBaseURL
		harness["provider_bridge_image"] = cfg.Provider.Bridge.Image
		harness["bridge_log"] = filepath.ToSlash(filepath.Join("transcripts", slug, "bridge.log"))
	}
	card["harness"] = harness
	metrics := parseCodexMetrics(filepath.Join(paths.transcriptDir, "codex-events.jsonl"))
	if metrics == nil {
		metrics = map[string]interface{}{}
	}
	metrics["generation_seconds"] = round2(timings.generationSeconds)
	metrics["grading_seconds"] = round2(timings.gradingSeconds)
	card["metrics"] = metrics
	out, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return os.WriteFile(paths.scorecard, out, 0o644)
}

func authStatus(repoRoot string, cfg harnessConfig) error {
	if _, err := ensurePersistentAuthHome(repoRoot, cfg); err != nil {
		return err
	}
	if err := ensureImage(repoRoot, cfg.Images.Agent); err != nil {
		return err
	}
	codexHome, _, err := prepareTempCodexHome(repoRoot, cfg, true, "")
	if err != nil {
		return err
	}
	defer os.RemoveAll(codexHome)
	args := []string{"run", "--rm", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "-e", "CODEX_HOME=/codex-home", "-v", codexHome + ":/codex-home", cfg.Images.Agent.Name, "codex", "login", "status"}
	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return annotateCmdErr("codex login status", cmd.Run())
}

func authLogin(repoRoot string, cfg harnessConfig, loginArgs []string, tty bool) error {
	authHome, err := ensurePersistentAuthHome(repoRoot, cfg)
	if err != nil {
		return err
	}
	if err := ensureImage(repoRoot, cfg.Images.Agent); err != nil {
		return err
	}
	codexHome, _, err := prepareTempCodexHome(repoRoot, cfg, false, "")
	if err != nil {
		return err
	}
	defer os.RemoveAll(codexHome)
	args := []string{"run", "--rm", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())}
	if tty {
		args = append(args, "-it")
	} else {
		args = append(args, "-i")
	}
	args = append(args, "-e", "CODEX_HOME=/codex-home", "-v", codexHome+":/codex-home", cfg.Images.Agent.Name)
	args = append(args, loginArgs...)
	cmd := exec.Command("docker", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return annotateCmdErr(strings.Join(loginArgs, " "), err)
	}
	if err := copyFile(filepath.Join(codexHome, "auth.json"), filepath.Join(authHome, "auth.json"), 0o600); err != nil {
		return fmt.Errorf("copy Codex auth.json after login: %w", err)
	}
	return nil
}

func copyWorkspaceFiles(repoRoot, workspace string, files []workspaceFile) error {
	for _, file := range files {
		if file.Source == "" {
			return errors.New("workspace.files source is required")
		}
		if file.Target == "" {
			return errors.New("workspace.files target is required")
		}
		targetRel, err := cleanWorkspaceTarget(file.Target)
		if err != nil {
			return fmt.Errorf("invalid workspace target %q: %w", file.Target, err)
		}
		src := resolvePath(repoRoot, file.Source)
		info, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("stat workspace source %s: %w", src, err)
		}
		if info.IsDir() {
			return fmt.Errorf("workspace source %s is a directory; configure files explicitly", src)
		}
		dst := filepath.Join(workspace, filepath.FromSlash(targetRel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := copyFile(src, dst, 0o644); err != nil {
			return fmt.Errorf("copy workspace file %s to %s: %w", src, targetRel, err)
		}
	}
	return nil
}

func cleanWorkspaceTarget(target string) (string, error) {
	const prefix = "/workspace/"
	if target == "/workspace" {
		return "", errors.New("target must name a file under /workspace")
	}
	if strings.HasPrefix(target, prefix) {
		target = strings.TrimPrefix(target, prefix)
	} else if filepath.IsAbs(target) {
		return "", errors.New("absolute targets must be under /workspace")
	}
	target = filepath.ToSlash(filepath.Clean(target))
	if target == "." || target == ".." || strings.HasPrefix(target, "../") {
		return "", errors.New("target must stay inside /workspace")
	}
	return target, nil
}

func authImportHost(repoRoot string, cfg harnessConfig) error {
	codexHome, err := ensurePersistentAuthHome(repoRoot, cfg)
	if err != nil {
		return err
	}
	hostHome := os.Getenv("CODEX_HOME")
	if hostHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		hostHome = filepath.Join(home, ".codex")
	}
	hostAuth := filepath.Join(hostHome, "auth.json")
	b, err := os.ReadFile(hostAuth)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("host Codex auth is not file-backed; run auth login-device or auth login-api-key for the eval CODEX_HOME")
		}
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("host Codex auth.json is not valid JSON: %w", err)
	}
	validKeys := []string{"OPENAI_API_KEY", "tokens", "agent_identity", "personal_access_token", "bedrock_api_key"}
	ok := false
	for _, k := range validKeys {
		if _, exists := raw[k]; exists {
			ok = true
			break
		}
	}
	if !ok {
		return errors.New("host Codex auth.json does not contain supported file-backed credentials")
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), b, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(codexHome, "auth.json"), 0o600); err != nil {
		return err
	}
	fmt.Println("imported file-backed Codex auth into eval CODEX_HOME")
	return nil
}

func skipTransientCodexDirs(path string, d fs.DirEntry) bool {
	if !d.IsDir() {
		return false
	}
	name := d.Name()
	return name == ".codex" || name == ".codex-eval" || name == "codex-home"
}

func copyDir(src, dst string, skip func(string, fs.DirEntry) bool) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != src && skip != nil && skip(path, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		to := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		if d.IsDir() {
			return os.MkdirAll(to, mode.Perm())
		}
		if mode&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, to)
		}
		return copyFile(path, to, mode.Perm())
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(dst, mode)
}

func annotateCmdErr(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s failed: %w", action, err)
}
