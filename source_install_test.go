//go:build !windows

package snip

import (
	_ "embed"
	"os/exec"
	"testing"
)

// Embedding makes shell-suite changes invalidate the Go test cache.
//
//go:embed tests/source-install/test.sh
var sourceInstallTestScript string

func TestSourceInstall(t *testing.T) {
	if sourceInstallTestScript == "" {
		t.Fatal("embedded source-install test is empty")
	}

	cmd := exec.Command("sh", "tests/source-install/test.sh")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("source-install tests failed: %v\n%s", err, output)
	}
}
