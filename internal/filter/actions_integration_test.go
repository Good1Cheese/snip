package filter

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edouard-claude/snip/internal/utils"
)

func fixturesDir() string {
	// Find project root by looking for go.mod
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "tests", "fixtures")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Fallback
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..", "tests", "fixtures")
}

func loadFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixturesDir(), name))
	if err != nil {
		t.Fatalf("load fixture %s: %v", name, err)
	}
	return string(data)
}

func loadFilter(t *testing.T, name string) *Filter {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixturesDir(), "..", "..", "filters", name))
	if err != nil {
		t.Fatalf("load filter %s: %v", name, err)
	}
	f, err := ParseFilter(data)
	if err != nil {
		t.Fatalf("parse filter %s: %v", name, err)
	}
	return f
}

func applyPipeline(f *Filter, input string) (string, error) {
	lines := strings.Split(input, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	result := ActionResult{
		Lines:    lines,
		Metadata: make(map[string]any),
	}

	for i, action := range f.Pipeline {
		fn, ok := GetAction(action.ActionName)
		if !ok {
			return "", nil
		}
		var err error
		result, err = fn(result, action.Params)
		if err != nil {
			return "", err
		}
		_ = i
	}

	return strings.Join(result.Lines, "\n") + "\n", nil
}

func TestGitLogFilterIntegration(t *testing.T) {
	// The git-log filter works by INJECTING --pretty=format:... BEFORE execution.
	// The pipeline then cleans up the already-compact output.
	// We test the full savings: raw verbose input vs pipeline-filtered output.
	fixture := loadFixture(t, "git_log_raw.txt")
	f := loadFilter(t, "git-log.yaml")

	filtered, err := applyPipeline(f, fixture)
	if err != nil {
		t.Fatalf("apply pipeline: %v", err)
	}

	// Should be shorter (pipeline removes blank lines, truncates)
	if len(filtered) >= len(fixture) {
		t.Errorf("filtered (%d) not shorter than input (%d)", len(filtered), len(fixture))
	}

	// Non-empty
	if strings.TrimSpace(filtered) == "" {
		t.Error("filtered output is empty")
	}

	// The real savings come from arg injection (--pretty=format:...).
	// The pipeline alone on verbose git log produces moderate savings.
	// Verify pipeline produces valid output (savings threshold relaxed for pipeline-only test).
	inputTokens := utils.EstimateTokens(fixture)
	outputTokens := utils.EstimateTokens(filtered)
	savings := float64(inputTokens-outputTokens) / float64(inputTokens) * 100
	t.Logf("git-log pipeline-only: %d -> %d tokens (%.1f%% savings)", inputTokens, outputTokens, savings)

	// Test with simulated post-injection output (what git log --pretty=format:... produces)
	injectedOutput := "8cb198e chore(master): release 0.22.0 (#201) (2 hours ago) <github-actions>\n393fa5b feat: add rtk wc command (#175) (3 hours ago) <John Doe>\nc29644b chore(master): release 0.21.1 (#179) (5 hours ago) <github-actions>\nd196c2d fix: gh run view drops flags (#159) (6 hours ago) <Jane Smith>\naa0b462 chore(master): release 0.21.0 (#178) (7 hours ago) <github-actions>\n510c491 feat(docker): add docker compose (#110) (8 hours ago) <Pierre Martin>\nc83a834 docs: add brew install note (#177) (10 hours ago) <Rui Chen>\n577e082 chore(master): release 0.20.1 (#167) (1 day ago) <github-actions>\n0b34772 fix: install to ~/.local/bin (#155) (1 day ago) <DevOps Bot>\n78c9e94 chore(master): release 0.20.0 (#152) (2 days ago) <github-actions>\n"

	injectedFiltered, err := applyPipeline(f, injectedOutput)
	if err != nil {
		t.Fatalf("apply pipeline on injected: %v", err)
	}

	// Full savings: raw verbose input tokens vs final filtered output tokens
	fullSavings := float64(inputTokens-utils.EstimateTokens(injectedFiltered)) / float64(inputTokens) * 100
	t.Logf("git-log full (inject+pipeline): %d -> %d tokens (%.1f%% savings)", inputTokens, utils.EstimateTokens(injectedFiltered), fullSavings)
	if fullSavings < 70 {
		t.Errorf("git-log full savings %.1f%% < 70%% minimum", fullSavings)
	}
}

func TestGitStatusFilterIntegration(t *testing.T) {
	// The git-status filter works by INJECTING --porcelain BEFORE execution.
	// The pipeline then runs on porcelain output, not the verbose fixture.
	// We test the full savings: raw verbose input vs pipeline-filtered porcelain output.
	fixture := loadFixture(t, "git_status_raw.txt")
	f := loadFilter(t, "git-status.yaml")

	// Simulated post-injection output (what git status --porcelain produces).
	// Mirrors the file set of the verbose fixture for an apples-to-apples comparison.
	injectedOutput := "A  internal/filter/kubectl.go\nA  internal/filter/kubectl_test.go\nM  internal/filter/registry.go\n M CHANGELOG.md\n M README.md\n M internal/cli/cli.go\n M internal/engine/pipeline.go\n M internal/filter/actions.go\n M internal/filter/loader.go\n M internal/tracking/tracker.go\n?? filters/kubectl-get-pods.yaml\n?? filters/kubectl-logs.yaml\n?? filters/kubectl-describe.yaml\n?? internal/filter/npm.go\n?? internal/filter/npm_test.go\n?? tests/fixtures/kubectl_pods_raw.txt\n?? tests/fixtures/kubectl_logs_raw.txt\n?? tests/fixtures/npm_install_raw.txt\n?? docs/kubectl-filters.md\n?? docs/npm-filters.md\n"

	filtered, err := applyPipeline(f, injectedOutput)
	if err != nil {
		t.Fatalf("apply pipeline on injected: %v", err)
	}

	if strings.TrimSpace(filtered) == "" {
		t.Error("filtered output is empty")
	}

	// Full savings: raw verbose input tokens vs final filtered output tokens.
	inputTokens := utils.EstimateTokens(fixture)
	outputTokens := utils.EstimateTokens(filtered)
	savings := float64(inputTokens-outputTokens) / float64(inputTokens) * 100
	t.Logf("git-status full (inject+pipeline): %d -> %d tokens (%.1f%% savings)", inputTokens, outputTokens, savings)
	// Threshold lower than other filters: git-status preserves filenames (issue #48),
	// trading some compression for usable output. Savings come from --porcelain injection
	// stripping verbose section headers, not from aggregating filenames away.
	if savings < 40 {
		t.Errorf("git-status savings %.1f%% < 40%% minimum", savings)
	}
}

func TestGitDiffFilterIntegration(t *testing.T) {
	fixture := loadFixture(t, "git_diff_raw.txt")
	f := loadFilter(t, "git-diff.yaml")

	filtered, err := applyPipeline(f, fixture)
	if err != nil {
		t.Fatalf("apply pipeline: %v", err)
	}

	if len(filtered) >= len(fixture) {
		t.Errorf("filtered (%d) not shorter than input (%d)", len(filtered), len(fixture))
	}

	inputTokens := utils.EstimateTokens(fixture)
	outputTokens := utils.EstimateTokens(filtered)
	savings := float64(inputTokens-outputTokens) / float64(inputTokens) * 100
	t.Logf("git-diff: %d -> %d tokens (%.1f%% savings)", inputTokens, outputTokens, savings)
	if savings < 70 {
		t.Errorf("git-diff savings %.1f%% < 70%% minimum", savings)
	}
}

func TestGoTestFilterIntegration(t *testing.T) {
	fixture := loadFixture(t, "go_test_raw.txt")
	f := loadFilter(t, "go-test.yaml")

	filtered, err := applyPipeline(f, fixture)
	if err != nil {
		t.Fatalf("apply pipeline: %v", err)
	}

	if len(filtered) >= len(fixture) {
		t.Errorf("filtered (%d) not shorter than input (%d)", len(filtered), len(fixture))
	}

	inputTokens := utils.EstimateTokens(fixture)
	outputTokens := utils.EstimateTokens(filtered)
	savings := float64(inputTokens-outputTokens) / float64(inputTokens) * 100
	t.Logf("go-test: %d -> %d tokens (%.1f%% savings)", inputTokens, outputTokens, savings)
	if savings < 80 {
		t.Errorf("go-test savings %.1f%% < 80%% minimum", savings)
	}
}

func TestCargoTestFilterIntegration(t *testing.T) {
	fixture := loadFixture(t, "cargo_test_raw.txt")
	f := loadFilter(t, "cargo-test.yaml")

	filtered, err := applyPipeline(f, fixture)
	if err != nil {
		t.Fatalf("apply pipeline: %v", err)
	}

	if len(filtered) >= len(fixture) {
		t.Errorf("filtered (%d) not shorter than input (%d)", len(filtered), len(fixture))
	}

	inputTokens := utils.EstimateTokens(fixture)
	outputTokens := utils.EstimateTokens(filtered)
	savings := float64(inputTokens-outputTokens) / float64(inputTokens) * 100
	t.Logf("cargo-test: %d -> %d tokens (%.1f%% savings)", inputTokens, outputTokens, savings)
	if savings < 80 {
		t.Errorf("cargo-test savings %.1f%% < 80%% minimum", savings)
	}
}

func TestRSpecFilterIntegration(t *testing.T) {
	fixture := loadFixture(t, "rspec_raw.txt")
	f := loadFilter(t, "rspec.yaml")

	filtered, err := applyPipeline(f, fixture)
	if err != nil {
		t.Fatalf("apply pipeline: %v", err)
	}

	// Should be much shorter
	if len(filtered) >= len(fixture) {
		t.Errorf("filtered (%d) not shorter than input (%d)", len(filtered), len(fixture))
	}

	// Should contain summary
	if !strings.Contains(filtered, "examples") {
		t.Error("filtered output missing examples count")
	}

	// Should preserve failure paths (essential for debugging)
	if strings.Contains(fixture, "rspec ./") && !strings.Contains(filtered, "rspec ./") {
		t.Error("filtered output missing failure paths (rspec ./spec/...)")
	}

	inputTokens := utils.EstimateTokens(fixture)
	outputTokens := utils.EstimateTokens(filtered)
	savings := float64(inputTokens-outputTokens) / float64(inputTokens) * 100
	t.Logf("rspec: %d -> %d tokens (%.1f%% savings)", inputTokens, outputTokens, savings)
}

// Edge case tests
func TestFilterEmptyInput(t *testing.T) {
	f := loadFilter(t, "git-log.yaml")
	filtered, err := applyPipeline(f, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should not crash, output may be minimal
	_ = filtered
}

func TestFilterUnicodeInput(t *testing.T) {
	f := loadFilter(t, "git-log.yaml")
	input := "abc123 héllo wörld — special chars (ñ) <用户>\nabc456 日本語テスト (2d ago) <テスター>\n"
	filtered, err := applyPipeline(f, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(filtered) == "" {
		t.Error("unicode input produced empty output")
	}
}

func TestFilterANSIInput(t *testing.T) {
	f := &Filter{
		Name: "test",
		Pipeline: Pipeline{
			{ActionName: "strip_ansi"},
			{ActionName: "keep_lines", Params: map[string]any{"pattern": `\S`}},
		},
	}
	input := "\x1b[31mred error\x1b[0m\n\x1b[32mgreen ok\x1b[0m\n"
	filtered, err := applyPipeline(f, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(filtered, "\x1b") {
		t.Error("ANSI codes not stripped")
	}
}

func TestBundleInstallFilterIntegration(t *testing.T) {
	fixture := loadFixture(t, "bundle_install_raw.txt")
	f := loadFilter(t, "bundle-install.yaml")

	filtered, err := applyPipeline(f, fixture)
	if err != nil {
		t.Fatalf("apply pipeline: %v", err)
	}

	if len(filtered) >= len(fixture) {
		t.Errorf("filtered (%d) not shorter than input (%d)", len(filtered), len(fixture))
	}

	if !strings.Contains(filtered, "Bundle complete") {
		t.Error("filtered output missing Bundle complete")
	}

	// Calculate and log token savings
	inputTokens := utils.EstimateTokens(fixture)
	outputTokens := utils.EstimateTokens(filtered)
	savings := float64(inputTokens-outputTokens) / float64(inputTokens) * 100
	t.Logf("bundle-install: %d -> %d tokens (%.1f%% savings)", inputTokens, outputTokens, savings)
}

func TestRailsRoutesFilterIntegration(t *testing.T) {
	fixture := loadFixture(t, "rails_routes_raw.txt")
	f := loadFilter(t, "rails-routes.yaml")

	filtered, err := applyPipeline(f, fixture)
	if err != nil {
		t.Fatalf("apply pipeline: %v", err)
	}

	// Should be shorter
	if len(filtered) >= len(fixture) {
		t.Errorf("filtered (%d) not shorter than input (%d)", len(filtered), len(fixture))
	}

	// Should contain the route count summary
	if !strings.Contains(filtered, " routes:") {
		t.Errorf("filtered output missing route count header: %q", filtered)
	}

	// The route table itself must survive the aggregate stage (issue #134)
	if !strings.Contains(filtered, "users#index") {
		t.Errorf("filtered output missing the route table: %q", filtered)
	}

	// Not asserted here: that the aggregate summary line does not leak into the
	// payload. This fixture has more routes than the head cap, so the appended
	// "routes: N" line is dropped by head whether or not the remove_lines stage
	// exists, and the assertion would pass either way. The filter's own inline
	// tests use a fixture under the cap and do pin it: deleting that stage fails
	// all three of them.

	// Calculate and log token savings
	inputTokens := utils.EstimateTokens(fixture)
	outputTokens := utils.EstimateTokens(filtered)
	savings := float64(inputTokens-outputTokens) / float64(inputTokens) * 100
	t.Logf("rails-routes: %d -> %d tokens (%.1f%% savings)", inputTokens, outputTokens, savings)
}

func TestRailsMigrateFilterIntegration(t *testing.T) {
	fixture := loadFixture(t, "rails_migrate_raw.txt")
	f := loadFilter(t, "rails-migrate.yaml")

	filtered, err := applyPipeline(f, fixture)
	if err != nil {
		t.Fatalf("apply pipeline: %v", err)
	}

	// Should be shorter
	if len(filtered) >= len(fixture) {
		t.Errorf("filtered (%d) not shorter than input (%d)", len(filtered), len(fixture))
	}

	// Should contain migrations executed summary
	if !strings.Contains(filtered, "migrations executed") {
		t.Error("filtered output missing 'migrations executed'")
	}

	// Calculate and log token savings
	inputTokens := utils.EstimateTokens(fixture)
	outputTokens := utils.EstimateTokens(filtered)
	savings := float64(inputTokens-outputTokens) / float64(inputTokens) * 100
	t.Logf("rails-migrate: %d -> %d tokens (%.1f%% savings)", inputTokens, outputTokens, savings)
}

func TestLsFilterIntegration(t *testing.T) {
	fixture := loadFixture(t, "ls_long_recursive_raw.txt")
	f := loadFilter(t, "ls.yaml")

	filtered, err := applyPipeline(f, fixture)
	if err != nil {
		t.Fatalf("apply pipeline: %v", err)
	}

	if len(filtered) >= len(fixture) {
		t.Errorf("filtered (%d) not shorter than input (%d)", len(filtered), len(fixture))
	}

	// "total" lines must be removed
	if strings.Contains(filtered, "total 2292") {
		t.Error("filtered output still contains 'total' lines")
	}

	// . and .. entries must be removed
	for _, line := range strings.Split(filtered, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "." || trimmed == ".." {
			t.Errorf("filtered output still contains %q entry", trimmed)
		}
	}

	// Long-format lines should be reformatted (no permissions like drwxr-xr-x)
	if strings.Contains(filtered, "drwxr-xr-x") || strings.Contains(filtered, "-rw-r--r--") {
		t.Error("filtered output still contains raw ls -l permissions")
	}

	// Filenames must survive the pipeline (including French-locale macOS section)
	for _, name := range []string{"CLAUDE.md", "config.toml", ".gitignore", "session-metadata.jsonl", "golang-expert.md", "svelte-expert.md"} {
		if !strings.Contains(filtered, name) {
			t.Errorf("filtered output missing filename %q", name)
		}
	}

	// Directory headers must survive (they don't match the replace regex)
	if !strings.Contains(filtered, "projects") {
		t.Error("filtered output missing directory header")
	}

	// Extension summary must be appended (group_by with append: true)
	if !strings.Contains(filtered, "json") {
		t.Error("filtered output missing extension summary for 'json'")
	}

	inputTokens := utils.EstimateTokens(fixture)
	outputTokens := utils.EstimateTokens(filtered)
	savings := float64(inputTokens-outputTokens) / float64(inputTokens) * 100
	t.Logf("ls: %d -> %d tokens (%.1f%% savings)", inputTokens, outputTokens, savings)
	if savings < 40 {
		t.Errorf("ls savings %.1f%% < 40%% minimum", savings)
	}
}

func TestLsFilterPlainOutput(t *testing.T) {
	// Plain ls (no -l): just filenames, the pipeline should pass them through
	f := loadFilter(t, "ls.yaml")
	input := "CLAUDE.md\nREADME.md\ngo.mod\ngo.sum\ninternal\nfilters\ncmd\n"

	filtered, err := applyPipeline(f, input)
	if err != nil {
		t.Fatalf("apply pipeline: %v", err)
	}

	// All filenames should survive
	for _, name := range []string{"CLAUDE.md", "README.md", "go.mod", "internal", "filters"} {
		if !strings.Contains(filtered, name) {
			t.Errorf("plain ls: missing %q in output", name)
		}
	}
}

func TestGracefulDegradation(t *testing.T) {
	// Bad filter YAML
	badYAML := `
name: "bad"
match:
  command: "test"
pipeline:
  - action: "nonexistent_action"
`
	_, err := ParseFilter([]byte(badYAML))
	if err == nil {
		t.Error("expected error for unknown action, but ParseFilter accepted it")
	}
}
