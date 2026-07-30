package hook

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// makeGrokPayload builds a Grok Build PreToolUse stdin payload. Grok Build
// sends camelCase keys (toolName/toolInput), unlike Claude Code's snake_case.
func makeGrokPayload(toolName, command string) string {
	payload := map[string]any{
		"hookEventName": "pre_tool_use",
		"toolName":      toolName,
		"toolInput":     map[string]any{"command": command},
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

// extractGrokDenyReason validates the Grok Build deny envelope
// ({"decision":"deny","reason":"..."}) and returns the reason.
func extractGrokDenyReason(t *testing.T, output string) string {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, output)
	}
	if result["decision"] != "deny" {
		t.Errorf("decision = %v, want deny", result["decision"])
	}
	if _, ok := result["hookSpecificOutput"]; ok {
		t.Errorf("hookSpecificOutput must not be set in Grok Build response: %s", output)
	}
	reason, _ := result["reason"].(string)
	if reason == "" {
		t.Fatalf("reason is empty")
	}
	return reason
}

func TestRunGrokDeniesSupportedCommand(t *testing.T) {
	commands := []string{"git", "go"}
	snipBin := "/usr/local/bin/snip"

	input := makeGrokPayload("run_terminal_cmd", "git log -10")
	var out bytes.Buffer
	denied, err := RunGrok(strings.NewReader(input), &out, commands, nil, snipBin)
	if err != nil {
		t.Fatalf("RunGrok: %v", err)
	}
	if !denied {
		t.Fatal("expected denied=true for supported command")
	}
	if out.Len() == 0 {
		t.Fatal("expected deny output for supported command, got empty")
	}

	reason := extractGrokDenyReason(t, out.String())
	wantSuggestion := `"/usr/local/bin/snip" run -- git log -10`
	if !strings.Contains(reason, wantSuggestion) {
		t.Errorf("reason = %q, want it to contain %q", reason, wantSuggestion)
	}
}

// TestRunGrokBashAliasToolName covers payloads carrying the Claude-style tool
// name "Bash" instead of Grok Build's native "run_terminal_cmd".
func TestRunGrokBashAliasToolName(t *testing.T) {
	commands := []string{"git"}
	snipBin := "/usr/local/bin/snip"

	input := makeGrokPayload("Bash", "git status")
	var out bytes.Buffer
	denied, err := RunGrok(strings.NewReader(input), &out, commands, nil, snipBin)
	if err != nil {
		t.Fatalf("RunGrok: %v", err)
	}
	if !denied {
		t.Fatal("expected denied=true for Bash alias tool name")
	}
	reason := extractGrokDenyReason(t, out.String())
	wantSuggestion := `"/usr/local/bin/snip" run -- git status`
	if !strings.Contains(reason, wantSuggestion) {
		t.Errorf("reason = %q, want it to contain %q", reason, wantSuggestion)
	}
}

// TestRunGrokShellToolNames pins issue #145: the released Grok CLI sends
// `run_terminal_command`, which was not classified as a shell tool, so the hook
// exited 0 with empty stdout — an allow — and no Grok session was ever filtered.
func TestRunGrokShellToolNames(t *testing.T) {
	for _, toolName := range []string{"run_terminal_command", "run_terminal_cmd", "Bash"} {
		t.Run(toolName, func(t *testing.T) {
			input := makeGrokPayload(toolName, "git status")
			var out bytes.Buffer
			denied, err := RunGrok(strings.NewReader(input), &out, []string{"git"}, nil, "/usr/local/bin/snip")
			if err != nil {
				t.Fatalf("RunGrok: %v", err)
			}
			if !denied {
				t.Fatalf("expected denied=true for tool name %q, got an empty allow", toolName)
			}
			reason := extractGrokDenyReason(t, out.String())
			if want := `"/usr/local/bin/snip" run -- git status`; !strings.Contains(reason, want) {
				t.Errorf("reason = %q, want it to contain %q", reason, want)
			}
		})
	}
}

func TestRunGrokUnsupportedPassthrough(t *testing.T) {
	commands := []string{"git", "go"}
	snipBin := "/usr/local/bin/snip"

	input := makeGrokPayload("run_terminal_cmd", "ls -la")
	var out bytes.Buffer
	denied, err := RunGrok(strings.NewReader(input), &out, commands, nil, snipBin)
	if err != nil {
		t.Fatalf("RunGrok: %v", err)
	}
	if denied {
		t.Error("expected denied=false for unsupported command")
	}
	if out.Len() != 0 {
		t.Errorf("expected no output for unsupported command, got: %s", out.String())
	}
}

func TestRunGrokAlreadyRewritten(t *testing.T) {
	commands := []string{"git"}
	snipBin := "/usr/local/bin/snip"

	already := `"/usr/local/bin/snip" run -- git status`
	input := makeGrokPayload("run_terminal_cmd", already)
	var out bytes.Buffer
	denied, err := RunGrok(strings.NewReader(input), &out, commands, nil, snipBin)
	if err != nil {
		t.Fatalf("RunGrok: %v", err)
	}
	if denied {
		t.Error("expected denied=false for already-rewritten command")
	}
	if out.Len() != 0 {
		t.Errorf("expected no output for already-rewritten command, got: %s", out.String())
	}
}

func TestRunGrokMultiSegmentSuggestionIncludesTail(t *testing.T) {
	commands := []string{"git"}
	snipBin := "/usr/local/bin/snip"

	input := makeGrokPayload("run_terminal_cmd", "git add . && git commit -m 'fix'")
	var out bytes.Buffer
	denied, err := RunGrok(strings.NewReader(input), &out, commands, nil, snipBin)
	if err != nil {
		t.Fatalf("RunGrok: %v", err)
	}
	if !denied {
		t.Fatal("expected denied=true")
	}

	reason := extractGrokDenyReason(t, out.String())
	wantSuggestion := `"/usr/local/bin/snip" run -- git add . && git commit -m 'fix'`
	if !strings.Contains(reason, wantSuggestion) {
		t.Errorf("reason = %q, want it to contain %q", reason, wantSuggestion)
	}
}

func TestRunGrokEnvVarPrefix(t *testing.T) {
	commands := []string{"go"}
	snipBin := "/usr/local/bin/snip"

	input := makeGrokPayload("run_terminal_cmd", "CGO_ENABLED=0 go test ./...")
	var out bytes.Buffer
	denied, err := RunGrok(strings.NewReader(input), &out, commands, nil, snipBin)
	if err != nil {
		t.Fatalf("RunGrok: %v", err)
	}
	if !denied {
		t.Fatal("expected denied=true")
	}

	reason := extractGrokDenyReason(t, out.String())
	wantSuggestion := `CGO_ENABLED=0 "/usr/local/bin/snip" run -- go test ./...`
	if !strings.Contains(reason, wantSuggestion) {
		t.Errorf("reason = %q, want it to contain %q", reason, wantSuggestion)
	}
}

func TestRunGrokNonShellTool(t *testing.T) {
	commands := []string{"git"}
	snipBin := "/usr/local/bin/snip"

	payload := map[string]any{
		"hookEventName": "pre_tool_use",
		"toolName":      "read_file",
		"toolInput":     map[string]any{"path": "/tmp/foo"},
	}
	data, _ := json.Marshal(payload)

	var out bytes.Buffer
	denied, err := RunGrok(strings.NewReader(string(data)), &out, commands, nil, snipBin)
	if err != nil {
		t.Fatalf("RunGrok: %v", err)
	}
	if denied {
		t.Error("expected denied=false for non-shell tool")
	}
	if out.Len() != 0 {
		t.Errorf("expected no output for non-shell tool, got: %s", out.String())
	}
}

func TestRunGrokEmptyCommand(t *testing.T) {
	commands := []string{"git"}
	snipBin := "/usr/local/bin/snip"

	input := makeGrokPayload("run_terminal_cmd", "")
	var out bytes.Buffer
	denied, err := RunGrok(strings.NewReader(input), &out, commands, nil, snipBin)
	if err != nil {
		t.Fatalf("RunGrok: %v", err)
	}
	if denied {
		t.Error("expected denied=false for empty command")
	}
	if out.Len() != 0 {
		t.Errorf("expected no output for empty command, got: %s", out.String())
	}
}

func TestRunGrokMalformedJSON(t *testing.T) {
	commands := []string{"git"}
	snipBin := "/usr/local/bin/snip"

	var out bytes.Buffer
	denied, err := RunGrok(strings.NewReader("{invalid json"), &out, commands, nil, snipBin)
	if err != nil {
		t.Fatalf("RunGrok must not error on malformed JSON: %v", err)
	}
	if denied {
		t.Error("expected denied=false for malformed JSON")
	}
	if out.Len() != 0 {
		t.Errorf("expected no output for malformed JSON, got: %s", out.String())
	}
}

// TestRunGrokClaudeStylePayloadIgnored documents that RunGrok only consumes
// Grok Build's camelCase payload: a Claude Code snake_case payload carries no
// toolName and must pass through untouched.
func TestRunGrokClaudeStylePayloadIgnored(t *testing.T) {
	commands := []string{"git"}
	snipBin := "/usr/local/bin/snip"

	input := makePayload("Bash", "git status") // snake_case tool_name/tool_input
	var out bytes.Buffer
	denied, err := RunGrok(strings.NewReader(input), &out, commands, nil, snipBin)
	if err != nil {
		t.Fatalf("RunGrok: %v", err)
	}
	if denied {
		t.Error("expected denied=false for Claude-style payload")
	}
	if out.Len() != 0 {
		t.Errorf("expected no output for Claude-style payload, got: %s", out.String())
	}
}
