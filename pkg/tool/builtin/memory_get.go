package toolbuiltin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/riverfjs/agentsdk-go/pkg/tool"
)

const memoryGetDescription = `Read specific lines from a memory file (MEMORY.md or memory/*.md).

Usage:
- Use after memory_search to pull only the needed lines and keep context small.
- path: workspace-relative path, e.g. "MEMORY.md" or "memory/2026-02-24.md"
- from: starting line number (1-based, optional, defaults to 1)
- lines: number of lines to read (optional, defaults to all remaining lines)
- Paths outside MEMORY.md and memory/ are rejected for security.`

var memoryGetSchema = &tool.JSONSchema{
	Type: "object",
	Properties: map[string]interface{}{
		"path": map[string]interface{}{
			"type":        "string",
			"description": "Workspace-relative path to the memory file, e.g. \"MEMORY.md\" or \"memory/2026-02-24.md\"",
		},
		"from": map[string]interface{}{
			"type":        "number",
			"description": "Starting line number (1-based). Defaults to 1.",
		},
		"lines": map[string]interface{}{
			"type":        "number",
			"description": "Number of lines to read. Defaults to all remaining lines.",
		},
	},
	Required: []string{"path"},
}

// MemoryGetTool reads specific lines from memory files.
type MemoryGetTool struct {
	workspaceDir string
}

// NewMemoryGetTool creates a MemoryGetTool rooted at workspaceDir.
func NewMemoryGetTool(workspaceDir string) *MemoryGetTool {
	return &MemoryGetTool{workspaceDir: workspaceDir}
}

func (t *MemoryGetTool) Name() string             { return "memory_get" }
func (t *MemoryGetTool) Description() string      { return memoryGetDescription }
func (t *MemoryGetTool) Schema() *tool.JSONSchema { return memoryGetSchema }

func (t *MemoryGetTool) Execute(ctx context.Context, params map[string]interface{}) (*tool.ToolResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is nil")
	}
	relPath, err := requireString(params, "path")
	if err != nil {
		return nil, err
	}
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return nil, fmt.Errorf("path cannot be empty")
	}

	// Security: only allow MEMORY.md and memory/**/*.md
	if err := validateMemoryPath(relPath); err != nil {
		return nil, err
	}

	absPath := filepath.Join(t.workspaceDir, relPath)
	absPath = filepath.Clean(absPath)

	// Prevent directory traversal
	if !strings.HasPrefix(absPath, filepath.Clean(t.workspaceDir)+string(os.PathSeparator)) &&
		absPath != filepath.Clean(t.workspaceDir) {
		return nil, fmt.Errorf("path %q is outside workspace", relPath)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("memory file not found: %s", relPath)
		}
		return nil, fmt.Errorf("read memory file: %w", err)
	}

	from := 1
	if v, ok := params["from"]; ok && v != nil {
		if n, err2 := coerceInt(v); err2 == nil && n > 0 {
			from = n
		}
	}
	linesCount := 0 // 0 means all
	if v, ok := params["lines"]; ok && v != nil {
		if n, err2 := coerceInt(v); err2 == nil && n > 0 {
			linesCount = n
		}
	}

	fileLines := strings.Split(string(data), "\n")
	totalLines := len(fileLines)

	start := from - 1
	if start < 0 {
		start = 0
	}
	if start >= totalLines {
		return &tool.ToolResult{
			Success: true,
			Output: fmt.Sprintf("Line %d is beyond end of file (total %d lines): %s",
				from, totalLines, relPath),
			Data: map[string]interface{}{
				"path":        relPath,
				"total_lines": totalLines,
				"text":        "",
			},
		}, nil
	}

	end := totalLines
	if linesCount > 0 {
		end = start + linesCount
		if end > totalLines {
			end = totalLines
		}
	}

	selected := fileLines[start:end]
	var out strings.Builder
	for i, line := range selected {
		fmt.Fprintf(&out, "%6d\t%s\n", start+i+1, line)
	}
	text := strings.TrimRight(out.String(), "\n")

	return &tool.ToolResult{
		Success: true,
		Output:  text,
		Data: map[string]interface{}{
			"path":        relPath,
			"from":        start + 1,
			"to":          end,
			"total_lines": totalLines,
			"text":        strings.Join(selected, "\n"),
		},
	}, nil
}

// validateMemoryPath ensures the path is within MEMORY.md or memory/ directory.
func validateMemoryPath(relPath string) error {
	clean := filepath.Clean(relPath)
	// Must not be absolute
	if filepath.IsAbs(clean) {
		return fmt.Errorf("path must be relative (got %q)", relPath)
	}
	// Must not traverse upward
	if strings.HasPrefix(clean, "..") {
		return fmt.Errorf("path traversal not allowed: %q", relPath)
	}
	// Allow MEMORY.md at workspace root
	if clean == "MEMORY.md" {
		return nil
	}
	// Allow anything inside memory/ that ends in .md
	parts := strings.SplitN(clean, string(os.PathSeparator), 2)
	if parts[0] == "memory" && strings.HasSuffix(strings.ToLower(clean), ".md") {
		return nil
	}
	return fmt.Errorf("path %q is not allowed: must be MEMORY.md or memory/*.md", relPath)
}

