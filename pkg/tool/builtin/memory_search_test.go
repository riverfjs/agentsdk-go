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
	if tool.Name() != "MemorySearch" {
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

func TestSearchChunksWithBleve(t *testing.T) {
	chunks := []MemoryChunk{
		{Path: "memory/a.md", StartLine: 1, EndLine: 3, Snippet: "permissions dsl and sandbox policy"},
		{Path: "memory/b.md", StartLine: 4, EndLine: 6, Snippet: "unrelated weather content"},
	}
	results, err := searchChunksWithBleve(chunks, "permission sandbox", 5)
	if err != nil {
		t.Fatalf("searchChunksWithBleve error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	if results[0].Path != "memory/a.md" {
		t.Fatalf("expected memory/a.md first, got %s", results[0].Path)
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

// TestSearchMemoryChineseQuery verifies that a Chinese-language query can find
// Chinese content in memory files (regression for ASCII-only tokenizer bug).
func TestSearchMemoryChineseQuery(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFile(t, dir, "memory/2026-02-24.md",
		"# 2026-02-24\n用户偏好中文交流\n项目路径是 ~/Documents/chatbot\n已安装 flight-monitor 技能")

	results, err := SearchMemory(dir, "用户偏好", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for Chinese query matching Chinese memory, got none")
	}
	if results[0].Score <= 0 {
		t.Fatalf("expected positive score, got %f", results[0].Score)
	}
}

// TestSearchMemoryChineseQueryNoFalsePositive verifies that a Chinese query does NOT
// match a memory file with unrelated content (old ASCII-only bug caused random injection).
func TestSearchMemoryChineseQueryNoFalsePositive(t *testing.T) {
	dir := t.TempDir()
	// Content contains only ASCII words, no Chinese
	writeMemoryFile(t, dir, "memory/2026-02-24.md",
		"flight-monitor check-all browser automation playwright")

	// Pure Chinese query — the old bug would return score=0 for all chunks
	// (empty tokens), but SearchMemory would still inject if somehow matched.
	// With the fix, empty-token Chinese query should return nothing.
	// NOTE: after CJK fix, "你好" tokenizes to ["你","好"]; those chars
	// don't appear in the ASCII-only content, so score should still be 0.
	results, err := SearchMemory(dir, "你好", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results for unrelated Chinese query, got %d: %+v", len(results), results)
	}
}

// TestSearchMemorySkillsSnippetPollution simulates the bug where the enriched
// prompt (with skill names prepended) was passed to SearchMemory instead of
// the raw user message. A "你好" query enriched with skill names should NOT
// recall flight-monitor memories when the raw query is "你好".
func TestSearchMemorySkillsSnippetPollution(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFile(t, dir, "memory/2026-02-24.md",
		"flight-monitor 每6小时检查价格\nbrowser automation\nflight-search skill")

	// Simulated enriched prompt (what was wrongly passed before the fix)
	enriched := "[Available skills:\n- browser\n- flight-search\n- flight-monitor]\n[Current time: 2026-02-26]\n你好"
	rawUser := "你好"

	enrichedResults, _ := SearchMemory(dir, enriched, 3)
	rawResults, _ := SearchMemory(dir, rawUser, 3)

	// The enriched prompt incorrectly finds flight-monitor content
	if len(enrichedResults) == 0 {
		t.Log("enriched prompt returned no results (unexpected, but acceptable)")
	} else {
		t.Logf("enriched prompt returned %d result(s) — this is the bug we fixed", len(enrichedResults))
	}

	// The raw "你好" should not match flight-monitor content
	if len(rawResults) != 0 {
		t.Errorf("raw '你好' query should not match flight-monitor content, got %d result(s)", len(rawResults))
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

