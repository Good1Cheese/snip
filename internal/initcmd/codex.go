package initcmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/edouard-claude/snip/internal/hook"
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

// initCodex installs the snip Codex hook in ~/.codex/hooks.json. Codex hooks
// are enabled by default. Existing deprecated codex_hooks=true settings are
// migrated to the current hooks=true name without reserializing config.toml.
func initCodex(snipBin, home, filterDir string) error {
	configPath := codexConfigPath(home)
	migrated, err := migrateDeprecatedCodexHooks(configPath)
	if err != nil {
		return fmt.Errorf("check codex config.toml: %w", err)
	}

	if err := os.MkdirAll(codexConfigDir(home), 0o755); err != nil {
		return fmt.Errorf("create codex config dir: %w", err)
	}

	// Reuse the command-rewrite quoting so the installed hook works with the
	// platform shell, including cmd.exe on Windows.
	hookCommand := hook.QuoteBinFor(snipBin, runtime.GOOS) + " " + codexHookSubcommand

	hooksPath := codexHooksPath(home)
	if err := patchCodexHooks(hooksPath, hookCommand); err != nil {
		return fmt.Errorf("patch codex hooks: %w", err)
	}

	legacyAgentsMd := detectLegacyAgentsMD(".")

	fmt.Println("snip init complete:")
	fmt.Printf("  agent: codex\n")
	fmt.Printf("  hook: %s\n", hookCommand)
	fmt.Printf("  filters: %s\n", filterDir)
	fmt.Printf("  hooks: %s\n", hooksPath)
	fmt.Println()
	fmt.Println("note: Codex hooks require Codex CLI 0.131.0 or later for PreToolUse")
	fmt.Println("      updatedInput. Supported commands are transparently")
	fmt.Println("      rewritten through snip. For older Codex releases use:")
	fmt.Println("      snip init --agent codex --mode prompt")
	if migrated {
		fmt.Printf("      original config.toml backed up to %s.bak\n", configPath)
	}
	if legacyAgentsMd != "" {
		fmt.Printf("      legacy %s detected — remove with `snip init --agent codex --uninstall`\n", legacyAgentsMd)
		fmt.Println("      or delete it manually if it has been edited or committed")
	}

	return nil
}

// uninstallCodex removes only the snip entry from ~/.codex/hooks.json. It
// leaves config.toml untouched so it cannot disable the user's other Codex
// lifecycle hooks. It also removes a legacy AGENTS.md prompt file if it still
// matches the snip template.
func uninstallCodex() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}

	hooksPath := codexHooksPath(home)
	if err := unpatchCodexHooks(hooksPath); err != nil {
		return fmt.Errorf("unpatch codex hooks: %w", err)
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

// errCodexHooksExplicitlyDisabled is returned when the user has disabled
// Codex hooks in config.toml. We refuse to silently override their choice.
var errCodexHooksExplicitlyDisabled = errors.New(
	"~/.codex/config.toml has [features].hooks = false (or deprecated codex_hooks = false); " +
		"set it to true (or remove the line) and re-run snip init")

var codexHooksSetting = regexp.MustCompile(`^(\s*)(?:codex_hooks|"codex_hooks"|'codex_hooks')(\s*=)`)
var codexHooksDottedSetting = regexp.MustCompile(`^(\s*features\.)(?:codex_hooks|"codex_hooks"|'codex_hooks')(\s*=)`)

// migrateDeprecatedCodexHooks validates the user's explicit hooks setting and
// replaces only the deprecated [features].codex_hooks=true key. A line-level
// edit preserves their comments, quotes, key order, and unrelated settings.
func migrateDeprecatedCodexHooks(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return false, nil
	}

	cfg := make(map[string]any)
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return false, nil
	}
	features, _ := cfg["features"].(map[string]any)
	if featureFlagDisabled(features, "hooks") || featureFlagDisabled(features, "codex_hooks") {
		return false, errCodexHooksExplicitlyDisabled
	}
	legacyEnabled, _ := features["codex_hooks"].(bool)
	if !legacyEnabled {
		return false, nil
	}

	canonical, canonicalPresent := features["hooks"]
	canonicalEnabled, canonicalIsBool := canonical.(bool)
	if canonicalPresent && !canonicalIsBool {
		return false, nil
	}
	updated, changed := rewriteDeprecatedCodexHooks(string(data), canonicalPresent && canonicalEnabled)
	if !changed {
		return false, nil
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		return false, nil
	}
	mode := info.Mode().Perm()
	if err := os.WriteFile(path+".bak", data, mode); err != nil {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(updated), mode); err != nil {
		return false, nil
	}
	return true, nil
}

func featureFlagDisabled(features map[string]any, key string) bool {
	value, present := features[key]
	disabled, isBool := value.(bool)
	return present && isBool && !disabled
}

// rewriteDeprecatedCodexHooks updates the legacy key in [features] or its
// top-level dotted-key form. If the canonical key already exists, it comments
// out the redundant legacy setting rather than creating a duplicate key or
// discarding its inline comment.
func rewriteDeprecatedCodexHooks(config string, canonicalPresent bool) (string, bool) {
	lines := strings.SplitAfter(config, "\n")
	inFeatures := false
	atTopLevel := true
	multilineDelimiter := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if multilineDelimiter != "" {
			if strings.Count(line, multilineDelimiter)%2 == 1 {
				multilineDelimiter = ""
			}
			continue
		}
		if strings.Contains(line, `"""`) {
			if strings.Count(line, `"""`)%2 == 1 {
				multilineDelimiter = `"""`
			}
			continue
		}
		if strings.Contains(line, `'''`) {
			if strings.Count(line, `'''`)%2 == 1 {
				multilineDelimiter = `'''`
			}
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			inFeatures = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")) == "features"
			atTopLevel = false
			continue
		}

		setting := codexHooksSetting
		if atTopLevel {
			setting = codexHooksDottedSetting
		} else if !inFeatures {
			continue
		}
		if !setting.MatchString(line) {
			continue
		}
		if canonicalPresent {
			lines[i] = "# Deprecated Codex setting removed by snip: " + line
		} else {
			lines[i] = setting.ReplaceAllString(line, "${1}hooks${2}")
		}
		return strings.Join(lines, ""), true
	}
	return config, false
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
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 {
		if err := json.Unmarshal(trimmed, &m); err != nil {
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
