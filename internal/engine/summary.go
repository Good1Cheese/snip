package engine

import (
	"fmt"
	"strings"

	"github.com/edouard-claude/snip/internal/utils"
)

// SummaryInfo holds the data needed to build a summary line.
type SummaryInfo struct {
	FilterName    string
	FilterVersion int
	InjectedArgs  []string
	PipelineNames []string
}

const maxArgDisplayLen = 20
const minLinesForSummary = 3

// BuildSummaryLine constructs a compact summary string from filter metadata.
func BuildSummaryLine(info SummaryInfo) string {
	if len(info.InjectedArgs) == 0 && len(info.PipelineNames) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[snip: %s v%d", info.FilterName, info.FilterVersion)

	if len(info.InjectedArgs) > 0 {
		b.WriteString(" | ")
		for i, arg := range info.InjectedArgs {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteByte('+')
			b.WriteString(utils.Truncate(arg, maxArgDisplayLen))
		}
	}

	if len(info.PipelineNames) > 0 {
		b.WriteString(" | ")
		b.WriteString(strings.Join(info.PipelineNames, ">"))
	}

	b.WriteByte(']')
	return b.String()
}

// PrependSummary prepends a summary line to filtered output without removing
// any content. The summary is only added when the token savings from filtering
// exceed the cost of the summary line itself, guaranteeing the output with
// summary is always smaller than the original raw output.
func PrependSummary(filtered string, summary string, inputTokens, filteredTokens int) string {
	if summary == "" {
		return filtered
	}

	// Count lines without allocating a slice: line count == "\n" count + 1
	// once trailing newlines are ignored.
	if strings.Count(strings.TrimRight(filtered, "\n"), "\n")+1 < minLinesForSummary {
		return filtered
	}

	summaryTokens := utils.EstimateTokens(summary + "\n")
	savedTokens := inputTokens - filteredTokens
	if summaryTokens >= savedTokens {
		return filtered
	}

	return summary + "\n" + filtered
}

// ComputeInjectedArgs returns args present in finalArgs but not in fullArgs.
func ComputeInjectedArgs(fullArgs, finalArgs []string) []string {
	original := make(map[string]int, len(fullArgs))
	for _, a := range fullArgs {
		original[a]++
	}

	var injected []string
	seen := make(map[string]int, len(finalArgs))
	for _, a := range finalArgs {
		seen[a]++
		if seen[a] > original[a] {
			injected = append(injected, a)
		}
	}
	return injected
}
