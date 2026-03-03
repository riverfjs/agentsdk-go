package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildSystemContextSnippetBasicFields(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 2, 12, 30, 0, 0, time.FixedZone("HKT", 8*3600))
	got := buildSystemContextSnippet("", now, "")
	if !strings.Contains(got, "<current_time>2026-03-02 12:30 HKT</current_time>") {
		t.Fatalf("missing current_time tag: %q", got)
	}
	if strings.Contains(got, "<current_model>") {
		t.Fatalf("should not include current_model when unset: %q", got)
	}
}

func TestBuildSystemContextSnippetIncludesSkills(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, ".claude", "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	content := "---\nname: demo\ndescription: demo desc\n---\n# body"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	got := buildSystemContextSnippet(root, time.Date(2026, 3, 2, 12, 31, 0, 0, time.UTC), "")
	if !strings.Contains(got, "<available_skills>") {
		t.Fatalf("missing available_skills tag: %q", got)
	}
	if !strings.Contains(got, "<name>demo</name>") {
		t.Fatalf("missing skill name tag: %q", got)
	}
	if !strings.Contains(got, "<description>demo desc</description>") {
		t.Fatalf("missing skill description tag: %q", got)
	}
	if !strings.Contains(got, "<location>") || !strings.Contains(got, "SKILL.md") {
		t.Fatalf("missing skill location tag: %q", got)
	}
}

func TestBuildSkillsSnippetReturnsStructuredTags(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, ".claude", "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	content := "---\nname: demo\ndescription: demo desc\n---\n# body"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	got := buildSkillsSnippet(root)
	if !strings.Contains(got, "<available_skills>") || !strings.Contains(got, "<skill>") {
		t.Fatalf("expected structured skills tags, got %q", got)
	}
}

func TestBuildSystemContextSnippetIncludesPrimaryModel(t *testing.T) {
	t.Parallel()
	got := buildSystemContextSnippet("", time.Date(2026, 3, 2, 12, 31, 0, 0, time.UTC), "anthropic/claude-opus-4.6")
	want := "<current_model>anthropic/claude-opus-4.6</current_model>"
	if !strings.Contains(got, want) {
		t.Fatalf("missing current_model tag: %q", got)
	}
}

func TestAppendPlainTextForTTSRule(t *testing.T) {
	t.Parallel()
	got := appendPlainTextForTTSRule("base prompt")
	if !strings.Contains(got, "base prompt") {
		t.Fatalf("missing base prompt: %q", got)
	}
	const rule = "Please reply in plain readable text for TTS, do not use Markdown/code blocks/tables/emoji."
	if !strings.Contains(got, rule) {
		t.Fatalf("missing plain text tts rule: %q", got)
	}
	again := appendPlainTextForTTSRule(got)
	if strings.Count(again, rule) != 1 {
		t.Fatalf("rule should be appended once: %q", again)
	}
}

