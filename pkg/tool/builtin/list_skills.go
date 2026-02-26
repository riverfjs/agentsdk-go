package toolbuiltin

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/riverfjs/agentsdk-go/pkg/tool"
	"gopkg.in/yaml.v3"
)

const listSkillsDescription = `List all installed skills available in the workspace.

Returns the name and description of every skill found under
<workspace>/.claude/skills/*/SKILL.md.

Use this tool to discover what skills are available before calling the Skill tool.
`

var listSkillsSchema = &tool.JSONSchema{
	Type: "object",
	Properties: map[string]interface{}{
		"filter": map[string]interface{}{
			"type":        "string",
			"description": "Optional. Filter skills by name (case-insensitive substring match). Omit to list all skills.",
		},
	},
}

// listSkillEntry is the minimal frontmatter we need for listing.
type listSkillEntry struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// ListSkillsTool scans workspace/.claude/skills/ and returns all SKILL.md metadata.
type ListSkillsTool struct {
	workspaceDir string
}

// NewListSkillsTool creates a ListSkillsTool rooted at workspaceDir.
func NewListSkillsTool(workspaceDir string) *ListSkillsTool {
	return &ListSkillsTool{workspaceDir: workspaceDir}
}

func (t *ListSkillsTool) Name() string        { return "list_skills" }
func (t *ListSkillsTool) Description() string { return listSkillsDescription }
func (t *ListSkillsTool) Schema() *tool.JSONSchema { return listSkillsSchema }

func (t *ListSkillsTool) Execute(ctx context.Context, params map[string]interface{}) (*tool.ToolResult, error) {
	filter, _ := params["filter"].(string)
	if ctx == nil {
		return nil, fmt.Errorf("context is nil")
	}

	skillsDir := filepath.Join(t.workspaceDir, ".claude", "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return &tool.ToolResult{
				Success: true,
				Output:  "No skills directory found. Create skills under <workspace>/.claude/skills/.",
				Data:    map[string]interface{}{"skills": []interface{}{}},
			}, nil
		}
		return nil, fmt.Errorf("list_skills: read dir: %w", err)
	}

	type skillInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Path        string `json:"path"`
	}

	var skills []skillInfo

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(skillsDir, entry.Name(), "SKILL.md")
		meta, err := readSkillFrontmatter(skillPath)
		if err != nil {
			// Skip directories without a valid SKILL.md
			continue
		}
		name := strings.TrimSpace(meta.Name)
		if name == "" {
			name = entry.Name()
		}
		skills = append(skills, skillInfo{
			Name:        name,
			Description: strings.TrimSpace(meta.Description),
			Path:        filepath.Join(".claude", "skills", entry.Name(), "SKILL.md"),
		})
	}

	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Name < skills[j].Name
	})

	// Apply optional filter
	if filter != "" {
		filterLower := strings.ToLower(filter)
		var filtered []skillInfo
		for _, s := range skills {
			if strings.Contains(strings.ToLower(s.Name), filterLower) {
				filtered = append(filtered, s)
			}
		}
		skills = filtered
	}

	if len(skills) == 0 {
		return &tool.ToolResult{
			Success: true,
			Output:  "No skills installed. Use the skill-creator skill to create new skills.",
			Data:    map[string]interface{}{"skills": []interface{}{}},
		}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d installed skill(s):\n\n", len(skills)))
	for _, s := range skills {
		sb.WriteString(fmt.Sprintf("**%s**", s.Name))
		if s.Description != "" {
			sb.WriteString(fmt.Sprintf(" — %s", s.Description))
		}
		sb.WriteByte('\n')
	}

	// Build data slice
	dataSkills := make([]interface{}, len(skills))
	for i, s := range skills {
		dataSkills[i] = map[string]interface{}{
			"name":        s.Name,
			"description": s.Description,
			"path":        s.Path,
		}
	}

	return &tool.ToolResult{
		Success: true,
		Output:  strings.TrimSpace(sb.String()),
		Data:    map[string]interface{}{"skills": dataSkills},
	}, nil
}

// SkillSummary holds the minimal metadata needed to describe a skill.
type SkillSummary struct {
	Name        string
	Description string
}

// ScanSkillsList reads {workspaceDir}/.claude/skills/*/SKILL.md and returns
// name+description for every valid skill, sorted by name.
// It is exported so that other packages (e.g. api) can inject the list into
// prompts without duplicating frontmatter-parsing logic.
func ScanSkillsList(workspaceDir string) []SkillSummary {
	skillsDir := filepath.Join(workspaceDir, ".claude", "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil
	}
	var out []SkillSummary
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		meta, err := readSkillFrontmatter(filepath.Join(skillsDir, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		name := strings.TrimSpace(meta.Name)
		if name == "" {
			name = e.Name()
		}
		out = append(out, SkillSummary{
			Name:        name,
			Description: strings.TrimSpace(meta.Description),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// readSkillFrontmatter parses only the YAML frontmatter block from a SKILL.md file.
func readSkillFrontmatter(path string) (listSkillEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return listSkillEntry{}, fs.ErrNotExist
		}
		return listSkillEntry{}, err
	}

	content := string(data)

	// SKILL.md starts with --- frontmatter ---
	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return listSkillEntry{}, fmt.Errorf("no frontmatter found")
	}

	// Skip the opening ---
	idx := strings.Index(content, "---")
	if idx < 0 {
		return listSkillEntry{}, fmt.Errorf("no opening --- found")
	}
	after := content[idx+3:]
	end := strings.Index(after, "---")
	if end < 0 {
		return listSkillEntry{}, fmt.Errorf("no closing --- found")
	}

	yamlBlock := after[:end]
	var entry listSkillEntry
	if err := yaml.Unmarshal([]byte(yamlBlock), &entry); err != nil {
		return listSkillEntry{}, fmt.Errorf("parse frontmatter: %w", err)
	}
	return entry, nil
}

