package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is the repository root relative to this package directory.
const repoRoot = "../.."

// TestWindowsScriptsUseCRLF covers the line endings of the repo's .bat scripts.
// cmd.exe misparses LF-only batch files (e.g. "setlocal" runs as "tlocal"),
// so every script must use CRLF and git must be told to keep it that way.
func TestWindowsScriptsUseCRLF(t *testing.T) {
	scripts, err := filepath.Glob(filepath.Join(repoRoot, "*.bat"))
	if err != nil || len(scripts) == 0 {
		t.Fatalf("expected .bat scripts in the repo root, got %v (err %v)", scripts, err)
	}

	t.Run("GIVEN the Windows batch scripts WHEN read from disk THEN every line ends with CRLF", func(t *testing.T) {
		for _, path := range scripts {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			lines := bytes.Count(data, []byte("\n"))
			crlf := bytes.Count(data, []byte("\r\n"))
			if lines == 0 || crlf != lines {
				t.Errorf("%s: %d of %d lines end with CRLF", filepath.Base(path), crlf, lines)
			}
		}
	})

	t.Run("GIVEN the repo's git attributes WHEN a .bat file is checked out THEN git writes CRLF", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(repoRoot, ".gitattributes"))
		if err != nil {
			t.Fatalf("expected a .gitattributes file: %v", err)
		}
		for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && fields[0] == "*.bat" && strings.Contains(line, "eol=crlf") {
				return
			}
		}
		t.Errorf(".gitattributes has no '*.bat ... eol=crlf' rule")
	})
}
