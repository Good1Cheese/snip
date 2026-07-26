package hook

import "testing"

// Benchmarks for the hook hot path: RewriteCommand runs on every shell command
// an agent issues, so its cost is part of the <10ms startup budget.
var benchCmdSet = map[string]struct{}{
	"git": {}, "go": {}, "grep": {}, "make": {}, "npm": {}, "cargo": {},
}

var benchSink RewriteResult

func benchRewrite(b *testing.B, cmd string) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		benchSink = RewriteCommand(cmd, benchCmdSet, nil, "/usr/local/bin/snip")
	}
}

// Realistic: the single most common shape an agent issues.
func BenchmarkRewriteSimple(b *testing.B) {
	benchRewrite(b, "go test ./...")
}

// Realistic: a compound command.
func BenchmarkRewriteCompound(b *testing.B) {
	benchRewrite(b, "git add . && go test ./... && git commit -m 'x'")
}

// Realistic: a multi-line script with a block in the middle.
func BenchmarkRewriteScript(b *testing.B) {
	benchRewrite(b, "go mod tidy\nfor f in *.go\ndo\n  gofmt -l $f\ndone\ngo build ./...\ngo test ./...")
}

// Pathological: nested blocks, quotes and a pipeline on the closer.
func BenchmarkRewriteBlock(b *testing.B) {
	benchRewrite(b, "for f in *.yaml\ndo\n  if grep -q 'a && b' $f; then\n    grep reason $f\n  fi\ndone | wc -l")
}

// Pathological: a heredoc payload that must be copied through verbatim.
func BenchmarkRewriteHeredoc(b *testing.B) {
	benchRewrite(b, "cat > Makefile <<'EOF'\ntest:\n\tgo test ./...\nbuild:\n\tgo build ./...\nEOF\ngo vet ./...")
}
