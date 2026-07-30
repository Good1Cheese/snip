package hook

import "strings"

// blockKind is what opened an entry on blockScope's stack. The kind matters
// because the same byte closes different things: ')' ends a subshell, but on a
// case-arm pattern line ("  a )") it ends the pattern and the case block stays
// open. Tracking only a depth cannot tell those apart, and reading "  a )" as a
// subshell close pops the case back to top level and re-wraps the arm body —
// the exact corruption issue #133 reports.
type blockKind uint8

const (
	// blockParen is an unquoted '(' that does not open a subshell because it is
	// not in command position: an array assignment ("arr=(a b)"), an extglob
	// pattern ("@(a|b)"), or the inner parens of an arithmetic command
	// ("((x = 1))"). It is pushed only so its matching ')' is consumed and
	// cannot be mistaken for the close of an enclosing subshell. It is not a
	// block, so it never suppresses rewriting.
	blockParen blockKind = iota
	blockSubshell
	blockBrace // { ... } group or function body
	blockLoop  // for / while / until / select ... done
	blockCond  // if ... fi
	blockCase  // case ... esac
)

// blockScope tracks which shell compound commands the segmenter currently sits
// inside: a loop, a conditional, a brace group, a function body or a subshell.
//
// The segmenter splits on top-level ';', '&&', '||', '&' and '\n', which means a
// block body becomes an ordinary group. The #111 guard that keeps a producer raw
// when its output feeds a pipe or a redirect only inspects the group it is in,
// so `done | wc -l` and `done > out.txt` — which live in a different group from
// the body — never protected the body. It was wrapped, and the consumer read
// snip's compacted view instead of the real output (issue #133, bug A).
//
// The rule the code implements is: nothing inside a block it detects is ever
// rewritten. It is a stack over machinery that already exists, not a shell
// parser, so the honest contract is narrower than that sentence alone:
//
//   - What opens a block: the reserved words `if`, `while`, `until`, `for`,
//     `select` and `case`; a '{' standing alone as a word in command position
//     (which is how a function body opens, both `NAME() {` and `function NAME {`
//     — the header only puts that '{' in command position); and a '(' in command
//     position, whatever follows it (`((` is arithmetic and pushes no block).
//     What closes one: `fi`, `done`, `esac`, a '}' in command position, and the
//     ')' matching a '(' — recognised where it occurs, not only at the end of a
//     group, and never on a case-arm pattern ("  a )"). Reserved words count
//     only in command position and comments are skipped whole, so neither
//     `echo done` nor `# done` closes anything.
//   - Command position is approximated, not decided the way the shell does. The
//     four shapes of issue #138 used to fool it and are now handled: a case-arm
//     pattern spelled like a reserved word (`case $s in done)`), an extglob
//     alternative (`for f in @(data|done).txt`), `time` with its own options
//     (`time -p for i in 1`), and a '#' flush against an operator ')'
//     (`a)# done marker`). The approximation remains one: a shape nobody has
//     hit yet can still read a pattern or an argument as a command.
//   - A missed opener costs correctness, not just savings: the body is rewritten
//     and a consumer of the block reads snip's compacted output. Every opener
//     above is therefore matched on the shell's own rule (command position),
//     never on a heuristic about what follows it.
//   - A spurious opener, and any unmatched opener, costs filtering only: the
//     scope stays open and the remainder of the command is passed through raw.
//     `closeKeyword` bounds that to the enclosing block by dropping stray
//     blockParen entries, so one malformed group cannot disable filtering for
//     the whole message.
//   - Not covered, by construction: line continuations (a trailing backslash is
//     a group boundary here but not for the shell), and consumer propagation —
//     deciding that a block's output has no consumer and rewriting its body
//     anyway. The latter is why `(cd sub && go test ./...)` is no longer
//     filtered: resolving which block a trailing pipe belongs to fails towards
//     wrong output, and this hook path prefers the opposite trade (see the #127
//     process-substitution fix).
type blockScope struct {
	stack []blockKind
	// blocks is the number of stack entries that are not blockParen, i.e. how
	// many real compound commands are open.
	blocks int
	// funcName is set between the `function` reserved word and the name that
	// follows it, so the '{' after the name is still seen in command position.
	funcName bool
	// casePattern is set while the words being read are a case-arm pattern
	// rather than a command: from the `in` of `case $s in` to that arm's ')',
	// and again from each ';;' to the next ')'. A pattern spelled like a
	// reserved word ("done)") must not be classified as one.
	casePattern bool
	// caseHeader is set between `case` and the `in` that ends its subject, so
	// only that `in` arms casePattern. An `in` in an arm body ("echo in") is an
	// ordinary word and must not turn the rest of the body into a pattern.
	caseHeader bool
	// afterTime is set just past `time` and its own option words, which keep
	// command position so the timed command — possibly a block opener — is
	// still seen ("time -p for i in 1").
	afterTime bool
}

// inBlock reports whether the group being flushed sits inside a compound
// command and must therefore be emitted verbatim.
func (s *blockScope) inBlock() bool { return s.blocks > 0 }

func (s *blockScope) push(k blockKind) {
	s.stack = append(s.stack, k)
	if k != blockParen {
		s.blocks++
	}
}

func (s *blockScope) pop() {
	n := len(s.stack)
	if n == 0 {
		return
	}
	if s.stack[n-1] != blockParen {
		s.blocks--
	}
	s.stack = s.stack[:n-1]
}

func (s *blockScope) top() (blockKind, bool) {
	if n := len(s.stack); n > 0 {
		return s.stack[n-1], true
	}
	return 0, false
}

// closeKeyword applies a closing reserved word ("done", "fi", "esac", "}"). Any
// unmatched blockParen entries above the block being closed are dropped first,
// so a stray '(' cannot leave a block open forever.
func (s *blockScope) closeKeyword() {
	for len(s.stack) > 0 && s.stack[len(s.stack)-1] == blockParen {
		s.stack = s.stack[:len(s.stack)-1]
	}
	if k, ok := s.top(); ok && k == blockCase {
		// `esac`: whatever arm state was open belongs to the case being closed.
		// An enclosing case, if any, is back in an arm body, never in a pattern.
		s.casePattern = false
		s.caseHeader = false
	}
	s.pop()
}

// caseArmEnd records the ';;' that ends a case arm. The words after it are the
// next arm's pattern, so a pattern spelled like a reserved word ("done)") does
// not close the case. The segmenter reports it because ';' is a group boundary
// and advance never sees one.
func (s *blockScope) caseArmEnd() {
	if k, ok := s.top(); ok && k == blockCase {
		s.casePattern = true
	}
}

// advance updates the scope from a group that has just been emitted. It walks
// the group byte by byte, honouring quotes, and keeps track of whether the next
// word sits in command position — the only place a reserved word is reserved.
//
// The '(' and ')' bytes are handled where they occur rather than only at the end
// of the group: a subshell closed before a trailing redirect or pipe
// ("( cd sub && go build ./... ) > out.txt") must still close, otherwise the
// scope stays open and every later top-level command silently loses filtering.
func (s *blockScope) advance(group string) {
	cmdPos := true
	// Set while the byte just consumed was a ')' acting as an operator: a
	// case-arm pattern's or a subshell's. Bash starts a word right after such a
	// ')', so a '#' flush against it opens a comment. isWordStart cannot tell
	// that ')' from the one closing a command substitution, where the '#' is
	// still inside the word ("$(ls)#tail"), so the distinction is made here,
	// where the kind of the paren being closed is known.
	operatorParen := false
	for i := 0; i < len(group); {
		afterOperatorParen := operatorParen
		operatorParen = false
		switch c := group[i]; c {
		case ' ', '\t', '\n':
			i++
		case '\'', '"':
			i = skipQuoted(group, i)
			cmdPos = false
		case ';', '|':
			// A single '|' starts a new pipeline stage, which is a command
			// position ("cat list | while read -r l" opens a loop). ';' is a
			// group boundary and cannot reach here, but costs nothing to accept.
			i++
			// Inside parentheses that are not a subshell, '|' is pattern
			// alternation and the word after it is a pattern: in
			// "@(data|done).txt" a bare `done` must not close the enclosing loop.
			if k, ok := s.top(); ok && k == blockParen {
				cmdPos = false
			} else {
				cmdPos = true
			}
		case '&', '<', '>':
			// Redirections only: "&&" and a bare "&" are group boundaries, so an
			// '&' here belongs to a form like "2>&1".
			i++
			cmdPos = false
		case '#':
			if !isWordStart(group, i) && !afterOperatorParen {
				// Not a comment: '#' can follow a quoted region inside one word
				// (`"x"#y`). The only way to land here is just past a closing
				// quote, which already cleared cmdPos.
				i++
				continue
			}
			// A comment runs to the end of the line and holds no command, so no
			// reserved word in it may open or close a block. The segmenter ends
			// the group at that newline (a ';' inside a comment is not a
			// boundary), so the comment always runs to the end of the group.
			return
		case '(':
			i, cmdPos = s.openParen(group, i, cmdPos)
		case ')':
			i++
			if k, ok := s.top(); ok && k == blockCase {
				// End of a case-arm pattern ("  a )" or "  -v | --verbose )").
				// The case block stays open, so the arm body stays unwrapped,
				// and the arm's own body is a command position.
				s.casePattern = false
				s.caseHeader = false
				operatorParen = true
				cmdPos = true
				continue
			} else if ok && (k == blockParen || k == blockSubshell) {
				operatorParen = k == blockSubshell
				s.pop()
			}
			cmdPos = false
		case '{':
			// A brace group only when '{' stands alone as a word; "x/{p,q}" is
			// brace expansion.
			if cmdPos && (i+1 >= len(group) || isTokenBreak(group[i+1])) {
				s.push(blockBrace)
				i++
				cmdPos = true
				continue
			}
			i++
			cmdPos = false
		case '}':
			if cmdPos {
				if k, ok := s.top(); ok && k == blockBrace {
					s.pop()
				}
			}
			i++
			cmdPos = false
		default:
			start := i
			for i < len(group) && !isWordBreak(group[i]) {
				i++
			}
			cmdPos = s.classify(group[start:i], cmdPos)
		}
	}
}

// openParen handles an unquoted '(' and returns the next index and the command
// position that follows it.
func (s *blockScope) openParen(group string, i int, cmdPos bool) (int, bool) {
	// "name()" / "function name ()" — a function header. The body's '{' that
	// follows is in command position even though a word (the name) came before
	// it. An empty "()" is never a subshell: the shell rejects "( )" as a syntax
	// error, so this check is safe in command position too.
	j := i + 1
	for j < len(group) && (group[j] == ' ' || group[j] == '\t') {
		j++
	}
	if j < len(group) && group[j] == ')' {
		return j + 1, true
	}

	if cmdPos {
		// "((expr))" is an arithmetic command, not a subshell: the two parens
		// are pushed as plain parens so the matching "))" consumes them without
		// ever suppressing rewriting. Bash tells the two apart by adjacency, so
		// "( (a) )" below is still nested subshells.
		if i+1 < len(group) && group[i+1] == '(' {
			s.push(blockParen)
			s.push(blockParen)
			return i + 2, false
		}
		// Every other '(' in command position opens a subshell, whatever
		// follows it. Requiring '(' to stand alone as a word missed
		// "(cd sub && go test ./...; echo x) | wc -l": the subshell spans three
		// groups, the `go test` group has no consumer of its own, and it was
		// wrapped while `wc -l` counted the whole subshell.
		s.push(blockSubshell)
		return i + 1, true
	}

	s.push(blockParen)
	return i + 1, false
}

// classify applies one word to the scope and returns whether the word after it
// is still in command position. Quoting is not stripped, matching the shell: a
// quoted 'for' is not a reserved word, and advance never reaches here for one.
func (s *blockScope) classify(word string, cmdPos bool) bool {
	// `in` closes the subject of a case and opens its first arm pattern. It is
	// read outside command position, which is where the subject leaves it.
	if s.caseHeader && word == "in" {
		if k, ok := s.top(); ok && k == blockCase {
			s.caseHeader = false
			s.casePattern = true
			return false
		}
	}
	if s.casePattern {
		if word == "esac" {
			// The one reserved word that still counts here: bash rejects a bare
			// `esac` as an arm pattern, so this closes the case. Without it the
			// scope would stay open and everything after it lose filtering.
			s.closeKeyword()
			return false
		}
		// Every other word is a pattern, not a command: `done)` opens an arm of
		// the case, it does not close it.
		return false
	}
	if !cmdPos {
		return false
	}
	if s.afterTime {
		// `time [-p] [--] pipeline`: its own options keep command position, so
		// the opener that follows them is still seen.
		if word == "-p" || word == "--" {
			return true
		}
		s.afterTime = false
	}
	if s.funcName {
		// The word after `function` is the name being defined, not a command.
		// The body's '{' comes right after it and must be seen in command
		// position, otherwise `function findall {` opens nothing and the body
		// is rewritten (the ksh/bash spelling of `findall() {`).
		s.funcName = false
		return true
	}
	switch word {
	case "function":
		s.funcName = true
		return true
	case "if":
		s.push(blockCond)
		return true // "if grep -q x f": a command follows
	case "while", "until":
		s.push(blockLoop)
		return true
	case "for", "select":
		s.push(blockLoop)
		return false // a variable name follows, not a command
	case "case":
		s.push(blockCase)
		s.caseHeader = true
		return false
	case "done", "fi", "esac":
		s.closeKeyword()
		return false
	case "time":
		s.afterTime = true
		return true
	case "then", "do", "else", "elif", "!":
		// Reserved words that introduce another command position, so a nested
		// opener on the same line ("then if false") is still seen.
		return true
	}
	return false
}

// skipQuoted returns the index just past the quoted region starting at i.
func skipQuoted(s string, i int) int {
	q := s[i]
	for i++; i < len(s); i++ {
		if s[i] == '\\' && q == '"' && i+1 < len(s) {
			i++
			continue
		}
		if s[i] == q {
			return i + 1
		}
	}
	return i
}

// isTokenBreak reports whether c ends a shell word. It excludes '(', ')', '{'
// and '}' so that a lone one of those is only treated as an operator when it
// stands alone: "((x" and "x/{p,q}" are single words.
func isTokenBreak(c byte) bool {
	switch c {
	case ' ', '\t', '\n', ';', '&', '|', '<', '>':
		return true
	}
	return false
}

// isWordBreak is isTokenBreak plus every byte advance handles itself, so the
// default branch of its scan always consumes at least one byte.
func isWordBreak(c byte) bool {
	switch c {
	case '(', ')', '{', '}', '\'', '"':
		return true
	}
	return isTokenBreak(c)
}

// heredocDelim is a pending heredoc body waiting for its terminator line.
type heredocDelim struct {
	delim     string
	stripTabs bool // <<- form: leading tabs are stripped before comparing
}

// parseHeredocDelim reads a heredoc delimiter starting at i, which is the index
// just past "<<". It returns the delimiter, the index just past it, and whether
// this really is a heredoc.
//
// The delimiter is unquoted the way the shell does ('EOF', "EOF" and \EOF all
// name EOF), because the terminator line is compared against the unquoted form.
func parseHeredocDelim(cmd string, i int) (heredocDelim, int, bool) {
	var d heredocDelim
	if i < len(cmd) && cmd[i] == '-' {
		d.stripTabs = true
		i++
	}
	for i < len(cmd) && (cmd[i] == ' ' || cmd[i] == '\t') {
		i++
	}

	var sb strings.Builder
scan:
	for i < len(cmd) {
		switch ch := cmd[i]; ch {
		case '\'', '"':
			i++
			for i < len(cmd) && cmd[i] != ch {
				sb.WriteByte(cmd[i])
				i++
			}
			if i < len(cmd) {
				i++
			}
		case '\\':
			if i+1 >= len(cmd) {
				break scan
			}
			sb.WriteByte(cmd[i+1])
			i += 2
		case ' ', '\t', '\n', ';', '&', '|', '<', '>', '(', ')':
			break scan
		default:
			sb.WriteByte(ch)
			i++
		}
	}

	d.delim = sb.String()
	if d.delim == "" {
		return heredocDelim{}, i, false
	}
	return d, i, true
}

// drainHeredocs returns the index just past the bodies of every pending heredoc,
// starting at the first byte of the line following the operator. Each body runs
// up to and including a line equal to its delimiter (leading tabs stripped first
// for the <<- form; bash requires an exact match otherwise). A delimiter that
// never appears consumes the rest of the command, which fails safe: the tail is
// emitted verbatim instead of being rewritten.
func drainHeredocs(cmd string, start int, delims []heredocDelim) int {
	pos := start
	for _, d := range delims {
		for pos < len(cmd) {
			line := cmd[pos:]
			next := len(cmd)
			if nl := strings.IndexByte(line, '\n'); nl >= 0 {
				line = line[:nl]
				next = pos + nl + 1
			}
			pos = next
			if d.stripTabs {
				line = strings.TrimLeft(line, "\t")
			}
			if line == d.delim {
				break
			}
		}
	}
	return pos
}

// isWordStart reports whether the byte at i begins a shell word, used to decide
// whether an unquoted '#' opens a comment.
func isWordStart(cmd string, i int) bool {
	if i == 0 {
		return true
	}
	switch cmd[i-1] {
	case ' ', '\t', '\n', ';', '&', '|', '(':
		return true
	}
	return false
}
