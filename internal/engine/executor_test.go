package engine

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestExecuteEcho(t *testing.T) {
	result, err := Execute("echo", []string{"hello", "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d", result.ExitCode)
	}
	if got := strings.TrimSpace(result.Stdout); got != "hello world" {
		t.Errorf("stdout = %q", got)
	}
	if result.Duration <= 0 {
		t.Error("duration should be positive")
	}
}

func TestExecuteStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skip on windows")
	}
	result, err := Execute("sh", []string{"-c", "echo error >&2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.TrimSpace(result.Stderr); got != "error" {
		t.Errorf("stderr = %q", got)
	}
}

func TestExecuteExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skip on windows")
	}
	result, err := Execute("sh", []string{"-c", "exit 42"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 42 {
		t.Errorf("exit code = %d, want 42", result.ExitCode)
	}
}

func TestExecuteNotFound(t *testing.T) {
	_, err := Execute("nonexistent-command-xyz", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}
}

// A nil *Result is how Execute signals "the command never ran". An error does
// not carry that meaning, since it can also mean the command ran to completion
// and only the wait bookkeeping failed. Callers decide from the Result whether
// re-running is safe, so this invariant has to hold.
func TestExecuteNilResultOnlyWhenNeverStarted(t *testing.T) {
	result, err := Execute("nonexistent-command-xyz", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}
	if result != nil {
		t.Errorf("result must be nil when the command never started, got %+v", result)
	}

	if runtime.GOOS == "windows" {
		t.Skip("skip on windows")
	}
	result, err = Execute("sh", []string{"-c", "echo out; echo err >&2; exit 3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result must be non-nil for a command that ran")
	}
	if result.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", result.ExitCode)
	}
	if strings.TrimSpace(result.Stdout) != "out" || strings.TrimSpace(result.Stderr) != "err" {
		t.Errorf("output not captured: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
}

func TestPassthrough(t *testing.T) {
	code, err := Passthrough("echo", []string{"test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d", code)
	}
}

func TestPassthroughExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skip on windows")
	}
	code, err := Passthrough("sh", []string{"-c", "exit 7"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
}

func TestExecuteShellBuiltin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skip on windows")
	}
	result, err := Execute("export", []string{"FOO=bar"})
	if err != nil {
		t.Fatalf("unexpected error executing shell builtin: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}
}

func TestPassthroughShellBuiltin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skip on windows")
	}
	code, err := Passthrough("export", []string{"FOO=bar"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestMakeCommandBuiltin(t *testing.T) {
	cmd := makeCommand("export", []string{"A=1", "B=2"})
	if cmd.Path == "" {
		t.Fatal("command path should not be empty")
	}
	// Should wrap with sh -c
	if cmd.Args[0] != "sh" {
		t.Errorf("expected sh wrapper, got %q", cmd.Args[0])
	}
	if cmd.Args[1] != "-c" {
		t.Errorf("expected -c flag, got %q", cmd.Args[1])
	}
}

func TestMakeCommandRegular(t *testing.T) {
	cmd := makeCommand("git", []string{"status"})
	// Should NOT wrap with sh
	if len(cmd.Args) > 0 && cmd.Args[0] == "sh" {
		t.Error("regular commands should not be wrapped with sh")
	}
}

// A command that leaves a process running in the background hands that
// descendant its copies of the pipe write ends, so the pipes only reach EOF
// once the descendant exits too. Execute must not wait for it.
//
// The descendant writes well after the grace period, which pins both sides of
// the trade-off: everything the command itself wrote is captured, and the late
// write is dropped rather than waited for.
func TestExecuteDoesNotWaitForBackgroundDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skip on windows")
	}
	started := time.Now()
	result, err := Execute("sh", []string{"-c", "(sleep 5; echo late) & echo started"})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); got != "started" {
		t.Errorf("stdout = %q, want %q", got, "started")
	}
	if elapsed > 3*time.Second {
		t.Errorf("Execute blocked for %s on a background descendant", elapsed)
	}
}

// Output well past the pipe buffer makes the command block on write until the
// reader drains. Nothing may be truncated, and neither side may deadlock.
func TestExecuteLargeOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skip on windows")
	}
	const want = 200000
	result, err := Execute("sh", []string{"-c", "seq 1 200000"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.Count(result.Stdout, "\n"); got != want {
		t.Errorf("captured %d lines, want %d", got, want)
	}
	if got := strings.TrimSpace(result.Stdout); !strings.HasSuffix(got, "\n200000") {
		t.Errorf("output truncated, ends with %q", got[max(0, len(got)-20):])
	}
}
