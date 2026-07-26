package initcmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

func TestPatchCodexHooksNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	hookCommand := "/usr/local/bin/snip hook codex"

	if err := patchCodexHooks(path, hookCommand); err != nil {
		t.Fatalf("patch: %v", err)
	}

	cfg := readSettings(t, path)
	hooks, ok := cfg["hooks"].(map[string]any)
	if !ok {
		t.Fatal("hooks not found")
	}
	preToolUse, ok := hooks["PreToolUse"].([]any)
	if !ok {
		t.Fatal("PreToolUse not found or not array")
	}
	if len(preToolUse) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(preToolUse))
	}
	entry := preToolUse[0].(map[string]any)
	if entry["matcher"] != "Bash" {
		t.Errorf("matcher = %v, want Bash", entry["matcher"])
	}
	entryHooks := entry["hooks"].([]any)
	hook := entryHooks[0].(map[string]any)
	if hook["command"] != hookCommand {
		t.Errorf("command = %v, want %s", hook["command"], hookCommand)
	}
}

func TestPatchCodexHooksWhitespaceOnlyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(path, []byte(" \n\t"), 0o600); err != nil {
		t.Fatal(err)
	}

	hookCommand := "/usr/local/bin/snip hook codex"
	if err := patchCodexHooks(path, hookCommand); err != nil {
		t.Fatalf("patch whitespace-only hooks.json: %v", err)
	}

	cfg := readSettings(t, path)
	hooks, ok := cfg["hooks"].(map[string]any)
	if !ok {
		t.Fatal("hooks not found")
	}
	preToolUse, ok := hooks["PreToolUse"].([]any)
	if !ok || len(preToolUse) != 1 {
		t.Fatalf("PreToolUse = %#v, want one entry", hooks["PreToolUse"])
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
}

func TestPatchCodexHooksIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	hookCommand := "/usr/local/bin/snip hook codex"

	_ = patchCodexHooks(path, hookCommand)
	_ = patchCodexHooks(path, hookCommand)

	cfg := readSettings(t, path)
	hooks := cfg["hooks"].(map[string]any)
	preToolUse := hooks["PreToolUse"].([]any)
	if len(preToolUse) != 1 {
		t.Errorf("expected 1 entry after double patch, got %d", len(preToolUse))
	}
}

func TestPatchCodexHooksPreservesForeignEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")

	existing := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{"type": "command", "command": "/opt/other/guard"},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := patchCodexHooks(path, "/usr/local/bin/snip hook codex"); err != nil {
		t.Fatalf("patch: %v", err)
	}

	cfg := readSettings(t, path)
	preToolUse := cfg["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(preToolUse) != 2 {
		t.Fatalf("expected 2 entries (foreign + snip), got %d", len(preToolUse))
	}
	first := preToolUse[0].(map[string]any)
	firstHooks := first["hooks"].([]any)
	firstHook := firstHooks[0].(map[string]any)
	if firstHook["command"] != "/opt/other/guard" {
		t.Errorf("foreign entry not preserved: %v", firstHook["command"])
	}
}

func TestUnpatchCodexHooksRemovesOnlySnip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")

	existing := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{"type": "command", "command": "/opt/other/guard"},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	_ = patchCodexHooks(path, "/usr/local/bin/snip hook codex")
	if err := unpatchCodexHooks(path); err != nil {
		t.Fatalf("unpatch: %v", err)
	}

	cfg := readSettings(t, path)
	preToolUse := cfg["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(preToolUse) != 1 {
		t.Fatalf("expected 1 entry after unpatch, got %d", len(preToolUse))
	}
	remaining := preToolUse[0].(map[string]any)
	hookEntry := remaining["hooks"].([]any)[0].(map[string]any)
	if hookEntry["command"] != "/opt/other/guard" {
		t.Errorf("foreign entry not preserved: %v", hookEntry["command"])
	}
}

func TestPatchCodexConfigTomlNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	res, err := patchCodexConfigToml(path, true)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if res.noOp {
		t.Error("noOp should be false for fresh write")
	}
	if res.backupWritten {
		t.Error("backupWritten should be false for fresh install (no original to back up)")
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Error("no backup should be written when there was no original file")
	}

	got := readToml(t, path)
	features, ok := got["features"].(map[string]any)
	if !ok {
		t.Fatalf("features section missing: %#v", got)
	}
	if v, _ := features["codex_hooks"].(bool); !v {
		t.Errorf("codex_hooks = %v, want true", features["codex_hooks"])
	}
}

func TestPatchCodexConfigTomlPreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	original := []byte(`model = "gpt-5"

[features]
some_other_flag = true

[other]
key = "value"
`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := patchCodexConfigToml(path, true)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if res.noOp {
		t.Error("noOp should be false when a write happened")
	}
	if !res.backupWritten {
		t.Error("backupWritten should be true when an existing file was rewritten")
	}

	got := readToml(t, path)
	if got["model"] != "gpt-5" {
		t.Errorf("model = %v, want gpt-5", got["model"])
	}
	features := got["features"].(map[string]any)
	if v, _ := features["some_other_flag"].(bool); !v {
		t.Error("some_other_flag was dropped")
	}
	if v, _ := features["codex_hooks"].(bool); !v {
		t.Error("codex_hooks not set")
	}
	other := got["other"].(map[string]any)
	if other["key"] != "value" {
		t.Errorf("other.key = %v, want value", other["key"])
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("backup not written: %v", err)
	}
	if string(bak) != string(original) {
		t.Error("backup does not match original")
	}
}

func TestPatchCodexConfigTomlAlreadyEnabledIsNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	original := []byte(`# user comment preserved
[features]
codex_hooks = true
`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	infoBefore, _ := os.Stat(path)

	res, err := patchCodexConfigToml(path, true)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if !res.noOp {
		t.Error("noOp should be true for already-enabled state")
	}
	if res.backupWritten {
		t.Error("backupWritten should be false for no-op")
	}
	infoAfter, _ := os.Stat(path)
	if infoBefore.ModTime() != infoAfter.ModTime() {
		t.Error("file should not have been rewritten")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Errorf("file was rewritten; comments lost. got:\n%s", got)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Error("no backup should be written for no-op")
	}
}

func TestPatchCodexConfigTomlExplicitOptOutRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(path, []byte("[features]\ncodex_hooks = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := patchCodexConfigToml(path, true)
	if err == nil {
		t.Fatal("expected error when codex_hooks is explicitly false")
	}
	if !errors.Is(err, errCodexHooksExplicitlyDisabled) {
		t.Errorf("err = %v, want errCodexHooksExplicitlyDisabled", err)
	}
}

func TestUnpatchCodexConfigTomlSetsFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	if _, err := patchCodexConfigToml(path, true); err != nil {
		t.Fatalf("patch: %v", err)
	}
	if _, err := patchCodexConfigToml(path, false); err != nil {
		t.Fatalf("unpatch: %v", err)
	}

	got := readToml(t, path)
	features, ok := got["features"].(map[string]any)
	if !ok {
		t.Fatalf("features missing")
	}
	if v, _ := features["codex_hooks"].(bool); v {
		t.Errorf("codex_hooks = true after unpatch; want false")
	}
}

func TestInitCodexEndToEnd(t *testing.T) {
	home := t.TempDir()
	filterDir := filepath.Join(home, ".config", "snip", "filters")
	if err := os.MkdirAll(filterDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := initCodex("/usr/local/bin/snip", home, filterDir); err != nil {
		t.Fatalf("initCodex: %v", err)
	}

	hooks := readSettings(t, codexHooksPath(home))
	preToolUse := hooks["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(preToolUse) != 1 {
		t.Fatalf("expected 1 PreToolUse entry, got %d", len(preToolUse))
	}
	entryHooks := preToolUse[0].(map[string]any)["hooks"].([]any)
	cmd := entryHooks[0].(map[string]any)["command"].(string)
	if !strings.HasSuffix(cmd, " hook codex") {
		t.Errorf("hook command = %q, want suffix ' hook codex'", cmd)
	}

	conf := readToml(t, codexConfigPath(home))
	features := conf["features"].(map[string]any)
	if v, _ := features["codex_hooks"].(bool); !v {
		t.Errorf("codex_hooks = %v, want true", features["codex_hooks"])
	}
}

func TestInitCodexThenUninstallSymmetric(t *testing.T) {
	home := t.TempDir()
	filterDir := filepath.Join(home, ".config", "snip", "filters")
	_ = os.MkdirAll(filterDir, 0o755)

	if err := initCodex("/usr/local/bin/snip", home, filterDir); err != nil {
		t.Fatalf("initCodex: %v", err)
	}

	t.Setenv("HOME", home)
	if err := uninstallCodex(); err != nil {
		t.Fatalf("uninstallCodex: %v", err)
	}

	hooks := readSettings(t, codexHooksPath(home))
	if h, ok := hooks["hooks"].(map[string]any); ok {
		if _, ok := h["PreToolUse"]; ok {
			t.Error("PreToolUse should be removed after uninstall")
		}
	}

	conf := readToml(t, codexConfigPath(home))
	features, ok := conf["features"].(map[string]any)
	if !ok {
		t.Fatal("features should exist with codex_hooks=false")
	}
	if v, _ := features["codex_hooks"].(bool); v {
		t.Errorf("codex_hooks should be false after uninstall, got %v", v)
	}
}

func TestDetectLegacyAgentsMD(t *testing.T) {
	dir := t.TempDir()
	if got := detectLegacyAgentsMD(dir); got != "" {
		t.Errorf("detectLegacyAgentsMD on empty dir = %q, want empty", got)
	}

	// Foreign AGENTS.md
	foreign := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(foreign, []byte("# My project rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectLegacyAgentsMD(dir); got != "" {
		t.Errorf("foreign AGENTS.md misdetected as legacy: %q", got)
	}

	// Snip-template AGENTS.md
	if err := os.WriteFile(foreign, []byte(promptContent("/usr/local/bin/snip")), 0o644); err != nil {
		t.Fatal(err)
	}
	got := detectLegacyAgentsMD(dir)
	if got == "" {
		t.Error("snip AGENTS.md not detected as legacy")
	}
}

func TestParseModeFlag(t *testing.T) {
	mode, remaining := parseMode([]string{"--mode", "prompt", "--uninstall"})
	if mode != "prompt" {
		t.Errorf("mode = %q, want prompt", mode)
	}
	if len(remaining) != 1 || remaining[0] != "--uninstall" {
		t.Errorf("remaining = %v, want [--uninstall]", remaining)
	}

	mode, remaining = parseMode([]string{"--mode=hook"})
	if mode != "hook" {
		t.Errorf("mode = %q, want hook", mode)
	}
	if len(remaining) != 0 {
		t.Errorf("remaining = %v, want empty", remaining)
	}

	mode, _ = parseMode([]string{})
	if mode != "" {
		t.Errorf("mode = %q, want empty", mode)
	}
}

func TestRunRejectsUnknownMode(t *testing.T) {
	err := Run([]string{"--agent", "codex", "--mode", "wat"})
	if err == nil {
		t.Fatal("expected error for unknown mode")
	}
	if !strings.Contains(err.Error(), "unknown mode") {
		t.Errorf("err = %q, want to contain 'unknown mode'", err.Error())
	}
}

func readToml(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read toml: %v", err)
	}
	out := make(map[string]any)
	if err := toml.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse toml: %v", err)
	}
	return out
}
