package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/edouard-claude/snip/internal/hookaudit"
)

// codexAgent is the value written to hookaudit.Event.Agent for Codex events.
const codexAgent = "codex"

// RunCodex reads a Codex PreToolUse JSON payload from r, determines whether the
// command can be safely rewritten through snip, and writes the updated tool
// input to w. If no safe rewrite is available, nothing is written and Codex
// executes the original command through its normal permission flow.
//
// Codex only accepts updatedInput together with permissionDecision "allow".
// Therefore this handler rewrites a command only when every runnable segment is
// recognized by snip. Mixed or unverifiable commands pass through unchanged so
// an unknown segment can never bypass Codex's native approval rules.
//
// Always returns nil; the caller must exit 0 (graceful degradation).
func RunCodex(r io.Reader, w io.Writer, commands []string, prefixes []TransparentPrefix, snipBin string) error {
	audit := hookaudit.Enabled()

	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}

	var input hookInput
	if err := json.Unmarshal(data, &input); err != nil {
		return nil
	}

	if input.ToolName != "Bash" {
		return nil
	}

	var ti toolInput
	if err := json.Unmarshal(input.ToolInput, &ti); err != nil {
		return nil
	}
	if ti.Command == "" {
		return nil
	}

	// Command substitutions and carriage returns contain executable content that
	// cannot be safely attested by the segment rewriter. Preserve Codex's native
	// permission flow by leaving them untouched.
	if HasUnverifiableConstruct(ti.Command) {
		return nil
	}

	cmdSet := make(map[string]struct{}, len(commands))
	for _, c := range commands {
		cmdSet[c] = struct{}{}
	}

	res := RewriteCommand(ti.Command, cmdSet, prefixes, snipBin)
	if !res.Changed || !res.AllKnown {
		if audit {
			base := firstBase(ti.Command)
			_, matched := cmdSet[base]
			hookaudit.Append(hookaudit.Event{
				Timestamp: time.Now().UTC(),
				Command:   ti.Command,
				Base:      base,
				Matched:   matched,
				Rewritten: false,
				Agent:     codexAgent,
			})
		}
		return nil
	}

	// Preserve every original tool_input field and replace only the command.
	var originalInput map[string]any
	if err := json.Unmarshal(input.ToolInput, &originalInput); err != nil {
		return nil
	}
	originalInput["command"] = res.Command

	output := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "allow",
			"permissionDecisionReason": "snip auto-rewrite",
			"updatedInput":             originalInput,
		},
	}

	if audit {
		hookaudit.Append(hookaudit.Event{
			Timestamp: time.Now().UTC(),
			Command:   ti.Command,
			Base:      firstBase(ti.Command),
			Matched:   true,
			Rewritten: true,
			Agent:     codexAgent,
		})
	}

	enc := json.NewEncoder(w)
	return enc.Encode(output)
}
