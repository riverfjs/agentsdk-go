package toolbuiltin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func writeSkill(t *testing.T, skillsDir, name, frontmatter string) {
	t.Helper()
	dir := filepath.Join(skillsDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	content := "---\n" + frontmatter + "\n---\n\n# Skill body"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func skillsDir(workspaceDir string) string {
	return filepath.Join(workspaceDir, ".claude", "skills")
}

// ── metadata ─────────────────────────────────────────────────────────────────

func TestListSkillsMetadata(t *testing.T) {
	tool := NewListSkillsTool("/tmp")
	if tool.Name() != "list_skills" {
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

func TestListSkillsNilContext(t *testing.T) {
	tool := NewListSkillsTool(t.TempDir())
	_, err := tool.Execute(nil, map[string]any{})
	if err == nil {
		t.Fatal("expected error for nil context")
	}
}

// ── no skills dir ─────────────────────────────────────────────────────────────

func TestListSkillsNoDir(t *testing.T) {
	tool := NewListSkillsTool(t.TempDir())
	res, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatal("expected success even with no skills dir")
	}
	if !strings.Contains(res.Output, "No skills") {
		t.Fatalf("expected no-skills message, got %q", res.Output)
	}
}

// ── empty skills dir ──────────────────────────────────────────────────────────

func TestListSkillsEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(skillsDir(dir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tool := NewListSkillsTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Output, "No skills") {
		t.Fatalf("expected no-skills message, got %q", res.Output)
	}
}

// ── lists skills with name + description ──────────────────────────────────────

func TestListSkillsListsAll(t *testing.T) {
	dir := t.TempDir()
	sd := skillsDir(dir)

	writeSkill(t, sd, "browser", "name: Browser\ndescription: Web automation using Playwright.")
	writeSkill(t, sd, "todo", "name: Todo\ndescription: Manage tasks and reminders.")

	tool := NewListSkillsTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, output: %s", res.Output)
	}
	if !strings.Contains(res.Output, "Browser") {
		t.Fatalf("expected Browser skill, got %q", res.Output)
	}
	if !strings.Contains(res.Output, "Todo") {
		t.Fatalf("expected Todo skill, got %q", res.Output)
	}
	if !strings.Contains(res.Output, "2 installed skill") {
		t.Fatalf("expected skill count, got %q", res.Output)
	}
}

// ── sorted alphabetically ─────────────────────────────────────────────────────

func TestListSkillsSorted(t *testing.T) {
	dir := t.TempDir()
	sd := skillsDir(dir)

	writeSkill(t, sd, "zz-last", "name: Zz\ndescription: Last.")
	writeSkill(t, sd, "aa-first", "name: Aa\ndescription: First.")

	tool := NewListSkillsTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	aaIdx := strings.Index(res.Output, "Aa")
	zzIdx := strings.Index(res.Output, "Zz")
	if aaIdx < 0 || zzIdx < 0 {
		t.Fatalf("both skills should appear, got %q", res.Output)
	}
	if aaIdx > zzIdx {
		t.Fatalf("skills should be sorted alphabetically; Aa should come before Zz")
	}
}

// ── filter by name ────────────────────────────────────────────────────────────

func TestListSkillsFilter(t *testing.T) {
	dir := t.TempDir()
	sd := skillsDir(dir)

	writeSkill(t, sd, "browser", "name: Browser\ndescription: Web automation.")
	writeSkill(t, sd, "todo", "name: Todo\ndescription: Task management.")

	tool := NewListSkillsTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{"filter": "brow"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Output, "Browser") {
		t.Fatalf("expected Browser in filtered output, got %q", res.Output)
	}
	if strings.Contains(res.Output, "Todo") {
		t.Fatalf("Todo should be filtered out, got %q", res.Output)
	}
}

// ── filter case-insensitive ───────────────────────────────────────────────────

func TestListSkillsFilterCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	sd := skillsDir(dir)
	writeSkill(t, sd, "browser", "name: Browser\ndescription: Web automation.")

	tool := NewListSkillsTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{"filter": "BROWSER"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Output, "Browser") {
		t.Fatalf("case-insensitive filter should match, got %q", res.Output)
	}
}

// ── filter with no match ──────────────────────────────────────────────────────

func TestListSkillsFilterNoMatch(t *testing.T) {
	dir := t.TempDir()
	sd := skillsDir(dir)
	writeSkill(t, sd, "browser", "name: Browser\ndescription: Web.")

	tool := NewListSkillsTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{"filter": "xyz-nonexistent"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Output, "No skills") {
		t.Fatalf("expected no-skills message when filter matches nothing, got %q", res.Output)
	}
}

// ── skill without SKILL.md is skipped ────────────────────────────────────────

func TestListSkillsSkipsInvalidDirs(t *testing.T) {
	dir := t.TempDir()
	sd := skillsDir(dir)

	// Valid skill
	writeSkill(t, sd, "browser", "name: Browser\ndescription: Web.")

	// Directory with no SKILL.md
	if err := os.MkdirAll(filepath.Join(sd, "broken"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Non-directory file in skills dir (should be ignored)
	if err := os.WriteFile(filepath.Join(sd, "README.md"), []byte("readme"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := NewListSkillsTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Output, "1 installed skill") {
		t.Fatalf("expected exactly 1 valid skill, got %q", res.Output)
	}
}

// ── data field ────────────────────────────────────────────────────────────────

func TestListSkillsDataField(t *testing.T) {
	dir := t.TempDir()
	sd := skillsDir(dir)
	writeSkill(t, sd, "browser", "name: Browser\ndescription: Web automation.")

	tool := NewListSkillsTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, ok := res.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map data, got %T", res.Data)
	}
	skills, ok := data["skills"].([]interface{})
	if !ok {
		t.Fatalf("expected skills slice, got %T", data["skills"])
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill in data, got %d", len(skills))
	}
}

// ── readSkillFrontmatter ──────────────────────────────────────────────────────

func TestReadSkillFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")

	content := "---\nname: My Skill\ndescription: Does something useful.\n---\n\n# Body"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	entry, err := readSkillFrontmatter(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Name != "My Skill" {
		t.Fatalf("expected name 'My Skill', got %q", entry.Name)
	}
	if entry.Description != "Does something useful." {
		t.Fatalf("unexpected description: %q", entry.Description)
	}
}

func TestReadSkillFrontmatterMissing(t *testing.T) {
	_, err := readSkillFrontmatter("/nonexistent/SKILL.md")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadSkillFrontmatterNoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("# Just a heading\nNo frontmatter."), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := readSkillFrontmatter(path)
	if err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

