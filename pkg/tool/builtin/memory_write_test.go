package toolbuiltin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── metadata ─────────────────────────────────────────────────────────────────

func TestMemoryWriteMetadata(t *testing.T) {
	tool := NewMemoryWriteTool("/tmp")
	if tool.Name() != "memory_write" {
		t.Fatalf("unexpected name %q", tool.Name())
	}
	if tool.Description() == "" || tool.Schema() == nil {
		t.Fatal("metadata should not be empty")
	}
}

// ── nil context / missing params ──────────────────────────────────────────────

func TestMemoryWriteNilContext(t *testing.T) {
	tool := NewMemoryWriteTool(t.TempDir())
	_, err := tool.Execute(nil, map[string]any{"path": "MEMORY.md", "content": "x"})
	if err == nil {
		t.Fatal("expected error for nil context")
	}
}

func TestMemoryWriteMissingPath(t *testing.T) {
	tool := NewMemoryWriteTool(t.TempDir())
	_, err := tool.Execute(context.Background(), map[string]any{"content": "hello"})
	if err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("expected path-required error, got %v", err)
	}
}

func TestMemoryWriteMissingContent(t *testing.T) {
	tool := NewMemoryWriteTool(t.TempDir())
	_, err := tool.Execute(context.Background(), map[string]any{"path": "MEMORY.md"})
	if err == nil || !strings.Contains(err.Error(), "content") {
		t.Fatalf("expected content-required error, got %v", err)
	}
}

// ── overwrite mode ────────────────────────────────────────────────────────────

func TestMemoryWriteOverwrite(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFile(t, dir, "MEMORY.md", "old content")

	tool := NewMemoryWriteTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{
		"path":    "MEMORY.md",
		"content": "new content",
		"mode":    "overwrite",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, output: %s", res.Output)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if string(got) != "new content" {
		t.Fatalf("expected overwritten content, got %q", string(got))
	}
	if !strings.Contains(res.Output, "overwrite") {
		t.Fatalf("expected 'overwrite' in output, got %q", res.Output)
	}
}

// ── append mode (default) ─────────────────────────────────────────────────────

func TestMemoryWriteAppend(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFile(t, dir, "MEMORY.md", "first line")

	tool := NewMemoryWriteTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{
		"path":    "MEMORY.md",
		"content": "second line",
		"mode":    "append",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, output: %s", res.Output)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if !strings.Contains(string(got), "first line") {
		t.Fatalf("original content should remain, got %q", string(got))
	}
	if !strings.Contains(string(got), "second line") {
		t.Fatalf("appended content should appear, got %q", string(got))
	}
}

// ── default mode is append ────────────────────────────────────────────────────

func TestMemoryWriteDefaultModeIsAppend(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFile(t, dir, "MEMORY.md", "existing")

	tool := NewMemoryWriteTool(dir)
	// No mode param → should default to append
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":    "MEMORY.md",
		"content": "appended",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if !strings.Contains(string(got), "existing") || !strings.Contains(string(got), "appended") {
		t.Fatalf("expected both original and appended content, got %q", string(got))
	}
}

// ── creates file if not exists ────────────────────────────────────────────────

func TestMemoryWriteCreatesFile(t *testing.T) {
	dir := t.TempDir()
	tool := NewMemoryWriteTool(dir)

	_, err := tool.Execute(context.Background(), map[string]any{
		"path":    "MEMORY.md",
		"content": "brand new",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err2 := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if err2 != nil {
		t.Fatalf("file should have been created: %v", err2)
	}
	if !strings.Contains(string(got), "brand new") {
		t.Fatalf("expected content, got %q", string(got))
	}
}

// ── creates memory/ subdirectory ──────────────────────────────────────────────

func TestMemoryWriteCreatesDailyDir(t *testing.T) {
	dir := t.TempDir()
	tool := NewMemoryWriteTool(dir)

	_, err := tool.Execute(context.Background(), map[string]any{
		"path":    "memory/2026-02-24.md",
		"content": "daily note",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err2 := os.ReadFile(filepath.Join(dir, "memory/2026-02-24.md"))
	if err2 != nil {
		t.Fatalf("daily file should have been created: %v", err2)
	}
	if !strings.Contains(string(got), "daily note") {
		t.Fatalf("expected content, got %q", string(got))
	}
}

// ── "today" shorthand ─────────────────────────────────────────────────────────

func TestMemoryWriteTodayShorthand(t *testing.T) {
	dir := t.TempDir()
	tool := NewMemoryWriteTool(dir)

	_, err := tool.Execute(context.Background(), map[string]any{
		"path":    "today",
		"content": "today's note",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// File should be at memory/YYYY-MM-DD.md
	today := time.Now().Format("2006-01-02")
	expectedPath := filepath.Join(dir, "memory", today+".md")
	got, err2 := os.ReadFile(expectedPath)
	if err2 != nil {
		t.Fatalf("today file not found at %s: %v", expectedPath, err2)
	}
	if !strings.Contains(string(got), "today's note") {
		t.Fatalf("expected content, got %q", string(got))
	}
}

// ── security: path traversal ──────────────────────────────────────────────────

func TestMemoryWritePathTraversal(t *testing.T) {
	tool := NewMemoryWriteTool(t.TempDir())
	cases := []string{
		"../etc/passwd",
		"/etc/hosts",
		"other/secrets.md",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), map[string]any{
				"path":    p,
				"content": "malicious",
			})
			if err == nil {
				t.Fatalf("expected security error for %q", p)
			}
		})
	}
}

// ── newline auto-padding ──────────────────────────────────────────────────────

func TestMemoryWriteNewlinePadding(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFile(t, dir, "MEMORY.md", "first")

	tool := NewMemoryWriteTool(dir)
	// Append without leading newline — should auto-add separator
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":    "MEMORY.md",
		"content": "second",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	// "first" and "second" should be on separate lines
	if !strings.Contains(string(got), "first\n") {
		t.Fatalf("expected newline between entries, got %q", string(got))
	}
}

// ── result data fields ────────────────────────────────────────────────────────

func TestMemoryWriteDataFields(t *testing.T) {
	dir := t.TempDir()
	tool := NewMemoryWriteTool(dir)

	res, err := tool.Execute(context.Background(), map[string]any{
		"path":    "MEMORY.md",
		"content": "test data",
		"mode":    "overwrite",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, ok := res.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map data, got %T", res.Data)
	}
	if data["path"] != "MEMORY.md" {
		t.Fatalf("unexpected path: %v", data["path"])
	}
	if data["mode"] != "overwrite" {
		t.Fatalf("unexpected mode: %v", data["mode"])
	}
	if _, ok := data["bytes"]; !ok {
		t.Fatal("expected bytes field in data")
	}
}

