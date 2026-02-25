package toolbuiltin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func writeMemoryFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// ── metadata ─────────────────────────────────────────────────────────────────

func TestMemorySearchMetadata(t *testing.T) {
	tool := NewMemorySearchTool("/tmp")
	if tool.Name() != "memory_search" {
		t.Fatalf("unexpected name %q", tool.Name())
	}
	if tool.Description() == "" {
		t.Fatal("description should not be empty")
	}
	if tool.Schema() == nil {
		t.Fatal("schema should not be nil")
	}
}

// ── nil context ───────────────────────────────────────────────────────────────

func TestMemorySearchNilContext(t *testing.T) {
	tool := NewMemorySearchTool(t.TempDir())
	_, err := tool.Execute(nil, map[string]any{"query": "test"})
	if err == nil {
		t.Fatal("expected error for nil context")
	}
}

// ── missing query ─────────────────────────────────────────────────────────────

func TestMemorySearchMissingQuery(t *testing.T) {
	tool := NewMemorySearchTool(t.TempDir())
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "query") {
		t.Fatalf("expected query-required error, got %v", err)
	}
}

// ── no memory files ───────────────────────────────────────────────────────────

func TestMemorySearchNoFiles(t *testing.T) {
	tool := NewMemorySearchTool(t.TempDir())
	res, err := tool.Execute(context.Background(), map[string]any{"query": "anything"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatal("expected success even with no files")
	}
	if !strings.Contains(res.Output, "No memory files found") {
		t.Fatalf("expected no-files message, got %q", res.Output)
	}
}

// ── basic search: MEMORY.md ───────────────────────────────────────────────────

func TestMemorySearchFindsInMemoryMD(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFile(t, dir, "MEMORY.md", "## Projects\nUser's main project is myclaw.\nPrefers Go over Python.")

	tool := NewMemorySearchTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{"query": "myclaw project"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, output: %s", res.Output)
	}
	if !strings.Contains(res.Output, "MEMORY.md") {
		t.Fatalf("expected result from MEMORY.md, got %q", res.Output)
	}
}

// ── search in memory/ directory ───────────────────────────────────────────────

func TestMemorySearchFindsInMemoryDir(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFile(t, dir, "memory/2026-02-24.md", "Discussed cron architecture with user.\nDecided to keep cron in gateway.")

	tool := NewMemorySearchTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{"query": "cron gateway"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Output, "2026-02-24") {
		t.Fatalf("expected result from daily file, got %q", res.Output)
	}
}

// ── no results for irrelevant query ───────────────────────────────────────────

func TestMemorySearchNoResults(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFile(t, dir, "MEMORY.md", "User likes Go programming.")

	tool := NewMemorySearchTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{"query": "quantum physics"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatal("expected success even with no match")
	}
	if !strings.Contains(res.Output, "No results found") {
		t.Fatalf("expected no-results message, got %q", res.Output)
	}
}

// ── max_results respected ─────────────────────────────────────────────────────

func TestMemorySearchMaxResults(t *testing.T) {
	dir := t.TempDir()
	// Write a large file that will generate multiple chunks
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("golang go language programming\n")
	}
	writeMemoryFile(t, dir, "MEMORY.md", sb.String())

	tool := NewMemorySearchTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{
		"query":       "golang programming",
		"max_results": float64(2),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Count result markers "[1]", "[2]", "[3]"…
	count := strings.Count(res.Output, "[")
	if count > 2 {
		t.Fatalf("expected at most 2 results, got %d", count)
	}
}

// ── tokenize ──────────────────────────────────────────────────────────────────

func TestTokenize(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"hello world", []string{"hello", "world"}},
		{"Go-lang", []string{"go-lang"}},
		{"foo123 bar", []string{"foo123", "bar"}},
		{"  spaces  ", []string{"spaces"}},
		{"", nil},
		{"!@#$%", nil},
	}
	for _, tc := range cases {
		got := tokenize(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("tokenize(%q): got %v, want %v", tc.input, got, tc.want)
			continue
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("tokenize(%q)[%d]: got %q, want %q", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}

// ── scoreChunk ────────────────────────────────────────────────────────────────

func TestScoreChunk(t *testing.T) {
	tokens := tokenize("myclaw memory")

	// chunk containing the terms should score > 0
	score := scoreChunk("myclaw is a memory management system", tokens)
	if score <= 0 {
		t.Fatalf("expected positive score, got %f", score)
	}

	// unrelated chunk should score 0
	zero := scoreChunk("completely unrelated content here", tokens)
	if zero != 0 {
		t.Fatalf("expected zero score for unrelated chunk, got %f", zero)
	}

	// empty tokens → zero
	if s := scoreChunk("myclaw memory", nil); s != 0 {
		t.Fatalf("expected 0 for empty tokens, got %f", s)
	}
}

// ── SearchMemory (exported, used by auto-recall) ──────────────────────────────

func TestSearchMemoryExported(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFile(t, dir, "MEMORY.md", "User's main workspace is ~/.myclaw/workspace")

	results, err := SearchMemory(dir, "workspace myclaw", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	if results[0].Score <= 0 {
		t.Fatalf("expected positive score, got %f", results[0].Score)
	}
}

func TestSearchMemoryEmptyQuery(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFile(t, dir, "MEMORY.md", "some content")

	results, err := SearchMemory(dir, "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Fatal("expected nil for empty query")
	}
}

func TestSearchMemoryNoFiles(t *testing.T) {
	results, err := SearchMemory(t.TempDir(), "anything", 3)
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Fatal("expected nil when no memory files exist")
	}
}

// ── collectMemoryFiles ────────────────────────────────────────────────────────

func TestCollectMemoryFiles(t *testing.T) {
	dir := t.TempDir()

	// Only MEMORY.md initially
	writeMemoryFile(t, dir, "MEMORY.md", "long-term")
	files, err := collectMemoryFiles(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	// Add daily journal
	writeMemoryFile(t, dir, "memory/2026-02-24.md", "daily")
	files, err = collectMemoryFiles(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(files), files)
	}

	// Non-.md files are ignored
	writeMemoryFile(t, dir, "memory/notes.txt", "ignored")
	files, err = collectMemoryFiles(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("txt file should be ignored, got %d files", len(files))
	}
}

