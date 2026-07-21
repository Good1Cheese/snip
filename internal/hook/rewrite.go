package hook

import (
	"fmt"
	"strings"
)

// RewriteResult is the outcome of segmenting and rewriting a compound command.
type RewriteResult struct {
	// Command is the rewritten command line.
	Command string
	// Changed reports whether at least one segment was wrapped in snip. When
	// false the caller should pass the original command through unchanged.
	Changed bool
	// AllKnown reports whether every runnable command segment is a known base
	// command (and thus attested by snip). Only when AllKnown is true may the
	// caller emit permissionDecision "allow": otherwise an uninspected segment
	// would have its confirmation prompt suppressed. See issue #88.
	AllKnown bool
}

// RewriteCommand rewrites each runnable segment of cmd whose base command is in
// cmdSet by wrapping it in `<snipBin> run -- ...`, mirroring rtk's per-segment
// rewrite so token savings are preserved across compound commands.
//
// cmd is split on top-level ';', '&&', '||', '&' and newline boundaries (quoted
// regions are respected). Within each resulting group the head command is
// wrapped only when its stdout is read directly by the LLM. A head whose output
// feeds a downstream consumer — a pipe stage or a file redirection ('>') — is
// left raw, because snip's lossy compaction (path compaction, line truncation,
// match caps, dedup, reformatting) would silently corrupt the exact count and
// content that consumer depends on (issue #111).
//
// The caller must reject commands containing unverifiable constructs
// (HasUnverifiableConstruct) before calling this, so cmd here is free of command
// substitution and carriage returns.
func RewriteCommand(cmd string, cmdSet map[string]struct{}, prefixes []TransparentPrefix, snipBin string) RewriteResult {
	quotedBin := fmt.Sprintf("%q", snipBin)

	var b strings.Builder
	b.Grow(len(cmd) + 32)

	changed := false
	allKnown := true

	flush := func(group string) {
		out, headKnown, hasTail := rewriteGroup(group, cmdSet, prefixes, quotedBin, snipBin)
		b.WriteString(out)
		if out != group {
			changed = true
		}
		// Empty/whitespace-only groups (e.g. a trailing ';') do not carry a
		// command and never block auto-allow.
		if strings.TrimSpace(group) != "" && (!headKnown || hasTail) {
			allKnown = false
		}
	}

	groupStart := 0
	var quote byte
	for i := 0; i < len(cmd); {
		ch := cmd[i]
		if quote != 0 {
			if ch == '\\' && quote == '"' && i+1 < len(cmd) {
				i += 2
				continue
			}
			if ch == quote {
				quote = 0
			}
			i++
			continue
		}
		switch ch {
		case '\'':
			quote = '\''
			i++
		case '"':
			quote = '"'
			i++
		case '\n', ';':
			flush(cmd[groupStart:i])
			b.WriteByte(ch)
			i++
			groupStart = i
		case '&':
			flush(cmd[groupStart:i])
			if i+1 < len(cmd) && cmd[i+1] == '&' {
				b.WriteString("&&")
				i += 2
			} else {
				b.WriteByte('&')
				i++
			}
			groupStart = i
		case '|':
			if i+1 < len(cmd) && cmd[i+1] == '|' {
				// "||" is a group boundary; a single "|" stays inside the group.
				flush(cmd[groupStart:i])
				b.WriteString("||")
				i += 2
				groupStart = i
			} else {
				i++
			}
		default:
			i++
		}
	}
	flush(cmd[groupStart:])

	return RewriteResult{Command: b.String(), Changed: changed, AllKnown: allKnown}
}

// rewriteGroup rewrites the head command of a single execution group (the text
// between two sequential boundaries). It returns the rewritten group, whether
// the head is a known/attested base command, and whether the group has a
// non-empty pipeline tail (extra stages that were left uninspected).
func rewriteGroup(group string, cmdSet map[string]struct{}, prefixes []TransparentPrefix, quotedBin, snipBin string) (out string, headKnown, hasTail bool) {
	head, tail := splitFirstPipe(group)
	hasTail = strings.TrimSpace(tail) != ""

	// A head whose stdout feeds a downstream consumer must not be wrapped: snip's
	// lossy compaction (compact_path, truncate_lines, head caps, dedup,
	// reformatting) is only safe when the LLM reads the output directly. A pipe
	// stage or a file redirection consumes the producer's exact count and
	// content, so wrapping would silently corrupt it (issue #111). splitFirstPipe
	// detects the pipe; hasTopLevelRedirect detects '>' (covering '>', '>>', '2>',
	// '&>'). For the pipe case hasTail also keeps the #88 no-auto-allow guard.
	feedsConsumer := hasTail || hasTopLevelRedirect(head)

	prefix, envVars, bareCmd := ParseSegment(head)
	base := BaseCommand(bareCmd)
	if base == "" {
		return group, false, hasTail
	}

	// Already wrapped in snip: treat as attested, leave untouched.
	trimmed := strings.TrimLeft(bareCmd, " \t")
	if base == quotedBin || base == snipBin ||
		strings.HasPrefix(trimmed, quotedBin) || strings.HasPrefix(trimmed, snipBin) {
		return group, true, hasTail
	}

	// Transparent runner prefix (e.g. "uv run pytest"): strip the prefix, locate
	// the inner command, and wrap the inner command so its filter applies. The
	// prefix is re-prepended unchanged so the wrapped snip still runs inside the
	// runner's environment. Attempted only when the inner command is a known snip
	// command; otherwise fall through to the normal base check below. Because the
	// inner base must be in cmdSet, a runner executing an unknown program
	// (e.g. "uv run bash -c ...") is never rewritten here and never auto-allowed.
	if tp, rest, ok := matchTransparentPrefix(bareCmd, prefixes); ok {
		if before, _, found := LocateInner(rest, cmdSet, tp.ValueFlags, tp.SkipFlags); found {
			// Known inner command, but leave the group raw when its output feeds a
			// consumer (issue #111). headKnown stays true so a redirected-but-known
			// producer does not needlessly block auto-allow of sibling groups.
			if feedsConsumer {
				return group, true, hasTail
			}
			wrappedHead := prefix + envVars + tp.Prefix + " " + before + quotedBin + " run -- " + rest[len(before):]
			return wrappedHead + tail, true, hasTail
		}
	}

	if _, ok := cmdSet[base]; !ok {
		return group, false, hasTail
	}

	// Known producer feeding a downstream consumer: leave raw (issue #111).
	if feedsConsumer {
		return group, true, hasTail
	}

	wrappedHead := prefix + envVars + quotedBin + " run -- " + bareCmd
	return wrappedHead + tail, true, hasTail
}

// splitFirstPipe splits group at its first top-level '|' (quoted regions are
// respected). head is the text before the pipe; tail includes the '|' and the
// remaining pipeline stages. When there is no pipe, tail is empty.
func splitFirstPipe(group string) (head, tail string) {
	var quote byte
	for i := 0; i < len(group); i++ {
		ch := group[i]
		if quote != 0 {
			if ch == '\\' && quote == '"' && i+1 < len(group) {
				i++
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '\'':
			quote = '\''
		case '"':
			quote = '"'
		case '|':
			return group[:i], group[i:]
		}
	}
	return group, ""
}

// hasTopLevelRedirect reports whether group contains an unquoted output
// redirection ('>'), i.e. the producer's stdout/stderr is written to a file
// rather than read by the LLM. Any top-level '>' counts, covering '>', '>>',
// '2>', '&>' and '>&'. snip fails safe here: an over-detection only forgoes
// compaction, whereas a miss would silently corrupt the redirected file
// (issue #111). '<' (stdin) is ignored — it never affects the filtered stdout.
func hasTopLevelRedirect(group string) bool {
	var quote byte
	for i := 0; i < len(group); i++ {
		ch := group[i]
		if quote != 0 {
			if ch == '\\' && quote == '"' && i+1 < len(group) {
				i++
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '\'':
			quote = '\''
		case '"':
			quote = '"'
		case '>':
			return true
		}
	}
	return false
}

// firstBase returns the base command of cmd's first segment, used for audit
// telemetry that predates per-segment rewriting.
func firstBase(cmd string) string {
	firstLine := cmd
	if idx := strings.IndexByte(firstLine, '\n'); idx >= 0 {
		firstLine = firstLine[:idx]
	}
	_, _, bareCmd := ParseSegment(ExtractFirstSegment(firstLine))
	return BaseCommand(bareCmd)
}
