//go:build !windows

package snip

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMakeLintPropagatesLinterStatus pins the bug this target was rewritten for:
// `which golangci-lint && golangci-lint run || echo "…"` reported success when
// the linter itself failed, so a real lint failure looked green locally.
func TestMakeLintPropagatesLinterStatus(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}

	tests := []struct {
		name       string
		linterExit string
		wantErr    bool
	}{
		{name: "linter fails", linterExit: "42", wantErr: true},
		{name: "linter passes", linterExit: "0", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := stageLintProject(t, tt.linterExit)

			cmd := exec.Command("make", "-C", dir, "lint")
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "bin")+":"+os.Getenv("PATH"))
			output, err := cmd.CombinedOutput()

			if tt.wantErr && err == nil {
				t.Errorf("make lint succeeded while the linter exited %s:\n%s", tt.linterExit, output)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("make lint failed while the linter succeeded: %v\n%s", err, output)
			}
		})
	}
}

// stageLintProject copies the Makefile and the pinned linter version into a
// throwaway module, alongside a fake golangci-lint reporting that same version
// and exiting with linterExit. Staging keeps `go vet ./...` off the real tree.
func stageLintProject(t *testing.T, linterExit string) string {
	t.Helper()

	version, err := os.ReadFile(".golangci-lint-version")
	if err != nil {
		t.Fatal(err)
	}
	makefile, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".golangci-lint-version"), string(version), 0644)
	writeFile(t, filepath.Join(dir, "Makefile"), string(makefile), 0644)
	writeFile(t, filepath.Join(dir, "go.mod"), "module lintprobe\n\ngo 1.25\n", 0644)
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n", 0644)

	// The fake reports the pinned version without its leading "v", matching what
	// `golangci-lint version --short` prints, so the target takes the
	// installed-binary branch instead of the go run fallback.
	fake := "#!/bin/sh\nif [ \"$1 $2\" = \"version --short\" ]; then\n" +
		"\tprintf '%s\\n' " + strings.TrimPrefix(strings.TrimSpace(string(version)), "v") + "\n" +
		"\texit 0\nfi\nexit " + linterExit + "\n"
	writeFile(t, filepath.Join(dir, "bin", "golangci-lint"), fake, 0755)

	return dir
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
