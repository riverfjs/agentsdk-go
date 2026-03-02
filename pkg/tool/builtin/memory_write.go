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

func init() {
	const shortToolDesc = "Create or update memory files safely."
	registerShortToolDesc("memorywrite", shortToolDesc)
}

const memoryWriteDescription = `Unified memory writer/editor for MEMORY.md and memory/*.md.

Usage:
- target: "today" | "projects" | "lessons" | "path" (default "path")
- operation: "append" | "overwrite" | "replace" | "insert_after" | "insert_before" (default "append")
- path is required when target="path"
- mode is accepted as backward-compatible alias of operation.
- Paths outside MEMORY.md and memory/ are rejected for security.

Examples:
  target="today", operation="append", content="结论：已完成"
  target="projects", operation="replace", old_string="A", new_string="B"
  target="path", path="MEMORY.md", operation="overwrite", content="<full content>"`

var memoryWriteSchema = &tool.JSONSchema{
	Type: "object",
	Properties: map[string]interface{}{
		"target": map[string]interface{}{
			"type":        "string",
			"enum":        []string{"today", "projects", "lessons", "path"},
			"description": "Target alias. Use path when writing a custom memory file path.",
		},
		"path": map[string]interface{}{
			"type":        "string",
			"description": "Workspace-relative path when target=path",
		},
		"content": map[string]interface{}{
			"type":        "string",
			"description": "Text payload used by append/overwrite/insert_*",
		},
		"operation": map[string]interface{}{
			"type": "string",
			"enum": []string{
				"append", "overwrite", "replace", "insert_after", "insert_before",
			},
			"description": "Edit operation type",
		},
		"mode": map[string]interface{}{
			"type":        "string",
			"enum":        []string{"append", "overwrite"},
			"description": "Deprecated alias for operation",
		},
		"old_string": map[string]interface{}{
			"type":        "string",
			"description": "Old text for replace operation",
		},
		"new_string": map[string]interface{}{
			"type":        "string",
			"description": "New text for replace operation",
		},
		"anchor": map[string]interface{}{
			"type":        "string",
			"description": "Anchor text for insert_before/insert_after",
		},
	},
	Required: []string{},
}

// MemoryWriteTool writes content to memory files.
type MemoryWriteTool struct {
	workspaceDir string
}

// NewMemoryWriteTool creates a MemoryWriteTool rooted at workspaceDir.
func NewMemoryWriteTool(workspaceDir string) *MemoryWriteTool {
	return &MemoryWriteTool{workspaceDir: workspaceDir}
}

func (t *MemoryWriteTool) Name() string             { return "MemoryWrite" }
func (t *MemoryWriteTool) Description() string      { return memoryWriteDescription }
func (t *MemoryWriteTool) Schema() *tool.JSONSchema { return memoryWriteSchema }

func (t *MemoryWriteTool) Execute(ctx context.Context, params map[string]interface{}) (*tool.ToolResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is nil")
	}

	relPath, err := resolveMemoryWritePath(params)
	if err != nil {
		return nil, err
	}
	operation := resolveMemoryWriteOperation(params)

	if err := validateMemoryPath(relPath); err != nil {
		return nil, err
	}

	absPath := filepath.Clean(filepath.Join(t.workspaceDir, relPath))
	if !strings.HasPrefix(absPath, filepath.Clean(t.workspaceDir)+string(os.PathSeparator)) &&
		absPath != filepath.Clean(t.workspaceDir) {
		return nil, fmt.Errorf("path %q is outside workspace", relPath)
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		return nil, fmt.Errorf("create directory: %w", err)
	}

	switch operation {
	case "append":
		content, err := requireString(params, "content")
		if err != nil {
			return nil, err
		}
		n, err := appendContent(absPath, content)
		if err != nil {
			return nil, err
		}
		return &tool.ToolResult{
			Success: true,
			Output:  fmt.Sprintf("Appended %d bytes to %s", n, relPath),
			Data: map[string]interface{}{
				"path":      relPath,
				"operation": "append",
				"bytes":     n,
			},
		}, nil
	case "overwrite":
		content, err := requireString(params, "content")
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
			return nil, fmt.Errorf("write memory file: %w", err)
		}
		return &tool.ToolResult{
			Success: true,
			Output:  fmt.Sprintf("Written %d bytes to %s (overwrite)", len(content), relPath),
			Data: map[string]interface{}{
				"path":      relPath,
				"operation": "overwrite",
				"bytes":     len(content),
			},
		}, nil
	case "replace":
		oldString, err := requireString(params, "old_string")
		if err != nil {
			return nil, err
		}
		newString, err := requireString(params, "new_string")
		if err != nil {
			return nil, err
		}
		n, err := editExistingFile(absPath, func(text string) (string, error) {
			if !strings.Contains(text, oldString) {
				return "", fmt.Errorf("old_string not found")
			}
			return strings.Replace(text, oldString, newString, 1), nil
		})
		if err != nil {
			return nil, err
		}
		return &tool.ToolResult{
			Success: true,
			Output:  fmt.Sprintf("Updated %s (replace)", relPath),
			Data: map[string]interface{}{
				"path":      relPath,
				"operation": "replace",
				"bytes":     n,
			},
		}, nil
	case "insert_after":
		anchor, err := requireString(params, "anchor")
		if err != nil {
			return nil, err
		}
		content, err := requireString(params, "content")
		if err != nil {
			return nil, err
		}
		n, err := editExistingFile(absPath, func(text string) (string, error) {
			idx := strings.Index(text, anchor)
			if idx < 0 {
				return "", fmt.Errorf("anchor not found")
			}
			pos := idx + len(anchor)
			return text[:pos] + content + text[pos:], nil
		})
		if err != nil {
			return nil, err
		}
		return &tool.ToolResult{
			Success: true,
			Output:  fmt.Sprintf("Updated %s (insert_after)", relPath),
			Data: map[string]interface{}{
				"path":      relPath,
				"operation": "insert_after",
				"bytes":     n,
			},
		}, nil
	case "insert_before":
		anchor, err := requireString(params, "anchor")
		if err != nil {
			return nil, err
		}
		content, err := requireString(params, "content")
		if err != nil {
			return nil, err
		}
		n, err := editExistingFile(absPath, func(text string) (string, error) {
			idx := strings.Index(text, anchor)
			if idx < 0 {
				return "", fmt.Errorf("anchor not found")
			}
			return text[:idx] + content + text[idx:], nil
		})
		if err != nil {
			return nil, err
		}
		return &tool.ToolResult{
			Success: true,
			Output:  fmt.Sprintf("Updated %s (insert_before)", relPath),
			Data: map[string]interface{}{
				"path":      relPath,
				"operation": "insert_before",
				"bytes":     n,
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported operation: %s", operation)
	}
}

func appendContent(absPath, content string) (int, error) {
	f, err := os.OpenFile(absPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return 0, fmt.Errorf("open memory file: %w", err)
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
		return 0, fmt.Errorf("append to memory file: %w", err)
	}
	return n, nil
}

func resolveMemoryWritePath(params map[string]interface{}) (string, error) {
	target := "path"
	if v, ok := params["target"]; ok && v != nil {
		if s, err := coerceString(v); err == nil {
			s = strings.TrimSpace(strings.ToLower(s))
			if s != "" {
				target = s
			}
		}
	}
	switch target {
	case "today":
		return "memory/" + time.Now().Format("2006-01-02") + ".md", nil
	case "projects":
		return "memory/projects.md", nil
	case "lessons":
		return "memory/lessons.md", nil
	case "path":
		relPath, err := requireString(params, "path")
		if err != nil {
			return "", err
		}
		relPath = strings.TrimSpace(relPath)
		if relPath == "today" || relPath == "memory/today" {
			relPath = "memory/" + time.Now().Format("2006-01-02") + ".md"
		}
		return relPath, nil
	default:
		return "", fmt.Errorf("target must be one of: today, projects, lessons, path")
	}
}

func resolveMemoryWriteOperation(params map[string]interface{}) string {
	operation := ""
	if v, ok := params["operation"]; ok && v != nil {
		if s, err := coerceString(v); err == nil {
			operation = strings.TrimSpace(strings.ToLower(s))
		}
	}
	if operation == "" {
		if v, ok := params["mode"]; ok && v != nil {
			if s, err := coerceString(v); err == nil {
				operation = strings.TrimSpace(strings.ToLower(s))
			}
		}
	}
	if operation == "" {
		return "append"
	}
	return operation
}

func editExistingFile(absPath string, f func(text string) (string, error)) (int, error) {
	raw, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("target file not found")
		}
		return 0, fmt.Errorf("read target file: %w", err)
	}
	out, err := f(string(raw))
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(absPath, []byte(out), 0644); err != nil {
		return 0, fmt.Errorf("write edited file: %w", err)
	}
	return len(out), nil
}

