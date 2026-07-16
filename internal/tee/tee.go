package tee

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Config for tee behavior.
type Config struct {
	Enabled     bool
	Mode        string // "failures", "always", "never"
	MaxFiles    int
	MaxFileSize int64
	Dir         string
	// ProjectMarker, when non-empty, redirects tee files to
	// <markerRoot>/.snip/tee/ where markerRoot is the closest ancestor of
	// the working directory containing the marker (e.g. ".git"). Falls back
	// to Dir when the marker is not found or the project directory is not
	// writable.
	ProjectMarker string
}

// DefaultConfig returns tee defaults.
func DefaultConfig() Config {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return Config{
		Enabled:     true,
		Mode:        "failures",
		MaxFiles:    20,
		MaxFileSize: 1 << 20, // 1MB
		Dir:         filepath.Join(home, ".local", "share", "snip", "tee"),
	}
}

// MaybeSave saves raw output if conditions are met. Returns hint string if saved.
func MaybeSave(raw string, exitCode int, cmd string, cfg Config) string {
	if !cfg.Enabled || cfg.Mode == "never" {
		return ""
	}

	// Check SNIP_TEE env override
	if os.Getenv("SNIP_TEE") == "0" {
		return ""
	}

	shouldSave := cfg.Mode == "always" || (cfg.Mode == "failures" && exitCode != 0)
	if !shouldSave {
		return ""
	}

	// Skip if output is too small
	if len(raw) < 500 {
		return ""
	}

	dir, project := resolveDir(cfg)

	// Truncate if too large (rune-safe)
	data := raw
	if int64(len(data)) > cfg.MaxFileSize {
		runes := []rune(data)
		// Find the rune boundary within MaxFileSize bytes
		byteCount := 0
		for i, r := range runes {
			byteCount += len(string(r))
			if int64(byteCount) > cfg.MaxFileSize {
				data = string(runes[:i])
				break
			}
		}
	}

	// Sanitize command name for filename
	safeName := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, cmd)

	filename := fmt.Sprintf("%d-%s.log", time.Now().Unix(), safeName)

	path, ok := writeTo(dir, project, filename, data)
	if !ok && project {
		// Project dir not writable (read-only checkout, root-owned repo):
		// fall back to the global dir rather than losing the recovery log.
		dir = cfg.Dir
		path, ok = writeTo(dir, false, filename, data)
	}
	if !ok {
		return "" // Silent failure
	}

	// Rotate
	rotateFiles(dir, cfg.MaxFiles)

	return fmt.Sprintf("[full output: %s]", path)
}

// writeTo creates dir and writes the log file into it. For project dirs it
// also drops a .gitignore so agent-driven `git add -A` never commits raw
// command output into the repo.
func writeTo(dir string, project bool, filename, data string) (string, bool) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", false
	}
	if project {
		gi := filepath.Join(dir, ".gitignore")
		if _, err := os.Stat(gi); err != nil {
			_ = os.WriteFile(gi, []byte("*\n"), 0644)
		}
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		return "", false
	}
	return path, true
}

// resolveDir returns the directory tee files are written to, and whether it
// is a project-local directory derived from the marker walk-up.
// Priority: SNIP_TEE_DIR env > project marker walk-up > cfg.Dir.
func resolveDir(cfg Config) (string, bool) {
	if envDir := os.Getenv("SNIP_TEE_DIR"); envDir != "" {
		return envDir, false
	}
	if cfg.ProjectMarker != "" {
		if root := findMarkerRoot(cfg.ProjectMarker); root != "" {
			return filepath.Join(root, ".snip", "tee"), true
		}
	}
	return cfg.Dir, false
}

// findMarkerRoot walks upward from the current working directory looking for
// marker (a file or directory name, e.g. ".git"). Returns the directory
// containing the marker, or "" if none is found up to the filesystem root.
func findMarkerRoot(marker string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // reached root on this platform
		}
	}
}

func rotateFiles(dir string, maxFiles int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	var logFiles []os.DirEntry
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".log") {
			logFiles = append(logFiles, e)
		}
	}

	if len(logFiles) <= maxFiles {
		return
	}

	// Sort by name (timestamp prefix = chronological)
	sort.Slice(logFiles, func(i, j int) bool {
		return logFiles[i].Name() < logFiles[j].Name()
	})

	// Remove oldest
	toRemove := len(logFiles) - maxFiles
	for i := 0; i < toRemove; i++ {
		_ = os.Remove(filepath.Join(dir, logFiles[i].Name()))
	}
}
