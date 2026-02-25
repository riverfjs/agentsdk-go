package toolbuiltin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/riverfjs/agentsdk-go/pkg/tool"
)

const memoryWriteDescription = `Write or append content to a memory file (MEMORY.md or memory/YYYY-MM-DD.md).

Usage:
- Use "MEMORY.md" for durable long-term facts, preferences, decisions, and important context.
- Use "memory/YYYY-MM-DD.md" (today's date) for daily journal entries and session notes.
- mode "append" adds content to the end of the file (default for daily logs).
- mode "overwrite" replaces the entire file content (use with care for MEMORY.md).
- Paths outside MEMORY.md and memory/ are rejected for security.

Examples:
  path="MEMORY.md", mode="append", content="User prefers concise replies."
  path="memory/2026-02-24.md", mode="append", content="Discussed memory architecture."
  path="MEMORY.md", mode="overwrite", content="<full updated content>"`

var memoryWriteSchema = &tool.JSONSchema{
	Type: "object",
	Properties: map[string]interface{}{
		"path": map[string]interface{}{
			"type":        "string",
			"description": "Workspace-relative path: \"MEMORY.md\" or \"memory/YYYY-MM-DD.md\"",
		},
		"content": map[string]interface{}{
			"type":        "string",
			"description": "The content to write or append",
		},
		"mode": map[string]interface{}{
			"type":        "string",
			"enum":        []string{"append", "overwrite"},
			"description": "Write mode: \"append\" (default) adds to end; \"overwrite\" replaces file",
		},
	},
	Required: []string{"path", "content"},
}

// MemoryWriteTool writes content to memory files.
type MemoryWriteTool struct {
	workspaceDir string
}

// NewMemoryWriteTool creates a MemoryWriteTool rooted at workspaceDir.
func NewMemoryWriteTool(workspaceDir string) *MemoryWriteTool {
	return &MemoryWriteTool{workspaceDir: workspaceDir}
}

func (t *MemoryWriteTool) Name() string             { return "memory_write" }
func (t *MemoryWriteTool) Description() string      { return memoryWriteDescription }
func (t *MemoryWriteTool) Schema() *tool.JSONSchema { return memoryWriteSchema }

func (t *MemoryWriteTool) Execute(ctx context.Context, params map[string]interface{}) (*tool.ToolResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is nil")
	}

	relPath, err := requireString(params, "path")
	if err != nil {
		return nil, err
	}
	relPath = strings.TrimSpace(relPath)

	// Auto-expand "today" shorthand
	if relPath == "today" || relPath == "memory/today" {
		relPath = "memory/" + time.Now().Format("2006-01-02") + ".md"
	}

	content, err := requireString(params, "content")
	if err != nil {
		return nil, err
	}

	mode := "append"
	if v, ok := params["mode"]; ok && v != nil {
		if s, err2 := coerceString(v); err2 == nil {
			s = strings.TrimSpace(strings.ToLower(s))
			if s == "overwrite" || s == "append" {
				mode = s
			}
		}
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

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		return nil, fmt.Errorf("create directory: %w", err)
	}

	if mode == "overwrite" {
		if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
			return nil, fmt.Errorf("write memory file: %w", err)
		}
		return &tool.ToolResult{
			Success: true,
			Output:  fmt.Sprintf("Written %d bytes to %s (overwrite)", len(content), relPath),
			Data: map[string]interface{}{
				"path":  relPath,
				"mode":  "overwrite",
				"bytes": len(content),
			},
		}, nil
	}

	// Append mode: ensure newline separation
	f, err := os.OpenFile(absPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open memory file: %w", err)
	}
	defer f.Close()

	// Add newline prefix if file is non-empty and content doesn't start with one
	info, _ := f.Stat()
	if info != nil && info.Size() > 0 && !strings.HasPrefix(content, "\n") {
		content = "\n" + content
	}
	if !strings.HasSuffix(content, "\n") {
		content = content + "\n"
	}

	n, err := f.WriteString(content)
	if err != nil {
		return nil, fmt.Errorf("append to memory file: %w", err)
	}

	return &tool.ToolResult{
		Success: true,
		Output:  fmt.Sprintf("Appended %d bytes to %s", n, relPath),
		Data: map[string]interface{}{
			"path":  relPath,
			"mode":  "append",
			"bytes": n,
		},
	}, nil
}

