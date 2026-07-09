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

// RunCodex reads a Codex PreToolUse JSON payload from r, determines if the
// command matches a snip filter, and writes a deny-with-suggestion response
// telling Codex (and the user) to re-run the command through snip.
//
// Codex's PreToolUse hook cannot rewrite the command in place — only "deny"
// with a free-form reason is honored. See openai/codex#18491. When that
// limitation is lifted, this function can return updatedInput like Run does.
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

	cmdSet := make(map[string]struct{}, len(commands))
	for _, c := range commands {
		cmdSet[c] = struct{}{}
	}

	sug := suggestSnipRerun(ti.Command, cmdSet, prefixes, snipBin)
	if sug.alreadySnip {
		return nil
	}
	if sug.command == "" {
		if audit {
			hookaudit.Append(hookaudit.Event{
				Timestamp: time.Now().UTC(),
				Command:   ti.Command,
				Base:      sug.base,
				Matched:   false,
				Rewritten: false,
				Agent:     codexAgent,
			})
		}
		return nil
	}

	reason := fmt.Sprintf("snip can filter this command. Re-run as: %s", sug.command)

	output := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		},
	}

	if audit {
		hookaudit.Append(hookaudit.Event{
			Timestamp: time.Now().UTC(),
			Command:   ti.Command,
			Base:      sug.base,
			Matched:   true,
			Rewritten: false,
			Agent:     codexAgent,
		})
	}

	enc := json.NewEncoder(w)
	return enc.Encode(output)
}
