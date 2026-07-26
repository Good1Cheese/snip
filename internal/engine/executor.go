package engine

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// drainGrace bounds how long Execute keeps reading output after the command
// itself has exited. Descendants it left running inherit the pipe write ends,
// so an unbounded drain waits for the longest-lived of them.
//
// A var rather than a const so tests can shrink it and force the boundary
// deterministically instead of waiting out the real window.
var drainGrace = 100 * time.Millisecond

// syncBuffer collects one captured stream. Access is guarded because Execute
// may read the buffer while its reader goroutine is still blocked: a descendant
// of the command can hold the pipe's write end open long after the command
// itself has exited, and closing the read end does not interrupt a blocked read
// on every platform.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Result holds the output of a command execution.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
	// Truncated reports that the drain grace period expired with the pipes
	// still open, so Stdout and Stderr hold only what had been read by then.
	// Without it, a partial capture is indistinguishable from a full one.
	Truncated bool
}

// shellBuiltins lists commands that are shell built-ins and cannot be
// executed directly via exec.Command.
var shellBuiltins = map[string]bool{
	"export":   true,
	"unset":    true,
	"source":   true,
	"alias":    true,
	"unalias":  true,
	"eval":     true,
	"set":      true,
	"shopt":    true,
	"declare":  true,
	"local":    true,
	"readonly": true,
	"typeset":  true,
	"ulimit":   true,
	"umask":    true,
}

// makeCommand creates an exec.Cmd, wrapping shell built-ins with sh -c
// so they can be executed. Shell built-ins like "export" have no binary
// in $PATH and would fail with exec.Command directly.
func makeCommand(command string, args []string) *exec.Cmd {
	if shellBuiltins[command] {
		shPath, err := lookPath("sh")
		if err != nil {
			shPath = "/bin/sh"
		}
		shArgs := make([]string, 0, len(args)+3)
		shArgs = append(shArgs, "-c", command+` "$@"`, "_")
		shArgs = append(shArgs, args...)
		return &exec.Cmd{
			Path: shPath,
			Args: append([]string{"sh"}, shArgs...),
		}
	}
	cmdPath, err := lookPath(command)
	if err != nil {
		cmdPath = command
	}
	return &exec.Cmd{
		Path: cmdPath,
		Args: append([]string{command}, args...),
	}
}

// Execute runs a command, capturing stdout and stderr concurrently via goroutines.
//
// A nil *Result means the command never ran. When the returned *Result is
// non-nil the command did run, even if err is also non-nil: err then reports a
// failure in the wait bookkeeping rather than in the command itself. Callers
// must decide whether re-running is safe from the *Result rather than from err.
func Execute(command string, args []string) (*Result, error) {
	start := time.Now()

	cmd := makeCommand(command, args)
	// Don't connect stdin for captured commands — prevents blocking on
	// commands that don't read stdin (most filtered commands).
	// Passthrough commands still get stdin via the Passthrough function.

	// Own the pipes rather than using cmd.StdoutPipe/StderrPipe, whose contract
	// requires every read to finish before cmd.Wait. Bounding the drain means
	// reaping the child first, which that contract does not allow.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	defer func() { _ = stdoutR.Close() }()
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdoutW.Close()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	defer func() { _ = stderrR.Close() }()
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	if err := cmd.Start(); err != nil {
		_ = stdoutW.Close()
		_ = stderrW.Close()
		return nil, fmt.Errorf("start command: %w", err)
	}
	// The child holds its own copies of the write ends now. Drop ours, or the
	// readers below would never see EOF.
	_ = stdoutW.Close()
	_ = stderrW.Close()

	var stdoutBuf, stderrBuf syncBuffer
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, _ = io.Copy(&stdoutBuf, stdoutR)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&stderrBuf, stderrR)
	}()

	drained := make(chan struct{})
	go func() {
		wg.Wait()
		close(drained)
	}()

	waitErr := cmd.Wait()

	// The command itself has exited. Anything it left running in the background
	// still holds the write ends, so the readers would block for that process's
	// lifetime. Give them a grace period to drain what is already buffered, then
	// close the read ends and take whatever they captured.
	//
	// Giving up here means output may still have been in flight, so record that
	// the capture is partial. The flag is written on this goroutine only, before
	// the Result is built: the reader goroutines never touch it, so no lock is
	// needed for it (unlike the buffers they are still writing to).
	truncated := false
	select {
	case <-drained:
	case <-time.After(drainGrace):
		truncated = true
		_ = stdoutR.Close()
		_ = stderrR.Close()
	}

	result, err := resultFrom(waitErr, stdoutBuf.String(), stderrBuf.String(), time.Since(start))
	// Set here rather than inside resultFrom: whether the drain ran out of time
	// is a property of the capture, not of how cmd.Wait ended, and resultFrom
	// stays a pure function of the wait outcome.
	result.Truncated = truncated
	return result, err
}

// resultFrom turns the error of cmd.Wait plus the captured streams into the
// pair Execute returns. It is a separate function because its middle branch is
// unreachable from a test through a real command: the Go runtime owns SIGCHLD
// and os/exec exposes no seam on the wait syscall.
//
// Every branch returns a non-nil *Result. The command has started by the time
// this is called, so "never ran" (the nil *Result) is not one of the cases.
func resultFrom(waitErr error, stdout, stderr string, d time.Duration) (*Result, error) {
	result := &Result{Stdout: stdout, Stderr: stderr, Duration: d}
	if waitErr == nil {
		return result, nil
	}
	if exitErr, ok := waitErr.(*exec.ExitError); ok {
		// The command failed, which is not an error of Execute.
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	// The command ran to completion; only the wait bookkeeping failed. Report
	// the error, but still return the Result so the caller does not re-run a
	// command whose side effects have already happened. The exit status went
	// missing with the bookkeeping, so it stays 0.
	return result, fmt.Errorf("wait command: %w", waitErr)
}

// Passthrough runs a command with inherited stdio (no capture).
func Passthrough(command string, args []string) (int, error) {
	cmd := makeCommand(command, args)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return 1, fmt.Errorf("passthrough: %w", err)
	}
	return 0, nil
}
