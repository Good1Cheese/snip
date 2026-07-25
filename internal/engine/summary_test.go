package engine

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/edouard-claude/snip/internal/utils"
)

func TestBuildSummaryLineInjectionOnly(t *testing.T) {
	got := BuildSummaryLine(SummaryInfo{
		FilterName:    "git-status",
		FilterVersion: 2,
		InjectedArgs:  []string{"--porcelain"},
	})
	want := "[snip: git-status v2 | +--porcelain]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildSummaryLinePipelineOnly(t *testing.T) {
	got := BuildSummaryLine(SummaryInfo{
		FilterName:    "find",
		FilterVersion: 2,
		PipelineNames: []string{"truncate_lines", "head"},
	})
	want := "[snip: find v2 | truncate_lines>head]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildSummaryLineBoth(t *testing.T) {
	got := BuildSummaryLine(SummaryInfo{
		FilterName:    "git-log",
		FilterVersion: 1,
		InjectedArgs:  []string{"--no-merges"},
		PipelineNames: []string{"keep_lines", "truncate_lines", "format_template"},
	})
	want := "[snip: git-log v1 | +--no-merges | keep_lines>truncate_lines>format_template]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildSummaryLineEmpty(t *testing.T) {
	got := BuildSummaryLine(SummaryInfo{
		FilterName:    "noop",
		FilterVersion: 1,
	})
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestBuildSummaryLineLongArgTruncated(t *testing.T) {
	got := BuildSummaryLine(SummaryInfo{
		FilterName:    "git-log",
		FilterVersion: 1,
		InjectedArgs:  []string{"--pretty=format:%h %s (%ar) <%an>"},
	})
	want := "[snip: git-log v1 | +--pretty=format:%...]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildSummaryLineMultibyteArgTruncated(t *testing.T) {
	// 25 two-byte runes (50 bytes). A byte-based slice would cut mid-rune and
	// emit invalid UTF-8; truncation must be rune-safe.
	arg := strings.Repeat("é", 25)
	got := BuildSummaryLine(SummaryInfo{
		FilterName:    "git-log",
		FilterVersion: 1,
		InjectedArgs:  []string{arg},
	})
	if !utf8.ValidString(got) {
		t.Fatalf("summary contains invalid UTF-8: %q", got)
	}
	want := "[snip: git-log v1 | +" + strings.Repeat("é", 17) + "...]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildSummaryLineMultipleArgs(t *testing.T) {
	got := BuildSummaryLine(SummaryInfo{
		FilterName:    "git-log",
		FilterVersion: 1,
		InjectedArgs:  []string{"--no-merges", "-n", "10"},
		PipelineNames: []string{"keep_lines"},
	})
	want := "[snip: git-log v1 | +--no-merges +-n +10 | keep_lines]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPrependSummaryEmptySkipped(t *testing.T) {
	filtered := "line1\nline2\nline3\n"
	got := PrependSummary(filtered, "", 100, 10)
	if got != filtered {
		t.Errorf("expected unchanged output, got %q", got)
	}
}

func TestPrependSummarySingleLineSkipped(t *testing.T) {
	filtered := "line1\n"
	got := PrependSummary(filtered, "[snip: test v1 | +--flag]", 1000, 10)
	if got != filtered {
		t.Errorf("expected unchanged output, got %q", got)
	}
}

func TestPrependSummaryTwoLinesSkipped(t *testing.T) {
	filtered := "line1\nline2\n"
	got := PrependSummary(filtered, "[snip: test v1 | +--flag]", 1000, 10)
	if got != filtered {
		t.Errorf("expected unchanged output, got %q", got)
	}
}

func TestPrependSummaryAddsLine(t *testing.T) {
	filtered := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\n"
	summary := "[snip: test v1 | +--x]"
	got := PrependSummary(filtered, summary, 1000, utils.EstimateTokens(filtered))
	if !strings.HasPrefix(got, summary+"\n") {
		t.Errorf("expected output to start with summary, got %q", got)
	}
	if !strings.HasSuffix(got, filtered) {
		t.Errorf("expected original content preserved, got %q", got)
	}
}

func TestPrependSummaryPreservesAllContent(t *testing.T) {
	filtered := "line1\nline2\nline3\nline4\nline5\n"
	summary := "[snip: git-status v2 | +--porcelain]"
	got := PrependSummary(filtered, summary, 1000, utils.EstimateTokens(filtered))
	for _, line := range []string{"line1", "line2", "line3", "line4", "line5"} {
		if !strings.Contains(got, line) {
			t.Errorf("expected %q to be preserved in output, got %q", line, got)
		}
	}
}

func TestPrependSummarySkipsWhenSavingsTooSmall(t *testing.T) {
	filtered := "line1\nline2\nline3\n"
	summary := "[snip: test v1 | +--flag]"
	summaryTokens := utils.EstimateTokens(summary + "\n")
	filteredTokens := utils.EstimateTokens(filtered)
	// inputTokens just barely above filteredTokens — savings < summary cost
	inputTokens := filteredTokens + summaryTokens - 1
	got := PrependSummary(filtered, summary, inputTokens, filteredTokens)
	if got != filtered {
		t.Errorf("expected unchanged output when savings too small, got %q", got)
	}
}

func TestPrependSummaryAppliesWhenSavingsSufficient(t *testing.T) {
	filtered := "line1\nline2\nline3\n"
	summary := "[snip: test v1 | +--flag]"
	summaryTokens := utils.EstimateTokens(summary + "\n")
	filteredTokens := utils.EstimateTokens(filtered)
	// inputTokens well above filteredTokens — savings > summary cost
	inputTokens := filteredTokens + summaryTokens + 100
	got := PrependSummary(filtered, summary, inputTokens, filteredTokens)
	if !strings.HasPrefix(got, summary+"\n") {
		t.Errorf("expected summary prepended, got %q", got)
	}
}

func TestPrependSummaryNeverExceedsRawInput(t *testing.T) {
	inputs := []struct {
		filtered    string
		inputTokens int
	}{
		{"line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\n", 500},
		{strings.Repeat("a long line with content\n", 20), 2000},
		{"short\nmedium length line\na very long line that has quite a lot of content in it\nfourth\nfifth\nsixth\n", 300},
	}
	summaries := []string{
		"[snip: git-status v2 | +--porcelain | keep_lines>group_by]",
		"[snip: test v1 | +--flag]",
		"[snip: go-test v3 | +-json | keep_lines>aggregate>format_template]",
	}

	for _, tc := range inputs {
		filteredTokens := utils.EstimateTokens(tc.filtered)
		for _, summary := range summaries {
			result := PrependSummary(tc.filtered, summary, tc.inputTokens, filteredTokens)
			resultTokens := utils.EstimateTokens(result)
			if resultTokens > tc.inputTokens {
				t.Errorf("output exceeds raw input: input=%d, result=%d, summary=%q",
					tc.inputTokens, resultTokens, summary)
			}
		}
	}
}

func TestComputeInjectedArgsBasic(t *testing.T) {
	got := ComputeInjectedArgs([]string{"status"}, []string{"status", "--porcelain"})
	if len(got) != 1 || got[0] != "--porcelain" {
		t.Errorf("got %v, want [--porcelain]", got)
	}
}

func TestComputeInjectedArgsNoChange(t *testing.T) {
	got := ComputeInjectedArgs([]string{"log", "--oneline"}, []string{"log", "--oneline"})
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestComputeInjectedArgsDefaults(t *testing.T) {
	got := ComputeInjectedArgs([]string{"log"}, []string{"log", "-n", "10", "--no-merges"})
	want := []string{"-n", "10", "--no-merges"}
	if len(got) != len(want) {
		t.Errorf("got %v, want %v", got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestComputeInjectedArgsDuplicatePreserved(t *testing.T) {
	got := ComputeInjectedArgs([]string{"diff", "--stat"}, []string{"diff", "--stat"})
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}
