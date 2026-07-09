package initcmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// codexHookSubcommand is the snip subsubcommand Codex invokes.
const codexHookSubcommand = "hook codex"

// codexConfigDir returns the Codex config directory for the given home.
func codexConfigDir(home string) string {
	return filepath.Join(home, ".codex")
}

// codexHooksPath returns the Codex hooks.json path for the given home.
func codexHooksPath(home string) string {
	return filepath.Join(codexConfigDir(home), "hooks.json")
}

// codexConfigPath returns the Codex config.toml path for the given home.
func codexConfigPath(home string) string {
	return filepath.Join(codexConfigDir(home), "config.toml")
}

// initCodex installs the snip Codex hook: writes ~/.codex/hooks.json with a
// PreToolUse entry and patches ~/.codex/config.toml to set
// [features].codex_hooks = true.
func initCodex(snipBin, home, filterDir string) error {
	if err := os.MkdirAll(codexConfigDir(home), 0o755); err != nil {
		return fmt.Errorf("create codex config dir: %w", err)
	}

	hookCommand := snipBin + " " + codexHookSubcommand

	hooksPath := codexHooksPath(home)
	if err := patchCodexHooks(hooksPath, hookCommand); err != nil {
		return fmt.Errorf("patch codex hooks: %w", err)
	}

	configPath := codexConfigPath(home)
	res, err := patchCodexConfigToml(configPath, true)
	if err != nil {
		return fmt.Errorf("patch codex config.toml: %w", err)
	}

	legacyAgentsMd := detectLegacyAgentsMD(".")

	fmt.Println("snip init complete:")
	fmt.Printf("  agent: codex\n")
	fmt.Printf("  hook: %s\n", hookCommand)
	fmt.Printf("  filters: %s\n", filterDir)
	fmt.Printf("  hooks: %s\n", hooksPath)
	fmt.Printf("  config: %s ([features].codex_hooks=true)\n", configPath)
	fmt.Println()
	fmt.Println("note: Codex hooks require a recent Codex CLI release that supports")
	fmt.Println("      the [features].codex_hooks flag. Codex denies matched commands")
	fmt.Println("      with a re-run suggestion (transparent rewrite is tracked upstream:")
	fmt.Println("      https://github.com/openai/codex/issues/18491). For older Codex")
	fmt.Println("      releases use:  snip init --agent codex --mode prompt")
	if res.backupWritten {
		fmt.Printf("      original config.toml backed up to %s.bak (comments are not\n", configPath)
		fmt.Println("      preserved by the TOML round-trip)")
	}
	if legacyAgentsMd != "" {
		fmt.Printf("      legacy %s detected — remove with `snip init --agent codex --uninstall`\n", legacyAgentsMd)
		fmt.Println("      or delete it manually if it has been edited or committed")
	}

	return nil
}

// uninstallCodex removes the snip entry from ~/.codex/hooks.json and flips
// [features].codex_hooks to false in ~/.codex/config.toml. It also removes a
// legacy AGENTS.md prompt file if it still matches the snip template.
func uninstallCodex() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}

	hooksPath := codexHooksPath(home)
	if err := unpatchCodexHooks(hooksPath); err != nil {
		return fmt.Errorf("unpatch codex hooks: %w", err)
	}

	configPath := codexConfigPath(home)
	if _, err := patchCodexConfigToml(configPath, false); err != nil {
		return fmt.Errorf("unpatch codex config.toml: %w", err)
	}

	// Best-effort cleanup of a legacy AGENTS.md. Skipped when the file is shared
	// with another prompt-mode agent (grok), which the helper reports on.
	removeLegacyPromptFile("codex")

	fmt.Println("snip uninstalled (codex)")
	return nil
}

// patchCodexHooks adds the snip hook to ~/.codex/hooks.json. Idempotent:
// existing snip entries are updated in place; foreign entries are preserved.
func patchCodexHooks(path, hookCommand string) error {
	config, mode, err := readJSONMap(path)
	if err != nil {
		return err
	}

	snipMatcher := map[string]any{
		"matcher": "Bash",
		"hooks": []any{
			map[string]any{"type": "command", "command": hookCommand},
		},
	}

	hooks, _ := config["hooks"].(map[string]any)
	if hooks == nil {
		hooks = make(map[string]any)
	}

	var preToolUse []any
	if existing, ok := hooks["PreToolUse"]; ok {
		if arr, ok := existing.([]any); ok {
			preToolUse = arr
		}
	}

	found := false
	for i, entry := range preToolUse {
		if isSnipCodexEntry(entry) {
			preToolUse[i] = snipMatcher
			found = true
			break
		}
	}
	if !found {
		preToolUse = append(preToolUse, snipMatcher)
	}

	hooks["PreToolUse"] = preToolUse
	config["hooks"] = hooks

	return writeJSONMap(path, config, mode)
}

// unpatchCodexHooks removes snip entries from ~/.codex/hooks.json.
func unpatchCodexHooks(path string) error {
	config, mode, err := readJSONMap(path)
	if err != nil {
		return err
	}
	if config == nil {
		return nil
	}

	hooks, _ := config["hooks"].(map[string]any)
	if hooks == nil {
		return nil
	}

	existing, ok := hooks["PreToolUse"]
	if !ok {
		return nil
	}
	arr, ok := existing.([]any)
	if !ok {
		return nil
	}

	var filtered []any
	for _, entry := range arr {
		if !isSnipCodexEntry(entry) {
			filtered = append(filtered, entry)
		}
	}

	if len(filtered) == 0 {
		delete(hooks, "PreToolUse")
	} else {
		hooks["PreToolUse"] = filtered
	}
	if len(hooks) == 0 {
		delete(config, "hooks")
	}

	return writeJSONMap(path, config, mode)
}

// isSnipCodexEntry reports whether a Codex PreToolUse entry was installed by
// snip. Detection looks for "snip hook codex" inside any nested hook command.
func isSnipCodexEntry(entry any) bool {
	m, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	hooksRaw, ok := m["hooks"]
	if !ok {
		return false
	}
	hooksArr, ok := hooksRaw.([]any)
	if !ok {
		return false
	}
	for _, h := range hooksArr {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		cmd, _ := hm["command"].(string)
		if strings.Contains(cmd, codexHookSubcommand) {
			return true
		}
	}
	return false
}

// errCodexHooksExplicitlyDisabled is returned when the user has set
// [features].codex_hooks = false in config.toml and tries to install snip.
// We refuse to silently flip their explicit choice.
var errCodexHooksExplicitlyDisabled = errors.New(
	"~/.codex/config.toml has [features].codex_hooks = false; " +
		"set it to true (or remove the line) and re-run snip init")

// configPatchResult reports the outcome of a config.toml patch so the
// install summary can decide which warnings to print.
type configPatchResult struct {
	// noOp is true when the desired state already matched on disk and no
	// write happened (comments preserved verbatim).
	noOp bool
	// backupWritten is true when an existing file was rewritten and a .bak
	// was saved alongside. False for fresh installs (no original existed).
	backupWritten bool
}

// patchCodexConfigToml sets [features].codex_hooks to enable.
//
// When enable is false (uninstall), the key is forced to false rather than
// deleted so user intent is recorded explicitly.
//
// Returns errCodexHooksExplicitlyDisabled when the user has already pinned
// codex_hooks=false and we'd be installing — we refuse to silently override.
func patchCodexConfigToml(path string, enable bool) (configPatchResult, error) {
	mode := os.FileMode(0o600)
	data, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return configPatchResult{}, fmt.Errorf("read config.toml: %w", err)
	}
	if exists {
		if info, statErr := os.Stat(path); statErr == nil {
			mode = info.Mode().Perm()
		}
	} else {
		data = nil
	}

	cfg := make(map[string]any)
	if len(data) > 0 {
		if err := toml.Unmarshal(data, &cfg); err != nil {
			return configPatchResult{}, fmt.Errorf("parse config.toml: %w", err)
		}
	}

	features, _ := cfg["features"].(map[string]any)
	if features == nil {
		features = make(map[string]any)
	}

	current, present := features["codex_hooks"]
	currentBool, _ := current.(bool)

	if enable {
		if present && !currentBool {
			return configPatchResult{}, errCodexHooksExplicitlyDisabled
		}
		if present && currentBool {
			return configPatchResult{noOp: true}, nil
		}
		features["codex_hooks"] = true
	} else {
		if present && !currentBool {
			return configPatchResult{noOp: true}, nil
		}
		features["codex_hooks"] = false
	}
	cfg["features"] = features

	if exists {
		_ = os.WriteFile(path+".bak", data, mode)
	}

	out, err := toml.Marshal(cfg)
	if err != nil {
		return configPatchResult{}, fmt.Errorf("marshal config.toml: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return configPatchResult{}, fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, out, mode); err != nil {
		return configPatchResult{}, fmt.Errorf("write config.toml: %w", err)
	}
	return configPatchResult{backupWritten: exists}, nil
}

// detectLegacyAgentsMD returns the path to AGENTS.md in dir if its content
// matches the snip prompt-injection template, or "" otherwise. Used to print
// a migration hint without touching files the user may have edited.
func detectLegacyAgentsMD(dir string) string {
	path := filepath.Join(dir, "AGENTS.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if !strings.Contains(string(data), "# Snip - CLI Token Optimizer") {
		return ""
	}
	return path
}

// readJSONMap reads a JSON file as map[string]any, returning the discovered
// file mode. A missing file yields an empty map and the default mode.
// Writes a .bak alongside if the file is non-empty.
func readJSONMap(path string) (map[string]any, os.FileMode, error) {
	mode := os.FileMode(0o644)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]any), mode, nil
		}
		return nil, mode, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	_ = os.WriteFile(path+".bak", data, mode)

	m := make(map[string]any)
	if len(data) > 0 {
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, mode, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
		}
	}
	return m, mode, nil
}

// writeJSONMap writes the given map to path with indented JSON, creating the
// parent directory if needed.
func writeJSONMap(path string, m map[string]any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return os.WriteFile(path, out, mode)
}
