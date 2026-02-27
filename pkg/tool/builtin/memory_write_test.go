package toolbuiltin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryWriteMetadata(t *testing.T) {
	tool := NewMemoryWriteTool("/tmp")
	if tool.Name() != "MemoryWrite" {
		t.Fatalf("unexpected name %q", tool.Name())
	}
	if tool.Description() == "" || tool.Schema() == nil {
		t.Fatal("metadata should not be empty")
	}
}

func TestMemoryWriteNilContext(t *testing.T) {
	tool := NewMemoryWriteTool(t.TempDir())
	_, err := tool.Execute(nil, map[string]any{"path": "MEMORY.md", "content": "x"})
	if err == nil {
		t.Fatal("expected error for nil context")
	}
}

func TestMemoryWriteMissingPathWhenTargetPath(t *testing.T) {
	tool := NewMemoryWriteTool(t.TempDir())
	_, err := tool.Execute(context.Background(), map[string]any{
		"target":  "path",
		"content": "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("expected path-required error, got %v", err)
	}
}

func TestMemoryWriteMissingContentForAppend(t *testing.T) {
	tool := NewMemoryWriteTool(t.TempDir())
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":      "MEMORY.md",
		"operation": "append",
	})
	if err == nil || !strings.Contains(err.Error(), "content") {
		t.Fatalf("expected content-required error, got %v", err)
	}
}

func TestMemoryWriteTargetToday(t *testing.T) {
	dir := t.TempDir()
	tool := NewMemoryWriteTool(dir)
	_, err := tool.Execute(context.Background(), map[string]any{"target": "today", "content": "today note"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	today := time.Now().Format("2006-01-02")
	got, err := os.ReadFile(filepath.Join(dir, "memory", today+".md"))
	if err != nil {
		t.Fatalf("today file should exist: %v", err)
	}
	if !strings.Contains(string(got), "today note") {
		t.Fatalf("unexpected content: %q", string(got))
	}
}

func TestMemoryWriteTargetProjectsAndLessons(t *testing.T) {
	dir := t.TempDir()
	tool := NewMemoryWriteTool(dir)
	_, err := tool.Execute(context.Background(), map[string]any{"target": "projects", "content": "p1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err = tool.Execute(context.Background(), map[string]any{"target": "lessons", "content": "l1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	projects, _ := os.ReadFile(filepath.Join(dir, "memory", "projects.md"))
	lessons, _ := os.ReadFile(filepath.Join(dir, "memory", "lessons.md"))
	if !strings.Contains(string(projects), "p1") {
		t.Fatalf("projects not written: %q", string(projects))
	}
	if !strings.Contains(string(lessons), "l1") {
		t.Fatalf("lessons not written: %q", string(lessons))
	}
}

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

func TestMemoryWriteReplace(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFile(t, dir, "memory/projects.md", "alpha\nbeta\n")
	tool := NewMemoryWriteTool(dir)
	_, err := tool.Execute(context.Background(), map[string]any{
		"target":     "projects",
		"operation":  "replace",
		"old_string": "beta",
		"new_string": "gamma",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "memory", "projects.md"))
	if !strings.Contains(string(got), "gamma") || strings.Contains(string(got), "beta") {
		t.Fatalf("replace failed: %q", string(got))
	}
}

func TestMemoryWriteInsertAfter(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFile(t, dir, "memory/lessons.md", "A\nB\n")
	tool := NewMemoryWriteTool(dir)
	_, err := tool.Execute(context.Background(), map[string]any{
		"target":    "lessons",
		"operation": "insert_after",
		"anchor":    "A",
		"content":   "\nX",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "memory", "lessons.md"))
	if !strings.Contains(string(got), "A\nX\nB") {
		t.Fatalf("insert_after failed: %q", string(got))
	}
}

func TestMemoryWriteInsertBefore(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFile(t, dir, "memory/lessons.md", "A\nB\n")
	tool := NewMemoryWriteTool(dir)
	_, err := tool.Execute(context.Background(), map[string]any{
		"target":    "lessons",
		"operation": "insert_before",
		"anchor":    "B",
		"content":   "Y\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "memory", "lessons.md"))
	if !strings.Contains(string(got), "A\nY\nB") {
		t.Fatalf("insert_before failed: %q", string(got))
	}
}

func TestMemoryWriteTodayShorthandCompat(t *testing.T) {
	dir := t.TempDir()
	tool := NewMemoryWriteTool(dir)
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":    "today",
		"content": "compat today",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	today := time.Now().Format("2006-01-02")
	got, _ := os.ReadFile(filepath.Join(dir, "memory", today+".md"))
	if !strings.Contains(string(got), "compat today") {
		t.Fatalf("today shorthand failed: %q", string(got))
	}
}

func TestMemoryWritePathTraversal(t *testing.T) {
	tool := NewMemoryWriteTool(t.TempDir())
	cases := []string{"../etc/passwd", "/etc/hosts", "other/secrets.md"}
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

func TestMemoryWriteErrorsOnMissingAnchorAndOldString(t *testing.T) {
	dir := t.TempDir()
	tool := NewMemoryWriteTool(dir)

	_, err := tool.Execute(context.Background(), map[string]any{
		"target":    "projects",
		"operation": "insert_after",
		"content":   "x",
	})
	if err == nil || !strings.Contains(err.Error(), "anchor") {
		t.Fatalf("expected anchor error, got %v", err)
	}
	_, err = tool.Execute(context.Background(), map[string]any{
		"target":    "projects",
		"operation": "replace",
		"new_string": "x",
	})
	if err == nil || !strings.Contains(err.Error(), "old_string") {
		t.Fatalf("expected old_string error, got %v", err)
	}
}

func TestMemoryWriteUnsupportedOperation(t *testing.T) {
	tool := NewMemoryWriteTool(t.TempDir())
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":      "MEMORY.md",
		"operation": "bad_op",
		"content":   "x",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported operation") {
		t.Fatalf("expected unsupported operation error, got %v", err)
	}
}

func TestMemoryWriteDataFields(t *testing.T) {
	dir := t.TempDir()
	tool := NewMemoryWriteTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{
		"path":      "MEMORY.md",
		"content":   "test data",
		"operation": "overwrite",
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
	if data["operation"] != "overwrite" {
		t.Fatalf("unexpected operation: %v", data["operation"])
	}
	if _, ok := data["bytes"]; !ok {
		t.Fatal("expected bytes field in data")
	}
}

