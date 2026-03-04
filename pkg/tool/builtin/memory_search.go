package toolbuiltin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	bleve "github.com/blevesearch/bleve/v2"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/cjk"
	"github.com/riverfjs/agentsdk-go/pkg/tool"
)

func init() {
	const shortToolDesc = "Search MEMORY.md and memory/*.md snippets."
	registerShortToolDesc("memorysearch", shortToolDesc)
}

const (
	memorySearchChunkSize    = 400  // target tokens per chunk (~4 chars/token estimate)
	memorySearchChunkOverlap = 80
	memorySearchSnippetMax   = 700  // chars returned per result
	memorySearchDefaultMax   = 3
	memorySearchMaxCap       = 5

	memorySearchDescription = `Mandatory recall step: semantically search MEMORY.md + memory/*.md before answering questions about prior work, decisions, dates, people, preferences, or todos.

Usage:
- Call this tool BEFORE answering anything about past events, decisions, preferences, or tasks.
- Returns top matching snippets with file path and line range.
- Default returns top 3 results.
- You may pass max_results to override, capped at 5.
- Follow up with MemoryGet to read the full content of a specific location.
- query: the search query in natural language or keywords`
)

var memorySearchSchema = &tool.JSONSchema{
	Type: "object",
	Properties: map[string]interface{}{
		"query": map[string]interface{}{
			"type":        "string",
			"description": "Natural language or keyword query to search in memory files",
		},
		"max_results": map[string]interface{}{
			"type":        "number",
			"description": "Maximum number of results to return (default 5)",
		},
	},
	Required: []string{"query"},
}

// MemoryChunk is a scored chunk of a memory file returned by SearchMemory.
type MemoryChunk struct {
	Path      string  `json:"path"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Snippet   string  `json:"snippet"`
	Score     float64 `json:"score"`
}

// MemorySearchTool searches MEMORY.md and memory/*.md files for relevant content.
type MemorySearchTool struct {
	workspaceDir string
}

// NewMemorySearchTool creates a MemorySearchTool rooted at workspaceDir.
func NewMemorySearchTool(workspaceDir string) *MemorySearchTool {
	return &MemorySearchTool{workspaceDir: workspaceDir}
}

func (t *MemorySearchTool) Name() string        { return "MemorySearch" }
func (t *MemorySearchTool) Description() string { return memorySearchDescription }
func (t *MemorySearchTool) Schema() *tool.JSONSchema { return memorySearchSchema }

func (t *MemorySearchTool) Execute(ctx context.Context, params map[string]interface{}) (*tool.ToolResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is nil")
	}
	query, err := requireString(params, "query")
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query cannot be empty")
	}
	maxResults := memorySearchDefaultMax
	if v, ok := params["max_results"]; ok && v != nil {
		if n, err2 := coerceInt(v); err2 == nil && n > 0 {
			maxResults = n
		}
	}
	if maxResults > memorySearchMaxCap {
		maxResults = memorySearchMaxCap
	}

	files, err := collectMemoryFiles(t.workspaceDir)
	if err != nil {
		return &tool.ToolResult{
			Success: true,
			Output:  fmt.Sprintf("No memory files found: %v", err),
			Data:    map[string]interface{}{"results": []interface{}{}},
		}, nil
	}
	if len(files) == 0 {
		return &tool.ToolResult{
			Success: true,
			Output:  "No memory files found (MEMORY.md and memory/*.md do not exist yet).",
			Data:    map[string]interface{}{"results": []interface{}{}},
		}, nil
	}

	var allChunks []MemoryChunk
	for _, filePath := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		relPath := toRelPath(t.workspaceDir, filePath)
		chunks, err := chunkFile(filePath, relPath)
		if err != nil {
			continue
		}
		allChunks = append(allChunks, chunks...)
	}

	scored, err := searchChunksWithBleve(allChunks, query, maxResults)
	if err != nil {
		return &tool.ToolResult{
			Success: true,
			Output:  fmt.Sprintf("Memory search failed: %v", err),
			Data:    map[string]interface{}{"results": []interface{}{}, "query": query},
		}, nil
	}

	// Clamp snippet length
	for i := range scored {
		if len(scored[i].Snippet) > memorySearchSnippetMax {
			scored[i].Snippet = scored[i].Snippet[:memorySearchSnippetMax] + "..."
		}
	}

	if len(scored) == 0 {
		return &tool.ToolResult{
			Success: true,
			Output:  fmt.Sprintf("No results found for query: %q", query),
			Data:    map[string]interface{}{"results": []interface{}{}, "query": query},
		}, nil
	}

	data, _ := json.Marshal(scored)
	var output strings.Builder
	for i, c := range scored {
		fmt.Fprintf(&output, "[%d] %s (lines %d-%d, score=%.2f)\n%s\n\n",
			i+1, c.Path, c.StartLine, c.EndLine, c.Score, c.Snippet)
	}

	return &tool.ToolResult{
		Success: true,
		Output:  strings.TrimSpace(output.String()),
		Data:    json.RawMessage(data),
	}, nil
}

// collectMemoryFiles returns all searchable memory files: MEMORY.md + memory/**/*.md
func collectMemoryFiles(workspaceDir string) ([]string, error) {
	var files []string
	longTerm := filepath.Join(workspaceDir, "MEMORY.md")
	if _, err := os.Stat(longTerm); err == nil {
		files = append(files, longTerm)
	}
	memDir := filepath.Join(workspaceDir, "memory")
	entries, err := os.ReadDir(memDir)
	if err != nil {
		if os.IsNotExist(err) {
			return files, nil
		}
		return files, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			files = append(files, filepath.Join(memDir, e.Name()))
		}
	}
	return files, nil
}

// chunkFile splits a markdown file into overlapping chunks.
// Each chunk is ~memorySearchChunkSize "tokens" (estimated as chars/4).
func chunkFile(filePath, relPath string) ([]MemoryChunk, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	content := string(data)
	lines := strings.Split(content, "\n")

	// Target ~400 tokens ≈ 1600 chars; overlap ~80 tokens ≈ 320 chars
	const targetChars = memorySearchChunkSize * 4
	const overlapChars = memorySearchChunkOverlap * 4

	var chunks []MemoryChunk
	startLine := 0
	for startLine < len(lines) {
		var buf strings.Builder
		endLine := startLine
		for endLine < len(lines) && buf.Len() < targetChars {
			buf.WriteString(lines[endLine])
			buf.WriteByte('\n')
			endLine++
		}
		snippet := strings.TrimSpace(buf.String())
		if snippet != "" {
			chunks = append(chunks, MemoryChunk{
				Path:      relPath,
				StartLine: startLine + 1,
				EndLine:   endLine,
				Snippet:   snippet,
			})
		}
		// Advance by chunk minus overlap
		advance := buf.Len() - overlapChars
		if advance <= 0 {
			advance = buf.Len()
		}
		// Count how many lines to skip
		skipped := 0
		for startLine < endLine {
			skipped += len(lines[startLine]) + 1
			startLine++
			if skipped >= advance {
				break
			}
		}
	}
	return chunks, nil
}

// tokenize lowercases and splits text into word tokens, removing punctuation.
// CJK characters are each emitted as individual tokens (no space-based segmentation).
func toRelPath(base, full string) string {
	rel, err := filepath.Rel(base, full)
	if err != nil {
		return full
	}
	return rel
}

// SearchMemory runs a BM25 search over MEMORY.md + memory/*.md and returns
// the top matching snippets. It is exported so the SDK runtime can call it
// before each agent turn to auto-inject relevant memories (auto-recall).
// Returns nil if no files exist or no results match.
// SearchMemory runs a BM25 search over MEMORY.md + memory/*.md and returns
// the top matching snippets. It is exported so the SDK runtime can call it
// before each agent turn to auto-inject relevant memories (auto-recall).
// Returns nil if no files exist or no results match.
func SearchMemory(workspaceDir, query string, maxResults int) ([]MemoryChunk, error) {
	query = strings.TrimSpace(query)
	if query == "" || maxResults <= 0 {
		return nil, nil
	}

	files, err := collectMemoryFiles(workspaceDir)
	if err != nil || len(files) == 0 {
		return nil, nil
	}

	var allChunks []MemoryChunk
	for _, filePath := range files {
		relPath := toRelPath(workspaceDir, filePath)
		chunks, err := chunkFile(filePath, relPath)
		if err != nil {
			continue
		}
		allChunks = append(allChunks, chunks...)
	}

	scored, err := searchChunksWithBleve(allChunks, query, maxResults)
	if err != nil {
		return nil, nil
	}
	return scored, nil
}

func requireString(params map[string]interface{}, key string) (string, error) {
	if params == nil {
		return "", fmt.Errorf("%s is required", key)
	}
	v, ok := params[key]
	if !ok || v == nil {
		return "", fmt.Errorf("%s is required", key)
	}
	s, err := coerceString(v)
	if err != nil {
		return "", fmt.Errorf("%s must be a string: %w", key, err)
	}
	return s, nil
}

type memorySearchDoc struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Snippet   string `json:"snippet"`
	Content   string `json:"content"`
}

func searchChunksWithBleve(chunks []MemoryChunk, query string, maxResults int) ([]MemoryChunk, error) {
	if len(chunks) == 0 {
		return nil, nil
	}
	mapping := bleve.NewIndexMapping()
	mapping.DefaultAnalyzer = "cjk"
	index, err := bleve.NewMemOnly(mapping)
	if err != nil {
		return nil, err
	}
	for i, c := range chunks {
		doc := memorySearchDoc{
			ID:        fmt.Sprintf("chunk-%d", i),
			Path:      c.Path,
			StartLine: c.StartLine,
			EndLine:   c.EndLine,
			Snippet:   c.Snippet,
			Content:   c.Snippet,
		}
		if err := index.Index(doc.ID, doc); err != nil {
			return nil, err
		}
	}

	q := bleve.NewMatchQuery(query)
	q.SetField("content")
	req := bleve.NewSearchRequestOptions(q, maxResults, 0, false)
	req.Fields = []string{"path", "start_line", "end_line", "snippet"}
	res, err := index.Search(req)
	if err != nil {
		return nil, err
	}

	out := make([]MemoryChunk, 0, len(res.Hits))
	for _, hit := range res.Hits {
		path, _ := hit.Fields["path"].(string)
		snippet, _ := hit.Fields["snippet"].(string)
		startLine := toIntField(hit.Fields["start_line"])
		endLine := toIntField(hit.Fields["end_line"])
		if path == "" || snippet == "" {
			continue
		}
		if len(snippet) > memorySearchSnippetMax {
			snippet = snippet[:memorySearchSnippetMax] + "..."
		}
		out = append(out, MemoryChunk{
			Path:      path,
			StartLine: startLine,
			EndLine:   endLine,
			Snippet:   snippet,
			Score:     hit.Score,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}

func toIntField(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

