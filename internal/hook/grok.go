package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/edouard-claude/snip/internal/hookaudit"
)

// grokAgent is the value written to hookaudit.Event.Agent for Grok Build events.
const grokAgent = "grok"

// grokToolName is the native shell tool name of Grok Build (xAI's coding CLI).
// Grok Build also maps Claude-style tool aliases, so "Bash" is accepted too.
const grokToolName = "run_terminal_cmd"

// grokInput represents the PreToolUse payload from Grok Build. Grok Build
// sends camelCase keys, unlike Claude Code's snake_case:
//
//	{"hookEventName":"pre_tool_use","toolName":"run_terminal_cmd",
//	 "toolInput":{"command":"npm test"},...}
type grokInput struct {
	ToolName  string          `json:"toolName"`
	ToolInput json.RawMessage `json:"toolInput"`
}

// RunGrok reads a Grok Build PreToolUse JSON payload from r, determines if the
// command matches a snip filter, and writes a deny-with-suggestion response
// telling Grok Build (and the user) to re-run the command through snip:
//
//	{"decision":"deny","reason":"snip can filter this command. Re-run as: ..."}
//
// Grok Build's PreToolUse hook cannot rewrite the command in place — the
// documented stdout contract is allow/deny only, with no updatedInput
// equivalent. A deny takes effect only when the hook process exits with code
// 2, so RunGrok returns denied=true and the caller must exit 2; every other
// path must exit 0. Grok Build is fail-open (crashes, timeouts, and malformed
// output let the command run unchanged), which preserves snip's graceful
// degradation.
//
// Errors are returned but the caller should still exit 0 unless denied is true.
func RunGrok(r io.Reader, w io.Writer, commands []string, prefixes []TransparentPrefix, snipBin string) (denied bool, err error) {
	audit := hookaudit.Enabled()

	data, err := io.ReadAll(r)
	if err != nil {
		return false, fmt.Errorf("read stdin: %w", err)
	}

	var input grokInput
	if err := json.Unmarshal(data, &input); err != nil {
		return false, nil // malformed JSON: pass through silently
	}

	if input.ToolName != grokToolName && input.ToolName != "Bash" {
		return false, nil
	}

	var ti toolInput
	if err := json.Unmarshal(input.ToolInput, &ti); err != nil {
		return false, nil
	}
	if ti.Command == "" {
		return false, nil
	}

	cmdSet := make(map[string]struct{}, len(commands))
	for _, c := range commands {
		cmdSet[c] = struct{}{}
	}

	sug := suggestSnipRerun(ti.Command, cmdSet, prefixes, snipBin)
	if sug.alreadySnip {
		return false, nil
	}
	if sug.command == "" {
		if audit {
			hookaudit.Append(hookaudit.Event{
				Timestamp: time.Now().UTC(),
				Command:   ti.Command,
				Base:      sug.base,
				Matched:   false,
				Rewritten: false,
				Agent:     grokAgent,
			})
		}
		return false, nil
	}

	reason := fmt.Sprintf("snip can filter this command. Re-run as: %s", sug.command)

	output := map[string]any{
		"decision": "deny",
		"reason":   reason,
	}

	if audit {
		hookaudit.Append(hookaudit.Event{
			Timestamp: time.Now().UTC(),
			Command:   ti.Command,
			Base:      sug.base,
			Matched:   true,
			Rewritten: false,
			Agent:     grokAgent,
		})
	}

	enc := json.NewEncoder(w)
	if err := enc.Encode(output); err != nil {
		// The deny envelope never reached Grok Build; report allow (exit 0) so
		// the fail-open contract lets the command run unfiltered.
		return false, err
	}
	return true, nil
}
