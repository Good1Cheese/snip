package hook

import "testing"

// TestRewriteBlockScope pins issue #133 bug A: the segmenter splits on top-level
// ';', '&&', '||', '&' and '\n' with no notion of block structure, so a loop or
// conditional body becomes an ordinary group and gets wrapped. The #111 guard
// (never wrap a producer whose output feeds a pipe or a redirect) only inspects
// the group it is in, and `done | wc -l` / `done > out.txt` sit in a different
// group from the body, so the body was wrapped and the consumer silently read
// snip's compacted view instead of the real output.
func TestRewriteBlockScope(t *testing.T) {
	const bin = "/usr/local/bin/snip"
	cmdSet := map[string]struct{}{"git": {}, "go": {}, "grep": {}, "wc": {}}

	cases := []struct {
		name     string
		cmd      string
		want     string
		changed  bool
		allKnown bool
	}{
		{
			// Bug A, the reported form: the body must stay raw so `wc -l` counts
			// the real matches (60), not the 50 that filters/grep.yaml caps at.
			name:     "multiline loop piped to consumer",
			cmd:      "for f in *.yaml\ndo\n  grep reason $f\ndone | wc -l",
			want:     "for f in *.yaml\ndo\n  grep reason $f\ndone | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			name:     "multiline loop redirected to file",
			cmd:      "for f in *.yaml\ndo\n  grep reason $f\ndone > out.txt",
			want:     "for f in *.yaml\ndo\n  grep reason $f\ndone > out.txt",
			changed:  false,
			allKnown: false,
		},
		{
			// Bug A in its SINGLE-LINE form: a second body command exposes the
			// body to rewriting, and the naive newline-armed guard misses it.
			name:     "single line loop with two body commands piped",
			cmd:      "for f in *.go; do echo $f; wc -l $f; done | sort -n",
			want:     "for f in *.go; do echo $f; wc -l $f; done | sort -n",
			changed:  false,
			allKnown: false,
		},
		{
			name:     "single line loop with two body commands redirected",
			cmd:      "for f in *.yaml; do echo start; grep x $f; done > out.txt",
			want:     "for f in *.yaml; do echo start; grep x $f; done > out.txt",
			changed:  false,
			allKnown: false,
		},
		{
			// The closing keyword glued to its operator must still be recognised.
			name:     "closer glued to pipe",
			cmd:      "for f in *.go\ndo\n  grep x $f\ndone|wc -l",
			want:     "for f in *.go\ndo\n  grep x $f\ndone|wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			// The block opener may sit behind a pipe stage: `|` is a command
			// position too, so `while` still opens a block here.
			name:     "opener behind a pipe stage",
			cmd:      "cat list | while read -r l\ndo\n  grep x $l\n  go test $l\ndone | wc -l",
			want:     "cat list | while read -r l\ndo\n  grep x $l\n  go test $l\ndone | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			name:     "if then fi body",
			cmd:      "if grep -q foo a.txt; then\n  go test ./...\n  grep x b.txt\nfi > out.txt",
			want:     "if grep -q foo a.txt; then\n  go test ./...\n  grep x b.txt\nfi > out.txt",
			changed:  false,
			allKnown: false,
		},
		{
			name:     "brace group redirected",
			cmd:      "{ echo a\n  go test ./...\n} > out.txt",
			want:     "{ echo a\n  go test ./...\n} > out.txt",
			changed:  false,
			allKnown: false,
		},
		{
			name:     "multiline subshell piped",
			cmd:      "(\n  go test ./...\n) | tee log",
			want:     "(\n  go test ./...\n) | tee log",
			changed:  false,
			allKnown: false,
		},
		{
			name:     "function body",
			cmd:      "deploy() {\n  go build ./...\n  git status\n}",
			want:     "deploy() {\n  go build ./...\n  git status\n}",
			changed:  false,
			allKnown: false,
		},
		{
			name:     "case esac body",
			cmd:      "case $1 in\n  a)\n    go test ./...\n    ;;\nesac | wc -l",
			want:     "case $1 in\n  a)\n    go test ./...\n    ;;\nesac | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			// Same arm spelled with a blank before the closing paren. A depth
			// counter that reads a trailing spaced ')' as a subshell close pops
			// the case back to top level here and re-wraps the arm body, which is
			// the exact corruption #133 reports (raw 60 matches, rewritten 51).
			// Only the KIND of the innermost open block tells the two apart.
			name:     "case pattern spaced before paren",
			cmd:      "case $x in\n  a )\n    grep reason f\n    ;;\nesac | wc -l",
			want:     "case $x in\n  a )\n    grep reason f\n    ;;\nesac | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			name:     "multi pattern case arm",
			cmd:      "case $1 in\n  -v | --verbose )\n    go test ./...\n    grep x f\n    ;;\nesac > out.txt",
			want:     "case $1 in\n  -v | --verbose )\n    go test ./...\n    grep x f\n    ;;\nesac > out.txt",
			changed:  false,
			allKnown: false,
		},
		{
			// Several arms in a row: every pattern line closes nothing, so the
			// case block stays open until `esac`.
			name:     "several case arms stay inside the block",
			cmd:      "case $1 in\n  a )\n    go test ./...\n    ;;\n  b)\n    grep x f\n    ;;\nesac | wc -l",
			want:     "case $1 in\n  a )\n    go test ./...\n    ;;\n  b)\n    grep x f\n    ;;\nesac | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			// A real subshell inside a case arm still closes on its own ')'.
			name:     "subshell inside a case arm",
			cmd:      "case $1 in\n  a )\n    ( go test ./... )\n    ;;\nesac | wc -l\ngo build ./...",
			want:     "case $1 in\n  a )\n    ( go test ./... )\n    ;;\nesac | wc -l\n" + `"/usr/local/bin/snip" run -- go build ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			name:     "select loop body",
			cmd:      "select f in a b\ndo\n  grep x $f\ndone | wc -l",
			want:     "select f in a b\ndo\n  grep x $f\ndone | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			name:     "until loop body",
			cmd:      "until grep -q x f\ndo\n  go build ./...\n  grep y f\ndone > out.txt",
			want:     "until grep -q x f\ndo\n  go build ./...\n  grep y f\ndone > out.txt",
			changed:  false,
			allKnown: false,
		},
		{
			// Nested blocks must unwind back to top level.
			name:     "nested blocks unwind",
			cmd:      "for f in *\ndo\n  if true; then\n    grep x $f\n  fi\ndone | wc -l\ngo test ./...",
			want:     "for f in *\ndo\n  if true; then\n    grep x $f\n  fi\ndone | wc -l\n" + `"/usr/local/bin/snip" run -- go test ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			// Blast radius is the block, not the whole command: a sibling
			// top-level command keeps its filtering. This is what the rejected
			// whole-command guard could not do.
			name:     "sibling top level command keeps filtering",
			cmd:      "go test ./...\nfor f in *.go\ndo\n  echo $f\n  grep x $f\ndone\ngit status",
			want:     `"/usr/local/bin/snip" run -- go test ./...` + "\nfor f in *.go\ndo\n  echo $f\n  grep x $f\ndone\n" + `"/usr/local/bin/snip" run -- git status`,
			changed:  true,
			allKnown: false,
		},
		{
			// One-line subshell: `(` is only an opener as a standalone word, so
			// this keeps behaving exactly as on master (the trailing group is
			// still guarded by the existing per-group #111 check).
			name:     "one line subshell still filtered",
			cmd:      "(cd sub && go test ./...)",
			want:     `(cd sub && "/usr/local/bin/snip" run -- go test ./...)`,
			changed:  true,
			allKnown: false,
		},
		{
			name:     "one line subshell piped stays raw",
			cmd:      "(cd sub && go test ./...) | wc -l",
			want:     "(cd sub && go test ./...) | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			// Balanced spaced subshell: opens and closes within one group.
			name:     "spaced one line subshell balanced",
			cmd:      "( go build ./... )\ngo test ./...",
			want:     "( go build ./... )\n" + `"/usr/local/bin/snip" run -- go test ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			// The close must be recognised where the ')' actually is, not only at
			// the end of the group. A trailing redirect after the closer used to
			// pin the depth above zero for the rest of the command, silently
			// disabling filtering for every later top-level command.
			name:     "subshell closed before a redirect",
			cmd:      "( cd sub && go build ./... ) > out.txt\ngo test ./...",
			want:     "( cd sub && go build ./... ) > out.txt\n" + `"/usr/local/bin/snip" run -- go test ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			name:     "subshell closed before a pipe",
			cmd:      "( cd sub && go build ./... ) | tee log\ngo test ./...",
			want:     "( cd sub && go build ./... ) | tee log\n" + `"/usr/local/bin/snip" run -- go test ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			name:     "subshell closed with no space before paren",
			cmd:      "( go build ./...)\ngo test ./...",
			want:     "( go build ./...)\n" + `"/usr/local/bin/snip" run -- go test ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			// Body groups of the subshell stay raw while it is open.
			name:     "subshell body stays raw until the closer",
			cmd:      "( cd sub && go build ./... ) > out.txt",
			want:     "( cd sub && go build ./... ) > out.txt",
			changed:  false,
			allKnown: false,
		},
		{
			// An array assignment inside a subshell must not close it: its ')'
			// is matched by the '(' of the assignment, not by the subshell's.
			name:     "array assignment inside a subshell does not close it",
			cmd:      "(\n  arr=(a b)\n  go test ./...\n) | wc -l\ngo build ./...",
			want:     "(\n  arr=(a b)\n  go test ./...\n) | wc -l\n" + `"/usr/local/bin/snip" run -- go build ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			// A quoted ')' is not a closer. Quotes must be skipped, not merely
			// stepped over.
			name:     "quoted paren inside a subshell does not close it",
			cmd:      "(\n  grep -c ')' f\n  go test ./...\n) | wc -l\ngo build ./...",
			want:     "(\n  grep -c ')' f\n  go test ./...\n) | wc -l\n" + `"/usr/local/bin/snip" run -- go build ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			// A closing keyword is only a keyword in command position: "echo
			// done" inside a loop body must not close the loop.
			name:     "closer keyword as an argument inside a block",
			cmd:      "for f in *.go\ndo\n  echo done\n  go test $f\ndone | wc -l",
			want:     "for f in *.go\ndo\n  echo done\n  go test $f\ndone | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			// A redirect target is never a command, so it is never a keyword
			// either — even when the file is named after one.
			name:     "closer keyword as a redirect target inside a block",
			cmd:      "for f in *.go\ndo\n  echo $f > done\n  go test $f\ndone | wc -l",
			want:     "for f in *.go\ndo\n  echo $f > done\n  go test $f\ndone | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			// "time" introduces another command position, so the loop behind it
			// is still seen as an opener.
			name:     "time before a loop still opens the block",
			cmd:      "time for f in *.go; do echo $f; go test $f; done | wc -l",
			want:     "time for f in *.go; do echo $f; go test $f; done | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			// A function header spelled on its own line still opens its body.
			name:     "function header then brace group",
			cmd:      "deploy() {\n  go build ./...\n}\ngo test ./...",
			want:     "deploy() {\n  go build ./...\n}\n" + `"/usr/local/bin/snip" run -- go test ./...`,
			changed:  true,
			allKnown: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := RewriteCommand(tc.cmd, cmdSet, nil, bin)
			if res.Command != tc.want {
				t.Errorf("Command = %q, want %q", res.Command, tc.want)
			}
			if res.Changed != tc.changed {
				t.Errorf("Changed = %v, want %v", res.Changed, tc.changed)
			}
			if res.AllKnown != tc.allKnown {
				t.Errorf("AllKnown = %v, want %v", res.AllKnown, tc.allKnown)
			}
		})
	}
}

// TestRewriteHeredoc pins issue #133 bug B: heredoc bodies are literal text the
// command writes, not commands to run. Rewriting them corrupts the file an agent
// is writing (a Makefile, a CI config, a shell script).
func TestRewriteHeredoc(t *testing.T) {
	const bin = "/usr/local/bin/snip"
	cmdSet := map[string]struct{}{"git": {}, "go": {}, "grep": {}, "ssh": {}}

	cases := []struct {
		name     string
		cmd      string
		want     string
		changed  bool
		allKnown bool
	}{
		{
			name:     "body is literal text",
			cmd:      "cat <<EOF\ngo test ./...\nEOF",
			want:     "cat <<EOF\ngo test ./...\nEOF",
			changed:  false,
			allKnown: false,
		},
		{
			name:     "quoted delimiter",
			cmd:      "cat > Makefile <<'EOF'\ntest:\n\tgo test ./...\nEOF",
			want:     "cat > Makefile <<'EOF'\ntest:\n\tgo test ./...\nEOF",
			changed:  false,
			allKnown: false,
		},
		{
			name:     "tab stripped delimiter",
			cmd:      "cat <<-EOF\n\tgo test ./...\n\tEOF",
			want:     "cat <<-EOF\n\tgo test ./...\n\tEOF",
			changed:  false,
			allKnown: false,
		},
		{
			name:     "two heredocs on one line",
			cmd:      "cat <<A <<B\ngo build ./...\nA\ngo vet ./...\nB",
			want:     "cat <<A <<B\ngo build ./...\nA\ngo vet ./...\nB",
			changed:  false,
			allKnown: false,
		},
		{
			name:     "unterminated heredoc swallows the rest",
			cmd:      "cat <<EOF\ngo test ./...\n",
			want:     "cat <<EOF\ngo test ./...\n",
			changed:  false,
			allKnown: false,
		},
		{
			// Commands after the terminator are ordinary commands again.
			name:     "command after terminator still filtered",
			cmd:      "cat <<EOF > f\ngo test ./...\nEOF\ngo build ./...",
			want:     "cat <<EOF > f\ngo test ./...\nEOF\n" + `"/usr/local/bin/snip" run -- go build ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			// A heredoc-fed producer must not be wrapped: `snip run` never wires
			// stdin to the child, so the wrapped form would read nothing. And an
			// uninspected heredoc payload must never be auto-allowed (#88).
			name:     "heredoc fed known producer left raw",
			cmd:      "ssh host <<EOF\nrm -rf /\nEOF",
			want:     "ssh host <<EOF\nrm -rf /\nEOF",
			changed:  false,
			allKnown: false,
		},
		{
			// Terminator matching must be pinned by what comes AFTER the heredoc:
			// every way of breaking it degrades to "swallow the rest of the
			// command", which is output-identical to the correct path unless a
			// later command is asserted on. Quoted delimiter: the terminator line
			// is the UNQUOTED word, so the quotes must be stripped when parsing.
			name:     "quoted delimiter then command still filtered",
			cmd:      "cat > Makefile <<'EOF'\ntest:\n\tgo test ./...\nEOF\ngo build ./...",
			want:     "cat > Makefile <<'EOF'\ntest:\n\tgo test ./...\nEOF\n" + `"/usr/local/bin/snip" run -- go build ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			name:     "double quoted delimiter then command still filtered",
			cmd:      "cat <<\"EOF\"\ngo test ./...\nEOF\ngo build ./...",
			want:     "cat <<\"EOF\"\ngo test ./...\nEOF\n" + `"/usr/local/bin/snip" run -- go build ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			// Backslash-escaped delimiter: \EOF also names EOF.
			name:     "escaped delimiter then command still filtered",
			cmd:      "cat <<\\EOF\ngo test ./...\nEOF\ngo build ./...",
			want:     "cat <<\\EOF\ngo test ./...\nEOF\n" + `"/usr/local/bin/snip" run -- go build ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			// <<- strips leading TABS from the terminator line before comparing.
			name:     "tab stripped delimiter then command still filtered",
			cmd:      "cat <<-EOF\n\tgo test ./...\n\tEOF\ngo build ./...",
			want:     "cat <<-EOF\n\tgo test ./...\n\tEOF\n" + `"/usr/local/bin/snip" run -- go build ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			// Without <<-, an indented terminator is NOT a terminator: bash wants
			// an exact match, so the rest of the command is part of the body.
			name:     "indented terminator without dash does not terminate",
			cmd:      "cat <<EOF\ngo test ./...\n\tEOF\ngo build ./...",
			want:     "cat <<EOF\ngo test ./...\n\tEOF\ngo build ./...",
			changed:  false,
			allKnown: false,
		},
		{
			// Two heredocs: the second body must start after the FIRST terminator,
			// otherwise the trailing command is swallowed.
			name:     "two heredocs then command still filtered",
			cmd:      "cat <<A <<B\ngo build ./...\nA\ngo vet ./...\nB\ngo test ./...",
			want:     "cat <<A <<B\ngo build ./...\nA\ngo vet ./...\nB\n" + `"/usr/local/bin/snip" run -- go test ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			// An arithmetic command and a heredoc on the same line: the "))"
			// must end the arithmetic context so the '<<' that follows is read
			// as a heredoc operator again.
			name:     "heredoc after an arithmetic command on the same line",
			cmd:      "((x = 1 << n)) && cat <<EOF\ngo test ./...\nEOF\ngo build ./...",
			want:     "((x = 1 << n)) && cat <<EOF\ngo test ./...\nEOF\n" + `"/usr/local/bin/snip" run -- go build ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			// A '#' that is not at word start does not open a comment, so the
			// heredoc here is armed and its body is not rewritten. If it were
			// read as a comment the heredoc would be missed and `go test ./...`
			// on the next line would be wrapped.
			name:     "hash inside a word does not suppress the heredoc",
			cmd:      "cat > notes#1.md <<EOF\ngo test ./...\nEOF\ngo build ./...",
			want:     "cat > notes#1.md <<EOF\ngo test ./...\nEOF\n" + `"/usr/local/bin/snip" run -- go build ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			// A here-string has no body: the next line is a real command, and the
			// here-string group itself keeps the master behaviour (wrapped). That
			// wrapping is unsound for a different, pre-existing reason — snip run
			// does not forward stdin — but '<' is out of scope here, exactly as
			// hasTopLevelRedirect documents.
			name:     "here string is not a heredoc",
			cmd:      "grep -c x <<<'go test'\ngo build ./...",
			want:     `"/usr/local/bin/snip" run -- grep -c x <<<'go test'` + "\n" + `"/usr/local/bin/snip" run -- go build ./...`,
			changed:  true,
			allKnown: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := RewriteCommand(tc.cmd, cmdSet, nil, bin)
			if res.Command != tc.want {
				t.Errorf("Command = %q, want %q", res.Command, tc.want)
			}
			if res.Changed != tc.changed {
				t.Errorf("Changed = %v, want %v", res.Changed, tc.changed)
			}
			if res.AllKnown != tc.allKnown {
				t.Errorf("AllKnown = %v, want %v", res.AllKnown, tc.allKnown)
			}
		})
	}
}

// TestRewriteNoFalsePositives is the regression set from issue #133: removing
// unsound filtering is the goal, removing sound filtering is a regression. Every
// command here must keep being rewritten exactly as it is on master. The naive
// guard that was tried first broke the first two, because strings.Fields ignores
// quoting and '<<' matched as a bare substring.
func TestRewriteNoFalsePositives(t *testing.T) {
	const bin = "/usr/local/bin/snip"
	const w = `"/usr/local/bin/snip" run -- `
	cmdSet := map[string]struct{}{"git": {}, "go": {}, "grep": {}, "make": {}}

	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{
			// "do" appears as an ordinary argument, never in command position.
			name: "keyword as argument",
			cmd:  "go test ./...\necho nothing else to do",
			want: w + "go test ./...\necho nothing else to do",
		},
		{
			// '<<' inside single quotes is not a heredoc operator.
			name: "quoted shift operator",
			cmd:  "go build ./...\ngrep '<<' main.go",
			want: w + "go build ./...\n" + w + "grep '<<' main.go",
		},
		{
			name: "then as an argument",
			cmd:  "make build\necho build ok then deploy",
			want: w + "make build\necho build ok then deploy",
		},
		{
			name: "plain multi line script",
			cmd:  "go mod tidy\ngo build ./...\ngo vet ./...\ngo test ./...",
			want: w + "go mod tidy\n" + w + "go build ./...\n" + w + "go vet ./...\n" + w + "go test ./...",
		},
		{
			name: "keyword inside a grep pattern",
			cmd:  "grep -rn 'for f in x; do echo; done' .",
			want: w + "grep -rn 'for f in x; do echo; done' .",
		},
		{
			name: "done as an argument",
			cmd:  "grep done Makefile\ngo test ./...",
			want: w + "grep done Makefile\n" + w + "go test ./...",
		},
		{
			name: "brace expansion is not a block",
			cmd:  "mkdir -p x/{p,q}\ngo test ./...",
			want: "mkdir -p x/{p,q}\n" + w + "go test ./...",
		},
		{
			name: "array assignment is not a subshell",
			cmd:  "arr=(a b)\ngo test ./...",
			want: "arr=(a b)\n" + w + "go test ./...",
		},
		{
			// A bare arithmetic command must not arm a phantom heredoc whose
			// delimiter never appears.
			name: "arithmetic shift is not a heredoc",
			cmd:  "((x = 1 << 2))\ngo test ./...",
			want: "((x = 1 << 2))\n" + w + "go test ./...",
		},
		{
			// Same, with a variable right operand: the delimiter would be "n",
			// which no all-digit guard can reject. Arithmetic is recognised by
			// its "((" instead.
			name: "arithmetic shift by a variable is not a heredoc",
			cmd:  "((x = 1 << n))\ngo test ./...",
			want: "((x = 1 << n))\n" + w + "go test ./...",
		},
		{
			// The arithmetic suppression is scoped to its line: an unbalanced
			// "((" must not disarm heredoc detection for the rest of the
			// command, or a heredoc body further down would be rewritten.
			name: "unbalanced arithmetic does not disarm later heredocs",
			cmd:  "((x = 1 << n\ncat <<EOF\ngo vet ./...\nEOF\ngo test ./...",
			want: "((x = 1 << n\ncat <<EOF\ngo vet ./...\nEOF\n" + w + "go test ./...",
		},
		{
			// A bare "<<" has no delimiter and is not a heredoc: arming one
			// would swallow every following command.
			name: "bare shift operator is not a heredoc",
			cmd:  "go build ./... <<\ngo test ./...",
			want: w + "go build ./... <<\n" + w + "go test ./...",
		},
		{
			// '<<' inside a comment is not a heredoc operator either.
			name: "shift operator in a comment",
			cmd:  "go test ./... # see <<EOF\ngit status",
			want: w + "go test ./... # see <<EOF\n" + w + "git status",
		},
		{
			name: "stray closer at top level",
			cmd:  "echo done\ngo test ./...",
			want: "echo done\n" + w + "go test ./...",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := RewriteCommand(tc.cmd, cmdSet, nil, bin)
			if res.Command != tc.want {
				t.Errorf("Command = %q, want %q", res.Command, tc.want)
			}
		})
	}
}
