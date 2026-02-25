// go test -v -run TestIntegration -tags integration ./pkg/agentsdk-go/pkg/tool/builtin/
//go:build integration

package toolbuiltin

import (
	"context"
	"fmt"
	"testing"
)

func TestIntegrationWebSearch(t *testing.T) {
	tool := NewWebSearchTool(nil)

	queries := []string{
		"伊朗局势 2026年2月 最新消息",
		"Iran US military conflict February 2026",
		"Bitcoin price February 2026",
	}

	for _, q := range queries {
		t.Run(q, func(t *testing.T) {
			res, err := tool.Execute(context.Background(), map[string]interface{}{
				"query": q,
			})
			if err != nil {
				t.Fatalf("search error: %v", err)
			}

			fmt.Printf("\n=== Query: %s ===\n", q)
			fmt.Printf("Output length: %d chars\n", len(res.Output))
			fmt.Printf("Output:\n%s\n", res.Output)

			// Check snippet lengths via raw search
			results, err2 := tool.search(context.Background(), q)
			if err2 == nil {
				fmt.Printf("\nSnippet lengths:\n")
				for i, r := range results {
					ends := ""
					sn := r.Snippet
					if len(sn) >= 3 && sn[len(sn)-3:] == "..." {
						ends = "(ends with ...)"
					} else if len(sn) > 0 && sn[len(sn)-1] == '.' {
						ends = "(ends with .)"
					}
					fmt.Printf("  [%d] snippet=%d chars %s\n", i+1, len(sn), ends)
					fmt.Printf("      url=%s\n", r.URL)
				}
			}
		})
	}
}
