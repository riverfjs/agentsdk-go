package toolbuiltin

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// ── metadata ─────────────────────────────────────────────────────────────────

func TestMemoryGetMetadata(t *testing.T) {
	tool := NewMemoryGetTool("/tmp")
	if tool.Name() != "MemoryGet" {
		t.Fatalf("unexpected name %q", tool.Name())
	}
	if tool.Description() == "" || tool.Schema() == nil {
		t.Fatal("metadata should not be empty")
	}
}

// ── nil context / missing params ──────────────────────────────────────────────

func TestMemoryGetNilContext(t *testing.T) {
	tool := NewMemoryGetTool(t.TempDir())
	_, err := tool.Execute(nil, map[string]any{"path": "MEMORY.md"})
	if err == nil {
		t.Fatal("expected error for nil context")
	}
}

func TestMemoryGetMissingPath(t *testing.T) {
	tool := NewMemoryGetTool(t.TempDir())
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("expected path-required error, got %v", err)
	}
}

// ── validateMemoryPath ────────────────────────────────────────────────────────

func TestValidateMemoryPath(t *testing.T) {
	cases := []struct {
		path    string
		wantErr bool
	}{
		{"MEMORY.md", false},
		{"memory/2026-02-24.md", false},
		{"memory/notes.md", false},
		{"../etc/passwd", true},
		{"/absolute/path.md", true},
		{"other/file.md", true},
		{"memory/notes.txt", true}, // not .md
		{"", true},
	}
	for _, tc := range cases {
		err := validateMemoryPath(tc.path)
		if tc.wantErr && err == nil {
			t.Errorf("validateMemoryPath(%q): expected error", tc.path)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("validateMemoryPath(%q): unexpected error: %v", tc.path, err)
		}
	}
}

// ── read MEMORY.md ────────────────────────────────────────────────────────────

func TestMemoryGetReadFullFile(t *testing.T) {
	dir := t.TempDir()
	content := "line1\nline2\nline3"
	writeMemoryFile(t, dir, "MEMORY.md", content)

	tool := NewMemoryGetTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{"path": "MEMORY.md"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, output: %s", res.Output)
	}
	if !strings.Contains(res.Output, "line1") || !strings.Contains(res.Output, "line3") {
		t.Fatalf("expected all lines, got %q", res.Output)
	}
	// Output is number-prefixed
	if !strings.Contains(res.Output, "1\t") {
		t.Fatalf("expected line numbers in output, got %q", res.Output)
	}
}

// ── from / lines slice ────────────────────────────────────────────────────────

func TestMemoryGetLineRange(t *testing.T) {
	dir := t.TempDir()
	lines := "alpha\nbeta\ngamma\ndelta\nepsilon"
	writeMemoryFile(t, dir, "MEMORY.md", lines)

	tool := NewMemoryGetTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{
		"path":  "MEMORY.md",
		"from":  float64(2),
		"lines": float64(2),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(res.Output, "alpha") {
		t.Fatalf("line 1 should not appear, got %q", res.Output)
	}
	if !strings.Contains(res.Output, "beta") || !strings.Contains(res.Output, "gamma") {
		t.Fatalf("expected lines 2-3, got %q", res.Output)
	}
	if strings.Contains(res.Output, "delta") {
		t.Fatalf("line 4 should not appear, got %q", res.Output)
	}
}

// ── from beyond end of file ───────────────────────────────────────────────────

func TestMemoryGetBeyondEOF(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFile(t, dir, "MEMORY.md", "only one line")

	tool := NewMemoryGetTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{
		"path": "MEMORY.md",
		"from": float64(999),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatal("expected success even beyond EOF")
	}
	if !strings.Contains(res.Output, "beyond end") {
		t.Fatalf("expected beyond-EOF message, got %q", res.Output)
	}
}

// ── file not found ────────────────────────────────────────────────────────────

func TestMemoryGetFileNotFound(t *testing.T) {
	tool := NewMemoryGetTool(t.TempDir())
	_, err := tool.Execute(context.Background(), map[string]any{"path": "MEMORY.md"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

// ── read from memory/ directory ───────────────────────────────────────────────

func TestMemoryGetDailyJournal(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFile(t, dir, "memory/2026-02-24.md", "Session notes here.")

	tool := NewMemoryGetTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{
		"path": "memory/2026-02-24.md",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Output, "Session notes") {
		t.Fatalf("expected file content, got %q", res.Output)
	}
}

// ── security: path traversal ──────────────────────────────────────────────────

func TestMemoryGetPathTraversal(t *testing.T) {
	tool := NewMemoryGetTool(t.TempDir())
	cases := []string{
		"../etc/passwd",
		"/etc/hosts",
		"other/secrets.md",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), map[string]any{"path": p})
			if err == nil {
				t.Fatalf("expected security error for %q", p)
			}
		})
	}
}

// ── data fields ───────────────────────────────────────────────────────────────

func TestMemoryGetDataFields(t *testing.T) {
	dir := t.TempDir()
	content := fmt.Sprintf("%s\n%s\n%s", "a", "b", "c")
	writeMemoryFile(t, dir, "MEMORY.md", content)

	tool := NewMemoryGetTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{
		"path":  "MEMORY.md",
		"from":  float64(1),
		"lines": float64(2),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, ok := res.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map data, got %T", res.Data)
	}
	if data["path"] != "MEMORY.md" {
		t.Fatalf("unexpected path in data: %v", data["path"])
	}
	if data["from"].(int) != 1 {
		t.Fatalf("unexpected from in data: %v", data["from"])
	}
	_ = filepath.Join(dir, "MEMORY.md") // just verify dir used
}

