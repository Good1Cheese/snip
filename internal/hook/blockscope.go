package hook

import "strings"

// blockScope tracks how deep the segmenter currently sits inside a shell
// compound command: a loop (for/while/until/select), a conditional (if/case), a
// brace group, a function body or a subshell.
//
// The segmenter splits on top-level ';', '&&', '||', '&' and '\n', which means a
// block body becomes an ordinary group. The #111 guard that keeps a producer raw
// when its output feeds a pipe or a redirect only inspects the group it is in,
// so `done | wc -l` and `done > out.txt` — which live in a different group from
// the body — never protected the body. It was wrapped, and the consumer read
// snip's compacted view instead of the real output (issue #133, bug A).
//
// The rule chosen here is deliberately one sentence: nothing inside a block is
// ever rewritten. It is a counter over machinery that already exists, not a
// shell parser, and every misdetection costs filtering rather than correctness —
// an unmatched opener pins the depth above zero and the remainder is simply
// passed through raw. Consumer propagation (resolving which block a trailing
// pipe belongs to and rewriting the bodies that have no consumer) would recover
// more savings, but its failure mode is wrong output, not lost savings, and this
// hook path prefers the opposite trade (see the #127 process-substitution fix).
type blockScope struct {
	depth int
}

// inBlock reports whether the group being flushed sits inside a compound
// command and must therefore be emitted verbatim.
func (s *blockScope) inBlock() bool { return s.depth > 0 }

// advance updates the depth from a group that has just been emitted.
func (s *blockScope) advance(group string) {
	// A block can open behind a pipe ("cat list | while read -r l"), so every
	// top-level pipeline stage is a command position and must be classified.
	rest := group
	for {
		head, tail := splitFirstPipe(rest)
		s.classify(firstToken(head))
		if tail == "" {
			break
		}
		rest = tail[1:]
	}

	// Openers and closers with no reserved word of their own: a function header
	// ("deploy() {") ends with a standalone '{', and a spaced one-line subshell
	// ("( go build ./... )") ends with a standalone ')'. Both are skipped when
	// the whole group is that single character, which firstToken already saw.
	t := strings.TrimSpace(group)
	if len(t) < 2 {
		return
	}
	switch prev, last := t[len(t)-2], t[len(t)-1]; {
	case last == '{' && (prev == ' ' || prev == '\t' || prev == ')'):
		s.depth++
	case last == ')' && (prev == ' ' || prev == '\t'):
		s.close()
	}
}

// classify applies one command-position token to the depth counter. Quoting is
// not stripped, matching the shell: a quoted 'for' is not a reserved word.
func (s *blockScope) classify(tok string) {
	switch tok {
	case "for", "while", "until", "if", "case", "select", "{", "(":
		s.depth++
	case "done", "fi", "esac", "}", ")":
		s.close()
	}
}

func (s *blockScope) close() {
	if s.depth > 0 {
		s.depth--
	}
}

// firstToken returns the first word of a command position: leading blanks are
// skipped, then bytes are read up to the first blank or shell operator. A lone
// '(', ')', '{' or '}' is returned as a single-character token only when it
// stands alone, so "(cd sub" and "arr=(a b)" are not read as subshell openers
// and "mkdir -p x/{p,q}" is not read as a brace group.
func firstToken(stage string) string {
	i := 0
	for i < len(stage) && (stage[i] == ' ' || stage[i] == '\t' || stage[i] == '\n') {
		i++
	}
	if i >= len(stage) {
		return ""
	}
	if c := stage[i]; c == '(' || c == ')' || c == '{' || c == '}' {
		if i+1 >= len(stage) || isTokenBreak(stage[i+1]) {
			return stage[i : i+1]
		}
	}
	start := i
	for i < len(stage) && !isTokenBreak(stage[i]) {
		i++
	}
	return stage[start:i]
}

func isTokenBreak(c byte) bool {
	switch c {
	case ' ', '\t', '\n', ';', '&', '|', '<', '>':
		return true
	}
	return false
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
// An all-digit delimiter is rejected: it never occurs in practice and it is what
// a bare arithmetic command such as "((x = 1 << 2))" would produce, where arming
// a heredoc would swallow the rest of the command and silently disable filtering
// (issue #133).
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
	digitsOnly := true
scan:
	for i < len(cmd) {
		switch ch := cmd[i]; ch {
		case '\'', '"':
			i++
			for i < len(cmd) && cmd[i] != ch {
				if cmd[i] < '0' || cmd[i] > '9' {
					digitsOnly = false
				}
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
			if cmd[i+1] < '0' || cmd[i+1] > '9' {
				digitsOnly = false
			}
			sb.WriteByte(cmd[i+1])
			i += 2
		case ' ', '\t', '\n', ';', '&', '|', '<', '>', '(', ')':
			break scan
		default:
			if ch < '0' || ch > '9' {
				digitsOnly = false
			}
			sb.WriteByte(ch)
			i++
		}
	}

	d.delim = sb.String()
	if d.delim == "" || digitsOnly {
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
