package toolbuiltin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cexll/agentsdk-go/pkg/tool"
)

const sendFileDescription = `Use this tool to send files (images, documents, etc.) to the user during execution. 

Common use cases:
1. Send screenshots from browser automation
2. Send generated reports or documents
3. Send downloaded files
4. Send any file that the user should receive

The file will be sent to the user's chat immediately.

Usage notes:
- Provide the absolute file path
- For screenshots from browser tool, use the "path" from the screenshot result
- The file must exist and be readable
- Supported types: images (png, jpg, gif, webp), documents (pdf, txt, json, etc.)
`

// SendFileTool sends a file to the user
type SendFileTool struct{}

func NewSendFileTool() *SendFileTool { return &SendFileTool{} }

func (t *SendFileTool) Name() string { return "SendFile" }

func (t *SendFileTool) Description() string { return sendFileDescription }

func (t *SendFileTool) Schema() *tool.JSONSchema { return sendFileSchema }

func (t *SendFileTool) Execute(ctx context.Context, params map[string]interface{}) (*tool.ToolResult, error) {
	if ctx == nil {
		return nil, errors.New("context is nil")
	}

	// Parse parameters
	path, err := readRequiredString(params, "path")
	if err != nil {
		return nil, fmt.Errorf("path: %w", err)
	}

	// Validate file exists
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", path)
		}
		return nil, fmt.Errorf("cannot access file: %w", err)
	}

	// Determine file type
	fileType := "file"
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		fileType = "image"
	case ".pdf":
		fileType = "document"
	}

	// Read caption if provided
	caption, _ := readOptionalString(params, "caption")

	// Return result with file metadata
	// Gateway will detect this and send the file
	data := map[string]interface{}{
		"file_path": path,
		"file_name": filepath.Base(path),
		"file_type": fileType,
	}
	if caption != "" {
		data["caption"] = caption
	}

	// Output as JSON for event extraction
	outputJSON, _ := json.Marshal(data)

	return &tool.ToolResult{
		Success: true,
		Output:  string(outputJSON),
		Data:    data,
	}, nil
}

func readOptionalString(obj map[string]interface{}, key string) (string, error) {
	raw, ok := obj[key]
	if !ok || raw == nil {
		return "", nil
	}
	value, err := coerceString(raw)
	if err != nil {
		return "", fmt.Errorf("must be string: %w", err)
	}
	return strings.TrimSpace(value), nil
}

var sendFileSchema = &tool.JSONSchema{
	Type:     "object",
	Required: []string{"path"},
	Properties: map[string]interface{}{
		"path": map[string]interface{}{
			"type":        "string",
			"description": "Absolute path to the file to send (e.g., /var/folders/.../screenshot.png)",
		},
		"caption": map[string]interface{}{
			"type":        "string",
			"description": "Optional caption or description for the file",
		},
	},
}

