# Error Guard Middleware Example

This example demonstrates how to use the `ErrorGuardMiddleware` to automatically detect consecutive tool failures and force the agent to report errors to the user instead of retrying indefinitely.

## How It Works

The Error Guard middleware:

1. **Tracks consecutive tool errors** across multiple tool calls
2. **Detects errors** by checking:
   - `is_error` flag in tool result metadata
   - Error markers in output (e.g., `⚠️ ERROR:`, `⚠️ STDERR:`, `Exit code`)
3. **Intervenes automatically** when consecutive errors reach threshold (default: 2)
4. **Injects system message** to force agent to ask user for guidance

## Usage

### Default Configuration (Recommended)

Error guard is **automatically enabled** by default with sensible settings:

```go
package main

import (
	"context"
	"log"

	"github.com/cexll/agentsdk-go/pkg/api"
	"github.com/cexll/agentsdk-go/pkg/model/anthropic"
)

func main() {
	provider := anthropic.NewProvider(anthropic.Config{
		APIKey: "your-api-key",
	})

	rt, err := api.New(context.Background(), api.Options{
		ModelFactory: provider,
		// ErrorGuard is enabled by default (threshold=2)
	})
	if err != nil {
		log.Fatalf("Failed to create runtime: %v", err)
	}

	// Run your agent - errors will be automatically detected
	resp, err := rt.Run(context.Background(), api.Request{
		Prompt: "调用浏览器查询股价",
	})
	if err != nil {
		log.Fatalf("Agent failed: %v", err)
	}

	log.Printf("Response: %s", resp.Result.Output)
}
```

### Custom Configuration

Customize error detection behavior via `api.Options`:

```go
rt, err := api.New(context.Background(), api.Options{
	ModelFactory: provider,
	
	// Customize error guard threshold
	ErrorGuardThreshold: 3, // Trigger after 3 consecutive errors (default: 2)
	
	// Add custom error detection patterns
	ErrorGuardMarkers: []string{
		"CUSTOM_ERROR",
		"Failed to",
	},
})
```

### Disable Error Guard

If you need to disable it completely:

```go
disabled := false
rt, err := api.New(context.Background(), api.Options{
	ModelFactory:      provider,
	ErrorGuardEnabled: &disabled, // Explicitly disable error guard
})
```

## Configuration Options

### `WithErrorThreshold(int)`

Sets the number of consecutive errors before intervention.

```go
errorGuard := middleware.NewErrorGuardMiddleware(
	middleware.WithErrorThreshold(3),
)
```

**Default**: 2 (same as nanobot)

### `WithErrorMarkers([]string)`

Adds custom error detection patterns beyond the defaults.

```go
errorGuard := middleware.NewErrorGuardMiddleware(
	middleware.WithErrorMarkers([]string{
		"CUSTOM_ERROR",
		"Failed to",
	}),
)
```

**Default markers**:
- `⚠️ ERROR:`
- `⚠️ STDERR:`
- `Exit code`
- `command failed:`
- `execution failed:`

## How It Prevents Infinite Retries

### Without Error Guard:

```
User: 调用浏览器查询9988.HK股价
Agent: [calls browser tool]
Tool: ⚠️ ERROR: navigation failed
Agent: [calls browser tool again]
Tool: ⚠️ ERROR: navigation failed
Agent: [calls browser tool again]
Tool: ⚠️ ERROR: navigation failed
... (continues retrying) ...
```

### With Error Guard:

```
User: 调用浏览器查询9988.HK股价
Agent: [calls browser tool]
Tool: ⚠️ ERROR: navigation failed
Agent: [calls browser tool again]
Tool: ⚠️ ERROR: navigation failed
System: ⚠️ SYSTEM: Multiple tool failures detected (2 consecutive errors).
        You MUST report these errors to the user immediately and ask for guidance.
        Do not continue making tool calls without user input.
Agent: "浏览器工具连续失败了两次。我遇到导航错误。您想尝试其他方法吗？"
```

## Integration with Agent Loop

The middleware is integrated at two points:

1. **AfterTool hook**: Detects errors and updates state
2. **Model adapter**: Reads state and injects system message when threshold is reached

This follows the same pattern as nanobot's error detection but is implemented as reusable middleware following agentsdk-go's architecture.

## See Also

- [Middleware Documentation](../../docs/middleware.md)
- [Agent Loop Architecture](../../docs/architecture.md)
- [Nanobot Error Detection](https://github.com/your-org/nanobot/blob/main/nanobot/agent/loop.py#L291-L310)

