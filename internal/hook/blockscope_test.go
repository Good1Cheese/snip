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
			// A '(' in command position opens a subshell whatever follows it, so
			// the body is raw even on one line. Wrapping it was safe only while
			// the whole subshell fitted in ONE group: the previous rule read
			// "(cd sub" as a non-opener and relied on the per-group #111 check,
			// which the next case defeats with a single ';'. The cost is real —
			// this shape is no longer filtered — and it is the price of never
			// feeding a consumer snip's compacted output.
			name:     "one line subshell stays raw",
			cmd:      "(cd sub && go test ./...)",
			want:     "(cd sub && go test ./...)",
			changed:  false,
			allKnown: false,
		},
		{
			// The gap the old "one line subshell piped stays raw" case hid: with
			// a ';' inside the parens the subshell spans three groups, the
			// `go test` group has no pipe of its own, and the per-group #111
			// check cannot see the `| wc -l` that consumes the whole subshell.
			// Raw prints 61 lines, the wrapped form printed 52.
			name:     "one line subshell with an inner semicolon piped stays raw",
			cmd:      "(cd sub && go test ./...; echo x) | wc -l",
			want:     "(cd sub && go test ./...; echo x) | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			name:     "one line subshell with an inner semicolon redirected stays raw",
			cmd:      "(cd sub && grep -c x f; echo x) > out.txt",
			want:     "(cd sub && grep -c x f; echo x) > out.txt",
			changed:  false,
			allKnown: false,
		},
		{
			// The subshell closes where its ')' is, so a sibling command after it
			// is filtered again: the blast radius is the subshell, not the rest
			// of the message.
			name:     "command after a one line subshell keeps filtering",
			cmd:      "(cd sub && go build ./...; echo x) | wc -l\ngo test ./...",
			want:     "(cd sub && go build ./...; echo x) | wc -l\n" + `"/usr/local/bin/snip" run -- go test ./...`,
			changed:  true,
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
			// Same for an arithmetic command: its "))" is matched by its own
			// "((", so it must not close the subshell it sits in.
			name:     "arithmetic command inside a subshell does not close it",
			cmd:      "(\n  ((x = 1))\n  go test ./...\n) | wc -l\ngo build ./...",
			want:     "(\n  ((x = 1))\n  go test ./...\n) | wc -l\n" + `"/usr/local/bin/snip" run -- go build ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			// Inside "((...))" the words are an arithmetic expression, not
			// commands, so a variable named after a reserved word must not close
			// the loop that a counting idiom like this lives in.
			name:     "arithmetic on a variable named done does not close the loop",
			cmd:      "for f in *.txt\ndo\n  ((done = done + 1))\n  grep MATCH $f\ndone | wc -l",
			want:     "for f in *.txt\ndo\n  ((done = done + 1))\n  grep MATCH $f\ndone | wc -l",
			changed:  false,
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
		{
			// The ksh/bash `function NAME {` spelling has no parentheses, so
			// nothing before the '{' put it in command position and the body was
			// rewritten: `findall | wc -l` then counted snip's capped output
			// (51) instead of the 60 real matches.
			name:     "function keyword without parens piped",
			cmd:      "function findall {\n  grep MATCH data.txt\n}\nfindall | wc -l",
			want:     "function findall {\n  grep MATCH data.txt\n}\nfindall | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			name:     "function keyword without parens redirected",
			cmd:      "function findall {\n  grep MATCH data.txt\n}\nfindall > out.txt",
			want:     "function findall {\n  grep MATCH data.txt\n}\nfindall > out.txt",
			changed:  false,
			allKnown: false,
		},
		{
			// Same on a single line, where the body is a group of its own.
			name:     "function keyword without parens on one line",
			cmd:      "function findall { grep MATCH f; }\nfindall | wc -l",
			want:     "function findall { grep MATCH f; }\nfindall | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			// The redundant `function NAME()` spelling keeps working, and the
			// body still closes so the next command is filtered again.
			name:     "function keyword with parens closes its body",
			cmd:      "function deploy() {\n  go build ./...\n}\ngo test ./...",
			want:     "function deploy() {\n  go build ./...\n}\n" + `"/usr/local/bin/snip" run -- go test ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			name:     "function keyword without parens closes its body",
			cmd:      "function deploy {\n  go build ./...\n}\ngo test ./...",
			want:     "function deploy {\n  go build ./...\n}\n" + `"/usr/local/bin/snip" run -- go test ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			// The `function` name must be consumed once and only once: a state
			// that is never cleared would swallow the `for` below as if it were
			// a function name, and the loop body would be rewritten.
			name:     "loop after a function definition still opens",
			cmd:      "function deploy {\n  go build ./...\n}\nfor f in *.txt\ndo\n  grep MATCH $f\ndone | wc -l",
			want:     "function deploy {\n  go build ./...\n}\nfor f in *.txt\ndo\n  grep MATCH $f\ndone | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			// A ';' inside a comment is not a group boundary, so the words after
			// it are still comment text. Reading them as a new command position
			// let `done` close the live loop and the rest of the body was
			// rewritten: `wc -l` counted 51 instead of 60.
			name:     "comment containing a semicolon and a closer",
			cmd:      "for f in 1; do\n  : # count matches; done below\n  grep MATCH data.txt\ndone | wc -l",
			want:     "for f in 1; do\n  : # count matches; done below\n  grep MATCH data.txt\ndone | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			// Same for the other boundaries the splitter honours.
			name:     "comment containing && and a closer",
			cmd:      "for f in 1; do\n  : # check && done\n  grep MATCH data.txt\ndone | wc -l",
			want:     "for f in 1; do\n  : # check && done\n  grep MATCH data.txt\ndone | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			name:     "comment containing || and a closer",
			cmd:      "for f in 1; do\n  : # check || esac fi done\n  grep MATCH data.txt\ndone | wc -l",
			want:     "for f in 1; do\n  : # check || esac fi done\n  grep MATCH data.txt\ndone | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			// A comment cannot open a block either, so the command after it is
			// still filtered.
			name:     "comment containing an opener does not open a block",
			cmd:      "echo a # for f in *; do\ngo test ./...",
			want:     "echo a # for f in *; do\n" + `"/usr/local/bin/snip" run -- go test ./...`,
			changed:  true,
			allKnown: false,
		},
		{
			// A command spelled inside a comment never runs, so it is not
			// rewritten either. This differs from master, which split the
			// comment at the ';' and wrapped its tail — harmless there, but the
			// same split is what corrupted the loop above.
			name:     "command inside a comment is not rewritten",
			cmd:      "echo a # go test ./...; go build ./...",
			want:     "echo a # go test ./...; go build ./...",
			changed:  false,
			allKnown: false,
		},
		{
			// A '#' glued to a closing quote stays inside the word, exactly as
			// the shell reads it. Treating it as a comment would hide the
			// `while` behind it and the loop body would be rewritten. This is
			// the only shape that reaches the check: a '#' anywhere else in a
			// word is swallowed by the word scan.
			name:     "hash right after a quoted word is not a comment",
			cmd:      "grep -c \"x\"#tag f | while read -r l\ndo\n  grep MATCH data.txt\ndone | wc -l",
			want:     "grep -c \"x\"#tag f | while read -r l\ndo\n  grep MATCH data.txt\ndone | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			// A block nested in a subshell: the word right after '(' is in
			// command position, so `for` opens a loop of its own. If it did not,
			// `done` would close the SUBSHELL instead and everything after it
			// inside the parens would be rewritten while `wc -l` counts the lot.
			name:     "loop nested in a one line subshell closes the loop not the subshell",
			cmd:      "(for f in *.txt; do grep MATCH $f; done; grep MATCH data.txt; echo x) | wc -l",
			want:     "(for f in *.txt; do grep MATCH $f; done; grep MATCH data.txt; echo x) | wc -l",
			changed:  false,
			allKnown: false,
		},
		{
			// A '#' that is not at word start is ordinary text, so this is a
			// real command and stays filtered.
			name:     "hash inside a word is not a comment",
			cmd:      "go test ./... > notes#1.md\ngo build ./...",
			want:     "go test ./... > notes#1.md\n" + `"/usr/local/bin/snip" run -- go build ./...`,
			changed:  true,
			allKnown: true,
		},
		{
			// Graceful degradation: a malformed command with an unmatched '(' is
			// still handed to the hook, and its stray paren must not pin the
			// loop open forever — the closing keyword drops it and filtering
			// resumes after the block.
			name:     "stray open paren inside a block does not survive its closer",
			cmd:      "for f in *.go\ndo\n  echo a(b\ndone\ngo test ./...",
			want:     "for f in *.go\ndo\n  echo a(b\ndone\n" + `"/usr/local/bin/snip" run -- go test ./...`,
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
			// The '(' here is not in command position (the assignment word came
			// first), so it opens no subshell and the next line keeps filtering.
			name: "array assignment is not a subshell",
			cmd:  "arr=(a b)\ngo test ./...",
			want: "arr=(a b)\n" + w + "go test ./...",
		},
		{
			name: "three element array assignment is not a subshell",
			cmd:  "arr=(a b c)\ngo test ./...",
			want: "arr=(a b c)\n" + w + "go test ./...",
		},
		{
			// "((" in command position is an arithmetic command, not a subshell.
			name: "arithmetic command is not a subshell",
			cmd:  "((x = 1 + 2))\ngo test ./...",
			want: "((x = 1 + 2))\n" + w + "go test ./...",
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

// TestRewriteCommandPosition pins issue #138: four shapes fooled the
// command-position approximation, so a reserved word that is not a command
// closed a block that was still open and the body was rewritten. Each case here
// corrupts identically on master, where a `grep` capped at 50 lines answered a
// consumer that must see all 60.
func TestRewriteCommandPosition(t *testing.T) {
	const bin = "/usr/local/bin/snip"
	const w = `"/usr/local/bin/snip" run -- `
	cmdSet := map[string]struct{}{"git": {}, "go": {}, "grep": {}, "wc": {}}

	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{
			// A case-arm pattern is a pattern, never a command: `done)` must not
			// pop the case it is an arm of.
			name: "case arm pattern is a reserved word",
			cmd:  "case $s in\n  done)\n    grep MATCH data.txt\n    ;;\nesac | wc -l",
			want: "case $s in\n  done)\n    grep MATCH data.txt\n    ;;\nesac | wc -l",
		},
		{
			// The arm pattern is re-armed after ';;', so a later arm is protected
			// too, not just the first one.
			name: "reserved word in a later case arm",
			cmd:  "case $s in\n  a)\n    echo x\n    ;;\n  done)\n    grep MATCH data.txt\n    ;;\nesac | wc -l",
			want: "case $s in\n  a)\n    echo x\n    ;;\n  done)\n    grep MATCH data.txt\n    ;;\nesac | wc -l",
		},
		{
			// Inside parentheses '|' is pattern alternation, not a pipeline, so it
			// does not restore command position.
			name: "extglob alternative names a reserved word",
			cmd:  "for f in @(data|done).txt\ndo\n  grep MATCH $f\ndone | wc -l",
			want: "for f in @(data|done).txt\ndo\n  grep MATCH $f\ndone | wc -l",
		},
		{
			// `time` keeps command position for the command it times; its own
			// options must not lose the opener that follows them.
			name: "time with its -p option",
			cmd:  "time -p for i in 1\ndo\n  grep MATCH data.txt\ndone | wc -l",
			want: "time -p for i in 1\ndo\n  grep MATCH data.txt\ndone | wc -l",
		},
		{
			// Bash starts a word right after the ')' operator, so this is a
			// comment and the `done` inside it closes nothing.
			name: "comment flush against a case arm paren",
			cmd:  "case $s in\n  a)# done marker\n    grep MATCH data.txt\n    ;;\nesac | wc -l",
			want: "case $s in\n  a)# done marker\n    grep MATCH data.txt\n    ;;\nesac | wc -l",
		},
		{
			name: "comment flush against a subshell paren",
			cmd:  "(grep MATCH data.txt)# done\ngrep x y",
			want: "(grep MATCH data.txt)# done\n" + w + "grep x y",
		},
		{
			// Not a comment: this ')' closes a command substitution, so '#' is
			// still inside the word. The block must keep closing normally.
			name: "hash after a command substitution",
			cmd:  "for f in $(ls)#tail\ndo\n  grep x $f\ndone\ngrep y z",
			want: "for f in $(ls)#tail\ndo\n  grep x $f\ndone\n" + w + "grep y z",
		},
		{
			// ${#f} is a length expansion, not a comment: the `done` after it must
			// still close the loop, otherwise the next command loses filtering.
			name: "length expansion inside a body",
			cmd:  "for f in *.go\ndo\n  grep ${#f} $f\ndone\ngrep x y",
			want: "for f in *.go\ndo\n  grep ${#f} $f\ndone\n" + w + "grep x y",
		},
		{
			// `time` is not a known base command, so the group is left alone;
			// what matters is that nothing here opens or closes a block.
			name: "bare time before a plain command",
			cmd:  "time grep MATCH data.txt\ngrep x y",
			want: "time grep MATCH data.txt\n" + w + "grep x y",
		},
		{
			// A pipeline after a case closes it: filtering resumes afterwards.
			name: "filtering resumes after a case",
			cmd:  "case $s in\n  done)\n    grep MATCH data.txt\n    ;;\nesac\ngrep x y",
			want: "case $s in\n  done)\n    grep MATCH data.txt\n    ;;\nesac\n" + w + "grep x y",
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
